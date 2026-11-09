package main

import (
	"clipsend/internal/push"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

var errDeviceUnavailable = errors.New("device is unavailable")

func readClipboard(ctx context.Context) ([]byte, error) {
	limited, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(limited, "wl-paste", "--no-newline", "--type", "text")
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, errors.New("cannot read clipboard; install wl-clipboard and run inside your Wayland session")
	}
	data, readErr := io.ReadAll(io.LimitReader(pipe, 64001))
	if len(data) > 64000 {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	if readErr != nil {
		return nil, readErr
	}
	if len(data) > 64000 {
		return nil, errors.New("clipboard exceeds 64,000 bytes; nothing sent")
	}
	if waitErr != nil {
		return nil, errors.New("cannot read text from the clipboard")
	}
	return data, nil
}

func companionCommand(ctx context.Context, args []string, out io.Writer) error {
	if len(args) < 2 || len(args) > 3 || (args[0] != "pair" && len(args) != 2) {
		return errors.New("usage: clipsend pair <alias> [pairing.json], unpair <alias>, or notify <alias>")
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	dir := filepath.Join(base, "clipsend")
	alias := strings.ToLower(args[1])
	switch args[0] {
	case "pair":
		var data []byte
		if len(args) == 3 {
			f, e := os.Open(args[2])
			if e != nil {
				return e
			}
			defer f.Close()
			data, err = io.ReadAll(io.LimitReader(f, 64001))
		} else {
			data, err = readClipboard(ctx)
		}
		if err != nil {
			return err
		}
		if len(data) > 64000 {
			return errors.New("pairing data is too large")
		}
		if err := push.Pair(dir, alias, data); err != nil {
			return err
		}
		fmt.Fprintf(out, "Paired notifications for %s.\n", safeLabel(alias))
	case "unpair":
		if err := push.Unpair(dir, alias); err != nil {
			return err
		}
		fmt.Fprintf(out, "Removed notification pairing for %s.\n", safeLabel(alias))
	case "notify":
		accepted, err := push.Notify(ctx, dir, alias)
		if err != nil {
			return err
		}
		if !accepted {
			return errors.New("notifications are not configured for this alias")
		}
		fmt.Fprintf(out, "Notification accepted by Apple for %s; check the device.\n", safeLabel(alias))
	}
	return nil
}

type deliveryService struct {
	discover func(context.Context) ([]Device, error)
	send     func(context.Context, Device, string) error
	notify   func(context.Context) (bool, error)
	wait     time.Duration
	interval time.Duration
}

func (s deliveryService) deliver(ctx context.Context, devices []Device, target, text string, in io.Reader, out io.Writer) error {
	device, err := chooseDevice(ctx, devices, target, in, out)
	if errors.Is(err, errDeviceUnavailable) && target != "" {
		missing := err
		accepted, notifyErr := s.notify(ctx)
		if notifyErr != nil {
			return notifyErr
		}
		if !accepted {
			return missing
		}
		fmt.Fprintf(out, "Waiting for %s. Tap the phone notification to open LocalSend; Ctrl+C cancels.\n", safeLabel(target))
		waitCtx, cancel := context.WithTimeout(ctx, s.wait)
		defer cancel()
		for {
			timer := time.NewTimer(s.interval)
			select {
			case <-waitCtx.Done():
				timer.Stop()
				return errors.New("LocalSend did not become available; nothing sent")
			case <-timer.C:
			}
			discovered, discoverErr := s.discover(waitCtx)
			if discoverErr != nil {
				if waitCtx.Err() != nil {
					return errors.New("waiting ended; nothing sent")
				}
				return fmt.Errorf("discovery failed while waiting; nothing sent: %w", discoverErr)
			}
			device, err = chooseDevice(waitCtx, discovered, target, strings.NewReader(""), io.Discard)
			if err == nil {
				break
			}
			if !errors.Is(err, errDeviceUnavailable) {
				return err
			}
		}
	}
	if err != nil {
		return err
	}
	if ctx.Err() != nil {
		return errors.New("cancelled; nothing sent")
	}
	limited, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	if err := s.send(limited, device, text); err != nil {
		return err
	}
	fmt.Fprintf(out, "Sent to %s.\n", safeLabel(device.Alias))
	return nil
}
