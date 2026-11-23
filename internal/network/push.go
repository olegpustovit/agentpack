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
	"regexp"
	"strings"
	"time"

	"github.com/SherClockHolmes/webpush-go"

	"github.com/olegpustovit/agentpack/internal/acp"
	"github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/obs"
)

// Web Push. A browser subscribes through the gateway origin it is using. VAPID key pairs and
// subscriptions replicate, secrets included, like MCP connections: Main watches the replicated
// sessions table and notifies every device, so a phone subscribed through a laptop still hears
// while that laptop is off. Without a Main each gateway notifies the subscriptions it created.
// Either way one subscription receives each transition once, whichever machine runs the session.

const (
	pushInterval     = 3 * time.Second
	pushTTL          = 60 * 60 // seconds; a notification older than an hour is noise
	pushMaxEndpoint  = 2048
	pushMaxKey       = 256
	pushSendDeadline = 10 * time.Second
	// pushContact is the VAPID "sub" claim. Apple refuses anything but an https: or mailto: URL with
	// HTTP 403 BadJwtToken, so it is never the gateway's loopback origin.
	pushContact = "https://github.com/olegpustovit/agentpack"
)

// pushWatch is owned by the runPush goroutine; tests call pushPass directly.
type pushWatch struct {
	seen   map[string]string // session → last conversation_state; nil until the first baseline
	sent   map[string]string // session → kind:seq of the last notification
	client webpush.HTTPClient
}

type pushSubscription struct {
	Endpoint       string `json:"endpoint"`
	ExpirationTime *int64 `json:"expirationTime"` // sent by every browser; unused
	Keys           struct {
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
	Timestamp int64        `json:"timestamp,omitempty"`
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
		public, _, err := g.pushKeys(r.Context())
		if err != nil {
			authFailure(w, r, "push keys unavailable", err)
			return
		}
		node, err := g.auth.store.NodeID(r.Context())
		if err != nil {
			authFailure(w, r, "push subscription unavailable", err)
			return
		}
		// The browser subscribed with the key GET /api/push/key returned: the current one.
		if _, err := g.auth.store.DB.ExecContext(r.Context(), `INSERT INTO push_subscriptions(endpoint,email,p256dh,auth,public_key,created_node,created_at) VALUES (?,?,?,?,?,?,?)
 ON CONFLICT(endpoint) DO UPDATE SET email=excluded.email,p256dh=excluded.p256dh,auth=excluded.auth,public_key=excluded.public_key,created_node=excluded.created_node,created_at=excluded.created_at`,
			input.Endpoint, email, input.Keys.P256dh, input.Keys.Auth, public, node, time.Now().UnixMilli()); err != nil {
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

// currentPushKey picks the mesh's VAPID key pair. Nodes that each created one before they met
// converge on the oldest, ties broken by public key.
const currentPushKey = "SELECT public_key,private_key FROM push_keys ORDER BY created_at,public_key LIMIT 1"

// pushKeys returns the mesh's current VAPID key pair, creating one on first use.
func (g *Gateway) pushKeys(ctx context.Context) (public, private string, err error) {
	err = g.auth.store.Reader.QueryRowContext(ctx, currentPushKey).Scan(&public, &private)
	if !errors.Is(err, sql.ErrNoRows) {
		return public, private, err
	}
	private, public, err = webpush.GenerateVAPIDKeys()
	if err != nil {
		return "", "", err
	}
	if _, err = g.auth.store.DB.ExecContext(ctx, "INSERT INTO push_keys(public_key,private_key,created_at) SELECT ?,?,? WHERE NOT EXISTS(SELECT 1 FROM push_keys)",
		public, private, time.Now().UnixMilli()); err != nil {
		return "", "", err
	}
	// A concurrent first request may have won; everyone uses the stored pair.
	err = g.auth.store.DB.QueryRowContext(ctx, currentPushKey).Scan(&public, &private)
	return public, private, err
}

type pushSubscriber struct {
	endpoint, email, p256dh, auth, node, public, private string
}

// pushSubscribers lists subscriptions whose account is enabled and that were created after the
// account's last sign-out everywhere or password change, with the key pair each was made with.
func (g *Gateway) pushSubscribers(ctx context.Context, where string, args ...any) ([]pushSubscriber, error) {
	rows, err := g.auth.store.Reader.QueryContext(ctx, `SELECT s.endpoint,s.email,s.p256dh,s.auth,s.created_node,k.public_key,k.private_key FROM push_subscriptions s
 JOIN users u ON u.email=s.email JOIN push_keys k ON k.public_key=s.public_key
 WHERE u.disabled=0 AND s.created_at>=u.sessions_valid_after AND `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []pushSubscriber
	for rows.Next() {
		var sub pushSubscriber
		if err := rows.Scan(&sub.endpoint, &sub.email, &sub.p256dh, &sub.auth, &sub.node, &sub.public, &sub.private); err != nil {
			return nil, err
		}
		result = append(result, sub)
	}
	return result, rows.Err()
}

func (g *Gateway) sendPush(ctx context.Context, sub pushSubscriber, message pushMessage) error {
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
		&webpush.Options{HTTPClient: client, Subscriber: pushContact, VAPIDPublicKey: sub.public, VAPIDPrivateKey: sub.private, TTL: pushTTL, Urgency: webpush.UrgencyHigh, Topic: pushTopic(message.Tag)})
	if err != nil {
		return errors.New("push service unreachable")
	}
	defer response.Body.Close()
	// Push services explain a refusal in a short body ({"reason":"BadJwtToken"}); it holds no secret.
	body, _ := io.ReadAll(io.LimitReader(response.Body, 4<<10))
	switch {
	case response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusGone:
		// The browser dropped the subscription; forget it.
		_, err := g.auth.store.DB.ExecContext(ctx, "DELETE FROM push_subscriptions WHERE endpoint=?", sub.endpoint)
		return err
	case response.StatusCode >= 300:
		return fmt.Errorf("push service returned HTTP %d: %.200s", response.StatusCode, strings.TrimSpace(string(body)))
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
	id, node, name, directory, worktreeOf, definitionRoot, agent, state, owner, waiting string
	seq                                                                                 int64
}

// pushPass sends one notification per new transition into needs_you or into idle from working or
// waiting. An idle chat with open background work counts as waiting, so it is not "finished" yet.
// The first pass after subscriptions appear only records a baseline, so a restart never
// re-announces what is already waiting, and neither does becoming Main.
func (g *Gateway) pushPass(ctx context.Context) (int, error) {
	local, err := g.auth.store.NodeID(ctx)
	if err != nil {
		return 0, err
	}
	roles, err := g.auth.store.Main(ctx)
	if err != nil {
		return 0, err
	}
	// Main notifies every device. Without a Main each gateway notifies the devices it subscribed.
	scope, scopeArgs := "1", []any{}
	switch {
	case roles.ActsAsMain(local):
	case roles.MainNode == "":
		scope, scopeArgs = "s.created_node=?", []any{local}
	default:
		g.push.seen = nil
		return 0, nil
	}
	var subscribed bool
	if err := g.auth.store.Reader.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM push_subscriptions s WHERE "+scope+")", scopeArgs...).Scan(&subscribed); err != nil {
		return 0, err
	}
	if !subscribed {
		g.push.seen = nil
		return 0, nil
	}
	rows, err := g.auth.store.Reader.QueryContext(ctx, `SELECT id,node_id,name,directory,worktree_of,definition_root,harness,conversation_state,owner_email,last_seq,waiting FROM sessions
 WHERE transport='acp' AND ended_at=0 AND archived_at=0 AND hidden=0`)
	if err != nil {
		return 0, err
	}
	var sessions []pushSession
	for rows.Next() {
		var s pushSession
		if err := rows.Scan(&s.id, &s.node, &s.name, &s.directory, &s.worktreeOf, &s.definitionRoot, &s.agent, &s.state, &s.owner, &s.seq, &s.waiting); err != nil {
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
	now := time.Now().UnixMilli()
	for _, s := range sessions {
		if s.state == "idle" && waitingOpen(s.waiting, now) {
			s.state = "waiting"
		}
		g.push.seen[s.id] = s.state
		if baseline {
			continue
		}
		kind := ""
		switch before := previous[s.id]; {
		case s.state == "needs_you" && before != "needs_you":
			kind = "needs_you"
		case s.state == "idle" && (before == "working" || before == "waiting"):
			kind = "finished"
		}
		key := fmt.Sprintf("%s:%d", kind, s.seq)
		if kind == "" || g.push.sent[s.id] == key {
			continue
		}
		g.push.sent[s.id] = key
		subs, err := g.pushSubscribers(ctx, "(s.email=? OR (?='' AND u.role='owner')) AND "+scope, append([]any{s.owner, s.owner}, scopeArgs...)...)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		for _, sub := range subs {
			// The device opens the link on the gateway it subscribed through, which calls itself "local".
			machine := s.node
			if machine == sub.node || machine == "" {
				machine = "local"
			}
			message, err := g.pushMessageFor(ctx, s, kind, machine)
			if err != nil {
				failures = append(failures, err)
				continue
			}
			message.Machine = machine
			message.URL = "/sessions/" + url.PathEscape(machine) + "/" + url.PathEscape(s.id)
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

// pushMessageFor leaves Machine and URL to the caller. The title is the project; the body names the
// agent, the machine when it is not the one the device subscribed through ("local"), the chat's
// user-given name, and what is needed. The first prompt never appears.
func (g *Gateway) pushMessageFor(ctx context.Context, s pushSession, kind, machine string) (pushMessage, error) {
	agent := harness.DisplayName(harness.Target(s.agent))
	if machine != "local" {
		var name string
		err := g.auth.store.Reader.QueryRowContext(ctx, "SELECT name FROM mesh_nodes WHERE node_id=?", s.node).Scan(&name)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return pushMessage{}, err
		}
		if name != "" {
			agent += " on " + name
		}
	}
	if s.name != "" {
		agent += " · " + s.name
	}
	message := pushMessage{Title: pushProject(s), Tag: "session:" + s.id, Session: s.id, Timestamp: time.Now().UnixMilli()}
	if kind == "finished" {
		message.Body = agent + "\nFinished · ready for review"
		return message, nil
	}
	message.Body = agent + "\nHas a question for you"
	request, err := pendingPermission(ctx, g.auth.store.Reader, s.id)
	if err != nil || request.RequestID == "" {
		return message, err
	}
	message.Body = agent + "\nWants to " + permissionVerb(request.Tool)
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

// pushProject names the folder a session belongs to, as the session list groups it: a worktree
// counts under its main folder (projectDirectory in ui/src/features/sessions/git.ts).
func pushProject(s pushSession) string {
	directory := s.worktreeOf
	if directory == "" && s.definitionRoot != "" && worktreePath.MatchString(s.directory) {
		directory = s.definitionRoot
	}
	if directory == "" {
		directory = s.directory
	}
	return filepath.Base(filepath.Clean(directory))
}

// worktreePath matches the folders agentpack creates for chat worktrees (WORKTREE_PATH in the UI).
var worktreePath = regexp.MustCompile(`[\\/]worktrees[\\/][0-9a-f]{16}[\\/][^\\/]+(?:[\\/]|$)`)

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

// waitingOpen reports whether a session's waiting column holds an item that has not expired.
func waitingOpen(raw string, now int64) bool {
	if raw == "" {
		return false
	}
	var items []acp.WaitItem
	if json.Unmarshal([]byte(raw), &items) != nil {
		return false
	}
	for _, item := range items {
		if item.Until == 0 || item.Until > now {
			return true
		}
	}
	return false
}
