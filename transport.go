package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	localSendPort = 53317
	localSendAPI  = "/api/localsend/v2"
	maxMessage    = 64000
	maxResponse   = 1 << 20
)

type Device struct {
	Alias, IP, Fingerprint string
	Port                   int
	TLS                    bool
}

type Sender struct {
	alias, fingerprint string
	certificate        tls.Certificate
}

func NewSender(configDir string) (*Sender, error) {
	if configDir == "" {
		return nil, errors.New("config directory is empty")
	}
	if err := os.MkdirAll(configDir, 0700); err != nil {
		return nil, err
	}
	certPath, keyPath := filepath.Join(configDir, "identity.pem"), filepath.Join(configDir, "identity.key")
	if _, err := os.Stat(certPath); errors.Is(err, os.ErrNotExist) {
		if _, keyErr := os.Stat(keyPath); keyErr == nil {
			return nil, errors.New("incomplete LocalSend identity pair")
		}
		if err := generateIdentity(certPath, keyPath); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	if _, err := os.Stat(keyPath); errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("incomplete LocalSend identity pair")
	} else if err != nil {
		return nil, err
	}
	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("load LocalSend identity: %w", err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, err
	}
	fp := sha256.Sum256(pair.Certificate[0])
	host, _ := os.Hostname()
	user := os.Getenv("USER")
	if user == "" {
		user = "clipsend"
	}
	alias := user + " on " + host
	pair.Leaf = leaf
	return &Sender{alias: alias, fingerprint: strings.ToUpper(hex.EncodeToString(fp[:])), certificate: pair}, nil
}

func generateIdentity(certPath, keyPath string) error {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return err
	}
	template := &x509.Certificate{SerialNumber: randomSerial(), Subject: pkix.Name{CommonName: "clipsend"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().AddDate(10, 0, 0), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return err
	}
	certTmp, err := os.CreateTemp(filepath.Dir(certPath), ".identity-*.pem")
	if err != nil {
		return err
	}
	certName := certTmp.Name()
	defer os.Remove(certName)
	keyTmp, err := os.CreateTemp(filepath.Dir(keyPath), ".identity-*.key")
	if err != nil {
		certTmp.Close()
		return err
	}
	keyName := keyTmp.Name()
	defer os.Remove(keyName)
	if err = pem.Encode(certTmp, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err == nil {
		err = certTmp.Sync()
	}
	if closeErr := certTmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		keyTmp.Close()
		return err
	}
	if err = pem.Encode(keyTmp, &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}); err == nil {
		err = keyTmp.Sync()
	}
	if closeErr := keyTmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Chmod(keyName, 0600); err != nil {
		return err
	}
	if err = os.Rename(keyName, keyPath); err != nil {
		return err
	}
	return os.Rename(certName, certPath)
}

func randomSerial() *big.Int {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return big.NewInt(time.Now().UnixNano())
	}
	return n
}

func (s *Sender) registerDTO() map[string]any {
	return map[string]any{"alias": s.alias, "version": "2.2", "deviceModel": runtime.GOOS, "deviceType": "desktop", "fingerprint": s.fingerprint, "port": localSendPort, "protocol": "http", "download": false}
}

func (s *Sender) Discover(ctx context.Context) ([]Device, error) {
	nets, own, err := lanNetworks()
	if err != nil {
		return nil, err
	}
	set := map[string]bool{}
	for _, n := range nets {
		for a := n.Masked().Addr().Next(); n.Contains(a); a = a.Next() {
			if a == (netip.Addr{}) {
				continue
			}
			if n.Bits() <= 30 && a == lastAddr(n) {
				continue
			}
			if !own[a] {
				set[a.String()] = true
			}
		}
	}
	ips := make([]string, 0, len(set))
	for ip := range set {
		ips = append(ips, ip)
	}
	sort.Strings(ips)
	// Keep scans bounded even when an interface has an unusually broad prefix.
	if len(ips) > 4096 {
		ips = ips[:4096]
	}
	type result struct {
		d  Device
		ok bool
	}
	jobs := make(chan string)
	results := make(chan result, len(ips))
	workers := 128
	if len(ips) < workers {
		workers = len(ips)
	}
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ip := range jobs {
				if ctx.Err() != nil {
					continue
				}
				hostCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
				if !portOpen(hostCtx, ip) {
					cancel()
					continue
				}
				d, ok := s.probe(hostCtx, ip)
				cancel()
				results <- result{d, ok}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, ip := range ips {
			select {
			case jobs <- ip:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() { wg.Wait(); close(results) }()
	found := map[string]Device{}
	for r := range results {
		if r.ok {
			found[r.d.IP] = r.d
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := make([]Device, 0, len(found))
	for _, d := range found {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool {
		if strings.EqualFold(out[i].Alias, out[j].Alias) {
			return out[i].IP < out[j].IP
		}
		return strings.ToLower(out[i].Alias) < strings.ToLower(out[j].Alias)
	})
	return out, nil
}

func lastAddr(n netip.Prefix) netip.Addr {
	a := n.Masked().Addr()
	b := a.As4()
	bits := n.Bits()
	for i := bits; i < 32; i++ {
		b[i/8] |= 1 << uint(7-i%8)
	}
	return netip.AddrFrom4(b)
}

func lanNetworks() ([]netip.Prefix, map[netip.Addr]bool, error) {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil, nil, err
	}
	var nets []netip.Prefix
	own := map[netip.Addr]bool{}
	for _, in := range ifs {
		name := strings.ToLower(in.Name)
		if in.Flags&net.FlagUp == 0 || in.Flags&net.FlagLoopback != 0 || strings.HasPrefix(name, "lo") || strings.HasPrefix(name, "tailscale") || strings.HasPrefix(name, "tun") || strings.HasPrefix(name, "wg") || strings.HasPrefix(name, "docker") || strings.HasPrefix(name, "veth") || strings.HasPrefix(name, "br-") || strings.HasPrefix(name, "virbr") {
			continue
		}
		addrs, _ := in.Addrs()
		for _, raw := range addrs {
			p, err := netip.ParsePrefix(raw.String())
			if err != nil || !p.Addr().Is4() || !p.Addr().IsGlobalUnicast() {
				continue
			}
			own[p.Addr()] = true
			if p.Bits() < 22 {
				continue
			}
			nets = append(nets, p)
		}
	}
	return nets, own, nil
}

func portOpen(ctx context.Context, ip string) bool {
	d := net.Dialer{Timeout: 350 * time.Millisecond}
	c, err := d.DialContext(ctx, "tcp", net.JoinHostPort(ip, fmt.Sprint(localSendPort)))
	if err != nil {
		return false
	}
	c.Close()
	return true
}

func (s *Sender) probe(ctx context.Context, ip string) (Device, bool) {
	for _, useTLS := range []bool{true, false} {
		body, _ := json.Marshal(s.registerDTO())
		req, _ := http.NewRequestWithContext(ctx, "POST", scheme(useTLS)+"//"+net.JoinHostPort(ip, fmt.Sprint(localSendPort))+localSendAPI+"/register", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		resp, err := s.clientFor(useTLS, "").Do(req)
		if err != nil {
			continue
		}
		peer := ""
		if useTLS && resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
			peer = fingerprint(resp.TLS.PeerCertificates[0].Raw)
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
		resp.Body.Close()
		if resp.StatusCode != 200 || len(data) > maxResponse {
			continue
		}
		var m struct {
			Alias string `json:"alias"`
			Port  int    `json:"port"`
		}
		if json.Unmarshal(data, &m) != nil || m.Alias == "" {
			continue
		}
		if m.Port <= 0 || m.Port > 65535 {
			m.Port = localSendPort
		}
		return Device{Alias: m.Alias, IP: ip, Fingerprint: peer, Port: m.Port, TLS: useTLS}, true
	}
	return Device{}, false
}

func scheme(tlsOn bool) string {
	if tlsOn {
		return "https:"
	}
	return "http:"
}
func (s *Sender) clientFor(useTLS bool, pin string) *http.Client {
	tr := &http.Transport{DisableKeepAlives: true, TLSClientConfig: &tls.Config{InsecureSkipVerify: true, // LocalSend accepts self-signed client identities. Its advertised CA list
		// need not include our issuer, so explicitly present our identity.
		GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) { return &s.certificate, nil }, VerifyConnection: func(cs tls.ConnectionState) error {
			if useTLS && pin != "" && (len(cs.PeerCertificates) == 0 || fingerprint(cs.PeerCertificates[0].Raw) != pin) {
				return errors.New("LocalSend certificate fingerprint mismatch")
			}
			return nil
		}}}
	return &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
func fingerprint(cert []byte) string {
	h := sha256.Sum256(cert)
	return strings.ToUpper(hex.EncodeToString(h[:]))
}
func (s *Sender) Send(ctx context.Context, d Device, text string) error {
	if text == "" || !utf8.ValidString(text) {
		return errors.New("message must be nonempty valid UTF-8 text")
	}
	payload := []byte(text)
	if len(payload) > maxMessage {
		return errors.New("message exceeds 64,000 UTF-8 bytes")
	}
	if d.Port < 1 || d.Port > 65535 || net.ParseIP(d.IP) == nil {
		return errors.New("invalid LocalSend device address")
	}
	if d.TLS && d.Fingerprint == "" {
		return errors.New("TLS device has no pinned certificate fingerprint")
	}
	id, err := newUUID()
	if err != nil {
		return err
	}
	info := s.registerDTO()
	prepare := map[string]any{"info": info, "files": map[string]any{id: map[string]any{"id": id, "fileName": id + ".txt", "size": len(payload), "fileType": "text/plain", "preview": text}}}
	body, _ := json.Marshal(prepare)
	req, err := http.NewRequestWithContext(ctx, "POST", scheme(d.TLS)+"//"+net.JoinHostPort(d.IP, fmt.Sprint(d.Port))+localSendAPI+"/prepare-upload", strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.clientFor(d.TLS, d.Fingerprint).Do(req)
	if err != nil {
		return fmt.Errorf("delivery was not confirmed; check the receiving device: %w", err)
	}
	response, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	resp.Body.Close()
	if readErr != nil {
		return readErr
	}
	if len(response) > maxResponse {
		return errors.New("receiver response is too large")
	}
	if resp.StatusCode == 204 {
		return nil
	}
	if resp.StatusCode == 200 {
		var answer struct {
			SessionID string `json:"sessionId"`
		}
		if json.Unmarshal(response, &answer) == nil && answer.SessionID != "" {
			if e := s.cancel(ctx, d, answer.SessionID); e != nil {
				return fmt.Errorf("receiver requested a file; cancellation failed: %w", e)
			}
			return errors.New("receiver requested a file; transfer cancelled")
		}
		return errors.New("receiver requested a file; no upload was sent")
	}
	return fmt.Errorf("receiver refused message (HTTP %d)", resp.StatusCode)
}

func (s *Sender) cancel(ctx context.Context, d Device, id string) error {
	target := scheme(d.TLS) + "//" + net.JoinHostPort(d.IP, fmt.Sprint(d.Port)) + localSendAPI + "/cancel?sessionId=" + url.QueryEscape(id)
	r, e := http.NewRequestWithContext(ctx, "POST", target, nil)
	if e != nil {
		return e
	}
	resp, e := s.clientFor(d.TLS, d.Fingerprint).Do(r)
	if e != nil {
		return e
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponse))
	resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("cancel rejected (HTTP %d)", resp.StatusCode)
	}
	return nil
}

func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:], nil
}
