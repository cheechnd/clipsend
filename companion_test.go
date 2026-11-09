package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAvailableDeviceSendsWithoutPush(t *testing.T) {
	calls := 0
	s := deliveryService{notify: func(context.Context) (bool, error) { t.Fatal("unexpected push"); return false, nil }, send: func(_ context.Context, d Device, text string) error {
		calls++
		if d.Alias != "Phone" || text != "captured" {
			t.Fatal("wrong transfer")
		}
		return nil
	}}
	if err := s.deliver(context.Background(), []Device{{Alias: "Phone"}}, "Phone", "captured", strings.NewReader(""), io.Discard); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("sends=%d", calls)
	}
}

func TestMissingDevicePushThenSendOnce(t *testing.T) {
	pushes, scans, sends := 0, 0, 0
	s := deliveryService{wait: time.Second, interval: time.Millisecond,
		notify: func(context.Context) (bool, error) { pushes++; return true, nil },
		discover: func(context.Context) ([]Device, error) {
			scans++
			if scans == 1 {
				return nil, nil
			}
			return []Device{{Alias: "Phone"}}, nil
		},
		send: func(_ context.Context, _ Device, text string) error {
			sends++
			if text != "captured\n🌿" {
				t.Fatal("clipboard changed")
			}
			return nil
		}}
	var out bytes.Buffer
	if err := s.deliver(context.Background(), nil, "Phone", "captured\n🌿", strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	if pushes != 1 || sends != 1 || scans != 2 {
		t.Fatalf("pushes %d scans %d sends %d", pushes, scans, sends)
	}
	if strings.Contains(out.String(), "captured") {
		t.Fatal("clipboard leaked to output")
	}
}

func TestWaitingNeverResendsUnknownTransfer(t *testing.T) {
	sends := 0
	s := deliveryService{wait: time.Second, interval: time.Millisecond, notify: func(context.Context) (bool, error) { return true, nil }, discover: func(context.Context) ([]Device, error) { return []Device{{Alias: "Phone"}}, nil }, send: func(context.Context, Device, string) error { sends++; return errors.New("delivery unconfirmed") }}
	err := s.deliver(context.Background(), nil, "Phone", "text", strings.NewReader(""), io.Discard)
	if err == nil || sends != 1 {
		t.Fatalf("err=%v sends=%d", err, sends)
	}
}

func TestWaitDeadlineAndCancelNeverSend(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		s := deliveryService{wait: 10 * time.Millisecond, interval: time.Millisecond,
			notify: func(context.Context) (bool, error) {
				if cancelled {
					cancel()
				}
				return true, nil
			},
			discover: func(context.Context) ([]Device, error) { return nil, nil },
			send:     func(context.Context, Device, string) error { t.Fatal("unexpected transfer"); return nil }}
		err := s.deliver(ctx, nil, "Phone", "text", strings.NewReader(""), io.Discard)
		cancel()
		if err == nil {
			t.Fatal("expected no-send error")
		}
	}
}

func TestPushUnavailableOrRejectedDoesNotWait(t *testing.T) {
	for _, pushError := range []error{nil, errors.New("APNs rejected request")} {
		s := deliveryService{notify: func(context.Context) (bool, error) { return false, pushError }, discover: func(context.Context) ([]Device, error) { t.Fatal("unexpected scan"); return nil, nil }, send: func(context.Context, Device, string) error { t.Fatal("unexpected send"); return nil }}
		if err := s.deliver(context.Background(), nil, "Phone", "text", strings.NewReader(""), io.Discard); err == nil {
			t.Fatal("expected unavailable error")
		}
	}
}

func TestAmbiguousDeviceNeverPushes(t *testing.T) {
	s := deliveryService{notify: func(context.Context) (bool, error) { t.Fatal("unexpected push"); return false, nil }}
	if err := s.deliver(context.Background(), []Device{{Alias: "Phone A"}, {Alias: "Phone B"}}, "Phone", "text", strings.NewReader(""), io.Discard); err == nil {
		t.Fatal("expected ambiguity")
	}
}

func TestWaitStopsIfTargetBecomesAmbiguous(t *testing.T) {
	s := deliveryService{wait: time.Second, interval: time.Millisecond, notify: func(context.Context) (bool, error) { return true, nil }, discover: func(context.Context) ([]Device, error) { return []Device{{Alias: "Phone A"}, {Alias: "Phone B"}}, nil }, send: func(context.Context, Device, string) error { t.Fatal("unexpected send"); return nil }}
	if err := s.deliver(context.Background(), nil, "Phone", "text", strings.NewReader(""), io.Discard); err == nil {
		t.Fatal("expected ambiguity")
	}
}

func TestNotificationOnlyCommandDoesNotReadClipboard(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	err := run(context.Background(), []string{"notify", "iphone"}, strings.NewReader(""), io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("unexpected result %v", err)
	}
}

func TestPairingFileDoesNotSendAndUnpairDeletes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("PATH", t.TempDir())
	path := filepath.Join(t.TempDir(), "pairing.json")
	data := `{"version":1,"deviceName":"Example Phone","deviceToken":"aabbccdd","bundleID":"dev.example.clipsend","environment":"sandbox"}`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), []string{"pair", "iphone", path}, strings.NewReader(""), io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	stored := filepath.Join(home, "clipsend", "devices.json")
	before, err := os.ReadFile(stored)
	if err != nil || !strings.Contains(string(before), "iphone") {
		t.Fatalf("pair missing %v", err)
	}
	if err := run(context.Background(), []string{"unpair", "iphone"}, strings.NewReader(""), io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(stored)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if strings.Contains(string(after), "iphone") || strings.Contains(string(after), "aabbccdd") {
		t.Fatal("pairing not deleted")
	}
}
