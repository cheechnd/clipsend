package main

import (
	"bytes"
	"clipsend/internal/relay"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

type relaySettings struct {
	URL            string `json:"url,omitempty"`
	OperatorConfig string `json:"operatorConfig,omitempty"`
	Token          string `json:"token"`
}

func (c relaySettings) validate() error {
	if len(c.Token) < 32 || len(c.Token) > 256 || strings.IndexFunc(c.Token, func(r rune) bool { return r < 33 || r > 126 }) >= 0 {
		return errors.New("invalid relay credential")
	}
	if c.OperatorConfig != "" {
		if c.URL != "" || !filepath.IsAbs(c.OperatorConfig) {
			return errors.New("operatorConfig must be an absolute path and cannot be combined with url")
		}
		return nil
	}
	u, err := url.Parse(c.URL)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return errors.New("relay URL must be an HTTPS origin")
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme != "https" && !(u.Scheme == "http" && ip != nil && ip.IsLoopback()) {
		return errors.New("relay requires HTTPS (HTTP is allowed only on numeric loopback addresses for local operation)")
	}
	if len(c.Token) < 32 || len(c.Token) > 256 || strings.IndexFunc(c.Token, func(r rune) bool { return r < 33 || r > 126 }) >= 0 {
		return errors.New("invalid relay credential")
	}
	return nil
}

func readRelaySettings(path string) (relaySettings, error) {
	var c relaySettings
	st, err := os.Lstat(path)
	if err != nil {
		return c, err
	}
	if !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 || st.Size() > 16384 {
		return c, errors.New("relay config must be a private regular file (chmod 600)")
	}
	f, err := os.Open(path)
	if err != nil {
		return c, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(st, opened) {
		return c, errors.New("relay config changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(f, 16385))
	if err != nil || len(data) > 16384 {
		return c, errors.New("cannot read relay config")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if dec.Decode(&c) != nil || dec.Decode(new(any)) != io.EOF {
		return c, errors.New("invalid relay config JSON")
	}
	return c, c.validate()
}

func saveRelaySettings(dir string, c relaySettings) error {
	if err := c.validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".relay-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	data, err := json.MarshalIndent(c, "", "  ")
	if err == nil {
		_, err = f.Write(append(data, '\n'))
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(name, filepath.Join(dir, "relay.json"))
}

// notify uses an explicitly configured personal APNs operator or hosted relay.
func (c relaySettings) notify(ctx context.Context, request relay.Request) error {
	if err := c.validate(); err != nil {
		return err
	}
	if c.OperatorConfig == "" {
		return (&relay.Client{RelayURL: c.URL, Token: c.Token}).Notify(ctx, request)
	}
	config, err := relay.LoadConfig(c.OperatorConfig)
	if err != nil {
		return err
	}
	service, err := relay.NewService(config)
	if err != nil {
		return err
	}
	return service.NotifyLocal(ctx, c.Token, request)
}
