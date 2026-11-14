// These opt-in tests exercise a real CLI process and HTTPS socket with disposable
// configuration. They never read or overwrite the user's clipboard or pairings.
package ui_test

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type event map[string]any
type harness struct {
	config  string
	t       *testing.T
	cmd     *exec.Cmd
	in      io.WriteCloser
	events  chan event
	backlog []event
	logs    bytes.Buffer
	mu      sync.Mutex
}

func (h *harness) send(id, command string, fields event) {
	h.t.Helper()
	fields["id"] = id
	fields["command"] = command
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := json.NewEncoder(h.in).Encode(fields); err != nil {
		h.t.Fatal(err)
	}
}
func (h *harness) wait(id, kind string) event {
	h.t.Helper()
	deadline := time.NewTimer(20 * time.Second)
	defer deadline.Stop()
	for {
		for i, e := range h.backlog {
			if e["id"] == id && (e["type"] == kind || e["type"] == "error") {
				h.backlog = append(h.backlog[:i], h.backlog[i+1:]...)
				if e["type"] == "error" && kind != "error" {
					h.t.Fatalf("%s failed: %v", id, e)
				}
				return e
			}
		}
		select {
		case e, ok := <-h.events:
			if !ok {
				h.t.Fatalf("CLI exited waiting for %s/%s", id, kind)
			}
			h.backlog = append(h.backlog, e)
		case <-deadline.C:
			h.t.Fatalf("timed out waiting for %s/%s; pending event types: %v", id, kind, eventTypes(h.backlog))
		}
	}
}
func eventTypes(events []event) []string {
	var result []string
	for _, e := range events {
		result = append(result, fmt.Sprint(e["id"], "/", e["type"]))
	}
	return result
}
func privateHost(t *testing.T) string {
	t.Helper()
	interfaces, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range interfaces {
		if n.Flags&net.FlagLoopback != 0 || n.Flags&net.FlagUp == 0 {
			continue
		}
		as, _ := n.Addrs()
		for _, a := range as {
			ip, _, _ := net.ParseCIDR(a.String())
			if ip.To4() != nil && ip.IsPrivate() {
				return ip.String()
			}
		}
	}
	t.Skip("no private IPv4 interface")
	return ""
}
func start(t *testing.T) (*harness, string) {
	t.Helper()
	binary := os.Getenv("CLIPSEND_TEST_BINARY")
	if binary == "" {
		t.Skip("set CLIPSEND_TEST_BINARY to exercise real CLI")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	host := privateHost(t)
	l, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	h := &harness{t: t, events: make(chan event, 100)}
	h.cmd = exec.Command(binary, "ui", "--host", host, "--port", strconv.Itoa(port))
	dir := t.TempDir()
	h.config = dir
	stubDir := filepath.Join(dir, "bin")
	if err := os.Mkdir(stubDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stubDir, "wl-copy"), []byte("#!/bin/sh\ncat > \"$CLIPSEND_TEST_CLIPBOARD\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stubDir, "wl-paste"), []byte("#!/bin/sh\n[ \"$3\" = text ] || exit 1\ncat \"$CLIPSEND_TEST_CLIPBOARD\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stubDir, "xdg-open"), []byte("#!/bin/sh\n[ \"$#\" = 1 ] || exit 1\nprintf '%s' \"$1\" > \"$CLIPSEND_TEST_OPEN\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	h.cmd.Env = append(os.Environ(), "XDG_CONFIG_HOME="+dir, "XDG_CACHE_HOME="+dir, "XDG_RUNTIME_DIR="+dir, "TMPDIR="+dir, "CLIPSEND_TEST_OPEN="+filepath.Join(dir, "opened-fixture"), "CLIPSEND_TEST_CLIPBOARD="+filepath.Join(dir, "clipboard-fixture"), "PATH="+stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	h.cmd.Stderr = &h.logs
	h.in, err = h.cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := h.cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = h.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		defer close(h.events)
		s := bufio.NewScanner(out)
		s.Buffer(make([]byte, 4096), 2<<20)
		for s.Scan() {
			var e event
			if json.Unmarshal(s.Bytes(), &e) == nil {
				h.events <- e
			}
		}
	}()
	t.Cleanup(func() {
		h.in.Close()
		done := make(chan error, 1)
		go func() { done <- h.cmd.Wait() }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			h.cmd.Process.Kill()
			<-done
		}
	})
	return h, "https://" + net.JoinHostPort(host, strconv.Itoa(port))
}
func request(t *testing.T, c *http.Client, method, url, token string, body []byte) (int, []byte) {
	t.Helper()
	r, err := http.NewRequest(method, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	r.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, data
}
func marshal(t *testing.T, x any) []byte {
	t.Helper()
	b, e := json.Marshal(x)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func pair(t *testing.T, h *harness) (*http.Client, string) {
	t.Helper()
	h.send("pair", "pair", event{"alias": "fixture"})
	qr := h.wait("pair", "pairing")
	raw, ok := qr["qr"].(string)
	if !ok {
		t.Fatal("missing QR payload")
	}
	var invitation map[string]any
	if err := json.Unmarshal([]byte(raw), &invitation); err != nil {
		t.Fatal(err)
	}
	pin := invitation["certificateSHA256"].(string)
	endpoint := invitation["endpoint"].(string)
	client := &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true, VerifyConnection: func(cs tls.ConnectionState) error {
		sum := sha256.Sum256(cs.PeerCertificates[0].Raw)
		if hex.EncodeToString(sum[:]) != pin {
			return fmt.Errorf("certificate pin mismatch")
		}
		return nil
	}}}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	deferClose := client.Transport.(*http.Transport)
	t.Cleanup(deferClose.CloseIdleConnections)
	response := make(chan []byte, 1)
	go func() {
		r, _ := http.NewRequest("POST", endpoint+"/v1/pair", bytes.NewReader(marshal(t, event{"invitation": invitation["invitation"], "deviceName": "Test receiver", "deviceToken": "aabb", "bundleID": "com.example.clipsend", "environment": "sandbox"})))
		r.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(r)
		if err != nil {
			response <- nil
			return
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 201 {
			response <- nil
			return
		}
		response <- b
	}()
	h.wait("pair", "approval")
	h.send("approve", "approve", event{"requestId": "pair", "allow": true})
	h.wait("pair", "result")
	var result map[string]any
	select {
	case b := <-response:
		if json.Unmarshal(b, &result) != nil {
			t.Fatal("pair did not complete")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("pair response timeout")
	}
	secret, _ := result["secret"].(string)
	if secret == "" {
		t.Fatal("missing pairing credential")
	}
	return client, secret
}

func TestRealUIReceiveIntegrityAndRevocation(t *testing.T) {
	h, endpoint := start(t)
	client, secret := pair(t, h)
	h.send("receive", "receive", event{"enabled": true})
	h.wait("receive", "result")
	data := []byte("ClipSend integration fixture ✓\n")
	sum := sha256.Sum256(data)
	manifest := event{"version": 1, "text": "from simulated iOS", "attachments": []event{{"id": "file1", "name": "fixture.txt", "mime": "text/plain", "size": len(data), "sha256": hex.EncodeToString(sum[:])}}}
	code, _ := request(t, client, "POST", endpoint+"/v1/incoming", "wrong", marshal(t, manifest))
	if code != 401 {
		t.Fatalf("unauthenticated upload status %d", code)
	}
	code, body := request(t, client, "POST", endpoint+"/v1/incoming", secret, marshal(t, manifest))
	if code != 201 {
		t.Fatalf("upload creation status %d", code)
	}
	var created event
	json.Unmarshal(body, &created)
	id := created["id"].(string)
	code, _ = request(t, client, "POST", endpoint+"/v1/incoming/"+id+"/complete", secret, nil)
	if code == 204 {
		t.Fatal("incomplete transfer accepted")
	}
	code, _ = request(t, client, "PUT", endpoint+"/v1/incoming/"+id+"/items/file1", secret, data)
	if code != 204 {
		t.Fatalf("upload status %d", code)
	}
	code, _ = request(t, client, "POST", endpoint+"/v1/incoming/"+id+"/complete", secret, nil)
	if code != 204 {
		t.Fatalf("completion status %d", code)
	}
	h.send("inbox", "inbox", event{})
	inbox := h.wait("inbox", "result")
	items, ok := inbox["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatal("missing completed inbox item")
	}
	h.send("copy-text", "copy", event{"transferId": id})
	h.wait("copy-text", "result")
	copied, err := os.ReadFile(filepath.Join(h.config, "clipboard-fixture"))
	if err != nil || string(copied) != "from simulated iOS" {
		t.Fatal("explicit Copy did not deliver text to clipboard command")
	}
	target := filepath.Join(t.TempDir(), "saved.txt")
	h.send("save", "save", event{"transferId": id, "itemId": "file1", "path": target})
	h.wait("save", "result")
	got, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatal("saved bytes differ")
	}
	h.send("overwrite", "save", event{"transferId": id, "itemId": "file1", "path": target})
	h.wait("overwrite", "error")
	h.send("open", "open", event{"transferId": id, "itemId": "file1", "path": "/etc/passwd"})
	if e := h.wait("open", "result"); e["state"] != "opened" {
		t.Fatalf("missing opened state: %v", e)
	}
	previewName, err := os.ReadFile(filepath.Join(h.config, "opened-fixture"))
	if err != nil || filepath.Base(string(previewName)) != "attachment.txt" || string(previewName) == target {
		t.Fatal("Open did not use a private typed preview")
	}
	h.send("open-again", "open", event{"transferId": id, "itemId": "file1"})
	h.wait("open-again", "result")
	again, _ := os.ReadFile(filepath.Join(h.config, "opened-fixture"))
	if !bytes.Equal(previewName, again) {
		t.Fatal("repeated Open duplicated preview")
	}
	h.send("open-path", "open", event{"transferId": id, "itemId": "/etc/passwd", "path": target})
	if e := h.wait("open-path", "error"); e["code"] != "open_failed" {
		t.Fatalf("wrong open error: %v", e)
	}
	h.send("clear", "clear", event{"transferId": id})
	h.wait("clear", "result")
	previewBytes, err := os.ReadFile(string(previewName))
	if err != nil || !bytes.Equal(previewBytes, data) {
		t.Fatal("Clear removed or changed opened preview")
	}
	h.send("open-cleared", "open", event{"transferId": id, "itemId": "file1"})
	if e := h.wait("open-cleared", "error"); e["code"] != "open_failed" {
		t.Fatalf("cleared item could reopen: %v", e)
	}
	h.send("empty", "inbox", event{})
	empty := h.wait("empty", "result")
	if entries, ok := empty["items"].([]any); ok && len(entries) != 0 {
		t.Fatal("clear retained inbox item")
	}

	// Declared quotas are enforced before allocating/writing attachment bodies.
	oversized := event{"version": 1, "text": "", "attachments": []event{{"id": "large", "name": "large.bin", "mime": "application/octet-stream", "size": (128 << 20) + 1, "sha256": hex.EncodeToString(sum[:])}}}
	code, _ = request(t, client, "POST", endpoint+"/v1/incoming", secret, marshal(t, oversized))
	if code == 201 {
		t.Fatal("oversized upload reservation accepted")
	}
	// Corrupt bodies cannot become complete inbox entries.
	code, body = request(t, client, "POST", endpoint+"/v1/incoming", secret, marshal(t, manifest))
	if code != 201 {
		t.Fatalf("corruption fixture create status %d", code)
	}
	var corrupt event
	json.Unmarshal(body, &corrupt)
	corruptID := corrupt["id"].(string)
	changed := append([]byte(nil), data...)
	changed[0] = 'X'
	code, _ = request(t, client, "PUT", endpoint+"/v1/incoming/"+corruptID+"/items/file1", secret, changed)
	// Implementations may reject at upload or at finalization, but never acknowledge completion.
	code, _ = request(t, client, "POST", endpoint+"/v1/incoming/"+corruptID+"/complete", secret, nil)
	if code == 204 {
		t.Fatal("corrupt attachment acknowledged")
	}
	request(t, client, "DELETE", endpoint+"/v1/incoming/"+corruptID, secret, nil)
	h.send("unpair", "unpair", event{"alias": "fixture"})
	h.wait("unpair", "result")
	code, _ = request(t, client, "POST", endpoint+"/v1/incoming", secret, marshal(t, manifest))
	if code != 401 {
		t.Fatalf("revoked credential status %d", code)
	}
	h.send("off", "receive", event{"enabled": false})
	h.wait("off", "result")
}

func TestNoNotificationCredentialInDeviceEvents(t *testing.T) {
	h, _ := start(t)
	_, secret := pair(t, h)
	h.send("list", "devices", event{})
	e := h.wait("list", "result")
	b := marshal(t, e)
	if strings.Contains(string(b), secret) || strings.Contains(string(b), "deviceToken") {
		t.Fatal("device event leaked credentials")
	}
}

func TestPairWhileReceivingAndCopyImages(t *testing.T) {
	h, endpoint := start(t)
	h.send("on", "receive", event{"enabled": true})
	h.wait("on", "result")
	client, secret := pair(t, h)
	if e := h.wait("pair", "status"); e["state"] != "receive_restored" {
		t.Fatalf("receiving was not restored: %v", e)
	}
	tokenUpdate := marshal(t, event{"deviceToken": "ccdd", "bundleID": "com.example.clipsend", "environment": "sandbox"})
	if code, _ := request(t, client, "POST", endpoint+"/v1/device/token", "wrong", tokenUpdate); code != 401 {
		t.Fatalf("unauthorized token update: %d", code)
	}
	if code, _ := request(t, client, "POST", endpoint+"/v1/device/token", secret, tokenUpdate); code != 204 {
		t.Fatalf("persistent receiver token update: %d", code)
	}
	picture := image.NewRGBA(image.Rect(0, 0, 2, 2))
	picture.Set(0, 0, color.RGBA{R: 255, A: 255})
	for _, format := range []string{"png", "jpeg"} {
		var encoded bytes.Buffer
		var err error
		if format == "png" {
			err = png.Encode(&encoded, picture)
		} else {
			err = jpeg.Encode(&encoded, picture, nil)
		}
		if err != nil {
			t.Fatal(err)
		}
		data := encoded.Bytes()
		sum := sha256.Sum256(data)
		manifest := event{"version": 1, "text": "", "attachments": []event{{"id": "picture", "name": "picture." + format, "mime": "image/" + format, "size": len(data), "sha256": hex.EncodeToString(sum[:])}}}
		code, body := request(t, client, "POST", endpoint+"/v1/incoming", secret, marshal(t, manifest))
		if code != 201 {
			t.Fatalf("image creation: %d", code)
		}
		var created event
		if err := json.Unmarshal(body, &created); err != nil {
			t.Fatal(err)
		}
		id := created["id"].(string)
		code, _ = request(t, client, "PUT", endpoint+"/v1/incoming/"+id+"/items/picture", secret, data)
		if code != 204 {
			t.Fatalf("image upload: %d", code)
		}
		code, _ = request(t, client, "POST", endpoint+"/v1/incoming/"+id+"/complete", secret, nil)
		if code != 204 {
			t.Fatalf("image completion: %d", code)
		}
		h.send("copy-"+format, "copy", event{"transferId": id, "itemId": "picture"})
		h.wait("copy-"+format, "result")
		copied, err := os.ReadFile(filepath.Join(h.config, "clipboard-fixture"))
		if err != nil || !bytes.Equal(copied, data) {
			t.Fatalf("%s clipboard bytes changed: %v", format, err)
		}
	}
}

func TestUIConcurrentControlsAndEOF(t *testing.T) {
	h, _ := start(t)
	for i := 0; i < 12; i++ {
		h.send(fmt.Sprintf("toggle%d", i), "receive", event{"enabled": i%2 == 0})
		h.send(fmt.Sprintf("list%d", i), "devices", event{})
	}
	// Every control request must settle. Resource conflicts may produce a clear error.
	for i := 0; i < 12; i++ {
		h.wait(fmt.Sprintf("list%d", i), "result")
	}
	h.send("last-off", "receive", event{"enabled": false})
	h.wait("last-off", "result")
}

func TestUISendWithReceiveEnabledAndNotification(t *testing.T) {
	h, endpoint := start(t)
	client, secret := pair(t, h)
	pushes := make(chan event, 1)
	notify := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload event
		json.NewDecoder(r.Body).Decode(&payload)
		pushes <- payload
		w.WriteHeader(202)
	}))
	defer notify.Close()
	cfg := marshal(t, event{"url": notify.URL, "token": strings.Repeat("t", 43)})
	if err := os.WriteFile(filepath.Join(h.config, "clipsend-native", "relay.json"), cfg, 0600); err != nil {
		t.Fatal(err)
	}
	h.send("on", "receive", event{"enabled": true})
	h.wait("on", "result")
	content := []byte("outbound attachment fixture")
	path := filepath.Join(t.TempDir(), "outbound.txt")
	os.WriteFile(path, content, 0600)
	h.send("send", "send", event{"alias": "fixture", "files": []string{path}})
	var push event
	select {
	case push = <-pushes:
	case <-time.After(5 * time.Second):
		t.Fatal("UI send did not notify while receiving was enabled")
	}
	notification, ok := push["notification"].(map[string]any)
	if !ok {
		t.Fatal("invalid notification shape")
	}
	if strings.Contains(string(marshal(t, push)), string(content)) || strings.Contains(string(marshal(t, push)), secret) {
		t.Fatal("notification leaked content or pairing credential")
	}
	id := notification["transferID"].(string)
	code, listBody := request(t, client, "GET", endpoint+"/v1/transfers", secret, nil)
	if code != 200 || !strings.Contains(string(listBody), id) {
		t.Fatal("actual mobile list-then-fetch flow unavailable")
	}

	code, body := request(t, client, "GET", endpoint+"/v1/transfers/"+id, secret, nil)
	if code != 200 {
		t.Fatalf("manifest HTTP %d", code)
	}
	var manifest event
	json.Unmarshal(body, &manifest)
	attachments := manifest["attachments"].([]any)
	if len(attachments) != 1 {
		t.Fatal("incorrect manifest")
	}
	item := attachments[0].(map[string]any)
	code, body = request(t, client, "GET", endpoint+"/v1/transfers/"+id+"/items/"+item["id"].(string), secret, nil)
	if code != 200 || !bytes.Equal(body, content) {
		t.Fatal("attachment download mismatch")
	}
	code, _ = request(t, client, "POST", endpoint+"/v1/transfers/"+id+"/ack", secret, nil)
	if code != 204 {
		t.Fatalf("ack HTTP %d", code)
	}
	h.wait("send", "result")
	// Exercise the panel's clipboard path, not explicit text or CLI --stdin.
	clipboardText := "Panel clipboard text ✓\nsecond line"
	if err := os.WriteFile(filepath.Join(h.config, "clipboard-fixture"), []byte(clipboardText), 0600); err != nil {
		t.Fatal(err)
	}
	h.send("clipboard-send", "send", event{"alias": "fixture"})
	select {
	case clipboardPush := <-pushes:
		n := clipboardPush["notification"].(map[string]any)
		transfer := n["transferID"].(string)
		status, b := request(t, client, "GET", endpoint+"/v1/transfers/"+transfer, secret, nil)
		var received event
		json.Unmarshal(b, &received)
		if status != 200 || received["text"] != clipboardText {
			t.Fatal("panel clipboard text was lost or changed")
		}
		request(t, client, "POST", endpoint+"/v1/transfers/"+transfer+"/ack", secret, nil)
		h.wait("clipboard-send", "result")
	case <-time.After(5 * time.Second):
		t.Fatal("panel clipboard send failed before notification")
	}
	h.send("still-on", "receive", event{"enabled": true})
	h.wait("still-on", "result")
	for attempt := 0; attempt < 4; attempt++ {
		text := fmt.Sprintf("  standalone CLI transfer %d ✓\nsecond line\n  ", attempt)
		argv := []string{"send", "fixture", "--stdin"}
		if attempt == 1 {
			argv = []string{"FIXTURE", "--stdin"}
		} else if attempt == 2 {
			argv = []string{"fixture", "--text", text}
		} else if attempt == 3 {
			argv = []string{"send", "fixture", "--text", text}
		}
		cli := exec.Command(h.cmd.Path, argv...)
		cli.Env = h.cmd.Env
		cli.Stdin = strings.NewReader(text)
		var output bytes.Buffer
		cli.Stdout = &output
		cli.Stderr = &output
		if err := cli.Start(); err != nil {
			t.Fatal(err)
		}
		finished := make(chan error, 1)
		go func() { finished <- cli.Wait() }()
		var forwarded event
		select {
		case forwarded = <-pushes:
		case err := <-finished:
			t.Fatalf("CLI handoff exited before notification: %v", err)
		case <-time.After(5 * time.Second):
			cli.Process.Kill()
			<-finished
			t.Fatal("CLI handoff did not notify")
		}
		n := forwarded["notification"].(map[string]any)
		transfer := n["transferID"].(string)
		status, b := request(t, client, "GET", endpoint+"/v1/transfers/"+transfer, secret, nil)
		var received event
		json.Unmarshal(b, &received)
		if status != 200 || received["text"] != text {
			cli.Process.Kill()
			<-finished
			t.Fatal("CLI handoff changed text")
		}
		request(t, client, "POST", endpoint+"/v1/transfers/"+transfer+"/ack", secret, nil)
		select {
		case err := <-finished:
			if err != nil {
				t.Fatalf("CLI handoff failed: %v", err)
			}
		case <-time.After(3 * time.Second):
			cli.Process.Kill()
			<-finished
			t.Fatal("CLI did not finish after ack")
		}
		if strings.Contains(output.String(), text) {
			t.Fatal("CLI logged clipboard content")
		}
	}

	cancelCLI := exec.Command(h.cmd.Path, "send", "fixture", "--stdin")
	cancelCLI.Env = h.cmd.Env
	cancelCLI.Stdin = strings.NewReader("cancel fixture")
	if err := cancelCLI.Start(); err != nil {
		t.Fatal(err)
	}
	cancelDone := make(chan error, 1)
	go func() { cancelDone <- cancelCLI.Wait() }()
	var pending event
	select {
	case pending = <-pushes:
	case <-time.After(5 * time.Second):
		cancelCLI.Process.Kill()
		<-cancelDone
		t.Fatal("cancel fixture did not start")
	}
	cancelID := pending["notification"].(map[string]any)["transferID"].(string)
	cancelCLI.Process.Signal(os.Interrupt)
	select {
	case <-cancelDone:
	case <-time.After(3 * time.Second):
		cancelCLI.Process.Kill()
		<-cancelDone
		t.Fatal("CLI cancellation hung")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		status, _ := request(t, client, "GET", endpoint+"/v1/transfers/"+cancelID, secret, nil)
		if status == 404 || status == 410 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("cancelled CLI left delegated transfer available")
		}
		time.Sleep(20 * time.Millisecond)
	}

}
