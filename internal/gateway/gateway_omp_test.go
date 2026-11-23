package gateway

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olegpustovit/agentpack/internal/harness/omp"
)

func TestOMPSettingsRoutesAndOverlay(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AGENTPACK_HOME", home)
	t.Setenv("OMP_PATH", filepath.Join(home, "no-omp")) // schema checks only; never the user's omp
	g, origin := identityGateway(t, home, nil)
	testBrowserSession(t, g.auth, testBrowserEmail, "owner")
	testBrowserSession(t, g.auth, "member@example.com", "member")
	handler := g.Handler()
	call := func(method, path, body, token string) (int, string) {
		r := httptest.NewRequest(method, origin.String()+path, strings.NewReader(body))
		r.Header.Set("Origin", origin.String())
		r.AddCookie(&http.Cookie{Name: "agentpack_session", Value: token})
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}
	if code, _ := call("PUT", "/api/omp/settings/edit.mode", `{"value":"patch"}`, "member"); code != http.StatusForbidden {
		t.Fatal("non-owner changed a setting", code)
	}
	if code, _ := call("PUT", "/api/omp/settings/no.such.key", `{"value":1}`, "owner"); code != http.StatusBadRequest {
		t.Fatal("unknown key accepted", code)
	}
	if code, body := call("PUT", "/api/omp/settings/edit.mode", `{"value":"bogus"}`, "owner"); code != http.StatusBadRequest || !strings.Contains(body, "Valid values: apply_patch") {
		t.Fatal("invalid value accepted", code, body)
	}
	if code, body := call("PUT", "/api/omp/settings/edit.mode", `{"value": "patch"}`, "owner"); code != 200 || !strings.Contains(body, `"edit.mode":"patch"`) {
		t.Fatal("owner change", code, body)
	}
	overlay, err := omp.OverlayPath()
	if err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(overlay); err != nil || string(data) != "edit:\n  mode: patch\n" {
		t.Fatalf("overlay after PUT = %q, %v", data, err)
	}
	// A row arriving by replication reaches the overlay on the next pass.
	if _, err := g.store.DB.Exec(`INSERT INTO omp_settings(key,value) VALUES ('edit.fuzzyMatch','false')`); err != nil {
		t.Fatal(err)
	}
	if err := g.writeOMPOverlay(t.Context()); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(overlay); err != nil || string(data) != "edit:\n  fuzzyMatch: false\n  mode: patch\n" {
		t.Fatalf("overlay after replication = %q, %v", data, err)
	}
	if code, body := call("GET", "/api/omp/settings", "", "member"); code != 200 || !strings.Contains(body, `"version":""`) || !strings.Contains(body, `"key":"edit.mode"`) {
		t.Fatal("read", code, body[:min(len(body), 200)])
	}
	// One role is set and reset on its own; resetting the record clears its roles too.
	if code, body := call("PUT", "/api/omp/settings/modelRoles.smol", `{"value":"anthropic/haiku:low"}`, "owner"); code != 200 || !strings.Contains(body, `"modelRoles.smol":"anthropic/haiku:low"`) {
		t.Fatal("role change", code, body)
	}
	if code, _ := call("DELETE", "/api/omp/settings/modelRoles", "", "owner"); code != 200 {
		t.Fatal("record reset", code)
	}
	if network, err := g.ompNetwork(t.Context()); err != nil || network["modelRoles.smol"] != nil {
		t.Fatalf("role kept after its record was reset: %v, %v", network, err)
	}
	for _, key := range []string{"edit.mode", "edit.fuzzyMatch"} {
		if code, _ := call("DELETE", "/api/omp/settings/"+key, "", "owner"); code != 200 {
			t.Fatal("reset", key, code)
		}
	}
	if _, err := os.Stat(overlay); !os.IsNotExist(err) {
		t.Fatal("overlay kept after the last reset", err)
	}
}
