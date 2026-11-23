package mcp

import "testing"

func TestCanonicalURLIPv6WithPort(t *testing.T) {
	got, err := CanonicalURL("https://[2001:DB8::1]:8443/mcp/")
	if err != nil || got != "https://[2001:db8::1]:8443/mcp" {
		t.Fatalf("CanonicalURL = %q, %v", got, err)
	}
}
