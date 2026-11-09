package push

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const alertPayload = `{"aps":{"alert":{"title":"ClipSend","body":"Clipboard ready. Tap to open LocalSend."}}}`

// Notify sends one generic alert for a paired alias. Only an HTTP/2 200 response
// from APNs yields true. It performs no setup writes and never retries a push.
func Notify(ctx context.Context, configDir, alias string) (bool, error) {
	return notify(ctx, configDir, alias, time.Now(), newTransport)
}

// The factory is private and injected only by tests; callers cannot select a
// host, proxy, unverified TLS connection, payload, or redirect policy.
func notify(ctx context.Context, dir, alias string, now time.Time, transport func() *http.Transport) (bool, error) {
	if ctx == nil {
		return false, errors.New("push: context is required")
	}
	if err := validateAlias(alias); err != nil {
		return false, err
	}
	if strings.TrimSpace(dir) == "" {
		return false, errors.New("push: config directory is required")
	}
	if err := ctx.Err(); err != nil {
		return false, fmt.Errorf("push: request canceled: %w", err)
	}
	data, missing, err := readPrivate(filepath.Join(dir, "push.json"), maxConfigBytes)
	if err != nil {
		return false, errors.New("push: cannot read private push.json")
	}
	if missing {
		return false, nil
	}
	devices, err := loadDevices(dir)
	if err != nil {
		return false, err
	}
	key, paired := findAlias(devices, alias)
	if !paired {
		return false, nil
	}
	p := devices[key]
	var cfg config
	if decodeStrict(data, &cfg) != nil {
		return false, errors.New("push: invalid push.json")
	}
	if err := cfg.validate(); err != nil {
		return false, err
	}
	jwt, err := providerToken(cfg, now)
	if err != nil {
		return false, err
	}
	endpoint, err := apnsEndpoint(p.Environment)
	if err != nil {
		return false, err
	}
	requestCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint+"/3/device/"+p.DeviceToken, strings.NewReader(alertPayload))
	if err != nil {
		return false, errors.New("push: cannot construct notification")
	}
	// HTTP/2 may replay even a POST on REFUSED_STREAM/GOAWAY. An explicit
	// failing GetBody blocks that replay, including an unused-connection retry.
	req.GetBody = func() (io.ReadCloser, error) {
		return nil, errors.New("push: notification replay is disabled")
	}
	req.Header.Set("Authorization", "bearer "+jwt)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("apns-topic", p.BundleID)
	req.Header.Set("apns-push-type", "alert")
	req.Header.Set("apns-priority", "10")
	req.Header.Set("apns-expiration", strconv.FormatInt(now.Add(120*time.Second).Unix(), 10))
	// Avoid putting the human alias itself in Apple-visible metadata.
	collapse := sha256.Sum256([]byte(key))
	req.Header.Set("apns-collapse-id", hex.EncodeToString(collapse[:]))
	req.Header.Set("User-Agent", "ClipSend")
	tr := transport()
	defer tr.CloseIdleConnections()
	// RoundTrip performs exactly one HTTP exchange and has no redirect or
	// cookie machinery. In particular, a 307 cannot even invoke GetBody.
	resp, err := tr.RoundTrip(req)
	if err != nil {
		// url.Error and TLS/network errors may contain the token-bearing URL
		// or remote-controlled text. Retain only cancellation classification.
		if requestCtx.Err() != nil {
			return false, fmt.Errorf("push: request canceled or timed out: %w", requestCtx.Err())
		}
		return false, errors.New("push: APNs request failed")
	}
	defer resp.Body.Close()
	if resp.ProtoMajor != 2 {
		return false, errors.New("push: APNs requires HTTP/2")
	}
	if resp.StatusCode == http.StatusOK {
		return true, nil
	}
	return false, rejectionError(resp)
}

func rejectionError(resp *http.Response) error {
	message := fmt.Sprintf("push: APNs rejected notification (HTTP %d)", resp.StatusCode)
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4097))
	if err != nil || len(data) > 4096 {
		return errors.New(message)
	}
	var response struct {
		Reason string `json:"reason"`
	}
	if json.Unmarshal(data, &response) != nil {
		return errors.New(message)
	}
	// Never interpolate even a recognized reason. Every user-visible word of
	// guidance is local, and arbitrary server text remains private.
	var guidance string
	switch response.Reason {
	case "BadDeviceToken":
		guidance = "re-pair the device and check its sandbox/production environment"
	case "Unregistered":
		guidance = "the device token is no longer registered; re-pair the device"
	case "DeviceTokenNotForTopic":
		guidance = "re-pair the device and check the app bundle ID"
	case "InvalidProviderToken":
		guidance = "check the APNs key ID, team ID, .p8 key, and system clock"
	case "ExpiredProviderToken":
		guidance = "check the system clock and APNs signing credentials"
	case "BadTopic", "TopicDisallowed":
		guidance = "check the paired bundle ID and the APNs key's permission for this app"
	case "Forbidden":
		guidance = "check the APNs key's permissions in the Apple Developer account"
	case "TooManyProviderTokenUpdates":
		guidance = "APNs is limiting signing-token updates; wait before sending again"
	case "TooManyRequests":
		guidance = "APNs is rate limiting this device; wait before sending again"
	case "InternalServerError", "ServiceUnavailable", "Shutdown":
		guidance = "APNs is temporarily unavailable; try again later"
	}
	if guidance != "" {
		message += "; " + guidance
	}
	return errors.New(message)
}

func apnsEndpoint(environment string) (string, error) {
	switch environment {
	case "sandbox":
		return "https://api.sandbox.push.apple.com", nil
	case "production":
		return "https://api.push.apple.com", nil
	default:
		return "", errors.New("push: invalid pairing environment")
	}
}

func newTransport() *http.Transport {
	protocols := new(http.Protocols)
	protocols.SetHTTP2(true)
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, NextProtos: []string{"h2"}}
	return &http.Transport{
		// Deliberately start from a new transport: DefaultTransport uses
		// ProxyFromEnvironment and is mutable by other packages.
		Proxy:                  nil,
		Protocols:              protocols,
		ForceAttemptHTTP2:      true,
		DisableKeepAlives:      true,
		DisableCompression:     true,
		MaxResponseHeaderBytes: 16 << 10,
		ResponseHeaderTimeout:  10 * time.Second,
		TLSClientConfig:        tlsConfig,
		DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			dialer := tls.Dialer{
				NetDialer: &net.Dialer{Timeout: 10 * time.Second},
				Config:    tlsConfig,
			}
			conn, err := dialer.DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			// Some TLS servers omit ALPN instead of rejecting h2. Refuse
			// those before any credentials or device token are transmitted.
			if conn.(*tls.Conn).ConnectionState().NegotiatedProtocol != "h2" {
				conn.Close()
				return nil, errors.New("push: APNs requires HTTP/2")
			}
			return conn, nil
		},
	}
}

func providerToken(cfg config, now time.Time) (string, error) {
	data, missing, err := readPrivate(cfg.KeyPath, maxConfigBytes)
	if err != nil || missing {
		return "", errors.New("push: cannot read private APNs .p8 key")
	}
	block, rest := pem.Decode(data)
	if !strings.HasPrefix(strings.TrimSpace(string(data)), "-----BEGIN PRIVATE KEY-----") ||
		block == nil || block.Type != "PRIVATE KEY" || len(block.Headers) != 0 || strings.TrimSpace(string(rest)) != "" {
		return "", errors.New("push: APNs key must be one PKCS#8 private-key PEM block")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return "", errors.New("push: invalid APNs PKCS#8 private key")
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok || key.Curve != elliptic.P256() {
		return "", errors.New("push: APNs key must use EC P-256")
	}
	header, _ := json.Marshal(struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}{"ES256", cfg.KeyID})
	claims, _ := json.Marshal(struct {
		Iss string `json:"iss"`
		Iat int64  `json:"iat"`
	}{cfg.TeamID, now.Unix()})
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(unsigned))
	r, s, err := ecdsa.Sign(rand.Reader, key, digest[:])
	if err != nil {
		return "", errors.New("push: cannot sign APNs provider token")
	}
	// JOSE ES256 uses fixed-width R || S, not an ASN.1 ECDSA signature.
	signature := make([]byte, 64)
	r.FillBytes(signature[:32])
	s.FillBytes(signature[32:])
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}
