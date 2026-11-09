package push

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testSetup(t *testing.T) (string, config, *ecdsa.PrivateKey) {
	t.Helper()
	dir := t.TempDir()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config{"KEY1234567", "TEAM123456", filepath.Join(dir, "private-sentinel.p8")}
	writePrivate(t, cfg.KeyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	writeConfig(t, dir, cfg)
	if err := Pair(dir, "Secret Alias", pairingJSON(t, testPairing())); err != nil {
		t.Fatal(err)
	}
	return dir, cfg, key
}

func writeConfig(t *testing.T, dir string, cfg config) {
	t.Helper()
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	writePrivate(t, filepath.Join(dir, "push.json"), data)
}

func mockAPNs(t *testing.T, h http.HandlerFunc, http2 bool) *httptest.Server {
	t.Helper()
	s := httptest.NewUnstartedServer(h)
	s.EnableHTTP2 = http2
	s.Config.ErrorLog = log.New(io.Discard, "", 0)
	s.StartTLS()
	t.Cleanup(s.Close)
	return s
}

func routedTransport(t *testing.T, s *httptest.Server, trust bool) func() *http.Transport {
	t.Helper()
	return func() *http.Transport {
		tr := newTransport()
		if trust {
			roots := x509.NewCertPool()
			roots.AddCert(s.Certificate())
			tr.TLSClientConfig.RootCAs = roots
		}
		dial := tr.DialTLSContext
		tr.DialTLSContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			if addr != "api.sandbox.push.apple.com:443" && addr != "api.push.apple.com:443" {
				t.Errorf("unexpected APNs destination: %q", addr)
				return nil, errors.New("unexpected destination")
			}
			return dial(ctx, network, s.Listener.Addr().String())
		}
		return tr
	}
}

func TestNotifyHTTP2JWTHeadersAndPrivacy(t *testing.T) {
	for _, env := range []string{"sandbox", "production"} {
		t.Run(env, func(t *testing.T) {
			dir, cfg, key := testSetup(t)
			p := testPairing()
			p.Environment = env
			if env == "sandbox" {
				// xtool prefixes development bundle IDs during signing. The
				// exported topic must reach APNs intact.
				p.BundleID = "XTL-1234ABCD." + p.BundleID
			}
			if err := Pair(dir, "secret alias", pairingJSON(t, p)); err != nil {
				t.Fatal(err)
			}
			now := time.Unix(1800000000, 0)
			var calls atomic.Int32
			s := mockAPNs(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.ProtoMajor != 2 || r.TLS == nil || r.TLS.NegotiatedProtocol != "h2" || r.Method != http.MethodPost {
					t.Error("request did not use HTTP/2 POST over TLS")
				}
				endpoint, _ := apnsEndpoint(env)
				if "https://"+r.Host != endpoint || r.URL.Path != "/3/device/"+p.DeviceToken || r.URL.RawQuery != "" {
					t.Error("wrong endpoint or token path")
				}
				collapse := sha256.Sum256([]byte("Secret Alias"))
				for header, want := range map[string]string{
					"Content-Type": "application/json", "apns-topic": p.BundleID,
					"apns-push-type": "alert", "apns-priority": "10",
					"apns-expiration":  strconv.FormatInt(now.Unix()+120, 10),
					"apns-collapse-id": hex.EncodeToString(collapse[:]), "User-Agent": "ClipSend",
				} {
					if r.Header.Get(header) != want {
						t.Errorf("incorrect %s", header)
					}
				}
				body, _ := io.ReadAll(r.Body)
				if string(body) != alertPayload {
					t.Error("payload is not the fixed, generic alert")
				}
				metadata, _ := json.Marshal(r.Header)
				for _, secret := range []string{p.DeviceName, "Secret Alias", "secret alias", cfg.KeyPath, p.DeviceToken} {
					if strings.Contains(string(metadata)+string(body), secret) {
						t.Error("unnecessary private metadata in headers or payload")
					}
				}
				verifyJWT(t, strings.TrimPrefix(r.Header.Get("Authorization"), "bearer "), cfg, key, now)
				w.WriteHeader(http.StatusOK)
			}, true)
			ok, err := notify(context.Background(), dir, "SECRET ALIAS", now, routedTransport(t, s, true))
			if !ok || err != nil || calls.Load() != 1 {
				t.Fatalf("accepted notification: %v, %v, calls=%d", ok, err, calls.Load())
			}
		})
	}
}

func verifyJWT(t *testing.T, jwt string, cfg config, key *ecdsa.PrivateKey, now time.Time) {
	t.Helper()
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Error("invalid provider JWT format")
		return
	}
	header, err1 := base64.RawURLEncoding.DecodeString(parts[0])
	claims, err2 := base64.RawURLEncoding.DecodeString(parts[1])
	sig, err3 := base64.RawURLEncoding.DecodeString(parts[2])
	if err1 != nil || err2 != nil || err3 != nil || len(sig) != 64 {
		t.Error("invalid JOSE ES256 encoding")
		return
	}
	var h map[string]any
	var c map[string]any
	if json.Unmarshal(header, &h) != nil || len(h) != 2 || h["alg"] != "ES256" || h["kid"] != cfg.KeyID {
		t.Error("incorrect JWT header")
	}
	if json.Unmarshal(claims, &c) != nil || len(c) != 2 || c["iss"] != cfg.TeamID || c["iat"] != float64(now.Unix()) {
		t.Error("incorrect JWT claims")
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if !ecdsa.Verify(&key.PublicKey, digest[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Error("JWT signature failed verification")
	}
}

func TestNotifyOptionalSetup(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	ok, err := Notify(context.Background(), missing, "phone")
	if ok || err != nil {
		t.Fatalf("absent setup: %v, %v", ok, err)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("Notify created configuration")
	}
	dir, cfg, _ := testSetup(t)
	if err := os.Remove(cfg.KeyPath); err != nil {
		t.Fatal(err)
	}
	ok, err = Notify(context.Background(), dir, "unpaired")
	if ok || err != nil {
		t.Fatalf("unpaired alias tried to read key: %v, %v", ok, err)
	}
	if err := os.Remove(filepath.Join(dir, "push.json")); err != nil {
		t.Fatal(err)
	}
	writePrivate(t, filepath.Join(dir, "devices.json"), []byte("malformed private registry"))
	ok, err = Notify(context.Background(), dir, "Secret Alias")
	if ok || err != nil {
		t.Fatalf("absent push.json: %v, %v", ok, err)
	}
}

func TestNotifyRejectionGuidanceIsWhitelisted(t *testing.T) {
	dir, cfg, _ := testSetup(t)
	cases := []struct {
		status int
		reason string
		want   string
	}{
		{400, "BadDeviceToken", "re-pair the device and check its sandbox/production environment"},
		{410, "Unregistered", "re-pair the device"},
		{400, "DeviceTokenNotForTopic", "check the app bundle ID"},
		{403, "InvalidProviderToken", "APNs key ID, team ID, .p8 key, and system clock"},
		{403, "ExpiredProviderToken", "system clock and APNs signing credentials"},
		{400, "BadTopic", "APNs key's permission"},
		{403, "TopicDisallowed", "APNs key's permission"},
		{403, "Forbidden", "Apple Developer account"},
		{429, "TooManyProviderTokenUpdates", "wait before sending again"},
		{429, "TooManyRequests", "wait before sending again"},
		{500, "InternalServerError", "temporarily unavailable"},
		{503, "ServiceUnavailable", "temporarily unavailable"},
		{503, "Shutdown", "temporarily unavailable"},
		{400, "BadDeviceToken\nprivate-sentinel", ""},
		{400, "baddevicetoken", ""},
		{400, cfg.KeyPath + testPairing().DeviceToken, ""},
		{201, "private-sentinel", ""},
	}
	for _, tc := range cases {
		t.Run(strconv.Itoa(tc.status)+"/"+tc.reason, func(t *testing.T) {
			var calls atomic.Int32
			s := mockAPNs(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("apns-id", "private-sentinel")
				w.WriteHeader(tc.status)
				_ = json.NewEncoder(w).Encode(map[string]any{"reason": tc.reason, "extra": "private-sentinel", "timestamp": 123})
			}, true)
			ok, err := notify(context.Background(), dir, "Secret Alias", time.Now(), routedTransport(t, s, true))
			if ok || err == nil || calls.Load() != 1 {
				t.Fatalf("rejection: %v, %v, calls=%d", ok, err, calls.Load())
			}
			message := err.Error()
			prefix := "push: APNs rejected notification (HTTP " + strconv.Itoa(tc.status) + ")"
			if !strings.HasPrefix(message, prefix) || tc.want != "" && !strings.Contains(message, tc.want) || tc.want == "" && message != prefix {
				t.Errorf("incorrect guidance: %s", message)
			}
			assertSanitized(t, message, cfg)
		})
	}
	for _, body := range []string{`{"reason":`, `{"reason":12}`, `{"reason":"BadDeviceToken"} trailing`, strings.Repeat("x", 4097)} {
		s := mockAPNs(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(400)
			_, _ = io.WriteString(w, body)
		}, true)
		ok, err := notify(context.Background(), dir, "Secret Alias", time.Now(), routedTransport(t, s, true))
		if ok || err == nil || err.Error() != "push: APNs rejected notification (HTTP 400)" {
			t.Fatalf("untrusted error body: %v, %v", ok, err)
		}
	}
}

func assertSanitized(t *testing.T, message string, cfg config) {
	t.Helper()
	for _, secret := range []string{cfg.KeyPath, "private-sentinel", testPairing().DeviceToken, testPairing().DeviceName, "Secret Alias", "eyJ", "PRIVATE KEY"} {
		if strings.Contains(message, secret) {
			t.Error("error disclosed private data")
		}
	}
}

func TestNotifyNoRedirectOrProxy(t *testing.T) {
	dir, _, _ := testSetup(t)
	var redirected atomic.Int32
	destination := mockAPNs(t, func(w http.ResponseWriter, r *http.Request) { redirected.Add(1) }, true)
	s := mockAPNs(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", destination.URL+"/private-sentinel")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}, true)
	t.Setenv("HTTPS_PROXY", destination.URL)
	t.Setenv("HTTP_PROXY", destination.URL)
	t.Setenv("ALL_PROXY", destination.URL)
	tr := newTransport()
	if tr.Proxy != nil || tr.TLSClientConfig.InsecureSkipVerify || !tr.Protocols.HTTP2() || tr.Protocols.HTTP1() {
		t.Fatal("unsafe transport settings")
	}
	ok, err := notify(context.Background(), dir, "Secret Alias", time.Now(), routedTransport(t, s, true))
	if ok || err == nil || !strings.Contains(err.Error(), "307") || redirected.Load() != 0 {
		t.Fatalf("redirect/proxy: %v, %v, calls=%d", ok, err, redirected.Load())
	}
}

func TestNotifyTLSVerificationAndHTTP2Required(t *testing.T) {
	dir, cfg, _ := testSetup(t)
	for _, tc := range []struct{ http2, trust bool }{{true, false}, {false, true}} {
		var requests atomic.Int32
		s := mockAPNs(t, func(w http.ResponseWriter, r *http.Request) { requests.Add(1) }, tc.http2)
		ok, err := notify(context.Background(), dir, "Secret Alias", time.Now(), routedTransport(t, s, tc.trust))
		if ok || err == nil || requests.Load() != 0 {
			t.Fatalf("credentials sent without verified HTTP/2 TLS: %v, %v", ok, err)
		}
		assertSanitized(t, err.Error(), cfg)
	}
}

func TestNotifyCancellationAndNetworkError(t *testing.T) {
	dir, cfg, _ := testSetup(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ok, err := Notify(ctx, dir, "Secret Alias")
	if ok || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v, %v", ok, err)
	}
	started := make(chan struct{})
	s := mockAPNs(t, func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}, true)
	ctx, cancel = context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	ok, err = notify(ctx, dir, "Secret Alias", time.Now(), routedTransport(t, s, true))
	if ok || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline: %v, %v", ok, err)
	}
	select {
	case <-started:
	default:
		t.Fatal("deadline test did not reach mock server")
	}
	ok, err = notify(context.Background(), dir, "Secret Alias", time.Now(), func() *http.Transport {
		tr := newTransport()
		tr.DialTLSContext = func(context.Context, string, string) (net.Conn, error) {
			return nil, errors.New("private-sentinel: " + cfg.KeyPath + testPairing().DeviceToken)
		}
		return tr
	})
	if ok || err == nil {
		t.Fatalf("network failure: %v, %v", ok, err)
	}
	assertSanitized(t, err.Error(), cfg)
}

func TestNotifyInvalidConfigAndKeys(t *testing.T) {
	for _, name := range []string{"bad JSON", "unknown field", "duplicate field", "wrong case", "null field", "short ID", "lowercase ID", "relative path", "wrong extension", "missing key", "malformed PEM", "trailing PEM", "non PKCS8", "wrong curve", "public key", "public config", "public key mode", "symlink key", "oversized config", "oversized key"} {
		t.Run(name, func(t *testing.T) {
			dir, cfg, _ := testSetup(t)
			configPath := filepath.Join(dir, "push.json")
			switch name {
			case "bad JSON":
				writePrivate(t, configPath, []byte("private-sentinel"))
			case "unknown field", "duplicate field", "wrong case", "null field":
				data, _ := os.ReadFile(configPath)
				text := string(data)
				switch name {
				case "unknown field":
					text = strings.TrimSuffix(text, "}") + `,"endpoint":"https://private-sentinel.invalid"}`
				case "duplicate field":
					text = strings.TrimSuffix(text, "}") + `,"keyID":"KEY1234567"}`
				case "wrong case":
					text = strings.Replace(text, "keyID", "KeyID", 1)
				case "null field":
					text = strings.Replace(text, `"keyID":"KEY1234567"`, `"keyID":null`, 1)
				}
				writePrivate(t, configPath, []byte(text))
			case "short ID":
				cfg.KeyID = "short"
				writeConfig(t, dir, cfg)
			case "lowercase ID":
				cfg.TeamID = "team123456"
				writeConfig(t, dir, cfg)
			case "relative path":
				cfg.KeyPath = "relative.p8"
				writeConfig(t, dir, cfg)
			case "wrong extension":
				cfg.KeyPath += ".pem"
				writeConfig(t, dir, cfg)
			case "missing key":
				if err := os.Remove(cfg.KeyPath); err != nil {
					t.Fatal(err)
				}
			case "malformed PEM":
				writePrivate(t, cfg.KeyPath, []byte("private-sentinel"))
			case "trailing PEM":
				data, _ := os.ReadFile(cfg.KeyPath)
				writePrivate(t, cfg.KeyPath, append(data, data...))
			case "non PKCS8":
				writePrivate(t, cfg.KeyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: []byte("private-sentinel")}))
			case "wrong curve":
				key, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
				if err != nil {
					t.Fatal(err)
				}
				der, err := x509.MarshalPKCS8PrivateKey(key)
				if err != nil {
					t.Fatal(err)
				}
				writePrivate(t, cfg.KeyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
			case "public key":
				writePrivate(t, cfg.KeyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("not a private key")}))
			case "public config":
				if err := os.Chmod(configPath, 0644); err != nil {
					t.Fatal(err)
				}
			case "public key mode":
				if err := os.Chmod(cfg.KeyPath, 0644); err != nil {
					t.Fatal(err)
				}
			case "symlink key":
				other := filepath.Join(dir, "other.p8")
				if err := os.Rename(cfg.KeyPath, other); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(other, cfg.KeyPath); err != nil {
					t.Fatal(err)
				}
			case "oversized config":
				writePrivate(t, configPath, []byte(strings.Repeat(" ", maxConfigBytes+1)))
			case "oversized key":
				writePrivate(t, cfg.KeyPath, []byte(strings.Repeat(" ", maxConfigBytes+1)))
			}
			var transports atomic.Int32
			ok, err := notify(context.Background(), dir, "Secret Alias", time.Now(), func() *http.Transport {
				transports.Add(1)
				return newTransport()
			})
			if ok || err == nil || transports.Load() != 0 {
				t.Fatalf("misconfigured setup attempted network: %v, %v", ok, err)
			}
			assertSanitized(t, err.Error(), cfg)
		})
	}
}

// A tiny TLS HTTP/2 peer sends REFUSED_STREAM, which normally makes Go replay
// requests. No HPACK decoder is needed: counting HEADERS frames tests replay.
func TestNotifyDoesNotReplayRefusedStream(t *testing.T) {
	dir, _, _ := testSetup(t)
	certSource := mockAPNs(t, func(http.ResponseWriter, *http.Request) {}, true)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tlsListener := tls.NewListener(ln, &tls.Config{Certificates: certSource.TLS.Certificates, NextProtos: []string{"h2"}, MinVersion: tls.VersionTLS12})
	var requests atomic.Int32
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := tlsListener.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
				preface := make([]byte, 24)
				if _, err := io.ReadFull(conn, preface); err != nil {
					return
				}
				if string(preface) != "PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n" {
					return
				}
				_, _ = conn.Write([]byte{0, 0, 0, 4, 0, 0, 0, 0, 0}) // SETTINGS
				for {
					var header [9]byte
					if _, err := io.ReadFull(conn, header[:]); err != nil {
						return
					}
					n := int(header[0])<<16 | int(header[1])<<8 | int(header[2])
					if n > 1<<20 {
						return
					}
					if _, err := io.CopyN(io.Discard, conn, int64(n)); err != nil {
						return
					}
					if header[3] == 4 && header[4]&1 == 0 {
						_, _ = conn.Write([]byte{0, 0, 0, 4, 1, 0, 0, 0, 0})
					}
					if header[3] == 1 {
						requests.Add(1)
						frame := []byte{0, 0, 4, 3, 0, 0, 0, 0, 0, 0, 0, 0, 7}
						binary.BigEndian.PutUint32(frame[5:9], binary.BigEndian.Uint32(header[5:9])&0x7fffffff)
						_, _ = conn.Write(frame)
						return
					}
				}
			}()
		}
	}()
	t.Cleanup(func() { _ = tlsListener.Close(); wg.Wait() })
	factory := routedTransport(t, certSource, true)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ok, err := notify(ctx, dir, "Secret Alias", time.Now(), func() *http.Transport {
		tr := factory()
		// Keep the production TLS dial/ALPN checks, changing only the local
		// test destination. certSource and this peer share the test certificate.
		tr.DialTLSContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			return (&tls.Dialer{Config: tr.TLSClientConfig}).DialContext(ctx, network, ln.Addr().String())
		}
		return tr
	})
	if ok || err == nil || requests.Load() != 1 {
		t.Fatalf("notification replayed or not sent: %v, %v, requests=%d", ok, err, requests.Load())
	}
}
