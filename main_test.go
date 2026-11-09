package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestChooseDevice(t *testing.T) {
	devices := []Device{{Alias: "Phone", IP: "192.0.2.1"}, {Alias: "Phone spare", IP: "192.0.2.2"}}
	for _, tt := range []struct {
		name, target, input, want string
		fail                      bool
	}{
		{"exact beats substring", "phone", "", "Phone", false},
		{"ambiguous", "phon", "", "", true},
		{"missing", "ipad", "", "", true},
		{"IP", "192.0.2.2", "", "Phone spare", false},
		{"picker", "", "2\n", "Phone spare", false},
		{"cancel", "", "\n", "", true},
		{"EOF", "", "", "", true},
		{"bad index", "", "0\n", "", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			d, err := chooseDevice(context.Background(), devices, tt.target, strings.NewReader(tt.input), io.Discard)
			if (err != nil) != tt.fail || d.Alias != tt.want {
				t.Fatalf("got %q, %v", d.Alias, err)
			}
		})
	}
}

func TestClipboardValidation(t *testing.T) {
	for _, tt := range []struct {
		text  string
		valid bool
	}{{"", false}, {"\xff", false}, {"hello\n世界\n", true}, {strings.Repeat("a", 64000), true}, {strings.Repeat("é", 32001), false}} {
		if (validateText(tt.text) == nil) != tt.valid {
			t.Errorf("unexpected validation for %d bytes", len(tt.text))
		}
	}
}

func TestAliasValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aliases.json")
	if _, err := loadAliases(path); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{`{"iphone":"Example Phone"}`, `{"iphone":""}`, `{"iphone":"a","IPHONE":"b"}`, `[]`, `{`} {
		if err := os.WriteFile(path, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := loadAliases(path)
		if (err == nil) != (value == `{"iphone":"Example Phone"}`) {
			t.Fatalf("%s: %v", value, err)
		}
	}
}

func TestDeviceLabelCannotInjectTerminalControls(t *testing.T) {
	if got := safeLabel("Phone\x1b\n\r\t"); got != "Phone" {
		t.Fatal(got)
	}
}

func TestHelpDoesNotReadClipboardOrCreateConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	if err := run(context.Background(), []string{"--help"}, strings.NewReader(""), io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(home)
	if err != nil || len(entries) != 0 {
		t.Fatalf("help created config: %v, %v", entries, err)
	}
}

func TestEmptyClipboardStopsBeforeDiscovery(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "wl-paste"), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	err := run(context.Background(), []string{"iphone"}, strings.NewReader(""), io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("unexpected result: %v", err)
	}
	entries, err := os.ReadDir(home)
	if err != nil || len(entries) != 0 {
		t.Fatalf("empty clipboard created identity: %v, %v", entries, err)
	}
}

func TestPickerInterrupt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r, w := io.Pipe()
	defer r.Close()
	defer w.Close()
	cancel()
	_, err := chooseDevice(ctx, []Device{{Alias: "Phone"}}, "", r, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("unexpected result: %v", err)
	}
}
