package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "clipsend:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) error {
	flags := flag.NewFlagSet("clipsend", flag.ContinueOnError)
	flags.SetOutput(errOut)
	list := flags.Bool("list", false, "list available LocalSend devices")
	flags.Usage = func() {
		fmt.Fprintln(errOut, "Usage: clipsend [iphone|ipad|device name]\n       clipsend --list\n\nSend clipboard text as a LocalSend message. With no destination, choose\na device in the terminal. Open LocalSend on the receiving device first.\nNo files, desktop notifications, or background service.")
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() > 1 || (*list && flags.NArg() != 0) {
		return errors.New("use one destination, or --list")
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	dir = filepath.Join(dir, "clipsend")
	aliases, err := loadAliases(filepath.Join(dir, "aliases.json"))
	if err != nil {
		return err
	}
	var text string
	if !*list {
		clipCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		cmd := exec.CommandContext(clipCtx, "wl-paste", "--no-newline", "--type", "text")
		pipe, err := cmd.StdoutPipe()
		if err != nil {
			return err
		}
		if err := cmd.Start(); err != nil {
			return errors.New("cannot read clipboard; install wl-clipboard and run inside your Wayland session")
		}
		data, readErr := io.ReadAll(io.LimitReader(pipe, 64001))
		if len(data) > 64000 {
			_ = cmd.Process.Kill()
		}
		waitErr := cmd.Wait()
		if readErr != nil {
			return readErr
		}
		if len(data) > 64000 {
			return errors.New("clipboard exceeds 64,000 bytes; nothing sent")
		}
		if waitErr != nil {
			return errors.New("cannot read text from the clipboard")
		}
		text = string(data)
		if err := validateText(text); err != nil {
			return err
		}
	}
	sender, err := NewSender(dir)
	if err != nil {
		return err
	}
	discoveryCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	devices, err := sender.Discover(discoveryCtx)
	cancel()
	if err != nil {
		return err
	}
	if len(devices) == 0 {
		return errors.New("no devices found; open LocalSend on your iPhone or iPad on the same LAN")
	}
	if *list {
		for _, d := range devices {
			fmt.Fprintf(out, "%s (%s)\n", safeLabel(d.Alias), d.IP)
		}
		return nil
	}
	target := flags.Arg(0)
	for k, v := range aliases {
		if strings.EqualFold(k, target) {
			target = v
			break
		}
	}
	device, err := chooseDevice(ctx, devices, target, in, out)
	if err != nil {
		return err
	}
	sendCtx, sendCancel := context.WithTimeout(ctx, 120*time.Second)
	defer sendCancel()
	if err := sender.Send(sendCtx, device, text); err != nil {
		return err
	}
	fmt.Fprintf(out, "Sent to %s.\n", safeLabel(device.Alias))
	return nil
}

func validateText(text string) error {
	if text == "" {
		return errors.New("clipboard is empty; nothing sent")
	}
	if !utf8.ValidString(text) {
		return errors.New("clipboard is not valid UTF-8 text; nothing sent")
	}
	if len(text) > 64000 {
		return errors.New("clipboard exceeds 64,000 bytes; nothing sent")
	}
	return nil
}

func loadAliases(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var aliases map[string]string
	if err := json.Unmarshal(data, &aliases); err != nil {
		return nil, fmt.Errorf("invalid aliases file: %w", err)
	}
	seen := map[string]bool{}
	for k, v := range aliases {
		key := strings.ToLower(k)
		if strings.TrimSpace(k) == "" || strings.TrimSpace(v) == "" || seen[key] {
			return nil, errors.New("aliases must have unique nonempty names and destinations")
		}
		seen[key] = true
	}
	return aliases, nil
}

func safeLabel(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}

func chooseDevice(ctx context.Context, devices []Device, target string, in io.Reader, out io.Writer) (Device, error) {
	if target != "" {
		var matches []Device
		for _, d := range devices {
			if strings.EqualFold(d.Alias, target) || d.IP == target {
				matches = append(matches, d)
			}
		}
		if len(matches) == 0 {
			for _, d := range devices {
				if strings.Contains(strings.ToLower(d.Alias), strings.ToLower(target)) {
					matches = append(matches, d)
				}
			}
		}
		if len(matches) == 1 {
			return matches[0], nil
		}
		if len(matches) > 1 {
			return Device{}, errors.New("destination is ambiguous; use the full device name or IP from --list")
		}
		return Device{}, fmt.Errorf("device %q is unavailable; open LocalSend on it and try again", safeLabel(target))
	}
	for i, d := range devices {
		fmt.Fprintf(out, "%d. %s\n", i+1, safeLabel(d.Alias))
	}
	fmt.Fprint(out, "Send to (number, Enter to cancel): ")
	type selection struct {
		line string
		err  error
	}
	answer := make(chan selection, 1)
	go func() { line, err := bufio.NewReader(in).ReadString('\n'); answer <- selection{line, err} }()
	var line string
	select {
	case <-ctx.Done():
		return Device{}, errors.New("cancelled; nothing sent")
	case result := <-answer:
		if result.err != nil && result.err != io.EOF {
			return Device{}, result.err
		}
		line = strings.TrimSpace(result.line)
	}
	if line == "" {
		return Device{}, errors.New("cancelled; nothing sent")
	}
	n, err := strconv.Atoi(line)
	if err != nil || n < 1 || n > len(devices) {
		return Device{}, errors.New("invalid selection; nothing sent")
	}
	return devices[n-1], nil
}
