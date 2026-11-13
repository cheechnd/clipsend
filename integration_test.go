package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"clipsend/internal/direct"
	"clipsend/internal/relay"
)

// Opt-in because this test binds a real LAN interface and announces Bonjour.
// All identities, pairing records and credentials are disposable test fixtures.
func TestNativeCLIEndToEnd(t *testing.T) {
	if os.Getenv("CLIPSEND_NETWORK_TEST") != "1" {
		t.Skip("set CLIPSEND_NETWORK_TEST=1 to exercise real LAN listener + CLI + notification handoff")
	}
	lan, err := selectLAN("")
	if err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", base)
	dir := filepath.Join(base, "clipsend-native")
	store, err := direct.OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	inv, err := store.NewInvitation()
	if err != nil {
		t.Fatal(err)
	}
	device, err := store.ApproveInvitation(inv.Value, "testpad", true, direct.PairRequest{DeviceName: "Test Pad", DeviceToken: "aabb", BundleID: "dev.example.clipsend", Environment: "sandbox"})
	if err != nil {
		t.Fatal(err)
	}
	pin := store.Identity().CertificateSHA256
	transport := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true, VerifyConnection: func(cs tls.ConnectionState) error {
		if len(cs.PeerCertificates) == 0 {
			return errors.New("missing certificate")
		}
		digest := sha256.Sum256(cs.PeerCertificates[0].Raw)
		if hex.EncodeToString(digest[:]) != pin {
			return errors.New("wrong pinned certificate")
		}
		return nil
	}}}
	defer transport.CloseIdleConnections()
	receiver := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	const text = "ClipSend native integration test\nUnicode: café 世界 🔒"
	received := make(chan error, 1)
	notification := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("r", 43) {
			http.Error(w, "unauthorized", 401)
			return
		}
		var msg relay.Request
		if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
			http.Error(w, "bad", 400)
			return
		}
		w.WriteHeader(202)
		go func() {
			endpoint := msg.Notification.Endpoints[0]
			req, _ := http.NewRequest("GET", endpoint+"/v1/transfers/"+msg.Notification.TransferID, nil)
			req.Header.Set("Authorization", "Bearer "+device.Secret)
			resp, err := receiver.Do(req)
			if err != nil {
				received <- err
				return
			}
			data, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				received <- err
				return
			}
			var payload struct {
				Text       string `json:"text"`
				ComputerID string `json:"computerID"`
			}
			if err = json.Unmarshal(data, &payload); err != nil {
				received <- err
				return
			}
			if resp.StatusCode != 200 || payload.Text != text || payload.ComputerID != msg.Notification.ComputerID {
				received <- errors.New("wrong message payload")
				return
			}
			ack, _ := http.NewRequest("POST", endpoint+"/v1/transfers/"+msg.Notification.TransferID+"/ack", nil)
			ack.Header.Set("Authorization", "Bearer "+device.Secret)
			result, err := receiver.Do(ack)
			if err == nil {
				result.Body.Close()
				if result.StatusCode != 204 {
					err = fmt.Errorf("ack status %d", result.StatusCode)
				}
			}
			received <- err
		}()
	}))
	defer notification.Close()
	if err := saveRelaySettings(dir, relaySettings{URL: notification.URL, Token: strings.Repeat("r", 43)}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var output bytes.Buffer
	if err := run(ctx, []string{"send", "testpad", "--host", lan.IP, "--port", nativeFixturePort(t, lan.IP), "--stdin"}, strings.NewReader(text), &output, &output); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-received:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("receiver did not get acknowledgment response")
	}
	if !strings.Contains(output.String(), "Received by testpad.") {
		t.Fatal("missing confirmed receipt")
	}
	if strings.Contains(output.String(), text) {
		t.Fatal("clipboard text leaked to terminal")
	}
	data, err := os.ReadFile(filepath.Join(dir, "store.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(text)) {
		t.Fatal("text persisted in state")
	}
}

func TestNativePairingQRCode(t *testing.T) {
	if os.Getenv("CLIPSEND_NETWORK_TEST") != "1" {
		t.Skip("requires real LAN and zbarimg QR decoder")
	}
	if _, err := exec.LookPath("zbarimg"); err != nil {
		t.Skip("zbarimg not installed")
	}
	lan, err := selectLAN("")
	if err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", base)
	qrPath := filepath.Join(base, "pair.png")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	finished := make(chan error, 1)
	port := nativeFixturePort(t, lan.IP)
	go func() {
		finished <- run(ctx, []string{"pair", "testpad", "--host", lan.IP, "--port", port, "--qr", qrPath}, strings.NewReader("yes\n"), io.Discard, io.Discard)
	}()
	var raw []byte
	for ctx.Err() == nil {
		select {
		case e := <-finished:
			t.Fatalf("pairing command ended before QR decoding: %v", e)
		default:
		}
		raw, err = exec.CommandContext(ctx, "zbarimg", "--quiet", "--raw", qrPath).Output()
		if err == nil {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if err != nil {
		t.Fatal("QR image could not be decoded")
	}
	var invitation struct {
		Version    int    `json:"version"`
		ComputerID string `json:"computerID"`
		Endpoint   string `json:"endpoint"`
		Pin        string `json:"certificateSHA256"`
		Invitation string `json:"invitation"`
	}
	if err = json.Unmarshal(bytes.TrimSpace(raw), &invitation); err != nil {
		t.Fatal("invalid pairing QR JSON")
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true, VerifyConnection: func(cs tls.ConnectionState) error {
		if len(cs.PeerCertificates) == 0 {
			return errors.New("no certificate")
		}
		h := sha256.Sum256(cs.PeerCertificates[0].Raw)
		if hex.EncodeToString(h[:]) != invitation.Pin {
			return errors.New("wrong certificate")
		}
		return nil
	}}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 8 * time.Second}
	body, _ := json.Marshal(direct.PairRequest{Invitation: invitation.Invitation, DeviceName: "Test Pad", DeviceToken: "aabb", BundleID: "dev.example.clipsend", Environment: "sandbox"})
	response, err := client.Post(invitation.Endpoint+"/v1/pair", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Secret     string `json:"secret"`
		ComputerID string `json:"computerID"`
	}
	if json.Unmarshal(data, &result) != nil || response.StatusCode != 201 || len(result.Secret) < 32 || result.ComputerID != invitation.ComputerID {
		t.Fatal("phone did not receive complete valid pairing credentials")
	}
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("CLI did not finish after approved pairing")
	}
	if _, err := os.Stat(qrPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("pairing QR was not removed")
	}
}

// Keep opt-in LAN fixtures independent of the user's running desktop listener.
func nativeFixturePort(t *testing.T, host string) string {
	t.Helper()
	listener, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		t.Fatal(err)
	}
	_, port, err := net.SplitHostPort(listener.Addr().String())
	listener.Close()
	if err != nil {
		t.Fatal(err)
	}
	return port
}
