package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"

	"clipsend/internal/direct"
	"github.com/spf13/cobra"
)

func TestCobraLegacyInvocationParsing(t *testing.T) {
	for _, test := range []struct {
		name           string
		argv, wantArgs []string
		host           string
		port           int
		stdin          bool
	}{
		{"short clipboard", []string{"ipad"}, []string{"ipad"}, "", defaultPort, false},
		{"explicit clipboard", []string{"send", "iphone"}, []string{"iphone"}, "", defaultPort, false},
		{"short stdin", []string{"IPAD", "--stdin"}, []string{"IPAD"}, "", defaultPort, true},
		{"explicit flags", []string{"send", "iphone", "--host", "192.0.2.10", "--port=53319", "--stdin"}, []string{"iphone"}, "192.0.2.10", 53319, true},
		{"single dash long", []string{"ipad", "-host=192.0.2.11", "-port", "53320", "-stdin"}, []string{"ipad"}, "192.0.2.11", 53320, true},
		{"files with spaces", []string{"send", "ipad", "--port", "53319", "my photo.png", "two.pdf"}, []string{"ipad", "my photo.png", "two.pdf"}, "", 53319, false},
		{"flags after first file stay files", []string{"ipad", "photo.png", "--stdin", "--port", "5555"}, []string{"ipad", "photo.png", "--stdin", "--port", "5555"}, "", defaultPort, false},
		{"explicit stop", []string{"send", "ipad", "--", "--stdin", "--host=x"}, []string{"ipad", "--stdin", "--host=x"}, "", defaultPort, false},
		{"dash file", []string{"ipad", "-", "--stdin"}, []string{"ipad", "-", "--stdin"}, "", defaultPort, false},
		{"bool false", []string{"ipad", "--stdin=false"}, []string{"ipad"}, "", defaultPort, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := newRootCommand()
			var got []string
			capture := func(cmd *cobra.Command, args []string) error {
				got = args
				host, _ := cmd.Flags().GetString("host")
				port, _ := cmd.Flags().GetInt("port")
				stdin, _ := cmd.Flags().GetBool("stdin")
				if host != test.host || port != test.port || stdin != test.stdin {
					t.Fatalf("flags: host=%q port=%d stdin=%v", host, port, stdin)
				}
				return nil
			}
			root.RunE = capture
			send, _, _ := root.Find([]string{"send"})
			send.RunE = capture
			root.SetArgs(legacyFlagPlacement(root, test.argv))
			if err := root.Execute(); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, test.wantArgs) {
				t.Fatalf("args %q; want %q", got, test.wantArgs)
			}
		})
	}
}

func TestCobraErrorsBeforeEffects(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, test := range []struct {
		argv    []string
		message string
	}{
		{[]string{"send"}, "usage: clipsend send"},
		{[]string{"pair"}, "usage: clipsend pair"},
		{[]string{"pair", "ipad", "extra", "--code"}, "unexpected pairing arguments"},
		{[]string{"ipad", "--port", "0"}, "port must be between"},
		{[]string{"send", "ipad", "--port=65536"}, "port must be between"},
		{[]string{"pair", "ipad", "--port=0"}, "port must be between"},
		{[]string{"ui", "--port", "0"}, "invalid port"},
		{[]string{"ui", "extra"}, "usage: clipsend ui"},
		{[]string{"ipad", "--stdin", "a file.txt"}, "--stdin cannot be combined"},
		{[]string{"ipad", "--port=bad"}, "invalid argument"},
		{[]string{"ipad", "--host"}, "flag needs an argument"},
		{[]string{"ipad", "--unknown"}, "unknown flag"},
		{[]string{"rename", "ipad"}, "usage: clipsend rename"},
		{[]string{"unpair"}, "usage: clipsend unpair"},
		{[]string{"relay"}, "usage: clipsend relay"},
	} {
		t.Run(strings.Join(test.argv, " "), func(t *testing.T) {
			var out, stderr bytes.Buffer
			err := run(context.Background(), test.argv, forbiddenInput{}, &out, &stderr)
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("error %v; want %q", err, test.message)
			}
			if out.Len() != 0 {
				t.Fatalf("unexpected stdout: %s", &out)
			}
		})
	}
}

type forbiddenInput struct{}

func (forbiddenInput) Read([]byte) (int, error) { panic("unexpected stdin/clipboard read") }

func pairedFixture(t *testing.T) (string, *direct.Store, []direct.Device) {
	t.Helper()
	base := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", base)
	store, err := direct.OpenStore(filepath.Join(base, "clipsend-native"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	var devices []direct.Device
	for _, alias := range []string{"iphone", "ipad"} {
		invite, err := store.NewInvitation()
		if err != nil {
			t.Fatal(err)
		}
		d, err := store.ApproveInvitation(invite.Value, alias, true, direct.PairRequest{DeviceName: "Fixture " + alias, DeviceToken: "aabb", BundleID: "dev.example.clipsend", Environment: "sandbox"})
		if err != nil {
			t.Fatal(err)
		}
		devices = append(devices, d)
	}
	return base, store, devices
}

func completion(t *testing.T, argv ...string) ([]string, cobra.ShellCompDirective) {
	t.Helper()
	var out, stderr bytes.Buffer
	err := run(context.Background(), append([]string{"__completeNoDesc"}, argv...), forbiddenInput{}, &out, &stderr)
	if err != nil || stderr.Len() != 0 {
		t.Fatalf("completion %q: error=%v stderr=%q", argv, err, &stderr)
	}
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	directive, err := strconv.Atoi(strings.TrimPrefix(lines[len(lines)-1], ":"))
	if err != nil {
		t.Fatalf("invalid completion protocol: %q", &out)
	}
	return lines[:len(lines)-1], cobra.ShellCompDirective(directive)
}

func TestPairedCompletion(t *testing.T) {
	base, _, _ := pairedFixture(t)
	before, _ := os.ReadFile(filepath.Join(base, "clipsend-native", "store.json"))
	for _, test := range []struct {
		argv, want []string
		directive  cobra.ShellCompDirective
	}{
		{[]string{"ip"}, []string{"ipad", "iphone"}, cobra.ShellCompDirectiveNoFileComp},
		{[]string{"IP"}, []string{"IPad", "IPhone"}, cobra.ShellCompDirectiveNoFileComp},
		{[]string{"send", "ip"}, []string{"ipad", "iphone"}, cobra.ShellCompDirectiveNoFileComp},
		{[]string{"unpair", "iP"}, []string{"iPad", "iPhone"}, cobra.ShellCompDirectiveNoFileComp},
		{[]string{"rename", "ip"}, []string{"ipad", "iphone"}, cobra.ShellCompDirectiveNoFileComp},
		{[]string{"rename", "iphone", "ip"}, nil, cobra.ShellCompDirectiveNoFileComp},
		{[]string{"unpair", "iphone", ""}, nil, cobra.ShellCompDirectiveNoFileComp},
		{[]string{"pair", "ip"}, nil, cobra.ShellCompDirectiveNoFileComp},
		{[]string{"ipad", "my photo"}, nil, cobra.ShellCompDirectiveDefault},
		{[]string{"send", "ipad", "my photo"}, nil, cobra.ShellCompDirectiveDefault},
		{[]string{"send", "ipad", "--host", "192.0.2.1", "my photo"}, nil, cobra.ShellCompDirectiveDefault},
		{[]string{"ipad", "first file", "--ho"}, nil, cobra.ShellCompDirectiveDefault},
		{[]string{"pair", "fresh", "--qr", "my qr"}, nil, cobra.ShellCompDirectiveDefault},
		{[]string{"relay", "my config"}, nil, cobra.ShellCompDirectiveDefault},
		{[]string{"send", "ipad", "--host", ""}, nil, cobra.ShellCompDirectiveNoFileComp},
		{[]string{"send", "ipad", "--port", ""}, nil, cobra.ShellCompDirectiveNoFileComp},
	} {
		t.Run(strings.Join(test.argv, " "), func(t *testing.T) {
			got, directive := completion(t, test.argv...)
			if len(got) != len(test.want) || strings.Join(got, "\n") != strings.Join(test.want, "\n") || directive != test.directive {
				t.Fatalf("got %q :%d; want %q :%d", got, directive, test.want, test.directive)
			}
		})
	}
	root, _ := completion(t, "")
	for _, item := range []string{"send", "pair", "devices", "rename", "unpair", "doctor", "relay", "completion", "iphone", "ipad"} {
		if !contains(root, item) {
			t.Errorf("root missing %q: %q", item, root)
		}
	}
	flags, _ := completion(t, "send", "ipad", "--")
	for _, flag := range []string{"--host", "--port", "--stdin", "--help"} {
		if !contains(flags, flag) {
			t.Errorf("missing flag %s", flag)
		}
	}
	after, _ := os.ReadFile(filepath.Join(base, "clipsend-native", "store.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("completion changed the pairing store")
	}
	if err := run(context.Background(), []string{"rename", "IPHONE", "Work Phone"}, forbiddenInput{}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), []string{"unpair", "IPAD"}, forbiddenInput{}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	got, _ := completion(t, "send", "")
	if !reflect.DeepEqual(got, []string{"Work Phone"}) {
		t.Fatalf("stale or hardcoded aliases: %q", got)
	}
	got, _ = completion(t, "work")
	if !reflect.DeepEqual(got, []string{"work Phone"}) {
		t.Fatalf("renamed case-insensitive completion: %q", got)
	}
}
func contains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func TestCompletionMissingAndUnreadableStore(t *testing.T) {
	for _, mode := range []string{"missing", "empty", "corrupt", "unsafe", "symlink"} {
		t.Run(mode, func(t *testing.T) {
			base := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", base)
			dir := filepath.Join(base, "clipsend-native")
			switch mode {
			case "missing":
			case "symlink":
				if err := os.Symlink(t.TempDir(), dir); err != nil {
					t.Fatal(err)
				}
			default:
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if mode != "empty" {
					perm := os.FileMode(0600)
					if mode == "unsafe" {
						perm = 0000
					}
					if err := os.WriteFile(filepath.Join(dir, "store.json"), []byte(`{broken`), perm); err != nil {
						t.Fatal(err)
					}
				}
			}
			got, directive := completion(t, "ip")
			if len(got) != 0 || directive != cobra.ShellCompDirectiveNoFileComp {
				t.Fatalf("bad aliases %q :%d", got, directive)
			}
			got, _ = completion(t, "se")
			if !contains(got, "send") {
				t.Fatalf("commands unavailable: %q", got)
			}
			got, _ = completion(t, "send", "ipad", "--st")
			if !contains(got, "--stdin") {
				t.Fatalf("flags unavailable: %q", got)
			}
			if _, err := os.Lstat(filepath.Join(dir, "store.lock")); !os.IsNotExist(err) {
				t.Fatal("completion created/opened normal store lock")
			}
			if mode == "missing" {
				if _, err := os.Lstat(dir); !os.IsNotExist(err) {
					t.Fatal("completion initialized store")
				}
			}
		})
	}
}

// Run the actual main entry point in a disposable process to assert exit codes.
func TestCLIProcess(t *testing.T) {
	if os.Getenv("CLIPSEND_CLI_PROCESS") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{"clipsend"}, os.Args[i+1:]...)
			main()
			os.Exit(0)
		}
	}
	os.Exit(99)
}
func cliProcess(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], append([]string{"-test.run=^TestCLIProcess$", "--"}, args...)...)
	cmd.Env = append(os.Environ(), "CLIPSEND_CLI_PROCESS=1")
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		if e, ok := err.(*exec.ExitError); ok {
			code = e.ExitCode()
		} else {
			t.Fatal(err)
		}
	}
	return out.String(), stderr.String(), code
}
func TestCLIHelpAndExitCodes(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, test := range []struct {
		args  []string
		code  int
		match string
	}{
		{nil, 0, "Usage:"}, {[]string{"--help"}, 0, "Available Commands:"}, {[]string{"--version"}, 0, "clipsend dev"},
		{[]string{"send", "--help"}, 0, "--stdin"}, {[]string{"pair", "--help"}, 0, "--qr"},
		{[]string{"ipad", "--help"}, 1, "Usage:"}, {[]string{"send", "ipad", "--help"}, 1, "Usage:"}, {[]string{"pair", "ipad", "-h"}, 1, "Usage:"},
		{[]string{"send"}, 1, "usage: clipsend send"}, {[]string{"ipad", "--port=bad"}, 1, "invalid argument"},
		{[]string{"ipad", "--stdin"}, 1, "clipboard is empty"},
		{[]string{"--list"}, 0, "No paired devices"},
		{[]string{"completion", "invalid"}, 1, "unknown command"},
	} {
		t.Run(fmt.Sprint(test.args), func(t *testing.T) {
			out, stderr, code := cliProcess(t, test.args...)
			if code != test.code || !strings.Contains(out+stderr, test.match) {
				t.Fatalf("exit %d out=%q err=%q; want %d / %q", code, out, stderr, test.code, test.match)
			}
		})
	}
}

func TestGeneratedCompletionScripts(t *testing.T) {
	for _, shell := range []string{"bash", "zsh", "fish", "powershell"} {
		var out, stderr bytes.Buffer
		err := run(context.Background(), []string{"completion", shell}, forbiddenInput{}, &out, &stderr)
		if err != nil || out.Len() < 100 || stderr.Len() != 0 {
			t.Fatalf("%s: error=%v out=%d stderr=%q", shell, err, out.Len(), &stderr)
		}
		if shell == "bash" {
			committed, err := os.ReadFile("completions/clipsend.bash")
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(out.Bytes(), committed) {
				t.Fatal("regenerate completions/clipsend.bash with clipsend completion bash")
			}
		}
	}
}

func TestCLISendsKeepSocketHandoff(t *testing.T) {
	// Only a disposable local Unix socket is involved. Clipboard reads belong to
	// the UI service, so forbiddenInput also protects the no-stdin quick path.
	base, err := os.MkdirTemp("", "cs-cli-")
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
	requests := make(chan uiRequest, 8)
	socket, _, err := startUISocket(dir, &wg, func(q uiRequest, conn net.Conn) {
		requests <- q
		_ = json.NewEncoder(conn).Encode(map[string]any{"version": 1, "id": q.ID, "type": "result", "state": "acked"})
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { socket.Close(); wg.Wait() }()
	for _, test := range []struct {
		args  []string
		text  string
		files []string
	}{
		{[]string{"iphone"}, "", nil},
		{[]string{"send", "ipad"}, "", nil},
		{[]string{"IPAD", "--stdin"}, "Unicode ✓\nline two", nil},
		{[]string{"send", "iphone", "--stdin"}, "stdin ✓", nil},
		{[]string{"ipad", "photo with spaces.png"}, "", []string{"photo with spaces.png"}},
		{[]string{"send", "iphone", "photo with spaces.png", "--stdin"}, "", []string{"photo with spaces.png", "--stdin"}},
	} {
		t.Run(strings.Join(test.args, " "), func(t *testing.T) {
			var out bytes.Buffer
			var in interface{ Read([]byte) (int, error) } = forbiddenInput{}
			if test.text != "" {
				in = strings.NewReader(test.text)
			}
			if err := run(context.Background(), test.args, in, &out, &out); err != nil {
				t.Fatal(err)
			}
			q := <-requests
			alias := test.args[0]
			if alias == "send" {
				alias = test.args[1]
			}
			if q.Alias != alias || q.Text != test.text {
				t.Fatalf("handoff alias/text: %#v", q)
			}
			var absolute []string
			for _, file := range test.files {
				p, err := filepath.Abs(file)
				if err != nil {
					t.Fatal(err)
				}
				absolute = append(absolute, p)
			}
			if len(q.Files) != len(absolute) || strings.Join(q.Files, "\n") != strings.Join(absolute, "\n") {
				t.Fatalf("files %q want %q", q.Files, absolute)
			}
			if !strings.Contains(out.String(), "Received by paired device.") {
				t.Fatal(&out)
			}
		})
	}
	// A completion request must not connect to the existing UI socket.
	completion(t, "ip")
	select {
	case <-requests:
		t.Fatal("completion contacted the UI service")
	default:
	}
}

func TestGeneratedBashReadline(t *testing.T) {
	for _, tool := range []string{"bash", "python3"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " unavailable")
		}
	}
	if _, err := os.Stat("/usr/share/bash-completion/bash_completion"); err != nil {
		t.Skip("bash-completion unavailable")
	}
	_, store, devices := pairedFixture(t)
	if err := store.RenameDevice(devices[0].ID, "Work Phone"); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	wrapper := filepath.Join(dir, "clipsend")
	if err := os.WriteFile(wrapper, []byte(`#!/bin/sh
exec "$CLIPSEND_TEST_EXECUTABLE" -test.run='^TestCLIProcess$' -- "$@"
`), 0700); err != nil {
		t.Fatal(err)
	}
	var generated bytes.Buffer
	if err := run(context.Background(), []string{"completion", "bash"}, forbiddenInput{}, &generated, &generated); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "completion.bash")
	if err := os.WriteFile(script, generated.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("python3", "scripts/test-bash-completion.py", script, dir)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Env = append(os.Environ(), "CLIPSEND_CLI_PROCESS=1", "CLIPSEND_TEST_EXECUTABLE="+executable, "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("readline: %v\n%s", err, out)
	}
	t.Log(string(out))
}
