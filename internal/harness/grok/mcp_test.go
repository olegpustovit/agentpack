package grok

import (
	"strings"
	"testing"

	"github.com/olegpustovit/agentpack/internal/mcp"
)

func TestMergeMCPWritesRemoteURL(t *testing.T) {
	url := "https://example.test/mcp"
	data, _, _, err := MergeMCPData(nil, mcp.Entries{"remote": {Server: mcp.Server{URL: &url}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), url) {
		t.Fatalf("output = %s", data)
	}
}
