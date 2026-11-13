package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"clipsend/internal/direct"
	"clipsend/internal/relay"
	"github.com/grandcat/zeroconf"
	"github.com/skip2/go-qrcode"
)

type uiRequest struct {
	ID         string   `json:"id"`
	Command    string   `json:"command"`
	Alias      string   `json:"alias"`
	Text       string   `json:"text,omitempty"`
	Files      []string `json:"files"`
	RequestID  string   `json:"requestId"`
	Enabled    bool     `json:"enabled"`
	NewAlias   string   `json:"newAlias"`
	TransferID string   `json:"transferId"`
	ItemID     string   `json:"itemId"`
	Path       string   `json:"path"`
	Allow      bool     `json:"allow"`
}
type uiEvent struct {
	Version int    `json:"version"`
	ID      string `json:"id"`
	Type    string `json:"type"`
	Data    any    `json:"-"`
}
type uiWriter struct {
	mu sync.Mutex
	w  *bufio.Writer
}
type uiTransfers struct {
	mu       sync.RWMutex
	sessions map[string]*direct.Session
	store    *direct.Store
}

func newUITransfers(s *direct.Store) *uiTransfers {
	return &uiTransfers{sessions: map[string]*direct.Session{}, store: s}
}
func (t *uiTransfers) Add(s *direct.Session) { t.mu.Lock(); t.sessions[s.ID()] = s; t.mu.Unlock() }
func (t *uiTransfers) Remove(id string)      { t.mu.Lock(); delete(t.sessions, id); t.mu.Unlock() }
func (t *uiTransfers) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 2 || parts[0] != "v1" || parts[1] != "transfers" {
		http.NotFound(w, r)
		return
	}
	t.mu.RLock()
	var s *direct.Session
	if len(parts) == 2 {
		var deviceID string
		for _, candidate := range t.sessions {
			if candidate.Authorized(r) {
				deviceID = candidate.DeviceID()
				break
			}
		}
		if deviceID == "" {
			_, authorized := t.store.AuthorizedDevice(r)
			t.mu.RUnlock()
			if !authorized {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(200)
			_ = json.NewEncoder(w).Encode(map[string]any{"version": 1, "transfers": []any{}})
			return
		}
		entries := []map[string]any{}
		for _, candidate := range t.sessions {
			if candidate.DeviceID() == deviceID {
				entries = append(entries, map[string]any{"id": candidate.ID(), "expiresAt": candidate.ExpiresAt().Format(time.RFC3339)})
			}
		}
		t.mu.RUnlock()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(200)
		_ = json.NewEncoder(w).Encode(map[string]any{"version": 1, "transfers": entries})
		return
	} else {
		s = t.sessions[parts[2]]
	}
	t.mu.RUnlock()
	if s == nil {
		http.NotFound(w, r)
		return
	}
	s.Handler().ServeHTTP(w, r)
}

func (w *uiWriter) emit(id, typ string, fields map[string]any) {
	m := map[string]any{"version": 1, "id": id, "type": typ}
	for k, v := range fields {
		m[k] = v
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	b, _ := json.Marshal(m)
	_, _ = w.w.Write(append(b, '\n'))
	_ = w.w.Flush()
}
func uiCommand(ctx context.Context, host string, port int, in io.Reader, out, errOut io.Writer) error {
	uiCtx, cancelUI := context.WithCancel(ctx)
	defer cancelUI()
	if port < 1 || port > 65535 {
		return errors.New("invalid port")
	}
	if host != "" {
		_, e := selectLAN(host)
		if e != nil {
			return e
		}
	}
	dir, e := nativeConfigDir()
	if e != nil {
		return e
	}
	store, e := direct.OpenStore(dir)
	if e != nil {
		return e
	}
	defer store.Close()
	writer := &uiWriter{w: bufio.NewWriter(out)}
	previews := newUIPreviews()
	defer previews.close()
	jobs := map[string]context.CancelFunc{}
	var jobsMu sync.Mutex
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	type line struct {
		b   []byte
		err error
	}
	lines := make(chan line)
	go func() {
		defer close(lines)
		for scanner.Scan() {
			select {
			case lines <- line{b: append([]byte(nil), scanner.Bytes()...)}:
			case <-uiCtx.Done():
				return
			}
		}
		if scanner.Err() != nil {
			select {
			case lines <- line{err: scanner.Err()}:
			case <-uiCtx.Done():
			}
		}
	}()
	var listener net.Listener
	var httpServer *http.Server
	var announcement *zeroconf.Server
	transfers := newUITransfers(store)
	var inbox *direct.Inbox
	if cache, e := os.UserCacheDir(); e == nil {
		inbox, e = direct.NewInbox(filepath.Join(cache, "clipsend", "inbox"))
		if e != nil {
			fmt.Fprintln(errOut, "clipsend: private inbox storage unavailable")
		}
	} else {
		fmt.Fprintln(errOut, "clipsend: private inbox storage unavailable")
	}
	pairApprovals := map[string]chan bool{}
	var pairServer *direct.PairingServer
	var stateMu sync.Mutex
	var jobWG sync.WaitGroup
	var pairWG sync.WaitGroup
	var socketWG sync.WaitGroup
	activeIDs := map[string]bool{}
	socket, socketPath, sockErr := startUISocket(dir, &socketWG, func(q uiRequest, conn net.Conn) {
		defer conn.Close()
		socketWriter := &uiWriter{w: bufio.NewWriter(conn)}
		fail := func(code, msg string) { socketWriter.emit(q.ID, "error", map[string]any{"code": code, "message": msg}) }
		stateMu.Lock()
		if activeIDs[q.ID] {
			stateMu.Unlock()
			fail("duplicate_id", "request id is already active")
			return
		}
		activeIDs[q.ID] = true
		stateMu.Unlock()
		defer func() { stateMu.Lock(); delete(activeIDs, q.ID); stateMu.Unlock() }()
		jobsMu.Lock()
		socketCtx, cancel := context.WithCancel(uiCtx)
		jobs[q.ID] = cancel
		jobsMu.Unlock()
		stateMu.Lock()
		on := httpServer != nil
		recv := listener
		var receiver *http.Server
		if on {
			receiver = httpServer
		}
		stateMu.Unlock()
		port := defaultPort
		if on {
			port = 0
		}
		watchDone := make(chan struct{})
		go func() { defer close(watchDone); var b [1]byte; _, _ = conn.Read(b[:]); cancel() }()
		sendUI(socketCtx, dir, store, q, socketWriter, jobs, &jobsMu, host, port, transfers, receiver, recv, fail)
		cancel()
		_ = conn.Close()
		<-watchDone
	})
	if sockErr != nil {
		fmt.Fprintln(errOut, "clipsend: standalone send delegation unavailable")
	}
	defer func() {
		cancelUI()
		jobsMu.Lock()
		for _, c := range jobs {
			c()
		}
		jobsMu.Unlock()
		if socket != nil {
			_ = socket.Close()
			_ = os.Remove(socketPath)
		}
		socketWG.Wait()
		jobWG.Wait()
		stateMu.Lock()
		activePair := pairServer
		pairServer = nil
		stateMu.Unlock()
		if activePair != nil {
			_ = activePair.Close()
		}
		pairWG.Wait()
		stateMu.Lock()
		activeHTTP := httpServer
		httpServer = nil
		activeListener := listener
		listener = nil
		activeAnnouncement := announcement
		announcement = nil
		activeInbox := inbox
		inbox = nil
		stateMu.Unlock()
		if activeAnnouncement != nil {
			activeAnnouncement.Shutdown()
		}
		if activeHTTP != nil {
			shutdownCtx, c := context.WithTimeout(context.Background(), 2*time.Second)
			_ = activeHTTP.Shutdown(shutdownCtx)
			c()
			_ = activeHTTP.Close()
		}
		if activeListener != nil {
			_ = activeListener.Close()
		}
		if activeInbox != nil {
			_ = activeInbox.Close()
		}
	}()
	for {
		select {
		case <-uiCtx.Done():
			return nil
		case ln, ok := <-lines:
			if !ok {
				return nil
			}
			if ln.err != nil {
				return ln.err
			}
			var q uiRequest
			if e := json.Unmarshal(ln.b, &q); e != nil || !validUIJSONKeys(ln.b) || q.ID == "" {
				writer.emit(q.ID, "error", map[string]any{"code": "invalid_request", "message": "invalid command"})
				continue
			}
			stateMu.Lock()
			if activeIDs[q.ID] {
				stateMu.Unlock()
				writer.emit(q.ID, "error", map[string]any{"code": "duplicate_id", "message": "request id is already active"})
				continue
			}
			activeIDs[q.ID] = true
			jobWG.Add(1)
			go func(q uiRequest) {
				defer jobWG.Done()
				defer func() { stateMu.Lock(); delete(activeIDs, q.ID); stateMu.Unlock() }()
				fail := func(code, msg string) { writer.emit(q.ID, "error", map[string]any{"code": code, "message": msg}) }
				result := func(fields map[string]any) { writer.emit(q.ID, "result", fields) }
				switch q.Command {
				case "devices":
					_, ds, e := store.Snapshot()
					if e != nil {
						fail("internal", e.Error())
						return
					}
					out := []map[string]string{}
					for _, d := range ds {
						out = append(out, map[string]string{"alias": safeLabel(d.Alias), "name": safeLabel(d.Name)})
					}
					result(map[string]any{"devices": out})
				case "rename":
					d, ok := store.FindDevice(q.Alias)
					if !ok {
						fail("not_found", "paired device not found")
						return
					}
					if e := store.RenameDevice(d.ID, q.NewAlias); e != nil {
						fail("invalid_alias", e.Error())
						return
					}
					result(nil)
				case "unpair":
					d, ok := store.FindDevice(q.Alias)
					if !ok {
						fail("not_found", "paired device not found")
						return
					}
					if e := store.RevokeDevice(d.ID); e != nil {
						fail("internal", "could not unpair device")
						return
					}
					result(nil)
					writer.emit(q.ID, "devices", map[string]any{"devices": deviceViews(store.Devices())})
				case "cancel":
					jobsMu.Lock()
					c := jobs[q.RequestID]
					jobsMu.Unlock()
					if c == nil {
						stateMu.Lock()
						pairCh := pairApprovals[q.RequestID]
						pair := pairServer
						stateMu.Unlock()
						if pairCh != nil {
							select {
							case pairCh <- false:
							default:
							}
							if pair != nil {
								_ = pair.Close()
							}
							result(nil)
						} else {
							fail("not_found", "job not found")
						}
					} else {
						c()
						result(nil)
					}
				case "approve":
					stateMu.Lock()
					ch := pairApprovals[q.RequestID]
					stateMu.Unlock()
					if ch == nil {
						fail("not_found", "pairing request not found")
					} else {
						select {
						case ch <- q.Allow:
							result(nil)
						default:
							fail("conflict", "pairing request already handled")
						}
					}
				case "receive":
					stateMu.Lock()
					if !q.Enabled {
						if httpServer != nil {
							_ = httpServer.Close()
							httpServer = nil
							if announcement != nil {
								announcement.Shutdown()
								announcement = nil
							}
							listener = nil
						}
						stateMu.Unlock()
						result(map[string]any{"enabled": false})
						return
					}
					if httpServer != nil {
						endpoint := "https://" + listener.Addr().String()
						stateMu.Unlock()
						result(map[string]any{"enabled": true, "endpoint": endpoint})
						return
					}
					ln, sv, ann, endpoint, e := startUIReceiver(host, port, dir, store, &inbox, transfers, writer)
					if e != nil {
						stateMu.Unlock()
						fail("listen_failed", e.Error())
						return
					}
					listener, httpServer, announcement = ln, sv, ann
					stateMu.Unlock()
					result(map[string]any{"enabled": true, "endpoint": endpoint})
				case "send":
					stateMu.Lock()
					isReceiving := httpServer != nil
					receiver := httpServer
					recvListener := listener
					stateMu.Unlock()
					sendPort := port
					if isReceiving {
						sendPort = 0
					}
					sendUI(uiCtx, dir, store, q, writer, jobs, &jobsMu, host, sendPort, transfers, receiver, recvListener, fail)
				case "pair":
					stateMu.Lock()
					wasReceiving := httpServer != nil
					if wasReceiving {
						_ = httpServer.Close()
						httpServer = nil
						if announcement != nil {
							announcement.Shutdown()
							announcement = nil
						}
						listener = nil
					}
					stateMu.Unlock()
					onDone := func() {
						if wasReceiving {
							if uiCtx.Err() != nil {
								return
							}
							stateMu.Lock()
							ln, sv, ann, endpoint, e := startUIReceiver(host, port, dir, store, &inbox, transfers, writer)
							if e == nil {
								listener, httpServer, announcement = ln, sv, ann
								writer.emit(q.ID, "status", map[string]any{"state": "receive_restored", "endpoint": endpoint})
							}
							stateMu.Unlock()
						}
					}
					startUIPair(uiCtx, store, q, host, port, writer, &pairServer, pairApprovals, &stateMu, &pairWG, fail, onDone)
				case "inbox":
					stateMu.Lock()
					currentInbox := inbox
					stateMu.Unlock()
					if currentInbox == nil {
						result(map[string]any{"items": []any{}})
					} else {
						result(map[string]any{"items": inboxViews(currentInbox, store)})
					}
				case "clear", "copy", "save", "open":
					stateMu.Lock()
					currentInbox := inbox
					stateMu.Unlock()
					if q.Command == "open" {
						if e := previews.openInbox(currentInbox, q.TransferID, q.ItemID); e != nil {
							fail("open_failed", e.Error())
						} else {
							result(map[string]any{"state": "opened"})
						}
						return
					}
					if currentInbox == nil {
						fail("not_found", "inbox item not found")
						return
					}
					if q.Command == "clear" {
						if e := currentInbox.Clear(q.TransferID); e != nil {
							fail("not_found", "inbox item not found")
						} else {
							result(nil)
							writer.emit(q.ID, "status", map[string]any{"state": "inbox_updated"})
						}
						return
					}
					if q.Command == "save" {
						p, e := currentInbox.File(q.TransferID, q.ItemID)
						if e != nil {
							fail("not_found", "inbox item not found")
							return
						}
						if e := saveNoOverwrite(p, q.Path); e != nil {
							fail("save_failed", e.Error())
						} else {
							result(nil)
						}
						return
					}
					if e := copyInbox(currentInbox, q.TransferID, q.ItemID); e != nil {
						fail("copy_failed", e.Error())
					} else {
						result(nil)
					}
				default:
					fail("invalid_command", "unknown command")
				}
			}(q)
			stateMu.Unlock()
		}
	}
}
func sendUI(parent context.Context, dir string, s *direct.Store, q uiRequest, w *uiWriter, jobs map[string]context.CancelFunc, mu *sync.Mutex, host string, port int, transfers *uiTransfers, receiver *http.Server, receiveListener net.Listener, fail func(string, string)) {
	if q.Alias == "" {
		fail("invalid_request", "alias is required")
		return
	}
	d, ok := s.FindDevice(q.Alias)
	if !ok {
		fail("not_found", "paired device not found")
		return
	}
	text := q.Text
	files := append([]string(nil), q.Files...)
	for _, p := range files {
		if !filepath.IsAbs(p) {
			fail("invalid_path", "attachment paths must be absolute")
			return
		}
	}
	if text != "" {
		if e := validateText(text); e != nil {
			fail("invalid_text", e.Error())
			return
		}
	}
	if len(files) == 0 && text == "" {
		var textErr error
		text, textErr = readClipboard(parent)
		if textErr != nil {
			imagePath, imageErr := readClipboardPNG(parent)
			if imageErr == nil {
				files = []string{imagePath}
				defer os.Remove(imagePath)
			} else {
				fail("clipboard_unavailable", textErr.Error())
				return
			}
		}
		if text == "" && len(files) == 0 {
			fail("empty_clipboard", "clipboard has no supported text or image")
			return
		}
	}
	if host == "" {
		lan, e := selectLAN("")
		if e != nil {
			fail("network", e.Error())
			return
		}
		host = lan.IP
	}
	dctx, cancel := context.WithCancel(parent)
	mu.Lock()
	jobs[q.ID] = cancel
	mu.Unlock()
	defer func() { cancel(); mu.Lock(); delete(jobs, q.ID); mu.Unlock() }()
	w.emit(q.ID, "status", map[string]any{"state": "preparing"})
	config := direct.SessionConfig{BindAddress: net.JoinHostPort(host, strconv.Itoa(port)), EndpointHost: host, DeviceID: d.ID, SenderName: computerName(), Text: text, Files: files, Progress: func(sent, total int64) { w.emit(q.ID, "progress", map[string]any{"bytes": sent, "total": total}) }}
	if receiver != nil && receiveListener != nil {
		config.ExternalEndpoint = "https://" + net.JoinHostPort(host, strconv.Itoa(receiveListener.Addr().(*net.TCPAddr).Port))
	}
	session, e := direct.ListenSession(s, config)
	if e != nil {
		fail("send_failed", e.Error())
		return
	}
	defer session.Cancel()
	if config.ExternalEndpoint != "" {
		transfers.Add(session)
	} else {
		go session.Serve()
		u, _ := url.Parse(session.Endpoint())
		transferPort, _ := strconv.Atoi(u.Port())
		if lan, e := selectLAN(host); e == nil {
			if announcement, e := advertise(lan, transferPort, s.Identity().ComputerID, session.ID()); e == nil {
				defer announcement.Shutdown()
			}
		}
	}
	go func() { <-session.Done(); transfers.Remove(session.ID()) }()
	w.emit(q.ID, "status", map[string]any{"state": "waiting", "endpoint": session.Endpoint()})
	grace := time.NewTimer(750 * time.Millisecond)
	defer grace.Stop()
	select {
	case <-session.Done():
		if session.Result() == direct.Acked {
			w.emit(q.ID, "result", map[string]any{"state": "received"})
		} else if session.Result() == direct.Expired {
			fail("expired", "send expired")
		} else {
			fail("cancelled", "send cancelled")
		}
		return
	case <-dctx.Done():
		fail("cancelled", "send cancelled")
		return
	case <-grace.C:
	}
	cfg, configErr := readRelaySettings(filepath.Join(dir, "relay.json"))
	if configErr == nil {
		notifyCtx, ncancel := context.WithTimeout(dctx, 15*time.Second)
		e = cfg.notify(notifyCtx, relay.Request{DeviceToken: d.DeviceToken, BundleID: d.BundleID, Environment: d.Environment, Notification: relay.Notification{Version: 1, ComputerID: s.Identity().ComputerID, TransferID: session.ID(), Endpoints: []string{session.Endpoint()}}})
		ncancel()
		if e != nil {
			w.emit(q.ID, "status", map[string]any{"state": "waiting", "message": "Notification could not be sent. Open ClipSend on the device to receive."})
		}
	} else if !errors.Is(configErr, os.ErrNotExist) {
		fail("configuration", "notification configuration is invalid")
		return
	}
	w.emit(q.ID, "status", map[string]any{"state": "sending"})
	select {
	case <-dctx.Done():
		fail("cancelled", "send cancelled")
	case <-session.Done():
		if session.Result() == direct.Acked {
			w.emit(q.ID, "result", map[string]any{"state": "received"})
		} else if session.Result() == direct.Expired {
			fail("expired", "send expired")
		} else {
			fail("cancelled", "send cancelled")
		}
	}
}

// command execution is isolated here so tests can replace the clipboard path via PATH.
var execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, name, args...)
}

func receiveHandler(store *direct.Store, inbox *direct.Inbox, events *uiWriter) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.URL.Path == "/v1/incoming" && r.Method == "POST" {
			dev, ok := store.AuthorizedDevice(r)
			if !ok {
				http.Error(w, "unauthorized", 401)
				return
			}
			var q struct {
				Version     int                         `json:"version"`
				Text        string                      `json:"text"`
				Attachments []direct.IncomingAttachment `json:"attachments"`
			}
			if !decodeUIBody(w, r, &q) {
				return
			}
			if q.Version != 1 {
				http.Error(w, "invalid version", 400)
				return
			}
			x, e := inbox.Create(dev.ID, q.Text, q.Attachments)
			if e != nil {
				http.Error(w, "invalid manifest", 400)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(201)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": x.ID, "expiresAt": x.ExpiresAt.Format(time.RFC3339)})
			return
		}
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) < 3 || parts[0] != "v1" || parts[1] != "incoming" {
			http.NotFound(w, r)
			return
		}
		dev, ok := store.AuthorizedDevice(r)
		if !ok {
			http.Error(w, "unauthorized", 401)
			return
		}
		id := parts[2]
		if r.Method == "DELETE" && len(parts) == 3 {
			if e := inbox.Cancel(id); e != nil {
				http.NotFound(w, r)
			} else {
				w.WriteHeader(204)
			}
			return
		}
		if len(parts) == 5 && parts[3] == "items" && r.Method == "PUT" {
			r.Body = http.MaxBytesReader(w, r.Body, direct.MaxItemBytes+1)
			if e := inbox.Put(id, parts[4], r.Body); e != nil {
				http.Error(w, "invalid attachment", 400)
			} else {
				w.WriteHeader(204)
			}
			return
		}
		if len(parts) == 4 && parts[3] == "complete" && r.Method == "POST" {
			if e := inbox.Complete(id); e != nil {
				http.Error(w, "incomplete transfer", 409)
				return
			}
			for _, x := range inbox.Items() {
				if x.ID == id {
					d, _ := store.Device(dev.ID)
					events.emit("incoming", "incoming", map[string]any{"transferId": id, "alias": safeLabel(d.Alias), "text": x.Text, "attachments": inboxAttachments(inbox, x)})
					break
				}
			}
			w.WriteHeader(204)
			return
		}
		http.NotFound(w, r)
	})
}
func startUIReceiver(host string, port int, dir string, store *direct.Store, inbox **direct.Inbox, transfers *uiTransfers, events *uiWriter) (net.Listener, *http.Server, *zeroconf.Server, string, error) {
	bind := host
	if bind == "" {
		lan, e := selectLAN("")
		if e != nil {
			return nil, nil, nil, "", e
		}
		bind = lan.IP
	}
	ln, e := net.Listen("tcp", net.JoinHostPort(bind, strconv.Itoa(port)))
	if e != nil {
		return nil, nil, nil, "", e
	}
	if *inbox == nil {
		cache, e := os.UserCacheDir()
		if e != nil {
			ln.Close()
			return nil, nil, nil, "", e
		}
		*inbox, e = direct.NewInbox(filepath.Join(cache, "clipsend", "inbox"))
		if e != nil {
			ln.Close()
			return nil, nil, nil, "", e
		}
	}
	incoming := receiveHandler(store, *inbox, events)
	tokens := store.TokenRefreshHandler()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/device/token" {
			tokens.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/v1/transfers" || strings.HasPrefix(r.URL.Path, "/v1/transfers/") {
			transfers.ServeHTTP(w, r)
			return
		}
		incoming.ServeHTTP(w, r)
	})
	srv := &http.Server{Handler: handler, TLSConfig: store.TLSConfig(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 5 * time.Minute, WriteTimeout: 5 * time.Minute, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	go func() { _ = srv.ServeTLS(ln, "", "") }()
	lan, _ := selectLAN(bind)
	var ann *zeroconf.Server
	if lan.IP != "" {
		ann, _ = advertiseReceive(lan, ln.Addr().(*net.TCPAddr).Port, store.Identity().ComputerID)
	}
	endpoint := "https://" + net.JoinHostPort(bind, strconv.Itoa(ln.Addr().(*net.TCPAddr).Port))
	return ln, srv, ann, endpoint, nil
}
func startUIPair(ctx context.Context, store *direct.Store, q uiRequest, host string, port int, events *uiWriter, server **direct.PairingServer, approvals map[string]chan bool, mu *sync.Mutex, wg *sync.WaitGroup, fail func(string, string), onDone func()) {
	if strings.TrimSpace(q.Alias) == "" || len(q.Alias) > 64 || safeLabel(q.Alias) != q.Alias {
		fail("invalid_alias", "use a short alias without control characters")
		if onDone != nil {
			onDone()
		}
		return
	}
	mu.Lock()
	already := *server != nil
	mu.Unlock()
	if already {
		fail("busy", "pairing is already active")
		if onDone != nil {
			onDone()
		}
		return
	}
	lan, e := selectLAN(host)
	if e != nil {
		fail("network", e.Error())
		if onDone != nil {
			onDone()
		}
		return
	}
	ch := make(chan bool, 1)
	var ps *direct.PairingServer
	mu.Lock()
	approvals[q.ID] = ch
	mu.Unlock()
	ps, e = direct.ListenPairing(store, direct.PairingConfig{BindAddress: net.JoinHostPort(lan.IP, strconv.Itoa(port)), EndpointHost: lan.IP, ComputerName: computerName(), ApprovePair: func(c context.Context, req direct.PairRequest) (string, bool) {
		events.emit(q.ID, "approval", map[string]any{"name": safeLabel(req.DeviceName)})
		select {
		case ok := <-ch:
			if !ok && ps != nil {
				go ps.Close()
			}
			return q.Alias, ok
		case <-c.Done():
			return "", false
		}
	}})
	if e != nil {
		mu.Lock()
		delete(approvals, q.ID)
		mu.Unlock()
		fail("pair_failed", "could not start pairing")
		if onDone != nil {
			onDone()
		}
		return
	}
	mu.Lock()
	*server = ps
	mu.Unlock()
	inv := ps.Invitation()
	identity := store.Identity()
	payload, _ := json.Marshal(map[string]any{"version": 1, "computerID": identity.ComputerID, "computerName": computerName(), "endpoint": ps.Endpoint(), "certificateSHA256": identity.CertificateSHA256, "invitation": inv.Value, "expiresAt": inv.ExpiresAt.Format(time.RFC3339)})
	qr, e := qrcode.Encode(string(payload), qrcode.Medium, 512)
	if e != nil {
		_ = ps.Close()
		fail("pair_failed", "could not generate pairing code")
		if onDone != nil {
			onDone()
		}
		return
	}
	events.emit(q.ID, "pairing", map[string]any{"qr": string(payload), "png": base64.StdEncoding.EncodeToString(qr), "expiresAt": inv.ExpiresAt.Format(time.RFC3339)})
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = ps.Serve()
		mu.Lock()
		delete(approvals, q.ID)
		if *server == ps {
			*server = nil
		}
		mu.Unlock()
		if ps.Result() == direct.Acked {
			events.emit(q.ID, "result", map[string]any{"alias": q.Alias})
		} else {
			events.emit(q.ID, "error", map[string]any{"code": "cancelled", "message": "pairing ended"})
		}
		if onDone != nil {
			onDone()
		}
	}()
	_ = ctx
}
func saveNoOverwrite(src, dst string) error {
	if dst == "" {
		return errors.New("destination is required")
	}
	in, e := os.Open(src)
	if e != nil {
		return e
	}
	defer in.Close()
	tmp, e := os.CreateTemp(filepath.Dir(dst), ".clipsend-save-")
	if e != nil {
		return e
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if e = tmp.Chmod(0600); e != nil {
		tmp.Close()
		return e
	}
	_, e = io.Copy(tmp, in)
	if e == nil {
		e = tmp.Sync()
	}
	ce := tmp.Close()
	if e != nil {
		return e
	}
	if ce != nil {
		return ce
	}
	if e = os.Link(tmpPath, dst); e != nil {
		return e
	}
	return nil
}

const uiPreviewRetention = 24 * time.Hour

type uiPreviews struct {
	mu    sync.Mutex
	files map[[2]string]string
	done  chan struct{}
	wg    sync.WaitGroup
}

func newUIPreviews() *uiPreviews {
	p := &uiPreviews{files: make(map[[2]string]string), done: make(chan struct{})}
	p.cleanup(time.Now())
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-p.done:
				return
			case now := <-ticker.C:
				p.cleanup(now)
			}
		}
	}()
	return p
}

func (p *uiPreviews) close() { close(p.done); p.wg.Wait() }

func (p *uiPreviews) cleanup(now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	entries, _ := os.ReadDir(os.TempDir())
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "clipsend-preview-") || !entry.IsDir() {
			continue
		}
		st, err := entry.Info()
		if err != nil || st.Mode().Perm() != 0700 || now.Sub(st.ModTime()) < uiPreviewRetention {
			continue
		}
		if sys, ok := st.Sys().(*syscall.Stat_t); !ok || sys.Uid != uint32(os.Getuid()) {
			continue
		}
		_ = os.RemoveAll(filepath.Join(os.TempDir(), entry.Name()))
	}
	for key, file := range p.files {
		if _, err := os.Stat(file); err != nil {
			delete(p.files, key)
		}
	}
}

// openInbox only accepts inbox IDs. Neither sender filenames/MIME declarations
// nor the UI request's path may select a file or its desktop handler.
func (previews *uiPreviews) openInbox(b *direct.Inbox, transfer, item string) error {
	previews.mu.Lock()
	defer previews.mu.Unlock()
	if b == nil {
		return errors.New("inbox attachment unavailable")
	}
	p, err := b.File(transfer, item)
	if err != nil {
		return errors.New("inbox attachment unavailable")
	}
	key := [2]string{transfer, item}
	if preview := previews.files[key]; preview != "" {
		if _, err := os.Stat(preview); err == nil {
			now := time.Now()
			if err := os.Chtimes(filepath.Dir(preview), now, now); err != nil {
				return errors.New("could not retain attachment preview")
			}
			return launchInboxPreview(preview)
		}
		delete(previews.files, key)
	}
	f, err := openInboxFile(p)
	if err != nil {
		return errors.New("inbox attachment unavailable")
	}
	data, err := io.ReadAll(io.LimitReader(f, direct.MaxItemBytes+1))
	f.Close()
	if err != nil || int64(len(data)) > direct.MaxItemBytes {
		return errors.New("could not read inbox attachment")
	}
	_, suffix := inboxPreviewType(data)
	if suffix == "" {
		return errors.New("this attachment cannot be opened safely; save it instead")
	}
	dir, err := os.MkdirTemp("", "clipsend-preview-")
	if err != nil {
		return errors.New("could not prepare attachment preview")
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.RemoveAll(dir)
		}
	}()
	preview, err := filepath.Abs(filepath.Join(dir, "attachment"+suffix))
	if err != nil {
		return errors.New("could not prepare attachment preview")
	}
	if err = os.WriteFile(preview, data, 0600); err != nil {
		return errors.New("could not prepare attachment preview")
	}
	if err := launchInboxPreview(preview); err != nil {
		return err
	}
	// Viewers may read lazily after the launcher exits. Clear and UI shutdown
	// retain this copy; expiry is 24 hours after the most recent explicit Open.
	previews.files[key] = preview
	keep = true
	return nil
}

func launchInboxPreview(preview string) error {
	// Absolute, generated paths are a single argument, never a shell command,
	// URI, sender-controlled extension, or executable file.
	cmd := exec.Command("xdg-open", preview)
	if err := cmd.Start(); err != nil {
		return errors.New("could not start the system viewer")
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	// Some desktop handlers keep xdg-open alive for the viewer's lifetime.
	// Reap it without tying that lifetime to the UI or killing the viewer.
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case err := <-done:
		if err != nil {
			return errors.New("the system viewer could not open this attachment")
		}
	case <-timer.C:
	}
	return nil
}

func openInboxFile(p string) (*os.File, error) {
	fd, err := syscall.Open(p, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), p)
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() > direct.MaxItemBytes {
		f.Close()
		return nil, errors.New("invalid inbox file")
	}
	return f, nil
}

// Identify HEIF-family files from a complete ISO BMFF file-type box. Go's
// HTTP sniffer does not identify HEIC; extensions and incoming MIME are untrusted.
func inboxHEIFType(data []byte) (string, string) {
	if len(data) < 16 || string(data[4:8]) != "ftyp" {
		return "", ""
	}
	n := int64(binary.BigEndian.Uint32(data[:4]))
	if n < 16 || n > 4096 || n > int64(len(data)) || n%4 != 0 {
		return "", ""
	}
	brands := map[string]bool{string(data[8:12]): true}
	for i := 16; i < int(n); i += 4 {
		brands[string(data[i:i+4])] = true
	}
	for _, kind := range []struct{ brand, mime, suffix string }{
		{"avis", "image/avif", ".avif"}, {"avif", "image/avif", ".avif"},
		{"hevc", "image/heic-sequence", ".heics"}, {"hevx", "image/heic-sequence", ".heics"},
		{"heic", "image/heic", ".heic"}, {"heix", "image/heic", ".heic"},
		{"msf1", "image/heif-sequence", ".heifs"}, {"mif1", "image/heif", ".heif"},
	} {
		if brands[kind.brand] {
			return kind.mime, kind.suffix
		}
	}
	return "", ""
}

func inboxPreviewType(data []byte) (string, string) {
	if mime, suffix := inboxHEIFType(data); suffix != "" {
		return mime, suffix
	}
	mime := http.DetectContentType(data)
	suffix := map[string]string{
		"image/png": ".png", "image/jpeg": ".jpg", "image/gif": ".gif",
		"image/webp": ".webp", "image/bmp": ".bmp", "image/x-icon": ".ico",
		"application/pdf": ".pdf", "audio/mpeg": ".mp3", "audio/wave": ".wav",
		"audio/aiff": ".aiff", "audio/basic": ".au", "application/ogg": ".ogg",
		"video/mp4": ".mp4", "video/webm": ".webm", "video/avi": ".avi",
	}[mime]
	if suffix != "" {
		return mime, suffix
	}
	if mime == "text/plain; charset=utf-8" && utf8.Valid(data) {
		// Check the whole text, not just the sniffer's first 512 bytes. Active
		// desktop entries and shebang scripts must never reach a desktop opener.
		text := bytes.TrimSpace(bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf}))
		if bytes.HasPrefix(text, []byte("<")) || bytes.Contains(data, []byte("#!")) || bytes.Contains(data, []byte("[Desktop Entry]")) {
			return "", ""
		}
		for _, c := range data {
			if c < 0x20 && c != '\t' && c != '\n' && c != '\r' {
				return "", ""
			}
		}
		return "text/plain", ".txt"
	}
	return "", ""
}

func copyInbox(b *direct.Inbox, transfer, item string) error {
	for _, x := range b.Items() {
		if x.ID == transfer {
			if x.Text != "" && item == "" {
				copyCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				cmd := exec.CommandContext(copyCtx, "wl-copy", "--type", "text/plain;charset=utf-8")
				cmd.Stdin = strings.NewReader(x.Text)
				return cmd.Run()
			}
			if len(x.Attachments) != 1 || x.Attachments[0].ID != item {
				return errors.New("select a single supported image")
			}
			return copyInboxItem(b, transfer, item)
		}
	}
	return os.ErrNotExist
}
func copyInboxItem(b *direct.Inbox, transfer, item string) error {
	p, e := b.File(transfer, item)
	if e != nil {
		return e
	}
	f, e := os.Open(p)
	if e != nil {
		return e
	}
	defer f.Close()
	st, e := f.Stat()
	if e != nil || st.Size() > direct.MaxItemBytes {
		return errors.New("invalid image")
	}
	data, e := io.ReadAll(io.LimitReader(f, direct.MaxItemBytes+1))
	if e != nil {
		return e
	}
	cfg, format, e := image.DecodeConfig(bytes.NewReader(data))
	if e != nil || cfg.Width < 1 || cfg.Height < 1 || int64(cfg.Width)*int64(cfg.Height) > 100_000_000 {
		return errors.New("image is not safely decodable")
	}
	if format != "png" && format != "jpeg" {
		return errors.New("copy supports PNG and JPEG images; save other formats instead")
	}
	copyCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(copyCtx, "wl-copy", "--type", "image/"+format)
	cmd.Stdin = bytes.NewReader(data)
	return cmd.Run()
}
func decodeUIBody(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	b, e := io.ReadAll(r.Body)
	if e != nil || !utf8.Valid(b) || !validUIJSONKeys(b) {
		http.Error(w, "invalid request", 400)
		return false
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(v) != nil {
		http.Error(w, "invalid request", 400)
		return false
	}
	var x any
	if e := d.Decode(&x); e != io.EOF {
		http.Error(w, "invalid request", 400)
		return false
	}
	return true
}
func validUIJSONKeys(b []byte) bool {
	d := json.NewDecoder(bytes.NewReader(b))
	if !scanUIValue(d) {
		return false
	}
	var extra any
	return d.Decode(&extra) == io.EOF
}
func scanUIValue(d *json.Decoder) bool {
	tok, e := d.Token()
	if e != nil {
		return false
	}
	delim, ok := tok.(json.Delim)
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
			if !scanUIValue(d) {
				return false
			}
		}
		end, e := d.Token()
		return e == nil && end == json.Delim('}')
	case '[':
		for d.More() {
			if !scanUIValue(d) {
				return false
			}
		}
		end, e := d.Token()
		return e == nil && end == json.Delim(']')
	default:
		return false
	}
}

// startUISocket provides the private same-UID CLI handoff: one JSON send request
// per 0600 Unix connection, followed by status/progress and a terminal event.
// SO_PEERCRED plus the 0700 config directory authorize the caller; no TCP port
// or shell command string is exposed by this interface.
func startUISocket(dir string, wg *sync.WaitGroup, handler func(uiRequest, net.Conn)) (*net.UnixListener, string, error) {
	path := filepath.Join(dir, "ui.sock")
	if len(path) >= 100 {
		return nil, "", errors.New("private socket path too long")
	}
	if fi, e := os.Lstat(path); e == nil {
		if fi.Mode()&os.ModeSocket == 0 {
			return nil, "", errors.New("UI socket path is not a socket")
		}
		if c, e := net.DialTimeout("unix", path, 100*time.Millisecond); e == nil {
			c.Close()
			return nil, "", errors.New("UI process already active")
		}
		if e = os.Remove(path); e != nil {
			return nil, "", e
		}
	} else if !os.IsNotExist(e) {
		return nil, "", e
	}
	addr := &net.UnixAddr{Name: path, Net: "unix"}
	ln, e := net.ListenUnix("unix", addr)
	if e != nil {
		return nil, "", e
	}
	if e = os.Chmod(path, 0600); e != nil {
		ln.Close()
		os.Remove(path)
		return nil, "", e
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			c, e := ln.AcceptUnix()
			if e != nil {
				return
			}
			wg.Add(1)
			go func(c *net.UnixConn) {
				defer wg.Done()
				defer c.Close()
				raw, e := c.SyscallConn()
				if e != nil {
					return
				}
				var cred *syscall.Ucred
				_ = raw.Control(func(fd uintptr) { cred, _ = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED) })
				if cred == nil || int(cred.Uid) != os.Getuid() {
					return
				}
				_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
				var q uiRequest
				d := json.NewDecoder(io.LimitReader(c, 1<<20))
				d.DisallowUnknownFields()
				if d.Decode(&q) != nil || q.ID == "" || q.Command != "send" {
					return
				}
				_ = c.SetReadDeadline(time.Time{})
				handler(q, c)
			}(c)
		}
	}()
	return ln, path, nil
}
func sendViaUI(ctx context.Context, dir string, q uiRequest, out io.Writer) (bool, error) {
	path := filepath.Join(dir, "ui.sock")
	parent, e := os.Stat(dir)
	if e != nil || parent.Mode().Perm()&0077 != 0 {
		return false, nil
	}
	if st, ok := parent.Sys().(*syscall.Stat_t); !ok || st.Uid != uint32(os.Getuid()) {
		return false, nil
	}
	fi, e := os.Lstat(path)
	if e != nil || fi.Mode()&os.ModeSocket == 0 || fi.Mode().Perm()&0077 != 0 {
		return false, nil
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); !ok || st.Uid != uint32(os.Getuid()) {
		return false, nil
	}
	conn, e := net.DialTimeout("unix", path, 500*time.Millisecond)
	if e != nil {
		return false, nil
	}
	defer conn.Close()
	stopWatch := make(chan struct{})
	defer close(stopWatch)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-stopWatch:
		}
	}()
	_ = conn.SetDeadline(time.Now().Add(130 * time.Second))
	if e = json.NewEncoder(conn).Encode(q); e != nil {
		return true, e
	}
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 4096), 2<<20)
	for scanner.Scan() {
		var event map[string]any
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			continue
		}
		switch event["type"] {
		case "error":
			msg, _ := event["message"].(string)
			return true, errors.New(msg)
		case "result":
			fmt.Fprintln(out, "Received by paired device.")
			return true, nil
		}
	}
	if ctx.Err() != nil {
		return true, ctx.Err()
	}
	if e = scanner.Err(); e != nil {
		return true, e
	}
	return true, errors.New("UI send ended without receipt confirmation")
}

func deviceViews(ds []direct.Device) []map[string]string {
	out := make([]map[string]string, 0, len(ds))
	for _, d := range ds {
		out = append(out, map[string]string{"alias": safeLabel(d.Alias), "name": safeLabel(d.Name)})
	}
	return out
}
func inboxAttachments(b *direct.Inbox, x *direct.Incoming) []direct.IncomingAttachment {
	attachments := append([]direct.IncomingAttachment(nil), x.Attachments...)
	for i := range attachments {
		p, err := b.File(x.ID, attachments[i].ID)
		if err != nil {
			continue
		}
		f, err := openInboxFile(p)
		if err != nil {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(f, 4096))
		f.Close()
		if err == nil {
			if mime, _ := inboxHEIFType(data); mime != "" {
				attachments[i].MIME = mime
			}
		}
	}
	return attachments
}

func inboxViews(b *direct.Inbox, s *direct.Store) []map[string]any {
	xs := b.Items()
	out := make([]map[string]any, 0, len(xs))
	for _, x := range xs {
		d, _ := s.Device(x.DeviceID)
		out = append(out, map[string]any{"transferId": x.ID, "alias": safeLabel(d.Alias), "receivedAt": x.ReceivedAt, "text": x.Text, "attachments": inboxAttachments(b, x)})
	}
	return out
}
