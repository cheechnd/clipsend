package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"clipsend/internal/direct"
	"clipsend/internal/relay"
	"github.com/skip2/go-qrcode"
)

var version = "dev"

const defaultPort = 53318

const usage = `ClipSend — send text directly to your paired iPhone or iPad.

Usage:
  clipsend <alias> [--host IP] [--port PORT] [--stdin]
  clipsend send <alias> [--host IP] [--port PORT] [--stdin]
  clipsend pair <alias> [--host IP] [--port PORT] [--qr PATH]
  clipsend devices
  clipsend rename <alias> <new-alias>
  clipsend unpair <alias>
  clipsend relay <private-config.json>
  clipsend doctor
  clipsend --version

Scan the pairing QR code in the ClipSend mobile app, then approve the device here.
Sends expire after two minutes. Text stays in memory; Copy is explicit on the device.
Both devices must be on the same LAN and this computer must remain awake.
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "clipsend:", err)
		os.Exit(1)
	}
}
func nativeConfigDir() (string, error) {
	d, e := os.UserConfigDir()
	if e != nil {
		return "", e
	}
	return filepath.Join(d, "clipsend-native"), nil
}
func run(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprint(out, usage)
		return nil
	}
	if args[0] == "--version" {
		fmt.Fprintln(out, "clipsend", version)
		return nil
	}
	dir, err := nativeConfigDir()
	if err != nil {
		return err
	}
	if args[0] == "doctor" {
		return doctor(dir, out)
	}
	if args[0] == "relay" {
		if len(args) != 2 {
			return errors.New("usage: clipsend relay <private-config.json>")
		}
		c, e := readRelaySettings(args[1])
		if e != nil {
			return e
		}
		if e = saveRelaySettings(dir, c); e != nil {
			return e
		}
		fmt.Fprintln(out, "Notification service configured.")
		return nil
	}
	command := args[0]
	if command == "send" {
		args = args[1:]
		if len(args) == 0 {
			return errors.New("usage: clipsend send <alias> [--host IP] [--port PORT] [--stdin]")
		}
		return sendCommand(ctx, dir, args, in, out, errOut)
	}
	if command == "pair" {
		return pairCommand(ctx, dir, args[1:], in, out, errOut)
	}
	if command == "devices" || command == "--list" || command == "unpair" || command == "rename" {
		if _, e := os.Stat(dir); errors.Is(e, os.ErrNotExist) {
			if command == "devices" || command == "--list" {
				fmt.Fprintln(out, "No paired devices. Run clipsend pair <alias>.")
				return nil
			}
			return errors.New("no paired devices")
		}
		store, e := direct.OpenStore(dir)
		if e != nil {
			return e
		}
		defer store.Close()
		_, devices, e := store.Snapshot()
		if e != nil {
			return e
		}
		switch command {
		case "devices", "--list":
			if len(args) != 1 {
				return errors.New("usage: clipsend devices")
			}
			sort.Slice(devices, func(i, j int) bool { return devices[i].Alias < devices[j].Alias })
			for _, d := range devices {
				fmt.Fprintf(out, "%s\t%s\n", safeLabel(d.Alias), safeLabel(d.Name))
			}
			if len(devices) == 0 {
				fmt.Fprintln(out, "No paired devices. Run clipsend pair <alias>.")
			}
			return nil
		case "unpair":
			if len(args) != 2 {
				return errors.New("usage: clipsend unpair <alias>")
			}
			d, e := resolveDevice(devices, args[1])
			if e != nil {
				return e
			}
			if e = store.RevokeDevice(d.ID); e != nil {
				return e
			}
			fmt.Fprintf(out, "Unpaired %s. Its access to this computer is revoked.\n", safeLabel(d.Alias))
			return nil
		case "rename":
			if len(args) != 3 {
				return errors.New("usage: clipsend rename <alias> <new-alias>")
			}
			d, e := resolveDevice(devices, args[1])
			if e != nil {
				return e
			}
			if e = store.RenameDevice(d.ID, args[2]); e != nil {
				return e
			}
			fmt.Fprintf(out, "Renamed %s to %s.\n", safeLabel(d.Alias), safeLabel(args[2]))
			return nil
		}
	}
	return sendCommand(ctx, dir, args, in, out, errOut)
}
func resolveDevice(devices []direct.Device, alias string) (direct.Device, error) {
	for _, d := range devices {
		if strings.EqualFold(d.Alias, alias) {
			return d, nil
		}
	}
	return direct.Device{}, fmt.Errorf("%s is not paired; run clipsend pair %s", safeLabel(alias), safeLabel(alias))
}
func readAnswer(ctx context.Context, in io.Reader) (string, error) {
	type result struct {
		s string
		e error
	}
	ch := make(chan result, 1)
	go func() { s, e := bufio.NewReader(io.LimitReader(in, 256)).ReadString('\n'); ch <- result{s, e} }()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case r := <-ch:
		return strings.TrimSpace(r.s), r.e
	}
}
func computerName() string {
	n, e := os.Hostname()
	if e != nil || strings.TrimSpace(n) == "" {
		return "Computer"
	}
	return n
}
func pairCommand(ctx context.Context, dir string, args []string, in io.Reader, out, errOut io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: clipsend pair <alias> [--host IP] [--port PORT] [--qr PATH]")
	}
	alias := args[0]
	if strings.TrimSpace(alias) == "" || len(alias) > 64 || safeLabel(alias) != alias {
		return errors.New("use a short nonempty device alias without control characters")
	}
	fs := flag.NewFlagSet("pair", flag.ContinueOnError)
	fs.SetOutput(errOut)
	host := fs.String("host", "", "local IPv4 address")
	listenPort := fs.Int("port", defaultPort, "local HTTPS port (allow it through your LAN firewall)")
	qrPath := fs.String("qr", "", "also write a private QR PNG to this path")
	showCode := fs.Bool("code", false, "also print pairing JSON for manual entry")
	if e := fs.Parse(args[1:]); e != nil {
		return e
	}
	if *listenPort < 1 || *listenPort > 65535 {
		return errors.New("port must be between 1 and 65535")
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected pairing arguments")
	}
	lan, err := selectLAN(*host)
	if err != nil {
		return err
	}
	store, err := direct.OpenStore(dir)
	if err != nil {
		return err
	}
	defer store.Close()
	identity, devices, err := store.Snapshot()
	if err != nil {
		return err
	}
	for _, d := range devices {
		if strings.EqualFold(d.Alias, alias) {
			return errors.New("that alias is already paired; choose another or explicitly unpair it first")
		}
	}
	pairCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	server, err := direct.ListenPairing(store, direct.PairingConfig{BindAddress: net.JoinHostPort(lan.IP, strconv.Itoa(*listenPort)), EndpointHost: lan.IP, ComputerName: computerName(), ApprovePair: func(requestCtx context.Context, req direct.PairRequest) (string, bool) {
		fmt.Fprintf(out, "\nPair %s as %s? [y/N] ", safeLabel(req.DeviceName), safeLabel(alias))
		answer, err := readAnswer(requestCtx, in)
		return alias, err == nil && (strings.EqualFold(answer, "y") || strings.EqualFold(answer, "yes"))
	}})
	if err != nil {
		return err
	}
	defer server.Close()
	invitation := server.Invitation()
	payload := struct {
		Version           int    `json:"version"`
		ComputerID        string `json:"computerID"`
		ComputerName      string `json:"computerName"`
		Endpoint          string `json:"endpoint"`
		CertificateSHA256 string `json:"certificateSHA256"`
		Invitation        string `json:"invitation"`
		ExpiresAt         string `json:"expiresAt"`
	}{1, identity.ComputerID, computerName(), server.Endpoint(), identity.CertificateSHA256, invitation.Value, invitation.ExpiresAt.Format(time.RFC3339)}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	qr, err := qrcode.New(string(data), qrcode.Medium)
	if err != nil {
		return err
	}
	if *qrPath != "" {
		png, err := qr.PNG(768)
		if err != nil {
			return err
		}
		f, err := os.OpenFile(*qrPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return fmt.Errorf("create QR image: %w", err)
		}
		_, err = f.Write(png)
		closeErr := f.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		defer os.Remove(*qrPath)
		fmt.Fprintf(out, "QR image: %s (removed when pairing ends)\n", *qrPath)
	}
	fmt.Fprintln(out, "Open ClipSend on your device and scan this code. It expires in five minutes.")
	printFirewallGuidance(out, *listenPort)
	fmt.Fprintln(out, qr.ToSmallString(false))
	if *showCode {
		fmt.Fprintln(out, "Manual pairing code (expires with this session):")
		fmt.Fprintln(out, string(data))
	}
	failures := make(chan error, 1)
	go func() { failures <- server.Serve() }()
	select {
	case <-pairCtx.Done():
		return errors.New("pairing cancelled or expired; create a new pairing code")
	case err := <-failures:
		if err != nil {
			return err
		}
		return errors.New("pairing server stopped")
	case <-server.Done():
		if server.Result() != direct.Acked {
			return errors.New("pairing was not completed")
		}
		if _, e := resolveDevice(store.Devices(), alias); e != nil {
			return errors.New("pairing was not completed")
		}
		fmt.Fprintf(out, "Paired %s.\n", safeLabel(alias))
		_ = server.Close()
		fmt.Fprintln(out, "To check notifications, return the device to its Home Screen first.")
		fmt.Fprint(out, "Send a test message now? [y/N] ")
		answer, answerErr := readAnswer(ctx, in)
		if answerErr == nil && (strings.EqualFold(answer, "y") || strings.EqualFold(answer, "yes")) {
			return sendCommand(ctx, dir, []string{alias, "--host", lan.IP, "--port", strconv.Itoa(*listenPort), "--stdin"}, strings.NewReader("ClipSend is ready.\nTap Copy to try your first delivery. ✓"), out, errOut)
		}
		return nil
	}
}
func sendCommand(ctx context.Context, dir string, args []string, in io.Reader, out, errOut io.Writer) error {
	alias := args[0]
	fs := flag.NewFlagSet("send", flag.ContinueOnError)
	fs.SetOutput(errOut)
	host := fs.String("host", "", "local IPv4 address")
	listenPort := fs.Int("port", defaultPort, "local HTTPS port (allow it through your LAN firewall)")
	stdin := fs.Bool("stdin", false, "read text from standard input instead of clipboard")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *listenPort < 1 || *listenPort > 65535 {
		return errors.New("port must be between 1 and 65535")
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected send arguments")
	}
	var text string
	var err error
	if *stdin {
		b, e := io.ReadAll(io.LimitReader(in, 64001))
		if e != nil {
			return e
		}
		text = string(b)
		err = validateText(text)
	} else {
		text, err = readClipboard(ctx)
	}
	if err != nil {
		return err
	}
	if _, e := os.Stat(dir); errors.Is(e, os.ErrNotExist) {
		return fmt.Errorf("no paired devices; run clipsend pair %s", safeLabel(alias))
	}
	store, err := direct.OpenStore(dir)
	if err != nil {
		return err
	}
	defer store.Close()
	identity, devices, err := store.Snapshot()
	if err != nil {
		return err
	}
	device, err := resolveDevice(devices, alias)
	if err != nil {
		return err
	}
	lan, err := selectLAN(*host)
	if err != nil {
		return err
	}
	session, err := direct.ListenSession(store, direct.SessionConfig{BindAddress: net.JoinHostPort(lan.IP, strconv.Itoa(*listenPort)), EndpointHost: lan.IP, DeviceID: device.ID, SenderName: computerName(), Text: text})
	if err != nil {
		return err
	}
	defer session.Cancel()
	failures := make(chan error, 1)
	go func() { failures <- session.Serve() }()
	u, _ := url.Parse(session.Endpoint())
	port, _ := strconv.Atoi(u.Port())
	announcement, announceErr := advertise(lan, port, identity.ComputerID, session.ID())
	if announceErr == nil {
		defer announcement.Shutdown()
	} else {
		fmt.Fprintln(errOut, "Local discovery unavailable; notification delivery can still locate this send.")
	}
	// Give an already-open receiver a brief chance to acknowledge before notifying.
	grace := time.NewTimer(750 * time.Millisecond)
	defer grace.Stop()
	select {
	case <-session.Done():
		return reportResult(session, device, out)
	case <-ctx.Done():
		return errors.New("cancelled; receipt was not confirmed")
	case <-grace.C:
	}
	cfg, configErr := readRelaySettings(filepath.Join(dir, "relay.json"))
	if configErr == nil {
		notifyCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		err = cfg.notify(notifyCtx, relay.Request{DeviceToken: device.DeviceToken, BundleID: device.BundleID, Environment: device.Environment, Notification: relay.Notification{Version: 1, ComputerID: identity.ComputerID, TransferID: session.ID(), Endpoints: []string{session.Endpoint()}}})
		cancel()
		if err != nil {
			fmt.Fprintln(errOut, "Notification could not be sent. Open ClipSend on the device to receive this text.")
		}
	} else if errors.Is(configErr, os.ErrNotExist) {
		fmt.Fprintln(out, "Open ClipSend on your device to receive. Notifications are not configured.")
	} else {
		return configErr
	}
	fmt.Fprintf(out, "Waiting for %s. Tap its notification or open ClipSend; Ctrl+C cancels.\n", safeLabel(device.Alias))
	select {
	case <-ctx.Done():
		return errors.New("cancelled; receipt was not confirmed")
	case err := <-failures:
		if session.Result() == direct.Acked {
			return reportResult(session, device, out)
		}
		if err != nil {
			return err
		}
		return errors.New("receiver session ended without confirmation")
	case <-session.Done():
		return reportResult(session, device, out)
	}
}
func reportResult(s *direct.Session, d direct.Device, out io.Writer) error {
	switch s.Result() {
	case direct.Acked:
		fmt.Fprintf(out, "Received by %s.\n", safeLabel(d.Alias))
		return nil
	case direct.Expired:
		return errors.New("send expired; receipt was not confirmed")
	default:
		return errors.New("send cancelled; receipt was not confirmed")
	}
}
func doctor(dir string, out io.Writer) error {
	fmt.Fprintln(out, "ClipSend", version)
	printFirewallGuidance(out, defaultPort)
	addresses, err := localAddresses()
	if err != nil {
		return err
	}
	for _, a := range addresses {
		fmt.Fprintf(out, "Local network: %s (%s)\n", a.IP, safeLabel(a.Interface.Name))
	}
	if len(addresses) == 0 {
		fmt.Fprintln(out, "No supported local IPv4 network found.")
	}
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(out, "Not paired yet. Run clipsend pair <alias>.")
		return nil
	}
	s, err := direct.OpenStore(dir)
	if err != nil {
		return err
	}
	defer s.Close()
	identity, devices, err := s.Snapshot()
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Computer ID: %s\nPaired devices: %d\n", identity.ComputerID, len(devices))
	_, err = readRelaySettings(filepath.Join(dir, "relay.json"))
	if err == nil {
		fmt.Fprintln(out, "Notification service: configured (not contacted)")
	} else if errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(out, "Notification service: not configured")
	} else {
		return err
	}
	fmt.Fprintln(out, "The mobile app needs Local Network and Notifications permission. Keep this computer awake while sending.")
	return nil
}

func printFirewallGuidance(out io.Writer, port int) {
	config, err := os.ReadFile("/etc/ufw/ufw.conf")
	if err == nil && strings.Contains("\n"+string(config), "\nENABLED=yes") {
		fmt.Fprintf(out, "UFW is enabled in its configuration. Allow incoming TCP %d from your trusted local subnet; use sudo ufw status to inspect rules.\n", port)
	} else {
		fmt.Fprintf(out, "Your firewall must allow incoming TCP %d from your trusted local subnet.\n", port)
	}
	fmt.Fprintln(out, "If pairing cannot connect, check Local Network permission, Wi-Fi client isolation, and firewall rules. ClipSend never changes firewall rules automatically.")
}
