// Package relay sends authenticated ClipSend wake notifications through APNs.
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
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const MaxBodyBytes = 16 << 10
const providerTokenLifetime = 50 * time.Minute

type Notification struct {
	Version    int      `json:"version"`
	ComputerID string   `json:"computerID"`
	TransferID string   `json:"transferID"`
	Endpoints  []string `json:"endpoints"`
}
type Request struct {
	DeviceToken  string       `json:"deviceToken"`
	BundleID     string       `json:"bundleID"`
	Environment  string       `json:"environment"`
	Notification Notification `json:"notification"`
}
type Client struct {
	RelayURL, Token string
	HTTPClient      *http.Client
}

// Notify performs one POST to the configured private relay; it never retries.
func (c *Client) Notify(ctx context.Context, r Request) error {
	if ctx == nil {
		return errors.New("relay: context required")
	}
	u, err := url.Parse(c.RelayURL)
	if err != nil || !validRelayURL(u) {
		return errors.New("relay: invalid relay URL")
	}
	if c.Token == "" {
		return errors.New("relay: token required")
	}
	body, err := json.Marshal(r)
	if err != nil {
		return errors.New("relay: invalid request")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.RelayURL, "/")+"/v1/notify", bytes.NewReader(body))
	if err != nil {
		return errors.New("relay: invalid request")
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	hc := c.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Transport: clientTransport()}
	}
	req.GetBody = func() (io.ReadCloser, error) { return nil, errors.New("relay: replay disabled") }
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req = req.WithContext(ctx)
	client := *hc
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return errors.New("relay: request failed")
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("relay: service returned HTTP %d", resp.StatusCode)
	}
	return nil
}

type Config struct {
	KeyID            string       `json:"keyID"`
	TeamID           string       `json:"teamID"`
	KeyPath          string       `json:"keyPath"`
	Credentials      []Credential `json:"credentials"`
	AllowedBundleIDs []string     `json:"allowedBundleIDs"`
	RateLimit        int          `json:"requestsPerMinute"`
	Listen           string       `json:"listen"`
}
type Credential struct {
	Token       string   `json:"token"`
	BundleIDs   []string `json:"bundleIDs"`
	ComputerIDs []string `json:"computerIDs"`
}
type Service struct {
	cfg              Config
	key              *ecdsa.PrivateKey
	mu               sync.Mutex
	credentials      map[[32]byte]credentialScope
	rates            map[[32]byte][]time.Time
	jwt              string
	jwtAt            time.Time
	now              func() time.Time
	sender           func(context.Context, Request, string) error
	provider         func(time.Time) (string, error)
	gate             chan struct{}
	apnsBaseURL      string
	transportFactory func() *http.Transport
}
type credentialScope struct{ bundleIDs, computerIDs map[string]bool }

func LoadConfig(path string) (Config, error) {
	var c Config
	b, missing, e := readPrivate(path, 1<<20)
	if e != nil || missing {
		return c, errors.New("relay: cannot read private service config")
	}
	if strict(b, &c) != nil {
		return c, errors.New("relay: invalid service config")
	}
	if e = c.validate(); e != nil {
		return c, e
	}
	return c, nil
}
func (c Config) validate() error {
	if len(c.KeyID) != 10 || len(c.TeamID) != 10 || !upperID(c.KeyID) || !upperID(c.TeamID) {
		return errors.New("relay: invalid APNs identifiers")
	}
	if !filepath.IsAbs(c.KeyPath) || filepath.Ext(c.KeyPath) != ".p8" {
		return errors.New("relay: keyPath must be absolute .p8 path")
	}
	if c.Listen == "" {
		c.Listen = "127.0.0.1:8787"
	}
	host, _, e := net.SplitHostPort(c.Listen)
	if e != nil {
		return errors.New("relay: invalid listen address")
	}
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return errors.New("relay: listen address must be loopback")
	}
	if c.RateLimit < 1 || c.RateLimit > 10000 || len(c.Credentials) == 0 || len(c.Credentials) > 10000 {
		return errors.New("relay: invalid limits or credentials")
	}
	allowed := map[string]bool{}
	for _, b := range c.AllowedBundleIDs {
		if !validBundle(b) {
			return errors.New("relay: invalid allowed bundle ID")
		}
		allowed[b] = true
	}
	if len(allowed) == 0 {
		return errors.New("relay: bundle allowlist required")
	}
	seen := map[string]bool{}
	for _, cr := range c.Credentials {
		if len(cr.Token) < 32 || len(cr.Token) > 256 || strings.TrimSpace(cr.Token) != cr.Token || seen[cr.Token] {
			return errors.New("relay: invalid credential")
		}
		seen[cr.Token] = true
		if len(cr.BundleIDs) == 0 || len(cr.ComputerIDs) == 0 {
			return errors.New("relay: credential scope required")
		}
		for _, b := range cr.BundleIDs {
			if !allowed[b] {
				return errors.New("relay: credential scope outside app allowlist")
			}
		}
		for _, id := range cr.ComputerIDs {
			if !validID(id) {
				return errors.New("relay: invalid computer scope")
			}
		}
	}
	return nil
}
func NewService(c Config) (*Service, error) {
	if e := c.validate(); e != nil {
		return nil, e
	}
	if c.Listen == "" {
		c.Listen = "127.0.0.1:8787"
	}
	b, missing, e := readPrivate(c.KeyPath, 16384)
	if e != nil || missing {
		return nil, errors.New("relay: cannot read APNs key")
	}
	block, rest := pem.Decode(b)
	if block == nil || block.Type != "PRIVATE KEY" || len(block.Headers) > 0 || strings.TrimSpace(string(rest)) != "" {
		return nil, errors.New("relay: invalid APNs key")
	}
	parsed, e := x509.ParsePKCS8PrivateKey(block.Bytes)
	if e != nil {
		return nil, errors.New("relay: invalid APNs key")
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok || key.Curve != elliptic.P256() {
		return nil, errors.New("relay: APNs key must be P-256")
	}
	creds := map[[32]byte]credentialScope{}
	for _, cr := range c.Credentials {
		h := sha256.Sum256([]byte(cr.Token))
		s := credentialScope{bundleIDs: map[string]bool{}, computerIDs: map[string]bool{}}
		for _, b := range cr.BundleIDs {
			s.bundleIDs[b] = true
		}
		for _, id := range cr.ComputerIDs {
			s.computerIDs[id] = true
		}
		creds[h] = s
	}
	svc := &Service{cfg: c, key: key, credentials: creds, rates: map[[32]byte][]time.Time{}, now: time.Now, gate: make(chan struct{}, 32)}
	svc.sender = svc.sendAPNs
	return svc, nil
}

// NotifyLocal sends directly for an operator who owns the APNs key. No HTTP
// listener is started. The same credential scope and request policy apply.
func (s *Service) NotifyLocal(ctx context.Context, token string, req Request) error {
	if ctx == nil {
		return errors.New("relay: context required")
	}
	scope, ok := s.credentials[sha256.Sum256([]byte(token))]
	if !ok {
		return errors.New("relay: unauthorized")
	}
	if req.validate(s.cfg.AllowedBundleIDs, scope) != nil {
		return errors.New("relay: invalid notification")
	}
	provider := s.providerToken
	if s.provider != nil {
		provider = s.provider
	}
	jwt, err := provider(s.now())
	if err != nil {
		return errors.New("relay: signing failed")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return s.sender(ctx, req, jwt)
}

func (s *Service) Handler() http.Handler { return http.HandlerFunc(s.serve) }
func (s *Service) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" || r.URL.Path != "/v1/notify" {
		http.NotFound(w, r)
		return
	}
	select {
	case s.gate <- struct{}{}:
		defer func() { <-s.gate }()
	default:
		http.Error(w, "busy", http.StatusServiceUnavailable)
		return
	}
	if r.URL.RawQuery != "" {
		http.Error(w, "bad request", 400)
		return
	}
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") || strings.Contains(h[7:], " ") {
		http.Error(w, "unauthorized", 401)
		return
	}
	token := h[7:]
	digest := sha256.Sum256([]byte(token))
	s.mu.Lock()
	scopes, ok := s.credentials[digest]
	if !ok {
		s.mu.Unlock()
		http.Error(w, "unauthorized", 401)
		return
	}
	now := s.now()
	recent := s.rates[digest][:0]
	for _, t := range s.rates[digest] {
		if now.Sub(t) < time.Minute {
			recent = append(recent, t)
		}
	}
	if len(recent) >= s.cfg.RateLimit {
		s.rates[digest] = recent
		s.mu.Unlock()
		http.Error(w, "rate limited", 429)
		return
	}
	s.rates[digest] = append(recent, now)
	s.mu.Unlock()
	r.Body = http.MaxBytesReader(w, r.Body, MaxBodyBytes)
	data, e := io.ReadAll(r.Body)
	if e != nil {
		http.Error(w, "bad request", 413)
		return
	}
	var req Request
	if strict(data, &req) != nil || req.validate(s.cfg.AllowedBundleIDs, scopes) != nil {
		http.Error(w, "bad request", 400)
		return
	}
	provider := s.providerToken
	if s.provider != nil {
		provider = s.provider
	}
	jwt, e := provider(now)
	if e != nil {
		http.Error(w, "service unavailable", 503)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if e = s.sender(ctx, req, jwt); e != nil {
		http.Error(w, "notification failed", 502)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}
func (r Request) validate(allowed []string, scope credentialScope) error {
	if len(r.DeviceToken) < 1 || len(r.DeviceToken) > 4096 || len(r.DeviceToken)%2 != 0 {
		return errors.New("token")
	}
	if _, e := hex.DecodeString(r.DeviceToken); e != nil {
		return e
	}
	if !validBundle(r.BundleID) || !scope.bundleIDs[r.BundleID] || !scope.computerIDs[r.Notification.ComputerID] {
		return errors.New("scope")
	}
	found := false
	for _, b := range allowed {
		if b == r.BundleID {
			found = true
		}
	}
	if !found {
		return errors.New("topic")
	}
	if r.Environment != "sandbox" && r.Environment != "production" {
		return errors.New("environment")
	}
	n := r.Notification
	if n.Version != 1 || !validID(n.ComputerID) || !validID(n.TransferID) || len(n.Endpoints) < 1 || len(n.Endpoints) > 8 {
		return errors.New("notification")
	}
	for _, raw := range n.Endpoints {
		u, e := url.Parse(raw)
		if e != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" || u.Host == "" {
			return errors.New("endpoint")
		}
		host := u.Hostname()
		ip, e := netip.ParseAddr(host)
		port, portErr := strconv.Atoi(u.Port())
		if e != nil || ip.Zone() != "" || !ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() || portErr != nil || port < 1 || port > 65535 {
			return errors.New("endpoint")
		}
	}
	if len(apnsPayload(n)) > 4096 {
		return errors.New("notification too large")
	}
	return nil
}
func (s *Service) providerToken(now time.Time) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.jwt != "" && !now.Before(s.jwtAt) && now.Sub(s.jwtAt) < providerTokenLifetime {
		return s.jwt, nil
	}
	header, _ := json.Marshal(map[string]string{"alg": "ES256", "kid": s.cfg.KeyID})
	claims, _ := json.Marshal(map[string]any{"iss": s.cfg.TeamID, "iat": now.Unix()})
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	d := sha256.Sum256([]byte(unsigned))
	r, ss, e := ecdsa.Sign(rand.Reader, s.key, d[:])
	if e != nil {
		return "", errors.New("relay: cannot sign provider token")
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	ss.FillBytes(sig[32:])
	s.jwt = unsigned + "." + base64.RawURLEncoding.EncodeToString(sig)
	s.jwtAt = now
	return s.jwt, nil
}
func (s *Service) sendAPNs(ctx context.Context, r Request, jwt string) error {
	host := "api.push.apple.com"
	if r.Environment == "sandbox" {
		host = "api.sandbox.push.apple.com"
	}
	body := apnsPayload(r.Notification)
	req, e := http.NewRequestWithContext(ctx, "POST", "https://"+host+"/3/device/"+strings.ToLower(r.DeviceToken), bytes.NewReader(body))
	if e != nil {
		return errors.New("relay: request failure")
	}
	req.GetBody = func() (io.ReadCloser, error) { return nil, errors.New("replay disabled") }
	req.Header.Set("authorization", "bearer "+jwt)
	req.Header.Set("content-type", "application/json")
	req.Header.Set("apns-topic", r.BundleID)
	req.Header.Set("apns-push-type", "alert")
	req.Header.Set("apns-priority", "10")
	req.Header.Set("apns-expiration", strconv.FormatInt(time.Now().Add(120*time.Second).Unix(), 10))
	base := s.apnsBaseURL
	if base == "" {
		base = "https://" + host
	}
	req.URL, e = url.Parse(strings.TrimRight(base, "/") + "/3/device/" + strings.ToLower(r.DeviceToken))
	if e != nil {
		return errors.New("relay: request failure")
	}
	factory := s.transportFactory
	if factory == nil {
		factory = privateTransport
	}
	tr := factory()
	defer tr.CloseIdleConnections()
	resp, e := tr.RoundTrip(req)
	if e != nil {
		return errors.New("relay: APNs failure")
	}
	defer resp.Body.Close()
	if resp.ProtoMajor != 2 || resp.StatusCode != 200 {
		return errors.New("relay: APNs rejected notification")
	}
	return nil
}
func apnsPayload(n Notification) []byte {
	payload := struct {
		APS struct {
			Alert struct {
				Title string `json:"title"`
				Body  string `json:"body"`
			} `json:"alert"`
		} `json:"aps"`
		ClipSend Notification `json:"clipsend"`
	}{ClipSend: n}
	payload.APS.Alert.Title = "ClipSend ready"
	payload.APS.Alert.Body = "Tap to receive in ClipSend."
	b, _ := json.Marshal(payload)
	return b
}
func privateTransport() *http.Transport {
	p := new(http.Protocols)
	p.SetHTTP2(true)
	tc := &tls.Config{MinVersion: tls.VersionTLS12, NextProtos: []string{"h2"}}
	return &http.Transport{Proxy: nil, Protocols: p, ForceAttemptHTTP2: true, DisableKeepAlives: true, DisableCompression: true, MaxResponseHeaderBytes: 16 << 10, ResponseHeaderTimeout: 10 * time.Second, TLSClientConfig: tc, DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		c, e := (&tls.Dialer{NetDialer: &net.Dialer{Timeout: 10 * time.Second}, Config: tc}).DialContext(ctx, network, addr)
		if e != nil {
			return nil, e
		}
		if c.(*tls.Conn).ConnectionState().NegotiatedProtocol != "h2" {
			c.Close()
			return nil, errors.New("HTTP/2 required")
		}
		return c, nil
	}}
}
func clientTransport() *http.Transport {
	return &http.Transport{Proxy: nil, ForceAttemptHTTP2: true, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, ResponseHeaderTimeout: 10 * time.Second, MaxResponseHeaderBytes: 16 << 10}
}
func validRelayURL(u *url.URL) bool {
	if u == nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || u.Hostname() == "" {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	if u.Scheme != "http" {
		return false
	}
	ip, e := netip.ParseAddr(u.Hostname())
	return e == nil && ip.IsLoopback()
}
func validBundle(s string) bool {
	if len(s) == 0 || len(s) > 255 || strings.HasPrefix(s, ".") || strings.HasSuffix(s, ".") || strings.Contains(s, "..") {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-') {
			return false
		}
	}
	return true
}
func validID(s string) bool {
	if len(s) == 0 || len(s) > 128 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}
func upperID(s string) bool {
	for _, r := range s {
		if !(r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}
func readPrivate(path string, limit int64) ([]byte, bool, error) {
	st, e := os.Lstat(path)
	if os.IsNotExist(e) {
		return nil, true, nil
	}
	if e != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 {
		return nil, false, errors.New("unsafe file")
	}
	f, e := os.Open(path)
	if e != nil {
		return nil, false, e
	}
	defer f.Close()
	now, e := f.Stat()
	if e != nil || !os.SameFile(st, now) {
		return nil, false, errors.New("changed file")
	}
	b, e := io.ReadAll(io.LimitReader(f, limit+1))
	if e != nil || int64(len(b)) > limit {
		return nil, false, errors.New("oversize")
	}
	return b, false, nil
}
func strict(b []byte, v any) error {
	if !utf8.Valid(b) || !json.Valid(b) || duplicateJSONKeys(b) != nil {
		return errors.New("json")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return e
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("trailing")
	}
	return nil
}

func duplicateJSONKeys(b []byte) error {
	d := json.NewDecoder(bytes.NewReader(b))
	var value func() error
	value = func() error {
		tok, e := d.Token()
		if e != nil {
			return e
		}
		delim, ok := tok.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				k, e := d.Token()
				if e != nil {
					return e
				}
				key, ok := k.(string)
				if !ok || seen[key] {
					return errors.New("duplicate JSON key")
				}
				seen[key] = true
				if e = value(); e != nil {
					return e
				}
			}
			_, e = d.Token()
			return e
		case '[':
			for d.More() {
				if e = value(); e != nil {
					return e
				}
			}
			_, e = d.Token()
			return e
		default:
			return errors.New("invalid JSON")
		}
	}
	if e := value(); e != nil {
		return e
	}
	if _, e := d.Token(); e != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}
