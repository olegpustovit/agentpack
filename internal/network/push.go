package network

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/SherClockHolmes/webpush-go"

	"github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/obs"
)

// Web Push. A browser subscribes through the gateway origin it is using; the subscription and
// this gateway's VAPID keys stay in local, never-replicated tables. Every gateway watches the
// replicated sessions table and notifies its own subscribers, so one subscription receives each
// transition once, whichever machine runs the session.
//
// shortcut: subscriptions stay on the gateway that created them, so a phone subscribed through
// a laptop gateway hears nothing while that laptop is off; forward subscriptions to Main when
// that becomes a real complaint.

const (
	pushInterval     = 3 * time.Second
	pushTTL          = 60 * 60 // seconds; a notification older than an hour is noise
	pushMaxEndpoint  = 2048
	pushMaxKey       = 256
	pushSendDeadline = 10 * time.Second
)

// pushWatch is owned by the runPush goroutine; tests call pushPass directly.
type pushWatch struct {
	seen   map[string]string // session → last conversation_state; nil until the first baseline
	sent   map[string]string // session → kind:seq of the last notification
	client webpush.HTTPClient
}

type pushSubscription struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
}

type pushAction struct {
	Action   string `json:"action"`
	Title    string `json:"title"`
	OptionID string `json:"option_id"`
}

// pushMessage is the payload the service worker renders. It never carries prompt, reply or tool
// argument text: the session's own title is its first prompt, so only the name the user gave it,
// the folder and the agent name appear.
type pushMessage struct {
	Title     string       `json:"title"`
	Body      string       `json:"body"`
	Tag       string       `json:"tag"`
	URL       string       `json:"url"`
	Session   string       `json:"session,omitempty"`
	Machine   string       `json:"machine,omitempty"`
	RequestID string       `json:"request_id,omitempty"`
	Actions   []pushAction `json:"actions,omitempty"`
}

func (g *Gateway) registerPush(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/push/key", func(w http.ResponseWriter, r *http.Request) {
		public, _, err := g.pushKeys(r.Context())
		if err != nil {
			authFailure(w, r, "push keys unavailable", err)
			return
		}
		writeJSON(w, map[string]string{"public_key": public})
	})
	mux.HandleFunc("POST /api/push/subscription", func(w http.ResponseWriter, r *http.Request) {
		email := g.auth.email(r)
		if email == "" {
			http.Error(w, "push notifications need a signed-in browser", http.StatusForbidden)
			return
		}
		var input pushSubscription
		if !readJSON(w, r, &input) {
			return
		}
		if !validPushSubscription(input) {
			http.Error(w, "invalid push subscription", http.StatusBadRequest)
			return
		}
		if _, err := g.auth.store.DB.ExecContext(r.Context(), `INSERT INTO push_subscriptions(endpoint,email,p256dh,auth,created_at) VALUES (?,?,?,?,?)
 ON CONFLICT(endpoint) DO UPDATE SET email=excluded.email,p256dh=excluded.p256dh,auth=excluded.auth,created_at=excluded.created_at`,
			input.Endpoint, email, input.Keys.P256dh, input.Keys.Auth, time.Now().UnixMilli()); err != nil {
			authFailure(w, r, "push subscription unavailable", err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("DELETE /api/push/subscription", func(w http.ResponseWriter, r *http.Request) {
		email := g.auth.email(r)
		var input struct {
			Endpoint string `json:"endpoint"`
		}
		if email == "" || !readJSON(w, r, &input) {
			if email == "" {
				http.Error(w, "push notifications need a signed-in browser", http.StatusForbidden)
			}
			return
		}
		if _, err := g.auth.store.DB.ExecContext(r.Context(), "DELETE FROM push_subscriptions WHERE endpoint=? AND email=?", input.Endpoint, email); err != nil {
			authFailure(w, r, "push subscription unavailable", err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /api/push/test", func(w http.ResponseWriter, r *http.Request) {
		email := g.auth.email(r)
		var input struct {
			Endpoint string `json:"endpoint"`
		}
		if email == "" || !readJSON(w, r, &input) {
			if email == "" {
				http.Error(w, "push notifications need a signed-in browser", http.StatusForbidden)
			}
			return
		}
		subs, err := g.pushSubscribers(r.Context(), "s.endpoint=? AND s.email=?", input.Endpoint, email)
		if err != nil {
			authFailure(w, r, "push subscription unavailable", err)
			return
		}
		if len(subs) == 0 {
			http.Error(w, "this browser is not subscribed", http.StatusNotFound)
			return
		}
		message := pushMessage{Title: "agentpack", Body: "Notifications from this gateway reach this device.", Tag: "agentpack-test", URL: "/sessions"}
		if err := g.sendPush(r.Context(), subs[0], message); err != nil {
			if event := obs.From(r.Context()); event != nil {
				event.Fail(err)
			}
			http.Error(w, "The push service refused the notification. Turn push off and on again on this device.", http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// pushServices are the browser vendors' push hosts. Any other endpoint would let a signed-in
// account make the gateway post to an address of its choosing. Tests add their own server.
var pushServices = []string{"fcm.googleapis.com", "android.googleapis.com", "push.services.mozilla.com", "push.apple.com", "notify.windows.com"}

func knownPushService(host string) bool {
	for _, service := range pushServices {
		if host == service || strings.HasSuffix(host, "."+service) {
			return true
		}
	}
	return false
}

func validPushSubscription(input pushSubscription) bool {
	endpoint, err := url.Parse(input.Endpoint)
	return err == nil && endpoint.Scheme == "https" && knownPushService(endpoint.Hostname()) && endpoint.User == nil &&
		len(input.Endpoint) <= pushMaxEndpoint && input.Keys.P256dh != "" && input.Keys.Auth != "" &&
		len(input.Keys.P256dh) <= pushMaxKey && len(input.Keys.Auth) <= pushMaxKey
}

// pushKeys returns this gateway's VAPID key pair, creating it on first use.
func (g *Gateway) pushKeys(ctx context.Context) (public, private string, err error) {
	err = g.auth.store.Reader.QueryRowContext(ctx, "SELECT public_key,private_key FROM push_keys WHERE id=1").Scan(&public, &private)
	if !errors.Is(err, sql.ErrNoRows) {
		return public, private, err
	}
	private, public, err = webpush.GenerateVAPIDKeys()
	if err != nil {
		return "", "", err
	}
	if _, err = g.auth.store.DB.ExecContext(ctx, "INSERT OR IGNORE INTO push_keys(id,public_key,private_key) VALUES (1,?,?)", public, private); err != nil {
		return "", "", err
	}
	// A concurrent first request may have won; everyone uses the stored pair.
	err = g.auth.store.DB.QueryRowContext(ctx, "SELECT public_key,private_key FROM push_keys WHERE id=1").Scan(&public, &private)
	return public, private, err
}

type pushSubscriber struct {
	endpoint, email, p256dh, auth string
}

// pushSubscribers lists subscriptions whose account is enabled and that were created after the
// account's last sign-out everywhere or password change.
func (g *Gateway) pushSubscribers(ctx context.Context, where string, args ...any) ([]pushSubscriber, error) {
	rows, err := g.auth.store.Reader.QueryContext(ctx, `SELECT s.endpoint,s.email,s.p256dh,s.auth FROM push_subscriptions s
 JOIN users u ON u.email=s.email WHERE u.disabled=0 AND s.created_at>=u.sessions_valid_after AND `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []pushSubscriber
	for rows.Next() {
		var sub pushSubscriber
		if err := rows.Scan(&sub.endpoint, &sub.email, &sub.p256dh, &sub.auth); err != nil {
			return nil, err
		}
		result = append(result, sub)
	}
	return result, rows.Err()
}

func (g *Gateway) sendPush(ctx context.Context, sub pushSubscriber, message pushMessage) error {
	public, private, err := g.pushKeys(ctx)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(message)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, pushSendDeadline)
	defer cancel()
	client := g.push.client
	if client == nil {
		client = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	response, err := webpush.SendNotificationWithContext(ctx, payload, &webpush.Subscription{Endpoint: sub.endpoint, Keys: webpush.Keys{P256dh: sub.p256dh, Auth: sub.auth}},
		&webpush.Options{HTTPClient: client, Subscriber: g.origin.Scheme + "://" + g.origin.Host, VAPIDPublicKey: public, VAPIDPrivateKey: private, TTL: pushTTL, Urgency: webpush.UrgencyHigh, Topic: pushTopic(message.Tag)})
	if err != nil {
		return errors.New("push service unreachable")
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
	switch {
	case response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusGone:
		// The browser dropped the subscription; forget it.
		_, err := g.auth.store.DB.ExecContext(ctx, "DELETE FROM push_subscriptions WHERE endpoint=?", sub.endpoint)
		return err
	case response.StatusCode >= 300:
		return fmt.Errorf("push service returned HTTP %d", response.StatusCode)
	}
	return nil
}

// pushTopic lets the push service replace an undelivered notification for the same session.
// Topics are at most 32 URL-safe characters.
func pushTopic(tag string) string {
	digest, _ := digestOf(tag, nil)
	return digest[:32]
}

func (g *Gateway) runPush(ctx context.Context, interval time.Duration) {
	if g.logs != nil {
		node, _ := g.auth.store.NodeID(ctx)
		ctx = obs.WithSink(ctx, g.logs, obs.BuildMetadata(g.options.Version, node, "gateway"))
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		passCtx, event := obs.Start(ctx, "push.notify", "session transitions")
		count, err := g.pushPass(passCtx)
		if err != nil && ctx.Err() == nil {
			event.Fail(err)
		}
		if err != nil || count > 0 {
			event.Set("count", count)
			event.End()
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

type pushSession struct {
	id, node, name, directory, agent, state, owner string
	seq                                            int64
}

// pushPass sends one notification per new transition into needs_you or from working to idle.
// The first pass after subscriptions appear only records a baseline, so a restart never
// re-announces what is already waiting.
func (g *Gateway) pushPass(ctx context.Context) (int, error) {
	var subscribed bool
	if err := g.auth.store.Reader.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM push_subscriptions)").Scan(&subscribed); err != nil {
		return 0, err
	}
	if !subscribed {
		g.push.seen = nil
		return 0, nil
	}
	local, err := g.auth.store.NodeID(ctx)
	if err != nil {
		return 0, err
	}
	rows, err := g.auth.store.Reader.QueryContext(ctx, `SELECT id,node_id,name,directory,harness,conversation_state,owner_email,last_seq FROM sessions
 WHERE transport='acp' AND ended_at=0 AND archived_at=0 AND hidden=0`)
	if err != nil {
		return 0, err
	}
	var sessions []pushSession
	for rows.Next() {
		var s pushSession
		if err := rows.Scan(&s.id, &s.node, &s.name, &s.directory, &s.agent, &s.state, &s.owner, &s.seq); err != nil {
			rows.Close()
			return 0, err
		}
		sessions = append(sessions, s)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return 0, err
	}
	baseline := g.push.seen == nil
	previous := g.push.seen
	g.push.seen = make(map[string]string, len(sessions))
	if g.push.sent == nil {
		g.push.sent = map[string]string{}
	}
	sent := 0
	var failures []error
	for _, s := range sessions {
		g.push.seen[s.id] = s.state
		if baseline {
			continue
		}
		kind := ""
		switch before := previous[s.id]; {
		case s.state == "needs_you" && before != "needs_you":
			kind = "needs_you"
		case s.state == "idle" && before == "working":
			kind = "finished"
		}
		key := fmt.Sprintf("%s:%d", kind, s.seq)
		if kind == "" || g.push.sent[s.id] == key {
			continue
		}
		g.push.sent[s.id] = key
		message, err := g.pushMessageFor(ctx, s, kind, local)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		subs, err := g.pushSubscribers(ctx, "(s.email=? OR (?='' AND u.role='owner'))", s.owner, s.owner)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		for _, sub := range subs {
			if err := g.sendPush(ctx, sub, message); err != nil {
				failures = append(failures, err)
				continue
			}
			sent++
		}
	}
	for id := range g.push.sent {
		if _, ok := g.push.seen[id]; !ok {
			delete(g.push.sent, id)
		}
	}
	return sent, errors.Join(failures...)
}

func (g *Gateway) pushMessageFor(ctx context.Context, s pushSession, kind, local string) (pushMessage, error) {
	machine := s.node
	if machine == local || machine == "" {
		machine = "local"
	}
	label := s.name
	if label == "" {
		label = harness.DisplayName(harness.Target(s.agent))
	}
	folder := filepath.Base(filepath.Clean(s.directory))
	message := pushMessage{Tag: "session:" + s.id, Session: s.id, Machine: machine,
		URL: "/sessions/" + url.PathEscape(machine) + "/" + url.PathEscape(s.id)}
	if kind == "finished" {
		message.Title = label + " finished"
		message.Body = "Ready for review in " + folder
		return message, nil
	}
	message.Title = label + " needs you"
	message.Body = "Waiting for your answer in " + folder
	request, err := pendingPermission(ctx, g.auth.store.Reader, s.id)
	if err != nil || request.RequestID == "" {
		return message, err
	}
	message.Body = "Permission to " + permissionVerb(request.Tool) + " in " + folder
	message.RequestID = request.RequestID
	// Only options that declare their kind become buttons, so a button never answers "always".
	for _, option := range request.Options {
		switch option.Kind {
		case "allow_once":
			message.Actions = append(message.Actions, pushAction{Action: "allow", Title: "Allow once", OptionID: option.ID})
		case "reject_once":
			message.Actions = append(message.Actions, pushAction{Action: "deny", Title: "Deny", OptionID: option.ID})
		}
	}
	return message, nil
}

// permissionVerb names an ACP tool kind without any of the call's arguments.
func permissionVerb(kind string) string {
	switch kind {
	case "execute":
		return "run a command"
	case "edit", "delete", "move":
		return kind + " files"
	case "fetch":
		return "fetch a URL"
	case "read":
		return "read files"
	}
	return "use a tool"
}

type pendingPermissionRequest struct {
	RequestID string `json:"requestId"`
	Tool      string `json:"tool"`
	Options   []struct {
		ID   string `json:"id"`
		Kind string `json:"kind"`
	} `json:"options"`
}

// pendingPermission returns the newest permission request that has no result yet.
func pendingPermission(ctx context.Context, db *sql.DB, session string) (pendingPermissionRequest, error) {
	var request pendingPermissionRequest
	var payload string
	err := db.QueryRowContext(ctx, `SELECT json_extract(body,'$.payload') FROM conversation_events e
 WHERE session_id=? AND json_extract(body,'$.kind')='permission_request'
 AND NOT EXISTS(SELECT 1 FROM conversation_events r WHERE r.session_id=e.session_id AND r.seq>e.seq
  AND json_extract(r.body,'$.kind')='permission_result'
  AND json_extract(r.body,'$.payload.requestId')=json_extract(e.body,'$.payload.requestId'))
 ORDER BY seq DESC LIMIT 1`, session).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return request, nil
	}
	if err != nil {
		return request, err
	}
	return request, json.Unmarshal([]byte(payload), &request)
}
