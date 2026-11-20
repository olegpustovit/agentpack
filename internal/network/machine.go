package network

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"sync"
	"time"

	base "github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/obs"
)

type machineAgent struct {
	Harness   string `json:"harness"`
	Installed bool   `json:"installed"`
	Version   string `json:"version,omitempty"`
}

type machineInfo struct {
	Version   string         `json:"version"`
	OS        string         `json:"os"`
	Arch      string         `json:"arch"`
	Cores     int            `json:"cores"`
	Memory    uint64         `json:"memory,omitempty"`
	DiskFree  uint64         `json:"disk_free,omitempty"`
	DiskTotal uint64         `json:"disk_total,omitempty"`
	Agents    []machineAgent `json:"agents"`
}

func (g *Gateway) machine(w http.ResponseWriter, r *http.Request) {
	ctx, event := obs.Start(r.Context(), "gateway", "machine")
	defer event.End()
	info := machineInfo{Version: g.options.Version, OS: runtime.GOOS, Arch: runtime.GOARCH, Cores: runtime.NumCPU()}
	var err error
	if info.Memory, info.DiskFree, info.DiskTotal, err = systemResources(g.options.Home); err != nil {
		// Partial facts beat none: the page still shows agents and versions.
		event.Fail(err)
	}
	info.Agents = agentVersions(ctx)
	writeJSON(w, info)
}

type agentVersion struct {
	modified time.Time
	size     int64
	version  string
}

var (
	agentVersionMu    sync.Mutex
	agentVersionCache = map[string]agentVersion{}
	versionPattern    = regexp.MustCompile(`\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.-]+)?`)
	// agentVersionTimeout bounds one slow CLI; tests shorten it.
	agentVersionTimeout = 3 * time.Second
)

// agentVersions runs each installed CLI's --version in parallel, once per binary file:
// the cache key is the path plus size and mtime, so an upgrade is noticed on the next read.
func agentVersions(ctx context.Context) []machineAgent {
	targets := base.AllTargets()
	agents := make([]machineAgent, len(targets))
	var wait sync.WaitGroup
	for index, target := range targets {
		agents[index].Harness = string(target)
		path, err := base.ResolveBinary(target.Binary())
		if err != nil {
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		agents[index].Installed = true
		agentVersionMu.Lock()
		cached, ok := agentVersionCache[path]
		agentVersionMu.Unlock()
		if ok && cached.size == info.Size() && cached.modified.Equal(info.ModTime()) {
			agents[index].Version = cached.version
			continue
		}
		wait.Add(1)
		go func() {
			defer wait.Done()
			version := probeAgentVersion(ctx, path)
			agents[index].Version = version
			if ctx.Err() == nil {
				agentVersionMu.Lock()
				agentVersionCache[path] = agentVersion{modified: info.ModTime(), size: info.Size(), version: version}
				agentVersionMu.Unlock()
			}
		}()
	}
	wait.Wait()
	return agents
}

func probeAgentVersion(ctx context.Context, path string) string {
	ctx, cancel := context.WithTimeout(ctx, agentVersionTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, path, "--version")
	command.Stdin = nil
	// A CLI that leaves a child holding the pipe must not hold the request open.
	command.WaitDelay = 500 * time.Millisecond
	output, _ := command.CombinedOutput()
	if len(output) > 4096 {
		output = output[:4096]
	}
	return versionPattern.FindString(string(output))
}
