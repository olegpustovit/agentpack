package gateway

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
	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/obs"
	"github.com/olegpustovit/agentpack/internal/state"
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

func (g *Gateway) registerPush(mux routeRegistrar) {
	mux.HandleFunc("GET /api/push/key", func(w http.ResponseWriter, r *http.Request) {
		public, _, err := g.pushKeys(r.Context())
		if err != nil {
			authFailure(w, r, "push keys unavailable", err)
			return
		}
		network.WriteJSON(w, map[string]string{"public_key": public})
	})
	mux.HandleFunc("POST /api/push/subscription", func(w http.ResponseWriter, r *http.Request) {
		email := g.auth.email(r)
		if email == "" {
			http.Error(w, "push notifications need a signed-in browser", http.StatusForbidden)
			return
		}
		var input pushSubscription
		if !network.ReadJSON(w, r, &input) {
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
		node, err := g.store.NodeID(r.Context())
		if err != nil {
			authFailure(w, r, "push subscription unavailable", err)
			return
		}
		// The browser subscribed with the key GET /api/push/key returned: the current one.
		if err := g.store.Push().Subscribe(r.Context(), state.PushSubscriber{Endpoint: input.Endpoint, Email: email, P256dh: input.Keys.P256dh, Auth: input.Keys.Auth, Public: public, Node: node}); err != nil {
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
		if email == "" || !network.ReadJSON(w, r, &input) {
			if email == "" {
				http.Error(w, "push notifications need a signed-in browser", http.StatusForbidden)
			}
			return
		}
		if err := g.store.Push().Unsubscribe(r.Context(), input.Endpoint, email); err != nil {
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
		if email == "" || !network.ReadJSON(w, r, &input) {
			if email == "" {
				http.Error(w, "push notifications need a signed-in browser", http.StatusForbidden)
			}
			return
		}
		subs, err := g.store.Push().Subscribers(r.Context(), state.PushFilter{Endpoint: input.Endpoint, Email: email})
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

// pushKeys returns the mesh's current VAPID key pair, creating one on first use.
func (g *Gateway) pushKeys(ctx context.Context) (public, private string, err error) {
	return g.store.Push().Keys(ctx, webpush.GenerateVAPIDKeys)
}

func (g *Gateway) sendPush(ctx context.Context, sub state.PushSubscriber, message pushMessage) error {
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
	response, err := webpush.SendNotificationWithContext(ctx, payload, &webpush.Subscription{Endpoint: sub.Endpoint, Keys: webpush.Keys{P256dh: sub.P256dh, Auth: sub.Auth}},
		&webpush.Options{HTTPClient: client, Subscriber: pushContact, VAPIDPublicKey: sub.Public, VAPIDPrivateKey: sub.Private, TTL: pushTTL, Urgency: webpush.UrgencyHigh, Topic: pushTopic(message.Tag)})
	if err != nil {
		return errors.New("push service unreachable")
	}
	defer response.Body.Close()
	// Push services explain a refusal in a short body ({"reason":"BadJwtToken"}); it holds no secret.
	body, _ := io.ReadAll(io.LimitReader(response.Body, 4<<10))
	switch {
	case response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusGone:
		// The browser dropped the subscription; forget it.
		return g.store.Push().Unsubscribe(ctx, sub.Endpoint, "")
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
		node, _ := g.store.NodeID(ctx)
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
	local, err := g.store.NodeID(ctx)
	if err != nil {
		return 0, err
	}
	roles, err := g.store.Main(ctx)
	if err != nil {
		return 0, err
	}
	// Main notifies every device. Without a Main each gateway notifies the devices it subscribed.
	scope := ""
	switch {
	case roles.ActsAsMain(local):
	case roles.MainNode == "":
		scope = local
	default:
		g.push.seen = nil
		return 0, nil
	}
	subscribed, err := g.store.Push().Subscribed(ctx, scope)
	if err != nil {
		return 0, err
	}
	if !subscribed {
		g.push.seen = nil
		return 0, nil
	}
	rows, err := g.store.Reader.QueryContext(ctx, `SELECT id,node_id,name,directory,worktree_of,definition_root,harness,conversation_state,owner_email,last_seq,waiting FROM sessions
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
		subs, err := g.store.Push().Subscribers(ctx, state.PushFilter{Node: scope, Recipient: &s.owner})
		if err != nil {
			failures = append(failures, err)
			continue
		}
		for _, sub := range subs {
			// The device opens the link on the gateway it subscribed through, which calls itself "local".
			machine := s.node
			if machine == sub.Node || machine == "" {
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
		name, err := g.store.Mesh().NodeName(ctx, s.node)
		if err != nil {
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
	request, err := pendingPermission(ctx, g.store.Reader, s.id)
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
 WHERE session_id=? AND kind='permission_request'
 AND NOT EXISTS(SELECT 1 FROM conversation_events r WHERE r.session_id=e.session_id AND r.seq>e.seq
  AND r.kind='permission_result'
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
