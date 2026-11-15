package cli

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olegpustovit/agentpack/internal/state"
)

func TestGatewayMainCommandsProtectArchiveHandover(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AGENTPACK_HOME", home)
	store, err := state.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.BindNode(t.Context(), "new-main"); err != nil {
		t.Fatal(err)
	}
	var output, warnings strings.Builder
	runner := Runner{Stdin: strings.NewReader(""), Stdout: &output, Stderr: &warnings}
	run := func(args ...string) error {
		t.Helper()
		_, err := runner.Execute(t.Context(), append([]string{"gateway", "main"}, args...))
		return err
	}
	if err := run(); err != nil || !strings.Contains(output.String(), "none") {
		t.Fatalf("no main: %s %v", output.String(), err)
	}
	if err := run("set"); err != nil {
		t.Fatal(err)
	}
	first, err := store.Main(t.Context())
	if err != nil || first.MainNode != "new-main" || first.ArchiveNode != "new-main" {
		t.Fatalf("first main: %+v %v", first, err)
	}
	if _, err := store.DB.Exec(`UPDATE mesh_settings SET value='old-main' WHERE key IN ('main_node','archive_node');
 INSERT INTO mesh_nodes(node_id,name) VALUES ('old-main','Old computer');
 INSERT INTO node_settings(node_id,key,value) VALUES ('old-main','archive_path','/old/home/archive.db')`); err != nil {
		t.Fatal(err)
	}
	runner.Stdin = strings.NewReader("cancel\n")
	if err := run("set"); err == nil {
		t.Fatal("switch accepted without confirmation")
	}
	roles, _ := store.Main(t.Context())
	if roles.MainNode != "old-main" {
		t.Fatal("cancelled switch changed role")
	}
	for _, text := range []string{"Old computer", "/old/home/archive.db", filepath.Join(home, "archive.db"), "ap gateway main archive-ready"} {
		if !strings.Contains(warnings.String(), text) {
			t.Fatalf("missing %q in warning: %s", text, warnings.String())
		}
	}
	runner.Stdin = strings.NewReader("switch\n")
	if err := run("set"); err != nil {
		t.Fatal(err)
	}
	roles, _ = store.Main(t.Context())
	if roles.MainNode != "new-main" || roles.ArchiveNode != "old-main" {
		t.Fatalf("switch bypassed archive copy: %+v", roles)
	}
	output.Reset()
	if err := run(); err != nil || !strings.Contains(output.String(), "archive not ready") {
		t.Fatalf("pending main status: %s %v", output.String(), err)
	}
	if err := run("archive-ready"); err == nil {
		t.Fatal("missing archive accepted")
	}
	path := filepath.Join(home, "archive.db")
	if err := os.WriteFile(path, []byte("invalid archive"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run("archive-ready", "--empty"); err == nil {
		t.Fatal("invalid existing archive accepted")
	}
	if body, err := os.ReadFile(path); err != nil || string(body) != "invalid archive" {
		t.Fatal("invalid archive was replaced", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := run("archive-ready", "--empty"); err != nil {
		t.Fatal(err)
	}
	roles, _ = store.Main(t.Context())
	if roles.ArchiveNode != "new-main" {
		t.Fatal("archive-ready did not record archive holder")
	}
	output.Reset()
	if err := run(); err != nil || !strings.Contains(output.String(), "archive ready") {
		t.Fatalf("ready status: %s %v", output.String(), err)
	}
	runner.Stderr = io.Discard
	if err := run("set", "--empty"); err == nil {
		t.Fatal("archive-only flag accepted for main set")
	}
}
