package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"clipsend/internal/direct"
)

func TestReadOnlyHelpAndUnpairedList(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	for _, args := range [][]string{{}, {"--help"}, {"--version"}, {"devices"}} {
		if err := run(context.Background(), args, strings.NewReader(""), io.Discard, io.Discard); err != nil {
			t.Fatal(err)
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatal("read-only commands created config")
	}
}
func TestBadTextStopsBeforeConfigCreation(t *testing.T) {
	for _, value := range []string{"", "\xff", strings.Repeat("x", 64001)} {
		t.Run(string(rune(len(value)+65)), func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", dir)
			err := run(context.Background(), []string{"send", "ipad", "--stdin"}, strings.NewReader(value), io.Discard, io.Discard)
			if err == nil {
				t.Fatal("accepted invalid text")
			}
			entries, _ := os.ReadDir(dir)
			if len(entries) != 0 {
				t.Fatal("created config before validation")
			}
		})
	}
}
func TestDeviceResolutionRequiresExactAlias(t *testing.T) {
	d := []direct.Device{{Alias: "iPad", ID: "a"}, {Alias: "iPad Pro", ID: "b"}}
	got, err := resolveDevice(d, "IPAD")
	if err != nil || got.ID != "a" {
		t.Fatal("exact case-insensitive lookup failed")
	}
	if _, err := resolveDevice(d, "pad"); err == nil {
		t.Fatal("partial alias selected unintended recipient")
	}
}
func TestLabelsStripTerminalControl(t *testing.T) {
	if safeLabel("iPad\x1b\n\u202e") != "iPad" {
		t.Fatal("unsafe terminal label")
	}
}
func TestRelayImportPrivateAndNoSecretOutput(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	source := filepath.Join(t.TempDir(), "relay.json")
	secret := strings.Repeat("a", 43)
	if err := os.WriteFile(source, []byte(`{"url":"https://notify.example.com","token":"`+secret+`"}`), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := run(context.Background(), []string{"relay", source}, strings.NewReader(""), &out, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), secret) {
		t.Fatal("secret in output")
	}
	st, err := os.Stat(filepath.Join(dir, "clipsend-native", "relay.json"))
	if err != nil || st.Mode().Perm() != 0600 {
		t.Fatal("relay config not private")
	}
	if err := os.Chmod(source, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := readRelaySettings(source); err == nil {
		t.Fatal("accepted public credentials")
	}
}
func TestRelayOriginValidation(t *testing.T) {
	for _, u := range []string{"http://example.com", "https://user:password@example.com", "https://example.com/path", "https://example.com?secret=x", "file:///etc/passwd"} {
		if (relaySettings{URL: u, Token: strings.Repeat("a", 43)}).validate() == nil {
			t.Fatalf("accepted %s", u)
		}
	}
}

func TestOperatorSettingsRequireExplicitExclusivePath(t *testing.T) {
	token := strings.Repeat("a", 43)
	for _, c := range []relaySettings{
		{OperatorConfig: "relative.json", Token: token},
		{OperatorConfig: "/private/operator.json", URL: "https://example.com", Token: token},
		{OperatorConfig: "/private/operator.json"},
	} {
		if c.validate() == nil {
			t.Fatal("accepted ambiguous or invalid operator config")
		}
	}
	if err := (relaySettings{OperatorConfig: "/private/operator.json", Token: token}).validate(); err != nil {
		t.Fatal(err)
	}
}
