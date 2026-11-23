package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

const testMarketplace = `{"plugins":[
 {"name":"hookify","description":"Turns corrections into hooks.","author":{"name":"Anthropic"},"source":"./plugins/hookify"},
 {"name":"sub","source":{"source":"git-subdir","url":"https://github.com/Org/skills.git","path":"plugins/sub","ref":"main","sha":"abc123"}},
 {"name":"whole","source":{"source":"url","url":"https://github.com/org/whole.git"}},
 {"name":"elsewhere","source":{"source":"url","url":"https://gitlab.com/org/x.git"}},
 {"name":"escape","source":"../outside"}
]}`

func TestLibraryCatalogCombinesSourcesAndCaches(t *testing.T) {
	registryCalls := 0
	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		registryCalls++
		if r.URL.Query().Get("version") != "latest" {
			t.Errorf("registry query = %s", r.URL.RawQuery)
		}
		fmt.Fprintf(w, `{"servers":[
 {"server":{"name":"io.github.microsoft/playwright","description":"Browser automation","packages":[{"registryType":"npm","identifier":"@playwright/mcp","transport":{"type":"stdio"}}]}},
 {"server":{"name":"com.example/remote","remotes":[{"type":"streamable-http","url":"https://example.com/mcp?a=1&b=2"}]}},
 {"server":{"name":"com.example/docker-only","packages":[{"registryType":"oci","identifier":"example/img","transport":{"type":"stdio"}}]}}
],"metadata":{"count":3,"search":%q}}`, r.URL.Query().Get("search"))
	}))
	defer registry.Close()
	now := time.Unix(1_000_000, 0)
	c := &catalog{
		marketplace: func(context.Context) ([]byte, error) { return []byte(testMarketplace), nil },
		registry:    registry.URL,
		client:      registry.Client(),
		now:         func() time.Time { return now },
	}
	get := func() Catalog {
		t.Helper()
		recorder := httptest.NewRecorder()
		c.ServeHTTP(recorder, httptest.NewRequest("GET", "/api/library/catalog", nil))
		var result Catalog
		if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	result := get()
	specs := map[string]string{}
	for _, item := range result.Items {
		specs[item.Name] = item.Spec + item.Command
	}
	want := map[string]string{
		"hookify":    "github.com/anthropics/claude-plugins-official/plugins/hookify",
		"sub":        "github.com/Org/skills/plugins/sub@abc123",
		"whole":      "github.com/org/whole",
		"playwright": "agentpack mcp add playwright --command npx -- -y @playwright/mcp",
		"remote":     "agentpack mcp add remote --url 'https://example.com/mcp?a=1&b=2' --type http",
	}
	if len(specs) != len(want) {
		t.Fatalf("items = %v", specs)
	}
	for name, spec := range want {
		if specs[name] != spec {
			t.Errorf("%s = %q, want %q", name, specs[name], spec)
		}
	}
	if len(result.Sources) != 2 || result.Sources[0].Count != 3 || result.Sources[1].Count != 2 {
		t.Fatalf("sources = %+v", result.Sources)
	}
	get()
	if registryCalls != 1 {
		t.Fatalf("registry called %d times within the cache window", registryCalls)
	}
	now = now.Add(catalogTTL)
	get()
	if registryCalls != 2 {
		t.Fatalf("registry called %d times after the cache expired", registryCalls)
	}
}

func TestLibraryCatalogReportsAFailingSource(t *testing.T) {
	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusBadGateway)
	}))
	defer registry.Close()
	c := &catalog{
		marketplace: func(context.Context) ([]byte, error) { return nil, errors.New("rate limited") },
		registry:    registry.URL,
		client:      registry.Client(),
		now:         time.Now,
	}
	recorder := httptest.NewRecorder()
	c.ServeHTTP(recorder, httptest.NewRequest("GET", "/api/library/catalog", nil))
	var result Catalog
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil || recorder.Code != http.StatusOK {
		t.Fatalf("status %d, %v", recorder.Code, err)
	}
	if len(result.Items) != 0 || result.Sources[0].Error != "rate limited" || result.Sources[1].Error == "" {
		t.Fatalf("catalog = %+v", result)
	}
}

func TestLibraryCatalogFollowsRegistryCursorUpToTheCap(t *testing.T) {
	var cursors []string
	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cursors = append(cursors, r.URL.Query().Get("cursor"))
		fmt.Fprintf(w, `{"servers":[{"server":{"name":"com.example/s%d","packages":[{"registryType":"pypi","identifier":"s","transport":{"type":"stdio"}}]}}],"metadata":{"nextCursor":"c%d"}}`, len(cursors), len(cursors))
	}))
	defer registry.Close()
	c := &catalog{registry: registry.URL, client: registry.Client(), now: time.Now}
	items, err := c.servers(context.Background(), "")
	if err != nil || len(items) != registryPages || len(cursors) != registryPages || cursors[0] != "" || cursors[1] != "c1" {
		t.Fatalf("items=%d cursors=%v err=%v", len(items), cursors, err)
	}
	if server := items[0].Server; server == nil || server.Name != "s1" || server.Command != "uvx" || len(server.Args) != 1 || server.Args[0] != "s" {
		t.Fatalf("server = %+v", server)
	}
	cursors = nil
	if items, err := c.servers(context.Background(), "s"); err != nil || len(items) != 1 || len(cursors) != 1 {
		t.Fatalf("a search read %d pages, %d items, %v", len(cursors), len(items), err)
	}
}
