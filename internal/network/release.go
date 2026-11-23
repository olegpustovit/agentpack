package network

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Masterminds/semver/v3"
)

// releaseURL is the GitHub latest-release endpoint; tests point it at a fake.
var releaseURL = "https://api.github.com/repos/olegpustovit/agentpack/releases/latest"

// One answer serves every tab, peer proxy and the updater for 15 minutes, so the
// version a machine shows is the version it installs and GitHub's anonymous limit holds.
var latestRelease struct {
	sync.Mutex
	version, url string
	at           time.Time
}

const releaseCacheTTL = 15 * time.Minute

func fetchLatestRelease(ctx context.Context) (version, url string, err error) {
	latestRelease.Lock()
	defer latestRelease.Unlock()
	if latestRelease.version != "" && time.Since(latestRelease.at) < releaseCacheTTL {
		return latestRelease.version, latestRelease.url, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, releaseURL, nil)
	if err != nil {
		return "", "", err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return "", "", errors.New("could not check the latest release")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", "", errors.New("could not check the latest release")
	}
	var release struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&release); err != nil ||
		!strings.HasPrefix(release.TagName, "v") ||
		release.HTMLURL != "https://github.com/olegpustovit/agentpack/releases/tag/"+release.TagName {
		return "", "", errors.New("GitHub returned invalid release metadata")
	}
	if _, err := semver.StrictNewVersion(strings.TrimPrefix(release.TagName, "v")); err != nil {
		return "", "", errors.New("GitHub returned invalid release metadata")
	}
	latestRelease.version, latestRelease.url, latestRelease.at = strings.TrimPrefix(release.TagName, "v"), release.HTMLURL, time.Now()
	return latestRelease.version, latestRelease.url, nil
}

func latestAgentpackVersion(w http.ResponseWriter, r *http.Request) {
	version, url, err := fetchLatestRelease(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]string{"version": version, "url": url})
}

// updateTarget uses only the local release lookup and replicated inventory.
// Build metadata identifies the running tree but is not part of a release tag.
func (g *Gateway) updateTarget(ctx context.Context) (string, int, error) {
	current, err := semver.StrictNewVersion(strings.TrimPrefix(g.options.Version, "v"))
	if err != nil {
		return "", http.StatusConflict, errors.New("cannot update a gateway without a valid running version")
	}
	rows, err := g.store.Reader.QueryContext(ctx, "SELECT DISTINCT app_version FROM mesh_nodes WHERE app_version<>''")
	if err != nil {
		return "", http.StatusInternalServerError, err
	}
	defer rows.Close()
	newest := current
	for rows.Next() {
		var version string
		if err := rows.Scan(&version); err != nil {
			return "", http.StatusInternalServerError, err
		}
		if candidate, err := semver.StrictNewVersion(strings.TrimPrefix(version, "v")); err == nil && candidate.GreaterThan(newest) {
			newest = candidate
		}
	}
	if err := rows.Err(); err != nil {
		return "", http.StatusInternalServerError, err
	}
	rows.Close()
	latest, _, releaseErr := fetchLatestRelease(ctx)
	if candidate, err := semver.StrictNewVersion(latest); err == nil && candidate.GreaterThan(newest) {
		newest = candidate
	}
	if newest == current {
		if releaseErr != nil {
			return "", http.StatusBadGateway, releaseErr
		}
		return "", http.StatusConflict, errors.New("already up to date")
	}
	target, err := newest.SetMetadata("")
	if err != nil {
		return "", http.StatusInternalServerError, err
	}
	return target.String(), http.StatusOK, nil
}
