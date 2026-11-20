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
	"strings"
	"sync"
	"testing"
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
	return `{"endpoint":"` + endpoint + `","keys":{"p256dh":"` + encode(d.key.PublicKey().Bytes()) + `","auth":"` + encode(d.auth) + `"}}`
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
	w.WriteHeader(status)
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
