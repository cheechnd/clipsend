package main

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/spf13/cobra"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// Never access the user's clipboard, even if the implementation regresses.
func guardTextClipboard(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	marker := filepath.Join(dir, "clipboard-accessed")
	t.Setenv("CLIPSEND_TEXT_CLIPBOARD_MARKER", marker)
	for _, name := range []string{"wl-paste", "wl-copy"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nprintf accessed > \"$CLIPSEND_TEXT_CLIPBOARD_MARKER\"\nexit 1\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Cleanup(func() {
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Error("--text accessed the clipboard")
		}
	})
}

func TestCLITextInputs(t *testing.T) {
	guardTextClipboard(t)
	// Use a short disposable Unix socket path, independent of all live pairings.
	base, err := os.MkdirTemp("", "cs-text-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(base)
	t.Setenv("XDG_CONFIG_HOME", base)
	dir := filepath.Join(base, "clipsend-native")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	requests := make(chan uiRequest, 1)
	socket, _, err := startUISocket(dir, &wg, func(q uiRequest, conn net.Conn) {
		requests <- q
		_ = json.NewEncoder(conn).Encode(map[string]any{"version": 1, "id": q.ID, "type": "result"})
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { socket.Close(); wg.Wait() }()
	for _, form := range [][]string{{"iphone"}, {"send", "ipad"}} {
		t.Run(strings.Join(form, " "), func(t *testing.T) {
			for _, test := range []struct {
				name, text string
				suffix     []string
				wantError  string
			}{
				{name: "URL", text: "https://example.com"},
				{name: "exact whitespace Unicode and newlines", text: " \tMeet downstairs — café 世界 ✓\r\nline two\n  "},
				{name: "whitespace only is preserved", text: " \t\n "},
				{name: "literal flag value", text: "--stdin"},
				{name: "64000 ASCII bytes", text: strings.Repeat("x", 64000)},
				{name: "64000 UTF8 bytes", text: strings.Repeat("é", 32000)},
				{name: "empty", wantError: "empty"},
				{name: "invalid UTF8", text: "\xff", wantError: "not valid UTF-8"},
				{name: "64001 bytes", text: strings.Repeat("x", 64001), wantError: "64,000 bytes"},
				{name: "multibyte limit", text: strings.Repeat("é", 32000) + "x", wantError: "64,000 bytes"},
				{name: "stdin conflict", text: "hello", suffix: []string{"--stdin"}, wantError: "text"},
				{name: "explicit stdin false conflicts too", text: "hello", suffix: []string{"--stdin=false"}, wantError: "text"},
				{name: "file conflict", text: "hello", suffix: []string{"my photo.png"}, wantError: "--text cannot be combined with attachment paths"},
			} {
				t.Run(test.name, func(t *testing.T) {
					args := append(append(append([]string(nil), form...), "--text", test.text), test.suffix...)
					var out, stderr bytes.Buffer
					err := run(context.Background(), args, forbiddenInput{}, &out, &stderr)
					if test.wantError != "" {
						if err == nil || !strings.Contains(err.Error(), test.wantError) {
							t.Fatalf("error %v; want %q", err, test.wantError)
						}
						select {
						case <-requests:
							t.Fatal("invalid --text was sent to the UI")
						default:
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					q := <-requests
					if q.Text != test.text || len(q.Files) != 0 || q.Alias != form[len(form)-1] {
						t.Fatalf("text content, destination, or files changed: text bytes=%d", len(q.Text))
					}
					if strings.Contains(out.String()+stderr.String(), test.text) {
						t.Fatal("sent text leaked to output")
					}
				})
			}
			t.Run("equals spelling", func(t *testing.T) {
				args := append(append([]string(nil), form...), "--text=  café\n  ")
				if err := run(context.Background(), args, forbiddenInput{}, io.Discard, io.Discard); err != nil {
					t.Fatal(err)
				}
				if q := <-requests; q.Text != "  café\n  " {
					t.Fatal("--text= content changed")
				}
			})
		})
	}
}

func TestCLITextWithoutUI(t *testing.T) {
	guardTextClipboard(t)
	base := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", base)
	for _, form := range [][]string{{"iphone"}, {"send", "ipad"}} {
		for _, text := range []string{"", "\xff", strings.Repeat("x", 64001), "  Unicode ✓\n "} {
			args := append(append([]string(nil), form...), "--text", text)
			err := run(context.Background(), args, forbiddenInput{}, io.Discard, io.Discard)
			expected := validateText(text)
			if expected != nil {
				if err == nil || err.Error() != expected.Error() {
					t.Fatalf("validation mismatch: got %v, want %v", err, expected)
				}
			} else if err == nil || !strings.Contains(err.Error(), "no paired devices") {
				t.Fatalf("valid explicit text did not reach standalone path: %v", err)
			}
		}
	}
	entries, err := os.ReadDir(base)
	if err != nil || len(entries) != 0 {
		t.Fatal("validation initialized pairing state")
	}
}

func TestCLITextHelpAndCompletion(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"send", "--help"}} {
		var out bytes.Buffer
		if err := run(context.Background(), args, forbiddenInput{}, &out, &out); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "--text string") {
			t.Fatal("--text missing from help")
		}
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	flags, _ := completion(t, "send", "ipad", "--te")
	if !contains(flags, "--text") {
		t.Fatal("--text missing from completion")
	}
	values, directive := completion(t, "send", "ipad", "--text", "")
	if len(values) != 0 || directive != cobra.ShellCompDirectiveNoFileComp {
		t.Fatal("text values should not complete files")
	}
}
