package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// stubGH replaces the gh lookup, resets the memo and returns the call counter.
func stubGH(t *testing.T, token string) *int {
	t.Helper()
	previous := ghAuthToken
	calls := 0
	ghAuthToken = func() string { calls++; return token }
	ghTokenOnce = sync.Once{}
	ghTokenValue = ""
	t.Cleanup(func() { ghAuthToken = previous; ghTokenOnce = sync.Once{}; ghTokenValue = "" })
	return &calls
}

func TestGithubTokenPrecedence(t *testing.T) {
	for _, test := range []struct {
		name, github, gh, cli, want string
		wantCalls                   int
	}{
		{"GITHUB_TOKEN wins", "a", "b", "c", "a", 0},
		{"GH_TOKEN second", "", "b", "c", "b", 0},
		{"gh CLI last", "", "", "c", "c", 1},
		{"gh missing or failed", "", "", "", "", 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("GITHUB_TOKEN", test.github)
			t.Setenv("GH_TOKEN", test.gh)
			calls := stubGH(t, test.cli)
			if got := githubToken(); got != test.want {
				t.Fatalf("githubToken() = %q, want %q", got, test.want)
			}
			if *calls != test.wantCalls {
				t.Fatalf("gh calls = %d, want %d", *calls, test.wantCalls)
			}
		})
	}
}

func TestGithubTokenMemoizesGH(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	calls := stubGH(t, "")
	for range 3 {
		githubToken()
	}
	if *calls != 1 {
		t.Fatalf("gh calls = %d, want 1 (failures are memoized too)", *calls)
	}
}

func TestGitAuthUsesAccessTokenUser(t *testing.T) {
	if gitAuth("") != nil {
		t.Fatal("empty token must give no auth")
	}
	if got := gitAuth("tok"); got == nil || !strings.Contains(got.String(), "x-access-token") {
		t.Fatalf("gitAuth = %v", got)
	}
}

func TestAPIAndCodeloadSendGHToken(t *testing.T) {
	t.Setenv("AGENTPACK_HOME", t.TempDir())
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	stubGH(t, "gh-secret")
	var apiAuth, codeloadAuth []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/repos/") {
			apiAuth = append(apiAuth, r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{"sha":"a123456789abcdef0123456789abcdef01234567"}`))
			return
		}
		auth := r.Header.Get("Authorization")
		codeloadAuth = append(codeloadAuth, auth)
		if auth == "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte("archive"))
	}))
	defer server.Close()
	previousAPI, previousCodeload := apiRoot, codeloadRoot
	apiRoot, codeloadRoot = server.URL, server.URL
	defer func() { apiRoot, codeloadRoot = previousAPI, previousCodeload }()

	if _, err := ResolveRef(context.Background(), server.Client(), "o", "r", "main", true); err != nil {
		t.Fatal(err)
	}
	if len(apiAuth) != 1 || apiAuth[0] != "Bearer gh-secret" {
		t.Fatalf("API Authorization = %v", apiAuth)
	}
	data, err := DownloadTarball(context.Background(), server.Client(), "o", "r", "a123456789abcdef0123456789abcdef01234567")
	if err != nil || string(data) != "archive" {
		t.Fatalf("DownloadTarball = %q, %v", data, err)
	}
	if len(codeloadAuth) != 2 || codeloadAuth[0] != "" || codeloadAuth[1] != "Bearer gh-secret" {
		t.Fatalf("codeload Authorization (anonymous first, then token) = %v", codeloadAuth)
	}
}

func TestAccessHints(t *testing.T) {
	const sha = "a123456789abcdef0123456789abcdef01234567"
	for _, test := range []struct {
		name, token, want string
	}{
		{"no token", "", "gh auth login"},
		{"token but 404", "tok", "may lack access to o/r"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("AGENTPACK_HOME", t.TempDir())
			t.Setenv("GITHUB_TOKEN", test.token)
			t.Setenv("GH_TOKEN", "")
			stubGH(t, "")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) }))
			defer server.Close()
			previousAPI, previousCodeload, previousRemote := apiRoot, codeloadRoot, gitRemoteRoot
			apiRoot, codeloadRoot, gitRemoteRoot = server.URL, server.URL, server.URL
			defer func() { apiRoot, codeloadRoot, gitRemoteRoot = previousAPI, previousCodeload, previousRemote }()

			_, err := ResolveRef(context.Background(), server.Client(), "o", "r", "main", true)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ResolveRef error = %v, want hint %q", err, test.want)
			}
			_, err = DownloadTarball(context.Background(), server.Client(), "o", "r", sha)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("DownloadTarball error = %v, want hint %q", err, test.want)
			}
			if strings.Contains(err.Error(), "tok") && test.token != "" && strings.Contains(err.Error(), "Bearer") {
				t.Fatal("error leaks the token")
			}
		})
	}
}
