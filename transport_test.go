package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testSender(t *testing.T) *Sender {
	t.Helper()
	s, err := NewSender(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func testDevice(server *httptest.Server, tlsOn bool, pin string) Device {
	port := 80
	if tlsOn {
		port = 443
	}
	return Device{Alias: "peer", IP: "127.0.0.1", Port: port, TLS: tlsOn, Fingerprint: pin}
}

func serverDevice(t *testing.T, s *httptest.Server, tlsOn bool, pin string) Device {
	t.Helper()
	d := testDevice(s, tlsOn, pin)
	// httptest uses an ephemeral port and loopback host.
	_, p, err := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(s.URL, "http://"), "https://"), ":")
	if !err {
		t.Fatalf("bad test URL %q", s.URL)
	}
	var port int
	if _, err := fmt.Sscan(strings.Split(p, "/")[0], &port); err != nil {
		t.Fatal(err)
	}
	d.Port = port
	return d
}

func TestSendMessageBodyUnicode(t *testing.T) {
	s := testSender(t)
	const message = "héllo 🌍"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != localSendAPI+"/prepare-upload" {
			t.Errorf("path = %s", r.URL.Path)
		}
		var body struct {
			Files map[string]struct {
				Size    int    `json:"size"`
				Type    string `json:"fileType"`
				Preview string `json:"preview"`
			} `json:"files"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if len(body.Files) != 1 {
			t.Errorf("files count = %d", len(body.Files))
			return
		}
		for _, f := range body.Files {
			if f.Preview != message || f.Size != len([]byte(message)) || f.Type != "text/plain" {
				t.Errorf("file payload = %#v", f)
			}
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	if err := s.Send(context.Background(), serverDevice(t, srv, false, ""), message); err != nil {
		t.Fatal(err)
	}
}

func TestSendFileResponseCancelsAndNeverUploads(t *testing.T) {
	s := testSender(t)
	var uploaded, cancelled bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case localSendAPI + "/prepare-upload":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"sessionId":"id with/space"}`))
		case localSendAPI + "/cancel":
			cancelled = r.URL.Query().Get("sessionId") == "id with/space"
			w.WriteHeader(http.StatusOK)
		default:
			uploaded = true
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	err := s.Send(context.Background(), serverDevice(t, srv, false, ""), "text")
	if err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("expected refusal after cancellation, got %v", err)
	}
	if !cancelled || uploaded {
		t.Fatalf("cancelled=%v uploaded=%v", cancelled, uploaded)
	}
}

func TestSendTLSMismatchBeforeRequestBodyTransmission(t *testing.T) {
	s := testSender(t)
	var requests int
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++; w.WriteHeader(http.StatusNoContent) }))
	defer srv.Close()
	wrong := strings.Repeat("0", 64)
	err := s.Send(context.Background(), serverDevice(t, srv, true, wrong), "private message")
	if err == nil || requests != 0 {
		t.Fatalf("err=%v server requests=%d", err, requests)
	}
}

func TestSendTLSPinSuccess(t *testing.T) {
	s := testSender(t)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer srv.Close()
	cert := srv.Certificate()
	sum := sha256.Sum256(cert.Raw)
	if err := s.Send(context.Background(), serverDevice(t, srv, true, strings.ToUpper(hex.EncodeToString(sum[:]))), "hello"); err != nil {
		t.Fatal(err)
	}
}

func TestSendRejectsMissingTLSPinAndInvalidBounds(t *testing.T) {
	s := testSender(t)
	d := Device{IP: "127.0.0.1", Port: 80, TLS: true}
	if err := s.Send(context.Background(), d, "hello"); err == nil || !strings.Contains(err.Error(), "fingerprint") {
		t.Fatalf("missing pin error = %v", err)
	}
	d.TLS = false
	for _, v := range []string{"", strings.Repeat("a", 64001)} {
		if err := s.Send(context.Background(), d, v); err == nil {
			t.Fatalf("accepted message length %d", len(v))
		}
	}
	if err := s.Send(context.Background(), d, strings.Repeat("a", 64000)); err == nil {
		t.Fatal("unexpected success without receiver")
	}
}

func TestSendHonorsContextTimeout(t *testing.T) {
	s := testSender(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := s.Send(ctx, serverDevice(t, srv, false, ""), "hello")
	if err == nil || time.Since(start) > time.Second {
		t.Fatalf("err=%v elapsed=%v", err, time.Since(start))
	}
}

func TestNewSenderIdentityStableSecureAndPartialPairFails(t *testing.T) {
	dir := t.TempDir()
	s, err := NewSender(dir)
	if err != nil {
		t.Fatal(err)
	}
	key, err := os.Stat(filepath.Join(dir, "identity.key"))
	if err != nil {
		t.Fatal(err)
	}
	if key.Mode().Perm() != 0600 {
		t.Fatalf("key mode %o", key.Mode().Perm())
	}
	s2, err := NewSender(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s.fingerprint != s2.fingerprint {
		t.Fatal("identity fingerprint changed")
	}
	partial := t.TempDir()
	if err := os.WriteFile(filepath.Join(partial, "identity.pem"), []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewSender(partial); err == nil {
		t.Fatal("partial identity silently replaced")
	}
	if _, err := os.Stat(filepath.Join(partial, "identity.key")); !os.IsNotExist(err) {
		t.Fatalf("key unexpectedly created: %v", err)
	}
}

func TestLocalSendMutualTLSWithUnrelatedAdvertisedCA(t *testing.T) {
	s := testSender(t)
	other := testSender(t)
	pool := x509.NewCertPool()
	pool.AddCert(other.certificate.Leaf)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
			t.Error("missing client identity")
			w.WriteHeader(401)
			return
		}
		if fingerprint(r.TLS.PeerCertificates[0].Raw) != s.fingerprint {
			t.Error("wrong client identity")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	srv.TLS = &tls.Config{ClientAuth: tls.RequireAnyClientCert, ClientCAs: pool}
	srv.StartTLS()
	defer srv.Close()
	if err := s.Send(context.Background(), serverDevice(t, srv, true, fingerprint(srv.Certificate().Raw)), "hello"); err != nil {
		t.Fatal(err)
	}
}
