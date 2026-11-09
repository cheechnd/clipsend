package relay

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func testService(t *testing.T, limit int) (*Service, Request) {
	t.Helper()
	c := Config{KeyID: "ABCDEFGHIJ", TeamID: "KLMNOPQRST", KeyPath: "/tmp/not-read.p8", Listen: "127.0.0.1:8787", RateLimit: limit, AllowedBundleIDs: []string{"com.example.app"}, Credentials: []Credential{{Token: "0123456789abcdef0123456789abcdef", BundleIDs: []string{"com.example.app"}, ComputerIDs: []string{"computer_1"}}}}
	// Avoid key parsing in handler-only tests.
	s := &Service{cfg: c, credentials: map[[32]byte]credentialScope{}, rates: map[[32]byte][]time.Time{}, now: time.Now, gate: make(chan struct{}, 32)}
	h := sha256sum(c.Credentials[0].Token)
	s.credentials[h] = credentialScope{bundleIDs: map[string]bool{"com.example.app": true}, computerIDs: map[string]bool{"computer_1": true}}
	s.sender = func(context.Context, Request, string) error { return nil }
	s.provider = func(time.Time) (string, error) { return "mock", nil }
	return s, Request{DeviceToken: "aabb", BundleID: "com.example.app", Environment: "sandbox", Notification: Notification{Version: 1, ComputerID: "computer_1", TransferID: "transfer_1", Endpoints: []string{"https://192.168.1.2:12345"}}}
}
func sha256sum(s string) [32]byte { return sha256.Sum256([]byte(s)) }
func send(t *testing.T, s *Service, body []byte, token string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("POST", "/v1/notify", strings.NewReader(string(body)))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}
func marshal(r Request) []byte { b, _ := json.Marshal(r); return b }
func TestHandlerAuthScopeAndTopic(t *testing.T) {
	s, r := testService(t, 10)
	if w := send(t, s, marshal(r), ""); w.Code != 401 {
		t.Fatalf("unauth=%d", w.Code)
	}
	r.BundleID = "com.other.app"
	if w := send(t, s, marshal(r), s.cfg.Credentials[0].Token); w.Code != 400 {
		t.Fatalf("topic=%d", w.Code)
	}
	r.BundleID = "com.example.app"
	r.Notification.ComputerID = "other"
	if w := send(t, s, marshal(r), s.cfg.Credentials[0].Token); w.Code != 400 {
		t.Fatalf("computer scope=%d", w.Code)
	}
}
func TestHandlerRateLimitAndEndpoint(t *testing.T) {
	s, r := testService(t, 1)
	token := s.cfg.Credentials[0].Token
	if w := send(t, s, marshal(r), token); w.Code != 202 {
		t.Fatalf("accepted=%d", w.Code)
	}
	if w := send(t, s, marshal(r), token); w.Code != 429 {
		t.Fatalf("limited=%d", w.Code)
	}
	s, _ = testService(t, 10)
	r.Notification.Endpoints = []string{"https://example.com:443"}
	if w := send(t, s, marshal(r), token); w.Code != 400 {
		t.Fatalf("public endpoint=%d", w.Code)
	}
}
func TestStrictRejectsDuplicateAndUnknown(t *testing.T) {
	var r Request
	if strict([]byte(`{"deviceToken":"aa","deviceToken":"bb"}`), &r) == nil {
		t.Fatal("duplicate accepted")
	}
	if strict([]byte(`{"unknown":1}`), &r) == nil {
		t.Fatal("unknown accepted")
	}
}
func TestCredentialNotReflected(t *testing.T) {
	s, _ := testService(t, 10)
	secret := s.cfg.Credentials[0].Token
	w := send(t, s, []byte(`{}`), secret)
	if strings.Contains(w.Body.String(), secret) {
		t.Fatal("credential reflected")
	}
}

func TestHandlerCallsMockAPNsOnce(t *testing.T) {
	s, r := testService(t, 10)
	calls := 0
	s.sender = func(_ context.Context, got Request, jwt string) error {
		calls++
		if jwt != "mock" || got.Notification.TransferID != r.Notification.TransferID {
			t.Fatal("unexpected upstream request")
		}
		return nil
	}
	if w := send(t, s, marshal(r), s.cfg.Credentials[0].Token); w.Code != 202 {
		t.Fatalf("status=%d", w.Code)
	}
	if calls != 1 {
		t.Fatalf("APNs calls=%d", calls)
	}
}
func TestClientURLPolicy(t *testing.T) {
	for _, tc := range []struct {
		u  string
		ok bool
	}{{"https://relay.example.com", true}, {"https://192.168.1.2", true}, {"http://127.0.0.1:8787", true}, {"http://relay.example.com", false}, {"https://user@relay.example.com", false}, {"https://relay.example.com/path", false}} {
		u, _ := url.Parse(tc.u)
		if validRelayURL(u) != tc.ok {
			t.Errorf("%s", tc.u)
		}
	}
}

func TestLocalNotificationEnforcesScope(t *testing.T) {
	s, r := testService(t, 10)
	calls := 0
	s.sender = func(context.Context, Request, string) error { calls++; return nil }
	token := s.cfg.Credentials[0].Token
	if err := s.NotifyLocal(context.Background(), "wrong", r); err == nil {
		t.Fatal("accepted wrong credential")
	}
	bad := r
	bad.BundleID = "com.other.app"
	if err := s.NotifyLocal(context.Background(), token, bad); err == nil {
		t.Fatal("accepted wrong topic")
	}
	bad = r
	bad.Notification.ComputerID = "other"
	if err := s.NotifyLocal(context.Background(), token, bad); err == nil {
		t.Fatal("accepted wrong computer")
	}
	bad = r
	bad.Notification.Endpoints = []string{"https://example.com"}
	if err := s.NotifyLocal(context.Background(), token, bad); err == nil {
		t.Fatal("accepted public endpoint")
	}
	if calls != 0 {
		t.Fatal("sent rejected notification")
	}
	if err := s.NotifyLocal(context.Background(), token, r); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("expected one notification")
	}
}
