package network

import (
	"bytes"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestGatewaySettingsSharingDoesNotStopAgents(t *testing.T) {
	home := t.TempDir()
	auth, err := newGatewayAuth(home)
	if err != nil {
		t.Fatal(err)
	}
	if err = WriteSharingConfig(home, SharingConfig{Enabled: true, Allow: []string{"tag:server"}}); err != nil {
		t.Fatal(err)
	}
	origin, _ := url.Parse("https://agentpack.example")
	stopped := false
	g := &Gateway{auth: auth, origin: origin, local: nodeRecord{Secret: strings.Repeat("x", 64)}, options: GatewayOptions{Home: home}, stop: func() { stopped = true }}
	handler := g.Handler()
	request := httptest.NewRequest("POST", origin.String()+"/api/settings/sharing", bytes.NewBufferString(`{"enabled":false}`))
	request.Header.Set("Authorization", "Bearer "+g.local.Secret)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 200 || stopped {
		t.Fatalf("sharing stop: %d stopped=%v", response.Code, stopped)
	}
	config, err := ReadSharingConfig(home)
	if err != nil || config.Enabled || len(config.Allow) != 1 {
		t.Fatalf("config %+v %v", config, err)
	}
	request = httptest.NewRequest("GET", origin.String()+"/api/settings", nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 401 {
		t.Fatalf("unauthenticated settings: %d", response.Code)
	}
	request.Header.Set("Authorization", "Bearer "+g.local.Secret)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"sharing_enabled":false`) {
		t.Fatal(response.Code, response.Body.String())
	}
}

func TestPeerHTTPRejectsBrowserAndAdministrativeRoutes(t *testing.T) {
	home := t.TempDir()
	auth, err := newGatewayAuth(home)
	if err != nil {
		t.Fatal(err)
	}
	origin, _ := url.Parse("https://agentpack.example")
	g := &Gateway{auth: auth, origin: origin, local: nodeRecord{Secret: strings.Repeat("x", 64)}, options: GatewayOptions{Home: home}}
	handler := g.peerHandler(&tailnetPeer{ID: "self", UserID: 7}, "100.100.1.2:7421")
	for _, test := range []struct {
		host, path, origin string
		want               int
	}{
		{"evil.example", "/api/sessions", "", 403},
		{"100.100.1.2:7421", "/api/sessions", "https://evil.example", 403},
		{"100.100.1.2:7421", "/api/node/stop", "", 404},
		{"100.100.1.2:7421", "/api/settings", "", 404},
		{"100.100.1.2:7421", "/api/sessions?node=another", "", 403},
	} {
		request := httptest.NewRequest("GET", "http://"+test.host+test.path, nil)
		request.Header.Set("Origin", test.origin)
		request.Header.Set("Authorization", "Bearer "+g.local.Secret)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != test.want {
			t.Fatalf("%s %s: %d", test.host, test.path, response.Code)
		}
	}
}
