package direct

import (
	"bytes"
	"context"
	"crypto/subtle"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

type PairRequest struct {
	DeviceName  string `json:"deviceName"`
	DeviceToken string `json:"deviceToken"`
	BundleID    string `json:"bundleID"`
	Environment string `json:"environment"`
	Invitation  string `json:"invitation"`
}
type ApprovalFunc func(context.Context, PairRequest) (alias string, approve bool)
type HandlerConfig struct {
	ComputerName string
	ApprovePair  ApprovalFunc
}
type PairingConfig struct {
	BindAddress, EndpointHost, ComputerName string
	ApprovePair                             ApprovalFunc
}

func (s *Store) TLSConfig() *tls.Config {
	id := s.Identity()
	cert, e := tls.X509KeyPair([]byte(id.CertificatePEM), []byte(id.PrivateKeyPEM))
	if e != nil {
		return &tls.Config{MinVersion: tls.VersionTLS12}
	}
	return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
}
func NewHandler(s *Store, c HandlerConfig) http.Handler {
	return &apiHandler{store: s, computerName: c.ComputerName, approve: c.ApprovePair}
}

type PairingServer struct {
	listener   net.Listener
	server     *http.Server
	endpoint   string
	invitation Invitation
	done       chan struct{}
	once       sync.Once
	mu         sync.Mutex
	result     Result
}

func ListenPairing(s *Store, c PairingConfig) (*PairingServer, error) {
	ln, e := net.Listen("tcp", c.BindAddress)
	if e != nil {
		return nil, e
	}
	host := c.EndpointHost
	if host == "" {
		host = "127.0.0.1"
	}
	ep := "https://" + net.JoinHostPort(host, fmt.Sprint(ln.Addr().(*net.TCPAddr).Port))
	inv, e := s.NewInvitation()
	if e != nil {
		_ = ln.Close()
		return nil, e
	}
	ps := &PairingServer{listener: ln, endpoint: ep, invitation: inv, done: make(chan struct{})}
	h := &apiHandler{store: s, computerName: c.ComputerName, approve: c.ApprovePair, pairing: ps}
	ps.server = &http.Server{Handler: h, TLSConfig: s.TLSConfig(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 2 * time.Minute, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	go func() {
		timer := time.NewTimer(time.Until(inv.ExpiresAt))
		defer timer.Stop()
		select {
		case <-ps.done:
			return
		case <-timer.C:
			ps.finish(Expired)
			_ = ps.server.Close()
		}
	}()
	return ps, nil
}
func (p *PairingServer) Endpoint() string       { return p.endpoint }
func (p *PairingServer) Invitation() Invitation { return p.invitation }
func (p *PairingServer) Done() <-chan struct{}  { return p.done }
func (p *PairingServer) Result() Result         { p.mu.Lock(); defer p.mu.Unlock(); return p.result }
func (p *PairingServer) finish(r Result) {
	p.once.Do(func() { p.mu.Lock(); p.result = r; p.mu.Unlock(); close(p.done) })
}
func (p *PairingServer) Serve() error {
	e := p.server.ServeTLS(p.listener, "", "")
	p.finish(Cancelled)
	if errors.Is(e, http.ErrServerClosed) {
		return nil
	}
	return e
}
func (p *PairingServer) Close() error {
	e := p.server.Close()
	p.finish(Cancelled)
	return e
}

type apiHandler struct {
	store        *Store
	computerName string
	approve      ApprovalFunc
	pairing      *PairingServer
}

func (a *apiHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodPost && r.URL.Path == "/v1/pair" {
		a.pair(w, r)
		return
	}
	if r.Method == http.MethodPost && r.URL.Path == "/v1/device/token" {
		a.refresh(w, r)
		return
	}
	http.NotFound(w, r)
}
func (a *apiHandler) pair(w http.ResponseWriter, r *http.Request) {
	var q PairRequest
	if !decodeBody(w, r, &q) {
		return
	}
	if q.Invitation == "" || !validPairFields(q) {
		http.Error(w, "invalid request", 400)
		return
	}
	if a.pairing != nil && q.Invitation != a.pairing.invitation.Value {
		http.Error(w, "pairing denied", 403)
		return
	}
	if e := a.store.consumeInvitation(q.Invitation); e != nil {
		http.Error(w, "pairing denied", 403)
		return
	}
	if a.approve == nil {
		http.Error(w, "pairing denied", 403)
		return
	}
	deadline := a.store.invitationDeadline(q.Invitation)
	if deadline.IsZero() || time.Until(deadline) > 2*time.Minute {
		deadline = time.Now().Add(2 * time.Minute)
	}
	ctx, cancel := context.WithDeadline(r.Context(), deadline)
	defer cancel()
	alias, ok := a.approve(ctx, q)
	if !ok || ctx.Err() != nil {
		http.Error(w, "pairing denied", 403)
		return
	}
	d, e := a.store.addApproved(alias, q)
	if e != nil {
		http.Error(w, "pairing denied", 403)
		return
	}
	writeJSON(w, 201, map[string]any{"version": 1, "computerID": a.store.Identity().ComputerID, "computerName": a.computerName, "deviceID": d.ID, "secret": d.Secret})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	if a.pairing != nil {
		a.pairing.finish(Acked)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_ = a.pairing.server.Shutdown(ctx)
		}()
	}
}
func (a *apiHandler) refresh(w http.ResponseWriter, r *http.Request) {
	id, ok := a.authorize(r)
	if !ok {
		unauthorized(w)
		return
	}
	var q struct {
		DeviceToken string `json:"deviceToken"`
		BundleID    string `json:"bundleID"`
		Environment string `json:"environment"`
	}
	if !decodeBody(w, r, &q) {
		return
	}
	if q.DeviceToken == "" || q.BundleID == "" || (q.Environment != "sandbox" && q.Environment != "production") {
		http.Error(w, "invalid request", 400)
		return
	}
	if e := a.store.updateToken(id, q.DeviceToken, q.BundleID, q.Environment); e != nil {
		unauthorized(w)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (a *apiHandler) authorize(r *http.Request) (string, bool) {
	f := strings.Fields(r.Header.Get("Authorization"))
	if len(f) != 2 || !strings.EqualFold(f[0], "Bearer") {
		return "", false
	}
	token := f[1]
	for _, d := range a.store.Devices() {
		if subtle.ConstantTimeCompare([]byte(token), []byte(d.Secret)) == 1 {
			return d.ID, true
		}
	}
	return "", false
}
func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 128*1024)
	b, err := io.ReadAll(r.Body)
	if err != nil || !utf8.Valid(b) || !validJSONKeys(b) {
		http.Error(w, "invalid request", 400)
		return false
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if e := dec.Decode(v); e != nil {
		http.Error(w, "invalid request", 400)
		return false
	}
	var extra any
	if e := dec.Decode(&extra); e != io.EOF {
		http.Error(w, "invalid request", 400)
		return false
	}
	return true
}
func validJSONKeys(b []byte) bool {
	d := json.NewDecoder(bytes.NewReader(b))
	if !scanJSONValue(d) {
		return false
	}
	var extra any
	return d.Decode(&extra) == io.EOF
}
func scanJSONValue(d *json.Decoder) bool {
	t, err := d.Token()
	if err != nil {
		return false
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return true
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			k, e := d.Token()
			if e != nil {
				return false
			}
			key, ok := k.(string)
			if !ok || seen[key] {
				return false
			}
			seen[key] = true
			if !scanJSONValue(d) {
				return false
			}
		}
		end, e := d.Token()
		return e == nil && end == json.Delim('}')
	case '[':
		for d.More() {
			if !scanJSONValue(d) {
				return false
			}
		}
		end, e := d.Token()
		return e == nil && end == json.Delim(']')
	default:
		return false
	}
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func unauthorized(w http.ResponseWriter) { http.Error(w, "unauthorized", http.StatusUnauthorized) }

type Result string

const (
	Acked     Result = "acked"
	Cancelled Result = "cancelled"
	Expired   Result = "expired"
)

type SessionConfig struct {
	BindAddress, EndpointHost, DeviceID, SenderName, Text string
	TTL                                                   time.Duration
}
type Session struct {
	store                                *Store
	id, deviceID, sender, text, endpoint string
	expires                              time.Time
	done                                 chan struct{}
	mu                                   sync.Mutex
	result                               Result
	server                               *http.Server
	listener                             net.Listener
	once                                 sync.Once
}

func ListenSession(s *Store, c SessionConfig) (*Session, error) {
	if c.TTL <= 0 {
		c.TTL = TransferTTL
	}
	if c.TTL > TransferTTL {
		c.TTL = TransferTTL
	}
	if c.Text == "" || len([]byte(c.Text)) > MaxTextBytes || !utf8.ValidString(c.Text) {
		return nil, errors.New("text must be nonempty UTF-8 up to 64000 bytes")
	}
	if _, ok := s.Device(c.DeviceID); !ok {
		return nil, errors.New("unknown recipient")
	}
	id, e := randomToken(18)
	if e != nil {
		return nil, e
	}
	ln, e := net.Listen("tcp", c.BindAddress)
	if e != nil {
		return nil, e
	}
	host := c.EndpointHost
	if host == "" {
		host = "127.0.0.1"
	}
	x := &Session{store: s, id: id, deviceID: c.DeviceID, sender: c.SenderName, text: c.Text, expires: time.Now().UTC().Add(c.TTL), done: make(chan struct{}), listener: ln, endpoint: "https://" + net.JoinHostPort(host, fmt.Sprint(ln.Addr().(*net.TCPAddr).Port))}
	x.server = &http.Server{Handler: &sessionHandler{x}, TLSConfig: s.TLSConfig(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		ln.Close()
		return nil, errClosed
	}
	s.sessions[id] = x
	s.mu.Unlock()
	go func() {
		timer := time.NewTimer(time.Until(x.expires))
		defer timer.Stop()
		select {
		case <-timer.C:
			x.finish(Expired)
		case <-x.done:
		}
	}()
	return x, nil
}
func (x *Session) ID() string            { return x.id }
func (x *Session) ExpiresAt() time.Time  { return x.expires }
func (x *Session) Endpoint() string      { return x.endpoint }
func (x *Session) Done() <-chan struct{} { return x.done }
func (x *Session) Result() Result        { x.mu.Lock(); defer x.mu.Unlock(); return x.result }
func (x *Session) Cancel()               { x.finish(Cancelled) }
func (x *Session) Serve() error {
	e := x.server.ServeTLS(x.listener, "", "")
	if errors.Is(e, http.ErrServerClosed) {
		return nil
	}
	return e
}
func (x *Session) finish(res Result) {
	x.once.Do(func() {
		x.mu.Lock()
		x.result = res
		x.text = ""
		x.mu.Unlock()
		// Release the listening port before reporting completion. Existing TLS
		// connections can finish flushing their acknowledgment during the grace
		// period, while the next command can bind the same fixed port immediately.
		_ = x.listener.Close()
		close(x.done)
		x.store.mu.Lock()
		delete(x.store.sessions, x.id)
		x.store.mu.Unlock()
		if res == Acked {
			go func() { time.Sleep(time.Second); _ = x.server.Close() }()
		} else {
			_ = x.server.Close()
		}
	})
}
func (x *Session) content() string { x.mu.Lock(); defer x.mu.Unlock(); return x.text }

type sessionHandler struct{ x *Session }

func (h *sessionHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	x := h.x
	if !x.store.authorizedFor(r, x.deviceID) {
		unauthorized(w)
		return
	}
	if x.Result() == Acked {
		if r.Method == "POST" && r.URL.Path == "/v1/transfers/"+x.id+"/ack" {
			w.WriteHeader(204)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			return
		}
		http.Error(w, "expired", 410)
		return
	}
	if !time.Now().Before(x.expires) {
		x.finish(Expired)
		http.Error(w, "expired", 410)
		return
	}
	if r.Method == http.MethodPost && r.URL.Path == "/v1/device/token" {
		var q struct {
			DeviceToken string `json:"deviceToken"`
			BundleID    string `json:"bundleID"`
			Environment string `json:"environment"`
		}
		if !decodeBody(w, r, &q) {
			return
		}
		if q.DeviceToken == "" || q.BundleID == "" || (q.Environment != "sandbox" && q.Environment != "production") {
			http.Error(w, "invalid request", 400)
			return
		}
		if err := x.store.updateToken(x.deviceID, q.DeviceToken, q.BundleID, q.Environment); err != nil {
			unauthorized(w)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	switch {
	case r.Method == "GET" && r.URL.Path == "/v1/transfers":
		writeJSON(w, 200, map[string]any{"version": 1, "transfers": []any{map[string]any{"id": x.id, "expiresAt": x.expires.Format(time.RFC3339)}}})
	case r.Method == "GET" && r.URL.Path == "/v1/transfers/"+x.id:
		writeJSON(w, 200, map[string]any{"version": 1, "id": x.id, "computerID": x.store.Identity().ComputerID, "senderName": x.sender, "text": x.content(), "expiresAt": x.expires.Format(time.RFC3339)})
	case r.Method == "POST" && r.URL.Path == "/v1/transfers/"+x.id+"/ack":
		w.WriteHeader(204)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		x.finish(Acked)
	default:
		if strings.HasPrefix(r.URL.Path, "/v1/transfers/") {
			http.NotFound(w, r)
		} else {
			http.NotFound(w, r)
		}
	}
}
func (s *Store) authorizedFor(r *http.Request, id string) bool {
	d, ok := s.Device(id)
	if !ok {
		return false
	}
	f := strings.Fields(r.Header.Get("Authorization"))
	return len(f) == 2 && strings.EqualFold(f[0], "Bearer") && subtle.ConstantTimeCompare([]byte(f[1]), []byte(d.Secret)) == 1
}
