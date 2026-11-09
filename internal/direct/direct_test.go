package direct

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func testStore(t *testing.T) (*Store, Device) {
	t.Helper()
	s, e := OpenStore(privateTempDir(t))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = s.Close() })
	i, e := s.NewInvitation()
	if e != nil {
		t.Fatal(e)
	}
	d, e := s.ApproveInvitation(i.Value, "Phone", true, PairRequest{DeviceName: "iPad", DeviceToken: "deadbeef", BundleID: "com.example.app", Environment: "sandbox"})
	if e != nil {
		t.Fatal(e)
	}
	return s, d
}
func privateTempDir(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "native")
	if e := os.Mkdir(p, 0700); e != nil {
		t.Fatal(e)
	}
	return p
}
func pinnedClient(t *testing.T, s *Store) *http.Client {
	t.Helper()
	id := s.Identity()
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(id.CertificatePEM)) {
		t.Fatal("bad cert")
	}
	return &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}}
}
func request(t *testing.T, c *http.Client, method, url, token, body string) (int, []byte) {
	t.Helper()
	r, e := http.NewRequest(method, url, strings.NewReader(body))
	if e != nil {
		t.Fatal(e)
	}
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	resp, e := c.Do(r)
	if e != nil {
		t.Fatal(e)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control=%q", resp.Header.Get("Cache-Control"))
	}
	if resp.Header.Get("Content-Type") == "application/json" && resp.Header.Get("Cache-Control") != "no-store" {
		t.Error("cacheable credential/text JSON response")
	}
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}
func TestPairingPinnedTLSReservationAndFlush(t *testing.T) {
	s, e := OpenStore(privateTempDir(t))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	entered := make(chan struct{})
	release := make(chan struct{})
	p, e := ListenPairing(s, PairingConfig{BindAddress: "127.0.0.1:0", EndpointHost: "127.0.0.1", ComputerName: "Desk", ApprovePair: func(ctx context.Context, q PairRequest) (string, bool) {
		close(entered)
		select {
		case <-release:
			return "Tablet", true
		case <-ctx.Done():
			return "", false
		}
	}})
	if e != nil {
		t.Fatal(e)
	}
	go p.Serve()
	c := pinnedClient(t, s)
	inv := p.Invitation()
	body := fmt.Sprintf(`{"invitation":%q,"deviceName":"iPad","deviceToken":"aabb","bundleID":"com.example.app","environment":"sandbox"}`, inv.Value)
	done := make(chan struct{})
	var code int
	var payload []byte
	go func() { code, payload = request(t, c, "POST", p.Endpoint()+"/v1/pair", "", body); close(done) }()
	<-entered
	// A second request with the same token must fail without another approval prompt.
	status, _ := request(t, c, "POST", p.Endpoint()+"/v1/pair", "", body)
	if status != 403 {
		t.Fatalf("duplicate status %d", status)
	}
	close(release)
	<-done
	if code != 201 {
		t.Fatalf("pair status %d: %s", code, payload)
	}
	select {
	case <-p.Done():
	case <-time.After(time.Second):
		t.Fatal("pair completion not signaled after response")
	}
	if p.Result() != Acked {
		t.Fatalf("pair result %q", p.Result())
	}
	var got map[string]any
	if e = json.Unmarshal(payload, &got); e != nil || got["secret"] == "" {
		t.Fatalf("bad pair response: %s", payload)
	}
}
func TestTransferAuthRefreshAckAndCompletionFlush(t *testing.T) {
	s, d := testStore(t)
	x, e := ListenSession(s, SessionConfig{BindAddress: "127.0.0.1:0", EndpointHost: "127.0.0.1", DeviceID: d.ID, SenderName: "Desktop", Text: "hello"})
	if e != nil {
		t.Fatal(e)
	}
	go x.Serve()
	c := pinnedClient(t, s)
	if strings.Contains(string(mustRead(t, filepath.Join(s.dir, "store.json"))), "hello") {
		t.Fatal("transfer text was persisted")
	}
	code, _ := request(t, c, "GET", x.Endpoint()+"/v1/transfers", "wrong", "")
	if code != 401 {
		t.Fatalf("unauthorized=%d", code)
	}
	code, b := request(t, c, "GET", x.Endpoint()+"/v1/transfers/"+x.ID(), d.Secret, "")
	if code != 200 || !strings.Contains(string(b), `"text":"hello"`) {
		t.Fatalf("get %d %s", code, b)
	}
	code, _ = request(t, c, "POST", x.Endpoint()+"/v1/device/token", d.Secret, `{"deviceToken":"aabbccdd","bundleID":"com.example.app","environment":"production"}`)
	if code != 204 {
		t.Fatalf("refresh=%d", code)
	}
	code, _ = request(t, c, "POST", x.Endpoint()+"/v1/transfers/"+x.ID()+"/ack", d.Secret, "")
	if code != 204 {
		t.Fatalf("ack=%d", code)
	}
	code, _ = request(t, c, "POST", x.Endpoint()+"/v1/transfers/"+x.ID()+"/ack", d.Secret, "")
	if code != 204 {
		t.Fatalf("repeated ack=%d", code)
	}
	select {
	case <-x.Done():
	case <-time.After(time.Second):
		t.Fatal("ack completion not signaled")
	}
	if x.Result() != Acked {
		t.Fatalf("result=%q", x.Result())
	}
	if x.text != "" {
		t.Fatal("terminal session retained transfer text")
	}
	// Receipt must release the listening port immediately, without waiting for
	// idle acknowledgment connections to close.
	next, err := net.Listen("tcp", x.listener.Addr().String())
	if err != nil {
		t.Fatalf("completed send still occupies its port: %v", err)
	}
	next.Close()
}
func TestStorePersistenceRevocationAndConcurrentSessions(t *testing.T) {
	dir := privateTempDir(t)
	s, e := OpenStore(dir)
	if e != nil {
		t.Fatal(e)
	}
	inv, _ := s.NewInvitation()
	d, e := s.ApproveInvitation(inv.Value, "Phone", true, PairRequest{DeviceName: "iPad", DeviceToken: "aabb", BundleID: "com.example.app", Environment: "sandbox"})
	if e != nil {
		t.Fatal(e)
	}
	id := s.Identity()
	s2, e := OpenStore(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s2.Close()
	if s2.Identity().ComputerID != id.ComputerID {
		t.Fatal("identity changed")
	}
	if e = s2.RenameDevice(d.ID, "Tablet"); e != nil {
		t.Fatal(e)
	}
	if got, ok := s.FindDevice("Tablet"); !ok || got.ID != d.ID {
		t.Fatal("rename not visible across stores")
	}
	const n = 12
	var wg sync.WaitGroup
	xs := make([]*Session, n)
	errs := make([]error, n)
	for i := range xs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			xs[i], errs[i] = ListenSession(s, SessionConfig{BindAddress: "127.0.0.1:0", DeviceID: d.ID, Text: fmt.Sprintf("text %d", i)})
		}(i)
	}
	wg.Wait()
	for i, x := range xs {
		if errs[i] != nil || x == nil {
			t.Fatalf("concurrent session creation failed: %v", errs[i])
		}
	}
	if e = s2.RevokeDevice(d.ID); e != nil {
		t.Fatal(e)
	}
	for _, x := range xs {
		if x.store.authorizedFor(mkReq(d.Secret), d.ID) {
			t.Fatal("revoked credential authorized")
		}
		x.Cancel()
	}
	ids, ports := map[string]bool{}, map[string]bool{}
	for _, x := range xs {
		if ids[x.ID()] {
			t.Fatal("duplicate transfer id")
		}
		ids[x.ID()] = true
		port := x.listener.Addr().(*net.TCPAddr).Port
		if ports[fmt.Sprint(port)] {
			t.Fatal("duplicate transfer port")
		}
		ports[fmt.Sprint(port)] = true
	}
}
func mkReq(token string) *http.Request {
	r, _ := http.NewRequest("GET", "https://example.invalid", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	return r
}
func TestMalformedOversizeAndExpiry(t *testing.T) {
	s, d := testStore(t)
	if _, e := ListenSession(s, SessionConfig{DeviceID: d.ID, Text: strings.Repeat("x", MaxTextBytes+1)}); e == nil {
		t.Fatal("oversize accepted")
	}
	exact, e := ListenSession(s, SessionConfig{BindAddress: "127.0.0.1:0", DeviceID: d.ID, Text: strings.Repeat("x", MaxTextBytes)})
	if e != nil {
		t.Fatalf("64000-byte text rejected: %v", e)
	}
	exact.Cancel()
	if _, e := ListenSession(s, SessionConfig{DeviceID: d.ID, Text: string([]byte{0xff})}); e == nil {
		t.Fatal("invalid utf8 accepted")
	}
	h := NewHandler(s, HandlerConfig{ComputerName: "Desk", ApprovePair: func(context.Context, PairRequest) (string, bool) {
		t.Fatal("malformed body prompted approval")
		return "", false
	}})
	rr := httptestPost(h, "{")
	if rr != 400 {
		t.Fatalf("malformed status=%d", rr)
	}
	x, e := ListenSession(s, SessionConfig{BindAddress: "127.0.0.1:0", DeviceID: d.ID, Text: "short", TTL: 40 * time.Millisecond})
	if e != nil {
		t.Fatal(e)
	}
	go x.Serve()
	select {
	case <-x.Done():
	case <-time.After(time.Second):
		t.Fatal("expiry did not complete")
	}
	if x.Result() != Expired {
		t.Fatalf("expiry result=%q", x.Result())
	}
}
func httptestPost(h http.Handler, body string) int {
	r := httptest.NewRecorder()
	q := httptest.NewRequest("POST", "/v1/pair", strings.NewReader(body))
	h.ServeHTTP(r, q)
	return r.Code
}

func TestOpenStoreExistingStateIsReadOnlyAndFailsClosed(t *testing.T) {
	dir := privateTempDir(t)
	s, e := OpenStore(dir)
	if e != nil {
		t.Fatal(e)
	}
	before, e := os.Stat(filepath.Join(dir, "store.json"))
	if e != nil {
		t.Fatal(e)
	}
	contents, e := os.ReadFile(filepath.Join(dir, "store.json"))
	if e != nil {
		t.Fatal(e)
	}
	_ = s.Close()
	s2, e := OpenStore(dir)
	if e != nil {
		t.Fatal(e)
	}
	_, _, e = s2.Snapshot()
	if e != nil {
		t.Fatal(e)
	}
	_ = s2.Close()
	after, e := os.Stat(filepath.Join(dir, "store.json"))
	if e != nil {
		t.Fatal(e)
	}
	contents2, _ := os.ReadFile(filepath.Join(dir, "store.json"))
	if !before.ModTime().Equal(after.ModTime()) || !bytes.Equal(contents, contents2) {
		t.Fatal("read-only open rewrote existing store")
	}
	// Existing unsafe permissions must be reported, not silently repaired.
	if e = os.Chmod(dir, 0755); e != nil {
		t.Fatal(e)
	}
	if _, e = OpenStore(dir); e == nil {
		t.Fatal("unsafe directory permissions accepted")
	}
	di, _ := os.Stat(dir)
	if di.Mode().Perm() != 0755 {
		t.Fatal("unsafe directory was silently chmodded")
	}
	_ = os.Chmod(dir, 0700)
	path := filepath.Join(dir, "store.json")
	_ = os.Chmod(path, 0644)
	if _, e = OpenStore(dir); e == nil {
		t.Fatal("unsafe state permissions accepted")
	}
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0644 {
		t.Fatal("unsafe state file was silently chmodded")
	}
	_ = os.Chmod(path, 0600)
	_ = os.WriteFile(path, []byte(`{}`), 0600)
	if _, e = OpenStore(dir); e == nil {
		t.Fatal("corrupt store reset instead of failing closed")
	}
}
func TestStoreRejectsSymlinksAndIdentityTampering(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if e := os.Mkdir(real, 0700); e != nil {
		t.Fatal(e)
	}
	link := filepath.Join(base, "link")
	if e := os.Symlink(real, link); e != nil {
		t.Fatal(e)
	}
	if _, e := OpenStore(link); e == nil {
		t.Fatal("symlink directory accepted")
	}
	d := privateTempDir(t)
	if e := os.Symlink("/dev/null", filepath.Join(d, "store.lock")); e != nil {
		t.Fatal(e)
	}
	if _, e := OpenStore(d); e == nil {
		t.Fatal("symlink lock accepted")
	}
	d2 := privateTempDir(t)
	if e := os.Symlink("/dev/null", filepath.Join(d2, "store.json")); e != nil {
		t.Fatal(e)
	}
	if _, e := OpenStore(d2); e == nil {
		t.Fatal("symlink store file accepted")
	}
	d3 := privateTempDir(t)
	s, e := OpenStore(d3)
	if e != nil {
		t.Fatal(e)
	}
	_ = s.Close()
	path := filepath.Join(d3, "store.json")
	b, _ := os.ReadFile(path)
	var obj map[string]any
	_ = json.Unmarshal(b, &obj)
	identity := obj["identity"].(map[string]any)
	identity["certificateSHA256"] = "00"
	b, _ = json.Marshal(obj)
	_ = os.WriteFile(path, b, 0600)
	if _, e = OpenStore(d3); e == nil {
		t.Fatal("changed certificate pin accepted")
	}
}
func TestAliasUniquenessAndStrictJSON(t *testing.T) {
	s, d := testStore(t)
	inv, e := s.NewInvitation()
	if e != nil {
		t.Fatal(e)
	}
	req := PairRequest{DeviceName: "iPhone", DeviceToken: "aabb", BundleID: "com.example.app", Environment: "sandbox"}
	if _, e = s.ApproveInvitation(inv.Value, "Tablet", true, req); e != nil {
		t.Fatal(e)
	}
	inv, e = s.NewInvitation()
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.ApproveInvitation(inv.Value, "PHONE", true, req); e == nil {
		t.Fatal("case-insensitive duplicate alias accepted")
	}
	if e = s.RenameDevice(d.ID, "tablet"); e == nil {
		t.Fatal("rename collision accepted")
	}
	called := false
	h := NewHandler(s, HandlerConfig{ComputerName: "Desk", ApprovePair: func(context.Context, PairRequest) (string, bool) { called = true; return "x", true }})
	cases := []string{`{"invitation":"a","invitation":"b","deviceName":"phone","deviceToken":"aabb","bundleID":"com.example.app","environment":"sandbox"}`, `{"invitation":"a","deviceName":"phone","deviceToken":"aabb","bundleID":"com.example.app","environment":"sandbox"} {}`, `{"invitation":"a","deviceName":"phone","deviceToken":"zz","bundleID":"com.example.app","environment":"sandbox"}`, strings.Repeat(" ", 128*1024+1)}
	for _, body := range cases {
		if code := httptestPostBody(h, body); code != 400 {
			t.Fatalf("malformed JSON status=%d", code)
		}
	}
	if called {
		t.Fatal("malformed JSON prompted approval")
	}
}
func TestExpiredInvitationCannotBeUsed(t *testing.T) {
	s, _ := testStore(t)
	i, e := s.NewInvitation()
	if e != nil {
		t.Fatal(e)
	}
	s.mu.Lock()
	s.invites[i.Value].expires = time.Now().Add(-time.Second)
	s.mu.Unlock()
	if _, e = s.ApproveInvitation(i.Value, "Expired", true, PairRequest{DeviceName: "Phone", DeviceToken: "aabb", BundleID: "com.example.app", Environment: "sandbox"}); e == nil {
		t.Fatal("expired invitation accepted")
	}
}
func httptestPostBody(h http.Handler, body string) int {
	r := httptest.NewRecorder()
	q := httptest.NewRequest("POST", "/v1/pair", strings.NewReader(body))
	h.ServeHTTP(r, q)
	return r.Code
}
func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func TestNativeTLSRejectsMissingOrChangedPin(t *testing.T) {
	s, d := testStore(t)
	p, e := ListenPairing(s, PairingConfig{BindAddress: "127.0.0.1:0", EndpointHost: "127.0.0.1", ComputerName: "Desk"})
	if e != nil {
		t.Fatal(e)
	}
	go p.Serve()
	defer p.Close()
	untrusted := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: x509.NewCertPool(), MinVersion: tls.VersionTLS12}}}
	req, _ := http.NewRequest("GET", p.Endpoint()+"/v1/pair", nil)
	if _, e = untrusted.Do(req); e == nil {
		t.Fatal("TLS accepted without the pinned certificate")
	}
	other, e := OpenStore(privateTempDir(t))
	if e != nil {
		t.Fatal(e)
	}
	defer other.Close()
	wrongPin := pinnedClient(t, other)
	req, _ = http.NewRequest("GET", p.Endpoint()+"/v1/pair", nil)
	if _, e = wrongPin.Do(req); e == nil {
		t.Fatal("TLS accepted changed certificate pin")
	}
	_ = d
}

func TestAttachmentManifestStreamsStablePrivateSnapshot(t *testing.T) {
	s, d := testStore(t)
	src := filepath.Join(t.TempDir(), "payload.txt")
	original := []byte("attachment bytes survive source replacement")
	if e := os.WriteFile(src, original, 0600); e != nil {
		t.Fatal(e)
	}
	x, e := ListenSession(s, SessionConfig{DeviceID: d.ID, Text: "", Files: []string{src}, ExternalEndpoint: "https://example.invalid"})
	if e != nil {
		t.Fatal(e)
	}
	defer x.Cancel()
	if e = os.WriteFile(src, []byte("changed"), 0600); e != nil {
		t.Fatal(e)
	}
	server := httptest.NewTLSServer(x.Handler())
	defer server.Close()
	client := server.Client()
	req, _ := http.NewRequest("GET", server.URL+"/v1/transfers/"+x.ID(), nil)
	req.Header.Set("Authorization", "Bearer "+d.Secret)
	resp, e := client.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	var m struct {
		Text        string       `json:"text"`
		Attachments []Attachment `json:"attachments"`
	}
	e = json.NewDecoder(resp.Body).Decode(&m)
	resp.Body.Close()
	if e != nil || m.Text != "" || len(m.Attachments) != 1 {
		t.Fatalf("bad manifest: %+v %v", m, e)
	}
	a := m.Attachments[0]
	if a.Size != int64(len(original)) {
		t.Fatalf("declared size %d", a.Size)
	}
	req, _ = http.NewRequest("GET", server.URL+"/v1/transfers/"+x.ID()+"/items/"+a.ID, nil)
	req.Header.Set("Authorization", "Bearer "+d.Secret)
	resp, e = client.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	got, e := io.ReadAll(resp.Body)
	resp.Body.Close()
	if e != nil || !bytes.Equal(got, original) {
		t.Fatalf("download changed: %q %v", got, e)
	}
	sum := sha256.Sum256(got)
	if hex.EncodeToString(sum[:]) != a.SHA256 {
		t.Fatal("wrong attachment hash")
	}
}
