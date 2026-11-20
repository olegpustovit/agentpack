package network

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// pushDevice is a browser subscription: it decrypts what the push service receives (RFC 8291).
type pushDevice struct {
	key  *ecdh.PrivateKey
	auth []byte
}

func newPushDevice(t *testing.T) pushDevice {
	t.Helper()
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	_, _ = rand.Read(auth)
	return pushDevice{key, auth}
}

func (d pushDevice) subscription(endpoint string) string {
	encode := base64.RawURLEncoding.EncodeToString
	// Shaped like PushSubscription.toJSON(), expirationTime included.
	return `{"endpoint":"` + endpoint + `","expirationTime":null,"keys":{"p256dh":"` + encode(d.key.PublicKey().Bytes()) + `","auth":"` + encode(d.auth) + `"}}`
}

func (d pushDevice) decrypt(t *testing.T, body []byte) pushMessage {
	t.Helper()
	salt, idLen := body[:16], int(body[20])
	server, err := ecdh.P256().NewPublicKey(body[21 : 21+idLen])
	if err != nil {
		t.Fatal(err)
	}
	shared, err := d.key.ECDH(server)
	if err != nil {
		t.Fatal(err)
	}
	info := append(append([]byte("WebPush: info\x00"), d.key.PublicKey().Bytes()...), server.Bytes()...)
	ikm, _ := hkdf.Key(sha256.New, shared, d.auth, string(info), 32)
	cek, _ := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: aes128gcm\x00", 16)
	nonce, _ := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: nonce\x00", 12)
	block, _ := aes.NewCipher(cek)
	gcm, _ := cipher.NewGCM(block)
	if size := binary.BigEndian.Uint32(body[16:20]); size == 0 {
		t.Fatal("record size missing")
	}
	plain, err := gcm.Open(nil, nonce, body[21+idLen:], nil)
	if err != nil {
		t.Fatal(err)
	}
	plain = bytes.TrimRight(plain, "\x00")
	var message pushMessage
	if err := json.Unmarshal(plain[:len(plain)-1], &message); err != nil {
		t.Fatal(err, string(plain))
	}
	return message
}

type pushService struct {
	mu       sync.Mutex
	bodies   [][]byte
	status   int
	requests []*http.Request
}

func (p *pushService) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.bodies = append(p.bodies, body)
	p.requests = append(p.requests, r)
	status := p.status
	if status == 0 {
		status = http.StatusCreated
	}
	// Like Apple: the VAPID subject must be an https: or mailto: contact, never the loopback origin.
	if !vapidSubjectValid(r.Header.Get("Authorization")) {
		status = http.StatusForbidden
	}
	w.WriteHeader(status)
}

func vapidSubjectValid(authorization string) bool {
	token, _, _ := strings.Cut(strings.TrimPrefix(authorization, "vapid t="), ",")
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return false
	}
	claims, err := base64.RawURLEncoding.DecodeString(parts[1])
	var jwt struct {
		Sub string `json:"sub"`
	}
	if err != nil || json.Unmarshal(claims, &jwt) != nil {
		return false
	}
	contact, err := url.Parse(jwt.Sub)
	return err == nil && (contact.Scheme == "https" && contact.Host != "" && !strings.HasPrefix(contact.Hostname(), "127.") || contact.Scheme == "mailto" && strings.Contains(contact.Opaque, "@"))
}

func (p *pushService) take() ([][]byte, []*http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	bodies, requests := p.bodies, p.requests
	p.bodies, p.requests = nil, nil
	return bodies, requests
}

func TestPushSubscriptionsAndTransitions(t *testing.T) {
	g, origin := identityGateway(t, t.TempDir(), nil)
	service := &pushService{}
	server := httptest.NewTLSServer(service)
	defer server.Close()
	g.push.client = server.Client()
	previous := pushServices
	pushServices = append(slices.Clone(pushServices), "127.0.0.1")
	t.Cleanup(func() { pushServices = previous })
	testBrowserSession(t, g.auth, testBrowserEmail, "owner")
	testBrowserSession(t, g.auth, "member@example.com", "member")
	handler := g.Handler()
	call := func(method, path, body, token string) int {
		r := httptest.NewRequest(method, origin.String()+path, strings.NewReader(body))
		r.Header.Set("Origin", origin.String())
		if token != "" {
			r.AddCookie(&http.Cookie{Name: "agentpack_session", Value: token})
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Code
	}
	owner, member := newPushDevice(t), newPushDevice(t)
	if code := call("POST", "/api/push/subscription", owner.subscription(server.URL+"/owner"), ""); code != 401 {
		t.Fatal("anonymous subscribe", code)
	}
	if code := call("POST", "/api/push/subscription", owner.subscription("http://push.example/owner"), "owner"); code != 400 {
		t.Fatal("plain HTTP endpoint", code)
	}
	if code := call("POST", "/api/push/subscription", owner.subscription("https://metadata.internal/owner"), "owner"); code != 400 {
		t.Fatal("endpoint outside the browser push services", code)
	}
	if code := call("POST", "/api/push/subscription", owner.subscription(server.URL+"/owner"), "owner"); code != 204 {
		t.Fatal("subscribe", code)
	}
	if code := call("POST", "/api/push/subscription", member.subscription(server.URL+"/member"), "member"); code != 204 {
		t.Fatal("member subscribe", code)
	}
	if code := call("POST", "/api/push/test", `{"endpoint":"`+server.URL+`/owner"}`, "owner"); code != 204 {
		t.Fatal("test push", code)
	}
	if bodies, _ := service.take(); len(bodies) != 1 || owner.decrypt(t, bodies[0]).Tag != "agentpack-test" {
		t.Fatal("test push not delivered", len(bodies))
	}
	if code := call("POST", "/api/push/test", `{"endpoint":"`+server.URL+`/owner"}`, "member"); code != 404 {
		t.Fatal("member tested another account's subscription", code)
	}

	// A locally started session (no owner email) belongs to the owner account.
	db := g.auth.store.DB
	if _, err := db.Exec(`INSERT INTO sessions(id,name,directory,harness,transport,conversation_state,node_id)
 VALUES ('session-a','','/work/api','claude','acp','working',(SELECT node_id FROM local_node WHERE id=1))`); err != nil {
		t.Fatal(err)
	}
	if count, err := g.pushPass(t.Context()); err != nil || count != 0 {
		t.Fatal("baseline sent", count, err)
	}
	request := `{"seq":4,"kind":"permission_request","payload":{"requestId":"p1","tool":"execute","summary":"rm -rf private-secret","options":[{"id":"yes","label":"Allow","kind":"allow_once"},{"id":"always","label":"Always","kind":"allow_always"},{"id":"no","label":"Deny","kind":"reject_once"}]}}`
	if _, err := db.Exec(`INSERT INTO conversation_events(session_id,seq,body) VALUES ('session-a',4,?);
 UPDATE sessions SET conversation_state='needs_you',last_seq=4,title='private prompt text' WHERE id='session-a'`, request); err != nil {
		t.Fatal(err)
	}
	if count, err := g.pushPass(t.Context()); err != nil || count != 1 {
		t.Fatal("needs_you", count, err)
	}
	bodies, requests := service.take()
	if len(bodies) != 1 || requests[0].URL.Path != "/owner" || !strings.HasPrefix(requests[0].Header.Get("Authorization"), "vapid ") || requests[0].Header.Get("Topic") == "" {
		t.Fatalf("delivery %d %v", len(bodies), requests)
	}
	if strings.Contains(string(bodies[0]), "private") {
		t.Fatal("payload is not encrypted")
	}
	message := owner.decrypt(t, bodies[0])
	raw, _ := json.Marshal(message)
	if message.Title != "Claude Code needs you" || message.RequestID != "p1" || message.URL != "/sessions/local/session-a" ||
		len(message.Actions) != 2 || message.Actions[0].OptionID != "yes" || message.Actions[1].OptionID != "no" ||
		strings.Contains(string(raw), "private") {
		t.Fatalf("message %s", raw)
	}
	if count, _ := g.pushPass(t.Context()); count != 0 {
		t.Fatal("repeated needs_you", count)
	}

	// Answered, then finished: one "finished" notification, deduplicated by sequence.
	if _, err := db.Exec(`UPDATE sessions SET conversation_state='working',last_seq=5 WHERE id='session-a'`); err != nil {
		t.Fatal(err)
	}
	_, _ = g.pushPass(t.Context())
	if _, err := db.Exec(`UPDATE sessions SET conversation_state='idle',last_seq=6 WHERE id='session-a'`); err != nil {
		t.Fatal(err)
	}
	if count, err := g.pushPass(t.Context()); err != nil || count != 1 {
		t.Fatal("finished", count, err)
	}
	bodies, _ = service.take()
	if message := owner.decrypt(t, bodies[0]); message.Title != "Claude Code finished" || message.Body != "Ready for review in api" {
		t.Fatalf("finished %+v", message)
	}

	// A gone endpoint is forgotten; a revoked account receives nothing.
	service.status = http.StatusGone
	if _, err := db.Exec(`UPDATE sessions SET conversation_state='needs_you',last_seq=7 WHERE id='session-a'`); err != nil {
		t.Fatal(err)
	}
	if _, err := g.pushPass(t.Context()); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := g.auth.store.Reader.QueryRow("SELECT count(*) FROM push_subscriptions WHERE email=?", testBrowserEmail).Scan(&left); err != nil || left != 0 {
		t.Fatal("gone subscription kept", left, err)
	}
	service.status = 0
	if _, err := db.Exec(`UPDATE users SET disabled=1 WHERE email='member@example.com';
 INSERT INTO sessions(id,directory,harness,transport,conversation_state,owner_email) VALUES ('session-b','/w','codex','acp','idle','member@example.com')`); err != nil {
		t.Fatal(err)
	}
	_, _ = g.pushPass(t.Context())
	if _, err := db.Exec(`UPDATE sessions SET conversation_state='needs_you',last_seq=1 WHERE id='session-b'`); err != nil {
		t.Fatal(err)
	}
	if count, err := g.pushPass(t.Context()); err != nil || count != 0 {
		t.Fatal("disabled account notified", count, err)
	}
	if code := call("DELETE", "/api/push/subscription", `{"endpoint":"`+server.URL+`/member"}`, "member"); code != 401 {
		t.Fatal("disabled account kept its login", code)
	}
}

func TestPushMainSendsForEveryNode(t *testing.T) {
	g, _ := identityGateway(t, t.TempDir(), nil)
	service := &pushService{}
	server := httptest.NewTLSServer(service)
	defer server.Close()
	g.push.client = server.Client()
	testBrowserSession(t, g.auth, testBrowserEmail, "owner")
	db := g.auth.store.DB
	local, err := g.auth.store.NodeID(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	public, _, err := g.pushKeys(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	// A subscription that replicated from a laptop gateway, and one this gateway made.
	laptop, here := newPushDevice(t), newPushDevice(t)
	encode := base64.RawURLEncoding.EncodeToString
	for _, device := range []struct {
		device     pushDevice
		path, node string
	}{{laptop, "/laptop", "site:laptop"}, {here, "/here", local}} {
		if _, err := db.Exec(`INSERT INTO push_subscriptions(endpoint,email,p256dh,auth,public_key,created_node,created_at) VALUES (?,?,?,?,?,?,?)`,
			server.URL+device.path, testBrowserEmail, encode(device.device.key.PublicKey().Bytes()), encode(device.device.auth), public, device.node, time.Now().UnixMilli()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO sessions(id,directory,harness,transport,conversation_state,node_id) VALUES ('s','/w','claude','acp','working',?)`, local); err != nil {
		t.Fatal(err)
	}
	transition := func() int {
		t.Helper()
		for _, state := range []string{"working", "needs_you"} {
			if _, err := db.Exec(`UPDATE sessions SET conversation_state=?,last_seq=last_seq+1 WHERE id='s'`, state); err != nil {
				t.Fatal(err)
			}
			count, err := g.pushPass(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if state == "needs_you" {
				return count
			}
		}
		return 0
	}
	setMain := func(node string) {
		t.Helper()
		if _, err := db.Exec(`INSERT INTO mesh_settings(key,value) VALUES ('main_node',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, node); err != nil {
			t.Fatal(err)
		}
	}

	// Without a Main, each gateway notifies the devices it subscribed.
	_, _ = g.pushPass(t.Context())
	if count := transition(); count != 1 {
		t.Fatal("no-Main fallback", count)
	}
	if _, requests := service.take(); len(requests) != 1 || requests[0].URL.Path != "/here" {
		t.Fatal("no-Main fallback reached", requests)
	}

	// Main notifies every device; the laptop's device links through its own gateway to this node.
	setMain(local)
	if count, err := g.pushPass(t.Context()); err != nil || count != 0 {
		t.Fatal("becoming Main re-announced", count, err)
	}
	if count := transition(); count != 2 {
		t.Fatal("Main", count)
	}
	bodies, requests := service.take()
	for i, request := range requests {
		if request.URL.Path == "/laptop" {
			if message := laptop.decrypt(t, bodies[i]); message.Machine != local || message.URL != "/sessions/"+url.PathEscape(local)+"/s" {
				t.Fatalf("laptop link %+v", message)
			}
		} else if message := here.decrypt(t, bodies[i]); message.Machine != "local" {
			t.Fatalf("local link %+v", message)
		}
	}

	// Another node is Main: this one stays quiet.
	setMain("site:laptop")
	if count := transition(); count != 0 {
		t.Fatal("non-Main sent", count)
	}

	// An older key pair from another node wins; existing subscriptions keep signing with their own.
	setMain(local)
	_, _ = g.pushPass(t.Context())
	if _, err := db.Exec(`INSERT INTO push_keys(public_key,private_key,created_at) VALUES ('older','older-private',1)`); err != nil {
		t.Fatal(err)
	}
	if current, _, err := g.pushKeys(t.Context()); err != nil || current != "older" {
		t.Fatal("older key did not win", current, err)
	}
	if count := transition(); count != 2 {
		t.Fatal("after key change", count)
	}
	if _, requests := service.take(); !strings.Contains(requests[0].Header.Get("Authorization"), "k="+public) {
		t.Fatal("subscription signed with another key", requests[0].Header.Get("Authorization"))
	}
}
