package gateway

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
	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/obs"
)

func (g *Gateway) registerUpdates(mux routeRegistrar) {
	mux.HandleFunc("GET /api/version/latest", latestAgentpackVersion)
	mux.HandleFunc("GET /api/node/update", g.serveUpdateStatus)
	mux.HandleFunc("POST /api/node/update", g.serveGatewayUpdate)
}

func (g *Gateway) serveUpdateStatus(w http.ResponseWriter, r *http.Request) {
	g.updateMu.Lock()
	defer g.updateMu.Unlock()
	state := "idle"
	if g.updating {
		state = "running"
	} else if g.updateError != "" {
		state = "failed"
	}
	network.WriteJSON(w, map[string]string{"state": state, "error": g.updateError})
}

func (g *Gateway) serveGatewayUpdate(w http.ResponseWriter, r *http.Request) {
	if g.options.Update == nil {
		http.Error(w, "gateway updates are unavailable", http.StatusNotImplemented)
		return
	}
	version, status, err := g.updateTarget(r.Context())
	if err != nil {
		if status == http.StatusInternalServerError {
			meshReadError(w, r, err)
			return
		}
		if event := obs.From(r.Context()); event != nil {
			event.Fail(err)
		}
		http.Error(w, err.Error(), status)
		return
	}
	g.updateMu.Lock()
	if g.updating {
		g.updateMu.Unlock()
		http.Error(w, "an update is already running", http.StatusConflict)
		return
	}
	if !g.beginBackground(&g.operations) {
		g.updateMu.Unlock()
		http.Error(w, "gateway is shutting down", http.StatusServiceUnavailable)
		return
	}
	g.updating = true
	g.updateError = ""
	g.updateMu.Unlock()
	obs.Go("gateway.update", func() {
		defer g.operations.Done()
		ctx, event := obs.Start(obs.WithRecording(context.Background(), r.Context()), "gateway", "update")
		event.Set("target_version", version)
		err := g.options.Update(ctx, version)
		if err != nil {
			event.Fail(err)
		}
		event.End()
		g.updateMu.Lock()
		g.updating = false
		if err != nil {
			g.updateError = err.Error()
		}
		g.updateMu.Unlock()
	})
	network.WriteJSON(w, map[string]any{"started": true, "version": version})
}

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
	network.WriteJSON(w, map[string]string{"version": version, "url": url})
}

// updateTarget uses only the local release lookup and replicated inventory.
// Build metadata identifies the running tree but is not part of a release tag.
func (g *Gateway) updateTarget(ctx context.Context) (string, int, error) {
	current, err := semver.StrictNewVersion(strings.TrimPrefix(g.options.Version, "v"))
	if err != nil {
		return "", http.StatusConflict, errors.New("cannot update a gateway without a valid running version")
	}
	nodes, err := g.store.Mesh().Nodes(ctx)
	if err != nil {
		return "", http.StatusInternalServerError, err
	}
	newest := current
	for _, node := range nodes {
		if candidate, err := semver.StrictNewVersion(strings.TrimPrefix(node.AppVersion, "v")); err == nil && candidate.GreaterThan(newest) {
			newest = candidate
		}
	}
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
