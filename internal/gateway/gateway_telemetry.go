package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/obs"
)

type telemetryWindow struct {
	since time.Time
	count int
}

type browserError struct {
	Name        string           `json:"name"`
	UIBuildID   string           `json:"ui_build_id"`
	Message     string           `json:"message"`
	Stack       string           `json:"stack"`
	Traceparent string           `json:"traceparent"`
	Route       string           `json:"route"`
	Search      map[string]any   `json:"search"`
	Viewport    []int            `json:"viewport"`
	Breadcrumbs []map[string]any `json:"breadcrumbs"`
}

func (a *gatewayAuth) allowTelemetry(r *http.Request, count int, now time.Time) int {
	cookie, err := r.Cookie(cookieName(r))
	if err != nil {
		return http.StatusUnauthorized
	}
	key := sha256.Sum256([]byte(cookie.Value))
	if !a.authenticated(r) {
		return http.StatusUnauthorized
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.telemetry == nil {
		a.telemetry = make(map[[32]byte]telemetryWindow)
	}
	for key, window := range a.telemetry {
		if now.Sub(window.since) >= time.Minute {
			delete(a.telemetry, key)
		}
	}
	window := a.telemetry[key]
	if now.Sub(window.since) >= time.Minute {
		window = telemetryWindow{since: now}
	}
	if window.count+count > 60 {
		return http.StatusTooManyRequests
	}
	window.count += count
	a.telemetry[key] = window
	return http.StatusNoContent
}

func (g *Gateway) telemetry(w http.ResponseWriter, r *http.Request) {
	// Local bearer and peer transports do not authorize browser telemetry.
	email := g.auth.email(r)
	if email == "" {
		http.Error(w, "login required", http.StatusUnauthorized)
		return
	}
	var batch struct {
		Events []browserError `json:"events"`
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
	if err == nil {
		err = json.Unmarshal(data, &batch)
	}
	if err != nil {
		var oversized *http.MaxBytesError
		status := http.StatusBadRequest
		if errors.As(err, &oversized) {
			status = http.StatusRequestEntityTooLarge
		}
		http.Error(w, "invalid telemetry batch", status)
		return
	}
	if len(batch.Events) == 0 || len(batch.Events) > 60 {
		http.Error(w, "invalid telemetry event count", http.StatusBadRequest)
		return
	}
	if status := g.auth.allowTelemetry(r, len(batch.Events), time.Now()); status != http.StatusNoContent {
		http.Error(w, "telemetry limit reached", status)
		return
	}
	if g.logs == nil {
		http.Error(w, "diagnostics unavailable", http.StatusServiceUnavailable)
		return
	}
	nodeID, err := g.store.NodeID(r.Context())
	if err != nil {
		http.Error(w, "gateway state unavailable", http.StatusServiceUnavailable)
		return
	}
	metadata := obs.BuildMetadata(g.options.Version, nodeID, "ui")
	for _, input := range batch.Events {
		// The upload's HTTP span is separate from the original browser failure.
		ctx := obs.WithSink(obs.WithTrace(context.Background(), input.Traceparent), g.logs, metadata)
		_, event := obs.Start(ctx, "ui.error", input.Name)
		event.User = email
		event.Set("error_name", input.Name)
		event.Set("ui_build_id", input.UIBuildID)
		event.Set("message", input.Message)
		event.Set("route", input.Route)
		event.Set("search", input.Search)
		event.Set("browser", r.UserAgent())
		if len(input.Viewport) == 2 {
			event.Set("viewport", map[string]any{"width": input.Viewport[0], "height": input.Viewport[1]})
		}
		if len(input.Breadcrumbs) > 30 {
			input.Breadcrumbs = input.Breadcrumbs[len(input.Breadcrumbs)-30:]
		}
		breadcrumbs := make([]any, 0, len(input.Breadcrumbs))
		for _, crumb := range input.Breadcrumbs {
			clean := make(map[string]any)
			for _, key := range []string{"kind", "name", "trace_id", "status"} {
				clean[key] = crumb[key]
			}
			breadcrumbs = append(breadcrumbs, clean)
		}
		event.Set("breadcrumbs", breadcrumbs)
		event.Fail(errors.New(input.Message))
		event.Stack = strings.Clone(input.Stack)
		event.End()
	}
	w.WriteHeader(http.StatusNoContent)
}

func (g *Gateway) localBrowserTiming(r *http.Request) bool {
	return g.origin != nil && strings.EqualFold(r.Host, g.origin.Host) && loopbackPeer(r) &&
		r.Header.Get("Forwarded") == "" && r.Header.Get("X-Forwarded-For") == "" &&
		r.Header.Get("X-Forwarded-Host") == "" && r.Header.Get("X-Forwarded-Proto") == ""
}

func (g *Gateway) writeChatTiming(w http.ResponseWriter, r *http.Request) {
	if !g.localBrowserTiming(r) || g.auth.email(r) == "" {
		http.Error(w, "local browser session required", http.StatusForbidden)
		return
	}
	var input struct {
		Name       string  `json:"name"`
		DurationMS float64 `json:"duration_ms"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 256)
	if !network.ReadJSON(w, r, &input) {
		return
	}
	if input.Name != "chat-first-frame" && input.Name != "prompt-confirmed" || input.DurationMS < 0 || input.DurationMS > 120_000 {
		http.Error(w, "invalid chat timing", http.StatusBadRequest)
		return
	}
	g.chatTimings.Store(input.Name, input.DurationMS)
	w.WriteHeader(http.StatusNoContent)
}

func (g *Gateway) readChatTimings(w http.ResponseWriter, r *http.Request) {
	if !g.localBrowserTiming(r) || !g.localRequest(r) {
		http.Error(w, "local access only", http.StatusForbidden)
		return
	}
	samples := make(map[string]float64, 2)
	g.chatTimings.Range(func(name, duration any) bool {
		samples[name.(string)] = duration.(float64)
		return true
	})
	network.WriteJSON(w, samples)
}
