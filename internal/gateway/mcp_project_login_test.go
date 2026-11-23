package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/olegpustovit/agentpack/internal/mcp"
	"github.com/olegpustovit/agentpack/internal/network"
)

func TestProjectMCPLoginChecksTheCredential(t *testing.T) {
	g, home := startCredentialGateway(t)
	type call struct{ directory, url, credential string }
	var calls []call
	g.options.ProjectMCPLogin = func(_ context.Context, directory, url, credential string) error {
		calls = append(calls, call{directory, url, credential})
		return nil
	}
	save := func(account, kind, url string) string {
		t.Helper()
		credential, err := newStaticMCPCredential(account, kind, "", "login", map[string]string{"TOKEN": "x"})
		if kind == mcp.CredentialHeader {
			credential, err = newStaticMCPCredential(account, kind, url, "", map[string]string{"Authorization": "Bearer x"})
		}
		if err != nil {
			t.Fatal(err)
		}
		credential.Match = url // an env row never matches a URL; force one to prove the kind check
		id, err := g.saveMCPCredential(context.Background(), credential)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	const linear = "https://mcp.linear.app/mcp"
	own := save(testBrowserEmail, mcp.CredentialHeader, linear)
	other := save("other@example.com", mcp.CredentialHeader, linear)
	elsewhere := save(testBrowserEmail, mcp.CredentialHeader, "https://x.example.com/mcp")
	env := save(testBrowserEmail, mcp.CredentialEnv, linear)
	record, err := network.LoadNode(home)
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(home, "project")
	put := func(directory, credential string) int {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"directory": directory, "url": "https://MCP.linear.app/mcp/", "credential": credential})
		response, err := network.LocalRequest(context.Background(), network.WorkerRecord{Address: record.Address, Secret: record.Secret}, http.MethodPut, "/api/projects/mcp-login", "", 0, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		return response.StatusCode
	}
	for _, test := range []struct {
		name, directory, credential string
		status                      int
	}{
		{"own", directory, own, http.StatusNoContent},
		{"another account's", directory, other, http.StatusNotFound},
		{"URL mismatch", directory, elsewhere, http.StatusNotFound},
		{"env kind", directory, env, http.StatusNotFound},
		{"relative directory", "project", own, http.StatusBadRequest},
		{"default", directory, "", http.StatusNoContent},
	} {
		if status := put(test.directory, test.credential); status != test.status {
			t.Errorf("%s: status %d, want %d", test.name, status, test.status)
		}
	}
	want := []call{{directory, linear, own}, {directory, linear, ""}}
	if len(calls) != len(want) || calls[0] != want[0] || calls[1] != want[1] {
		t.Fatalf("hook calls %+v, want %+v", calls, want)
	}

	// Saving a project permission has the same credential ownership and URL checks.
	directory = seedFolder(t, home)
	var saved []mcp.PluginLogin
	g.options.SaveProject = func(_ context.Context, input ProjectSettingsInput) error {
		saved = append(saved, input.PluginLogins["linear"])
		return nil
	}
	for _, test := range []struct {
		id     string
		status int
	}{{own, http.StatusNoContent}, {other, http.StatusNotFound}, {elsewhere, http.StatusNotFound}, {env, http.StatusNotFound}} {
		body, err := json.Marshal(ProjectSettingsInput{
			Directory: directory,
			PluginLogins: map[string]mcp.PluginLogin{
				"linear": {Module: "github.com/acme/linear", URL: "https://MCP.linear.app/mcp/", Credential: test.id},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		response, err := network.LocalRequest(t.Context(), network.WorkerRecord{Address: record.Address, Secret: record.Secret}, http.MethodPut, "/api/projects/settings", "", 0, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != test.status {
			t.Errorf("plugin login: status %d, want %d", response.StatusCode, test.status)
		}
	}
	if len(saved) != 1 || saved[0].Credential != own || saved[0].URL != linear {
		t.Fatalf("unexpected project plugin selections: %+v", saved)
	}
}
