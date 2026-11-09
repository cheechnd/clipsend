package relay

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func serviceFixture(t *testing.T) (*Service, *ecdsa.PrivateKey, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "AuthKey_TEST.p8")
	if err = os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	c := Config{KeyID: "ABCDEFGHIJ", TeamID: "KLMNOPQRST", KeyPath: keyPath, Listen: "127.0.0.1:0", RateLimit: 10, AllowedBundleIDs: []string{"com.example.app"}, Credentials: []Credential{{Token: "0123456789abcdef0123456789abcdef", BundleIDs: []string{"com.example.app"}, ComputerIDs: []string{"computer_1"}}}}
	svc, err := NewService(c)
	if err != nil {
		t.Fatal(err)
	}
	return svc, key, keyPath
}
func validAPNSRequest() Request {
	return Request{DeviceToken: "aabb", BundleID: "com.example.app", Environment: "sandbox", Notification: Notification{Version: 1, ComputerID: "computer_1", TransferID: "transfer_1", Endpoints: []string{"https://192.168.1.2:12345"}}}
}
func testHTTP2Transport(server *httptest.Server) *http.Transport {
	p := new(http.Protocols)
	p.SetHTTP2(true)
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	return &http.Transport{Protocols: p, ForceAttemptHTTP2: true, Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, ServerName: "127.0.0.1"}, DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", server.Listener.Addr().String())
	}}
}
func verifyJWT(t *testing.T, token string, key *ecdsa.PrivateKey) map[string]any {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatal("malformed JWT")
	}
	head, _ := base64.RawURLEncoding.DecodeString(parts[0])
	claims, _ := base64.RawURLEncoding.DecodeString(parts[1])
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	var h, c map[string]any
	if json.Unmarshal(head, &h) != nil || json.Unmarshal(claims, &c) != nil || len(sig) != 64 {
		t.Fatal("invalid JWT fields")
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if !ecdsa.Verify(&key.PublicKey, digest[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Fatal("JWT signature invalid")
	}
	if h["alg"] != "ES256" || h["kid"] != "ABCDEFGHIJ" || c["iss"] != "KLMNOPQRST" {
		t.Fatalf("unexpected JWT claims: %v %v", h, c)
	}
	return c
}
func TestProviderTokenES256ReuseAndRotation(t *testing.T) {
	s, key, _ := serviceFixture(t)
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	one, e := s.providerToken(now)
	if e != nil {
		t.Fatal(e)
	}
	claim := verifyJWT(t, one, key)
	if claim["iat"] != float64(now.Unix()) {
		t.Fatalf("iat=%v", claim["iat"])
	}
	same, e := s.providerToken(now.Add(49 * time.Minute))
	if e != nil || same != one {
		t.Fatal("token not reused within 50 minutes")
	}
	rotated, e := s.providerToken(now.Add(50 * time.Minute))
	if e != nil || rotated == one {
		t.Fatal("token not rotated at 50 minutes")
	}
	verifyJWT(t, rotated, key)
}
func TestSendAPNsHTTP2PayloadAndHeaders(t *testing.T) {
	s, key, _ := serviceFixture(t)
	var calls atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.ProtoMajor != 2 {
			t.Errorf("protocol=%s", r.Proto)
		}
		if r.URL.Path != "/3/device/aabb" {
			t.Errorf("path=%s", r.URL.Path)
		}
		if r.Header.Get("apns-topic") != "com.example.app" || r.Header.Get("apns-push-type") != "alert" || r.Header.Get("apns-priority") != "10" {
			t.Errorf("APNs headers=%v", r.Header)
		}
		expiry, e := strconv.ParseInt(r.Header.Get("apns-expiration"), 10, 64)
		if e != nil || expiry < time.Now().Unix()+110 || expiry > time.Now().Unix()+121 {
			t.Errorf("expiration=%d err=%v", expiry, e)
		}
		token := strings.TrimPrefix(r.Header.Get("authorization"), "bearer ")
		verifyJWT(t, token, key)
		b, _ := io.ReadAll(r.Body)
		var p map[string]any
		if json.Unmarshal(b, &p) != nil {
			t.Error("invalid APNs payload")
		}
		if strings.Contains(strings.ToLower(string(b)), "clipboard") || strings.Contains(string(b), "secret text") {
			t.Errorf("sensitive payload: %s", b)
		}
		aps := p["aps"].(map[string]any)
		alert := aps["alert"].(map[string]any)
		if alert["title"] != "Text ready" || alert["body"] != "Tap to receive in ClipSend." {
			t.Errorf("alert=%v", alert)
		}
		meta := p["clipsend"].(map[string]any)
		if meta["computerID"] != "computer_1" || meta["transferID"] != "transfer_1" {
			t.Errorf("metadata=%v", meta)
		}
		w.WriteHeader(200)
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()
	s.apnsBaseURL = server.URL
	s.transportFactory = func() *http.Transport { return testHTTP2Transport(server) }
	jwt, e := s.providerToken(time.Now())
	if e != nil {
		t.Fatal(e)
	}
	if e = s.sendAPNs(context.Background(), validAPNSRequest(), jwt); e != nil {
		t.Fatal(e)
	}
	if calls.Load() != 1 {
		t.Fatalf("requests=%d", calls.Load())
	}
}
func TestSendAPNsRejectsTLSAndALPN(t *testing.T) {
	s, _, _ := serviceFixture(t)
	plain := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer plain.Close()
	s.apnsBaseURL = plain.URL
	s.transportFactory = func() *http.Transport { return privateTransport() }
	if e := s.sendAPNs(context.Background(), validAPNSRequest(), "jwt"); e == nil || strings.Contains(e.Error(), "127.0.0.1") {
		t.Fatalf("TLS error not safely redacted: %v", e)
	}
	h1 := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	h1.TLS = &tls.Config{NextProtos: []string{"http/1.1"}}
	h1.StartTLS()
	defer h1.Close()
	s.apnsBaseURL = h1.URL
	s.transportFactory = func() *http.Transport { return testHTTP2Transport(h1) }
	if e := s.sendAPNs(context.Background(), validAPNSRequest(), "jwt"); e == nil {
		t.Fatal("non-H2 APNs accepted")
	}
}
func TestSendAPNsRejectionRedirectAndNoReplay(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		body     string
		redirect bool
		wantErr  bool
	}{{"rejection", 400, "provider-secret", false, true}, {"redirect", 307, "", true, true}} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _ := serviceFixture(t)
			var first, second atomic.Int32
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/target" {
					second.Add(1)
					w.WriteHeader(200)
					return
				}
				first.Add(1)
				if tc.redirect {
					w.Header().Set("Location", "/target")
				}
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			server.EnableHTTP2 = true
			server.StartTLS()
			defer server.Close()
			s.apnsBaseURL = server.URL
			s.transportFactory = func() *http.Transport { return testHTTP2Transport(server) }
			err := s.sendAPNs(context.Background(), validAPNSRequest(), "provider-secret")
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v", err)
			}
			if err != nil && strings.Contains(err.Error(), "provider-secret") {
				t.Fatal("provider response leaked")
			}
			if first.Load() != 1 || second.Load() != 0 {
				t.Fatalf("requests first=%d redirect=%d", first.Load(), second.Load())
			}
		})
	}
}
func TestNewServicePrivateKeyAndConfigFiles(t *testing.T) {
	s, _, keyPath := serviceFixture(t)
	_ = s
	cfg := Config{KeyID: "ABCDEFGHIJ", TeamID: "KLMNOPQRST", KeyPath: keyPath, Listen: "127.0.0.1:0", RateLimit: 10, AllowedBundleIDs: []string{"com.example.app"}, Credentials: []Credential{{Token: "0123456789abcdef0123456789abcdef", BundleIDs: []string{"com.example.app"}, ComputerIDs: []string{"computer_1"}}}}
	path := filepath.Join(t.TempDir(), "config.json")
	b, _ := json.Marshal(cfg)
	if e := os.WriteFile(path, b, 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := LoadConfig(path); e != nil {
		t.Fatal(e)
	}
	for _, malformed := range [][]byte{
		[]byte(`{"keyID":"ABCDEFGHIJ","keyID":"ABCDEFGHIJ"}`),
		[]byte(`{"unexpected":true}`),
	} {
		if e := os.WriteFile(path, malformed, 0600); e != nil {
			t.Fatal(e)
		}
		if _, e := LoadConfig(path); e == nil {
			t.Fatal("malformed config accepted")
		}
	}
	if e := os.WriteFile(path, b, 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.Chmod(path, 0644); e != nil {
		t.Fatal(e)
	}
	if _, e := LoadConfig(path); e == nil {
		t.Fatal("permissive config accepted")
	}
	if e := os.Chmod(path, 0600); e != nil {
		t.Fatal(e)
	}
	link := filepath.Join(t.TempDir(), "link.json")
	if e := os.Symlink(path, link); e != nil {
		t.Fatal(e)
	}
	if _, e := LoadConfig(link); e == nil {
		t.Fatal("config symlink accepted")
	}
	keyLink := filepath.Join(t.TempDir(), "key.p8")
	if e := os.Symlink(keyPath, keyLink); e != nil {
		t.Fatal(e)
	}
	cfg.KeyPath = keyPath
	if e := os.Chmod(keyPath, 0644); e != nil {
		t.Fatal(e)
	}
	if _, e := NewService(cfg); e == nil {
		t.Fatal("permissive APNs key accepted")
	}
	if e := os.Chmod(keyPath, 0600); e != nil {
		t.Fatal(e)
	}
	cfg.KeyPath = keyLink
	if _, e := NewService(cfg); e == nil {
		t.Fatal("key symlink accepted")
	}
}
func TestEndpointPortAndMetadataBounds(t *testing.T) {
	s, r := testService(t, 10)
	scope := s.credentials[sha256sum(s.cfg.Credentials[0].Token)]
	for _, endpoint := range []string{"https://192.168.1.2:0", "https://192.168.1.2:65536"} {
		r.Notification.Endpoints = []string{endpoint}
		if r.validate(s.cfg.AllowedBundleIDs, scope) == nil {
			t.Errorf("accepted port %s", endpoint)
		}
	}
	r = validAPNSRequest()
	r.Notification.TransferID = strings.Repeat("x", 128)
	r.Notification.Endpoints = []string{"https://192.168.1.2:12345"}
	r.Notification.ComputerID = strings.Repeat("c", 128)
	if r.validate(s.cfg.AllowedBundleIDs, scope) == nil {
		t.Fatal("oversize metadata accepted")
	}
}
func TestHandlerOversizeAndConcurrencyLimit(t *testing.T) {
	s, r := testService(t, 100)
	token := s.cfg.Credentials[0].Token
	if w := send(t, s, bytes.Repeat([]byte("x"), MaxBodyBytes+1), token); w.Code != 413 {
		t.Fatalf("oversize=%d", w.Code)
	}
	s, _ = testService(t, 100)
	entered := make(chan struct{})
	release := make(chan struct{})
	s.sender = func(context.Context, Request, string) error { close(entered); <-release; return nil }
	done := make(chan struct{})
	go func() { send(t, s, marshal(r), token); close(done) }()
	<-entered
	for i := 0; i < 31; i++ {
		s.gate <- struct{}{}
	}
	if w := send(t, s, marshal(r), token); w.Code != 503 {
		t.Fatalf("concurrency cap=%d", w.Code)
	}
	for i := 0; i < 31; i++ {
		<-s.gate
	}
	close(release)
	<-done
}
