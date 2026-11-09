package main

import (
	"context"
	"errors"
	"image/png"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

func readClipboard(ctx context.Context) (string, error) {
	limited, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(limited, "wl-paste", "--no-newline", "--type", "text")
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	if err = cmd.Start(); err != nil {
		return "", errors.New("cannot read clipboard; install wl-clipboard and run inside your Wayland session")
	}
	data, readErr := io.ReadAll(io.LimitReader(pipe, 64001))
	if len(data) > 64000 {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	if readErr != nil {
		return "", errors.New("could not read clipboard")
	}
	if len(data) > 64000 {
		return "", errors.New("clipboard exceeds 64,000 bytes; nothing sent")
	}
	if waitErr != nil {
		return "", errors.New("cannot read text from the clipboard")
	}
	text := string(data)
	if err := validateText(text); err != nil {
		return "", err
	}
	return text, nil
}
func readClipboardPNG(ctx context.Context) (string, error) {
	limited, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(limited, "wl-paste", "--no-newline", "--type", "image/png")
	pipe, e := cmd.StdoutPipe()
	if e != nil {
		return "", e
	}
	if e = cmd.Start(); e != nil {
		return "", errors.New("cannot read PNG clipboard")
	}
	f, e := os.CreateTemp("", "clipsend-clipboard-*.png")
	if e != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return "", e
	}
	name := f.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(name)
		}
	}()
	n, re := io.Copy(f, io.LimitReader(pipe, (128<<20)+1))
	if n > 128<<20 {
		_ = cmd.Process.Kill()
	}
	we := cmd.Wait()
	if ce := f.Close(); re != nil || we != nil || ce != nil {
		return "", errors.New("could not read PNG clipboard")
	}
	if n == 0 || n > 128<<20 {
		return "", errors.New("clipboard image is empty or too large")
	}
	check, e := os.Open(name)
	if e != nil {
		return "", e
	}
	cfg, e := png.DecodeConfig(check)
	_ = check.Close()
	if e != nil || cfg.Width < 1 || cfg.Height < 1 || int64(cfg.Width)*int64(cfg.Height) > 100_000_000 {
		return "", errors.New("clipboard image is not a supported PNG")
	}
	cleanup = false
	return name, nil
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
func safeLabel(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			return -1
		}
		return r
	}, s)
}
