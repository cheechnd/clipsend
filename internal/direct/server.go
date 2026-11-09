package direct

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
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

// TokenRefreshHandler keeps APNs routing current on persistent receiving listeners.
func (s *Store) TokenRefreshHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodPost || r.URL.Path != "/v1/device/token" {
			http.NotFound(w, r)
			return
		}
		(&apiHandler{store: s}).refresh(w, r)
	})
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
	Files                                                 []string
	ExternalEndpoint                                      string
	Progress                                              func(int64, int64)
	TTL                                                   time.Duration
}
type Attachment struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	MIME   string `json:"mime"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}
type sessionFile struct {
	Attachment
	path string
}
type Session struct {
	store                                *Store
	id, deviceID, sender, text, endpoint string
	expires                              time.Time
	done                                 chan struct{}
	mu                                   sync.Mutex
	result                               Result
	files                                []sessionFile
	tempDir                              string
	progress                             func(int64, int64)
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
	if c.Text != "" && (len([]byte(c.Text)) > MaxTextBytes || !utf8.ValidString(c.Text)) {
		return nil, errors.New("text must be UTF-8 up to 64000 bytes")
	}
	if len(c.Files) > MaxItems || (c.Text == "" && len(c.Files) == 0) {
		return nil, errors.New("transfer requires text or up to 16 attachments")
	}
	var files []sessionFile
	var aggregate int64
	tempDir, e := os.MkdirTemp("", "clipsend-outgoing-")
	if e != nil {
		return nil, e
	}
	if e = os.Chmod(tempDir, 0700); e != nil {
		os.RemoveAll(tempDir)
		return nil, e
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(tempDir)
		}
	}()
	for _, p := range c.Files {
		fd, e := syscall.Open(p, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if e != nil {
			return nil, errors.New("attachment could not be safely opened")
		}
		f := os.NewFile(uintptr(fd), p)
		st, e := f.Stat()
		if e != nil || !st.Mode().IsRegular() || st.Size() > MaxItemBytes {
			f.Close()
			return nil, errors.New("attachment must be a regular file no larger than 128 MiB")
		}
		h := sha256.New()
		id, e := randomToken(12)
		if e != nil {
			f.Close()
			return nil, e
		}
		dst := filepath.Join(tempDir, id)
		snap, e := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			f.Close()
			return nil, e
		}
		n, e := io.Copy(io.MultiWriter(h, snap), io.LimitReader(f, MaxItemBytes+1))
		ce := snap.Close()
		if e == nil {
			e = ce
		}
		if e != nil || n != st.Size() {
			f.Close()
			return nil, errors.New("attachment changed or could not be snapshotted")
		}
		f.Close()
		aggregate += st.Size()
		if aggregate > MaxTransferBytes {
			return nil, errors.New("attachments exceed 256 MiB")
		}
		mimeType := mime.TypeByExtension(filepath.Ext(p))
		if mimeType == "" {
			mimeType = "application/octet-stream"
		}
		files = append(files, sessionFile{Attachment: Attachment{ID: id, Name: safeFilename(filepath.Base(p)), MIME: mimeType, Size: st.Size(), SHA256: hex.EncodeToString(h.Sum(nil))}, path: dst})
	}
	if _, ok := s.Device(c.DeviceID); !ok {
		return nil, errors.New("unknown recipient")
	}
	id, e := randomToken(18)
	if e != nil {
		return nil, e
	}
	var ln net.Listener
	if c.ExternalEndpoint == "" {
		ln, e = net.Listen("tcp", c.BindAddress)
		if e != nil {
			return nil, e
		}
	}
	host := c.EndpointHost
	if host == "" {
		host = "127.0.0.1"
	}
	cleanup = false
	endpoint := c.ExternalEndpoint
	if endpoint == "" {
		endpoint = "https://" + net.JoinHostPort(host, fmt.Sprint(ln.Addr().(*net.TCPAddr).Port))
	}
	x := &Session{store: s, id: id, deviceID: c.DeviceID, sender: c.SenderName, text: c.Text, files: files, tempDir: tempDir, progress: c.Progress, expires: time.Now().UTC().Add(c.TTL), done: make(chan struct{}), listener: ln, endpoint: endpoint}
	x.server = &http.Server{Handler: &sessionHandler{x}, TLSConfig: s.TLSConfig(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 5 * time.Minute, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		ln.Close()
		return nil, errClosed
	}
	s.sessions[id] = x
	s.mu.Unlock()
	cleanup = false
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
	if x.listener == nil {
		return errors.New("session is attached to an external listener")
	}
	e := x.server.ServeTLS(x.listener, "", "")
	if errors.Is(e, http.ErrServerClosed) {
		return nil
	}
	return e
}
func (x *Session) Handler() http.Handler           { return &sessionHandler{x} }
func (x *Session) Authorized(r *http.Request) bool { return x.store.authorizedFor(r, x.deviceID) }
func (x *Session) DeviceID() string                { return x.deviceID }
func (x *Session) finish(res Result) {
	x.once.Do(func() {
		x.mu.Lock()
		x.result = res
		x.text = ""
		x.files = nil
		x.mu.Unlock()
		// Release the listening port before reporting completion. Existing TLS
		// connections can finish flushing their acknowledgment during the grace
		// period, while the next command can bind the same fixed port immediately.
		if x.listener != nil {
			_ = x.listener.Close()
		}
		close(x.done)
		x.store.mu.Lock()
		delete(x.store.sessions, x.id)
		x.store.mu.Unlock()
		if res == Acked {
			go func() { time.Sleep(time.Second); _ = x.server.Close() }()
		} else {
			_ = x.server.Close()
		}
		_ = os.RemoveAll(x.tempDir)
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
		writeJSON(w, 200, map[string]any{"version": 1, "id": x.id, "computerID": x.store.Identity().ComputerID, "senderName": x.sender, "text": x.content(), "attachments": x.attachments(), "expiresAt": x.expires.Format(time.RFC3339)})
	case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/v1/transfers/"+x.id+"/items/"):
		itemID := strings.TrimPrefix(r.URL.Path, "/v1/transfers/"+x.id+"/items/")
		if strings.Contains(itemID, "/") || !validRandom(itemID, 12) {
			http.NotFound(w, r)
			return
		}
		var item *sessionFile
		x.mu.Lock()
		for i := range x.files {
			if x.files[i].ID == itemID {
				item = &x.files[i]
				break
			}
		}
		x.mu.Unlock()
		if item == nil {
			http.NotFound(w, r)
			return
		}
		f, e := os.Open(item.path)
		if e != nil {
			http.Error(w, "attachment unavailable", 410)
			return
		}
		defer f.Close()
		w.Header().Set("Content-Length", strconv.FormatInt(item.Size, 10))
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = io.CopyN(&sessionProgressWriter{w: w, total: item.Size, progress: x.progress}, f, item.Size)
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

type sessionProgressWriter struct {
	w        io.Writer
	total    int64
	sent     int64
	progress func(int64, int64)
}

func (p *sessionProgressWriter) Write(b []byte) (int, error) {
	n, e := p.w.Write(b)
	p.sent += int64(n)
	if p.progress != nil {
		p.progress(p.sent, p.total)
	}
	return n, e
}
func (x *Session) attachments() []Attachment {
	x.mu.Lock()
	defer x.mu.Unlock()
	out := make([]Attachment, 0, len(x.files))
	for _, f := range x.files {
		out = append(out, f.Attachment)
	}
	return out
}
func safeFilename(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r < 32 || r == 127 {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if s == "" || s == "." || s == ".." {
		return "attachment"
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}
func (s *Store) authorizedFor(r *http.Request, id string) bool {
	d, ok := s.Device(id)
	if !ok {
		return false
	}
	f := strings.Fields(r.Header.Get("Authorization"))
	return len(f) == 2 && strings.EqualFold(f[0], "Bearer") && subtle.ConstantTimeCompare([]byte(f[1]), []byte(d.Secret)) == 1
}
func (s *Store) AuthorizedDevice(r *http.Request) (Device, bool) {
	f := strings.Fields(r.Header.Get("Authorization"))
	if len(f) != 2 || !strings.EqualFold(f[0], "Bearer") {
		return Device{}, false
	}
	for _, d := range s.Devices() {
		if subtle.ConstantTimeCompare([]byte(f[1]), []byte(d.Secret)) == 1 {
			return d, true
		}
	}
	return Device{}, false
}
