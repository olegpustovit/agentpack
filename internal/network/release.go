package network

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

type agentpackRelease struct {
	TagName string `json:"tag_name"`
	HTMLURL string `json:"html_url"`
}

func latestAgentpackVersion(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/olegpustovit/agentpack/releases/latest", nil)
	if err != nil {
		http.Error(w, "could not check the latest release", http.StatusBadGateway)
		return
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		http.Error(w, "could not check the latest release", http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		http.Error(w, "could not check the latest release", http.StatusBadGateway)
		return
	}
	var release agentpackRelease
	if err := json.NewDecoder(http.MaxBytesReader(w, response.Body, 64<<10)).Decode(&release); err != nil || !strings.HasPrefix(release.TagName, "v") || release.HTMLURL != "https://github.com/olegpustovit/agentpack/releases/tag/"+release.TagName {
		http.Error(w, "GitHub returned invalid release metadata", http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]string{"version": strings.TrimPrefix(release.TagName, "v"), "url": release.HTMLURL})
}
