package registry

import "testing"

func TestCachedModuleRejectsPathsOutsideTheRepository(t *testing.T) {
	if module, ok := cachedModule("Owner", "Repo", ""); !ok || module != "github.com/owner/repo" {
		t.Fatalf("root module = %q %t", module, ok)
	}
	if _, ok := cachedModule("owner", "repo", `skills\one`); !ok {
		t.Fatal("a Windows-separated in-repo path was rejected")
	}
	for _, bad := range [][3]string{{"..", "repo", ""}, {"owner", "a/b", ""}, {"owner", "repo", "skills/../x"}, {"owner", "repo", "skills//x"}, {"", "repo", ""}} {
		if module, ok := cachedModule(bad[0], bad[1], bad[2]); ok {
			t.Errorf("cachedModule(%q) = %q, want rejection", bad, module)
		}
	}
}
