package state

import (
	"database/sql"
	"errors"
	"testing"
)

func TestMCPCredentialGenerationRules(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := t.Context()
	creds := store.Credentials()
	row := MCPCredential{Match: "https://mcp", Kind: "oauth", Label: "x", Account: "a@x", Client: "{}", State: `{"generation":3,"status":"ok"}`}
	id, err := creds.SaveMCP(ctx, row, "mcpc-1")
	if err != nil || id != "mcpc-1" {
		t.Fatal(id, err)
	}
	// A replacement keeps the ID and rises above the stored generation.
	row.State = `{"generation":0,"status":"ok"}`
	if again, err := creds.SaveMCP(ctx, row, "mcpc-2"); err != nil || again != "mcpc-1" {
		t.Fatal(again, err)
	}
	generation := func() (g int64) {
		if err := store.Reader.QueryRow("SELECT json_extract(state,'$.generation') FROM mcp_credentials WHERE id='mcpc-1'").Scan(&g); err != nil {
			t.Fatal(err)
		}
		return g
	}
	if g := generation(); g != 4 {
		t.Fatal("generation", g)
	}
	// A needs-login mark from an older generation loses to the newer login.
	old := int64(3)
	if changed, err := creds.SetMCPState(ctx, "mcpc-1", `{"generation":3,"status":"needs_login"}`, &old); err != nil || changed {
		t.Fatal("stale mark applied", changed, err)
	}
	if changed, err := creds.SetMCPState(ctx, "mcpc-1", `{"generation":9,"status":"ok"}`, nil); err != nil || !changed {
		t.Fatal(changed, err)
	}
	if err := creds.Approve(ctx, "mcpc-1", "github.com/o/r"); err != nil {
		t.Fatal(err)
	}
	listed, err := creds.MCPForAccount(ctx, "a@x")
	if err != nil || len(listed) != 1 || len(listed[0].Plugins) != 1 {
		t.Fatal(listed, err)
	}
	if err := creds.DeleteMCP(ctx, "mcpc-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := creds.MCP(ctx, "mcpc-1"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
	if value, err := creds.Agent(ctx, "claude/x"); err != nil || value != nil {
		t.Fatal(value, err)
	}
	if _, _, ok, err := creds.AgentInstall(ctx, "claude/x"); err != nil || ok {
		t.Fatal(ok, err)
	}
}
