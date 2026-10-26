package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
)

const ghTokenTimeout = 5 * time.Second

// ghAuthToken asks the authenticated GitHub CLI for its token. Tests replace it.
var ghAuthToken = func() string {
	path, err := exec.LookPath("gh")
	if err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), ghTokenTimeout)
	defer cancel()
	output, err := exec.CommandContext(ctx, path, "auth", "token", "--hostname", "github.com").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

var (
	ghTokenOnce  sync.Once
	ghTokenValue string
)

// githubToken returns GITHUB_TOKEN, then GH_TOKEN, then the token of the
// authenticated gh CLI (looked up once per process). Empty means anonymous.
func githubToken() string {
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		return token
	}
	if token := os.Getenv("GH_TOKEN"); token != "" {
		return token
	}
	ghTokenOnce.Do(func() { ghTokenValue = ghAuthToken() })
	return ghTokenValue
}

func gitAuth(token string) transport.AuthMethod {
	if token == "" {
		return nil
	}
	return &githttp.BasicAuth{Username: "x-access-token", Password: token}
}

// apiStatusError is a non-2xx answer from the GitHub REST API.
type apiStatusError struct {
	status  int
	message string
}

func (e *apiStatusError) Error() string { return e.message }

func accessDenied(status int) bool {
	return status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusNotFound
}

// accessHint explains an access failure on owner/repo, naming the next step.
func accessHint(owner, repo string, hadToken bool) string {
	if hadToken {
		return fmt.Sprintf("the authenticated GitHub account may lack access to %s/%s (or the repository does not exist)", owner, repo)
	}
	return fmt.Sprintf("%s/%s may be private or missing and no GitHub credentials were found: run `gh auth login` or set GH_TOKEN", owner, repo)
}

func withAccessHint(err error, owner, repo string) error {
	var statusErr *apiStatusError
	if errors.As(err, &statusErr) && accessDenied(statusErr.status) {
		return fmt.Errorf("%w\n  hint: %s", err, accessHint(owner, repo, githubToken() != ""))
	}
	return err
}
