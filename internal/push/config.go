package push

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

const (
	maxPairBytes    = 8192
	maxDevicesBytes = 1 << 20
	maxDevices      = 128
	maxConfigBytes  = 16384
)

type pairing struct {
	Version     int    `json:"version"`
	DeviceName  string `json:"deviceName"`
	DeviceToken string `json:"deviceToken"`
	BundleID    string `json:"bundleID"`
	Environment string `json:"environment"`
}

type config struct {
	KeyID   string `json:"keyID"`
	TeamID  string `json:"teamID"`
	KeyPath string `json:"keyPath"`
}

var pairingMu sync.Mutex

// Pair validates and atomically saves a native pairing under alias. Re-pairing
// replaces that alias's actual record; unrelated pairings are preserved.
func Pair(configDir, alias string, data []byte) error {
	if err := validateAlias(alias); err != nil {
		return err
	}
	var p pairing
	if len(data) > maxPairBytes || decodeStrict(data, &p) != nil {
		return errors.New("push: invalid pairing JSON")
	}
	if err := p.validate(); err != nil {
		return err
	}
	p.DeviceToken = strings.ToLower(p.DeviceToken)
	pairingMu.Lock()
	defer pairingMu.Unlock()
	if err := privateDir(configDir); err != nil {
		return err
	}
	devices, err := loadDevices(configDir)
	if err != nil {
		return err
	}
	if old, ok := findAlias(devices, alias); ok {
		// Preserve the spelling, and hence the collapse ID, when re-pairing.
		alias = old
	} else if len(devices) >= maxDevices {
		return errors.New("push: too many paired devices")
	}
	devices[alias] = p
	return saveDevices(configDir, devices)
}

// Unpair removes the actual pairing, without retaining an archive. An absent
// alias or devices.json is a successful no-op.
func Unpair(configDir, alias string) error {
	if err := validateAlias(alias); err != nil {
		return err
	}
	if strings.TrimSpace(configDir) == "" {
		return errors.New("push: config directory is required")
	}
	pairingMu.Lock()
	defer pairingMu.Unlock()
	devices, err := loadDevices(configDir)
	if err != nil {
		return err
	}
	key, ok := findAlias(devices, alias)
	if !ok {
		return nil
	}
	if err := privateDir(configDir); err != nil {
		return err
	}
	delete(devices, key)
	return saveDevices(configDir, devices)
}

func validateAlias(alias string) error {
	if alias == "" || len(alias) > 128 || !utf8.ValidString(alias) ||
		strings.TrimSpace(alias) != alias || alias == "." || alias == ".." {
		return errors.New("push: invalid alias")
	}
	for _, r := range alias {
		if !unicode.IsPrint(r) || r == '/' || r == '\\' {
			return errors.New("push: invalid alias")
		}
	}
	return nil
}

func (p pairing) validate() error {
	if p.Version != 1 {
		return errors.New("push: unsupported pairing version")
	}
	if p.DeviceName == "" || len(p.DeviceName) > 256 || !utf8.ValidString(p.DeviceName) ||
		strings.TrimSpace(p.DeviceName) != p.DeviceName {
		return errors.New("push: invalid pairing device name")
	}
	for _, r := range p.DeviceName {
		if !unicode.IsPrint(r) {
			return errors.New("push: invalid pairing device name")
		}
	}
	if len(p.DeviceToken) == 0 || len(p.DeviceToken) > 4096 || len(p.DeviceToken)%2 != 0 {
		return errors.New("push: invalid pairing device token")
	}
	if _, err := hex.DecodeString(p.DeviceToken); err != nil {
		return errors.New("push: invalid pairing device token")
	}
	if p.BundleID == "" || len(p.BundleID) > 255 || strings.HasPrefix(p.BundleID, ".") ||
		strings.HasSuffix(p.BundleID, ".") || strings.Contains(p.BundleID, "..") {
		return errors.New("push: invalid pairing bundle ID")
	}
	for _, c := range p.BundleID {
		if !asciiAlnum(c) && c != '.' && c != '-' {
			return errors.New("push: invalid pairing bundle ID")
		}
	}
	if p.Environment != "sandbox" && p.Environment != "production" {
		return errors.New("push: invalid pairing environment")
	}
	return nil
}

func asciiAlnum(c rune) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

func (c config) validate() error {
	for _, id := range []string{c.KeyID, c.TeamID} {
		if len(id) != 10 {
			return errors.New("push: keyID and teamID must be ten uppercase letters or digits")
		}
		for _, ch := range id {
			if !(ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9') {
				return errors.New("push: keyID and teamID must be ten uppercase letters or digits")
			}
		}
	}
	if !filepath.IsAbs(c.KeyPath) || len(c.KeyPath) > 4096 || filepath.Ext(c.KeyPath) != ".p8" || strings.ContainsRune(c.KeyPath, 0) {
		return errors.New("push: keyPath must be an absolute .p8 path")
	}
	return nil
}

func findAlias(devices map[string]pairing, alias string) (string, bool) {
	for key := range devices {
		if strings.EqualFold(key, alias) {
			return key, true
		}
	}
	return "", false
}

func loadDevices(dir string) (map[string]pairing, error) {
	data, missing, err := readPrivate(filepath.Join(dir, "devices.json"), maxDevicesBytes)
	if err != nil {
		return nil, errors.New("push: cannot read private devices.json")
	}
	devices := make(map[string]pairing)
	if missing {
		return devices, nil
	}
	if decodeStrict(data, &devices) != nil || devices == nil || len(devices) > maxDevices {
		return nil, errors.New("push: invalid devices.json")
	}
	seen := make(map[string]pairing)
	for alias, p := range devices {
		if validateAlias(alias) != nil || p.validate() != nil {
			return nil, errors.New("push: invalid devices.json entry")
		}
		if _, ok := findAlias(seen, alias); ok {
			return nil, errors.New("push: duplicate devices.json alias")
		}
		seen[alias] = p
	}
	return devices, nil
}

// readPrivate refuses special files and symlinks, bounds reads, and does not
// propagate filesystem errors (which can contain sensitive paths).
func readPrivate(path string, limit int64) ([]byte, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, true, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, false, errors.New("unsafe or unreadable private file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, false, errors.New("cannot open private file")
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) || !opened.Mode().IsRegular() || opened.Mode().Perm()&0077 != 0 {
		return nil, false, errors.New("private file changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, false, errors.New("cannot read bounded private file")
	}
	return data, false, nil
}

func privateDir(dir string) error {
	if strings.TrimSpace(dir) == "" {
		return errors.New("push: config directory is required")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return errors.New("push: cannot create config directory")
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("push: config directory must be a real directory")
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return errors.New("push: cannot secure config directory")
	}
	return nil
}

func saveDevices(dir string, devices map[string]pairing) error {
	data, err := json.MarshalIndent(devices, "", "  ")
	if err != nil || len(data)+1 > maxDevicesBytes {
		return errors.New("push: devices.json exceeds storage limit")
	}
	f, err := os.CreateTemp(dir, ".devices-*")
	if err != nil {
		return errors.New("push: cannot create pairing file")
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	defer f.Close()
	if err := f.Chmod(0600); err != nil {
		return errors.New("push: cannot secure pairing file")
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		return errors.New("push: cannot write pairing file")
	}
	if err := f.Sync(); err != nil {
		return errors.New("push: cannot sync pairing file")
	}
	if err := f.Close(); err != nil {
		return errors.New("push: cannot close pairing file")
	}
	if err := os.Rename(tmp, filepath.Join(dir, "devices.json")); err != nil {
		return errors.New("push: cannot replace devices.json")
	}
	d, err := os.Open(dir)
	if err != nil {
		return errors.New("push: cannot open pairing directory for sync")
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return errors.New("push: cannot sync pairing directory")
	}
	return nil
}

// encoding/json alone accepts duplicate keys, case-insensitive field names,
// and invalid UTF-8. Reject these before decoding a security-sensitive schema.
func decodeStrict(data []byte, dst any) error {
	if !utf8.Valid(data) {
		return errors.New("invalid JSON encoding")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	var schema string
	switch dst.(type) {
	case *pairing:
		schema = "pairing"
	case *config:
		schema = "config"
	case *map[string]pairing:
		schema = "devices"
	default:
		return errors.New("unsupported JSON schema")
	}
	if err := checkJSONObject(d, schema); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("trailing JSON data")
	}
	d = json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	return d.Decode(dst)
}

func checkJSONObject(d *json.Decoder, schema string) error {
	token, err := d.Token()
	if err != nil {
		return err
	}
	if token != json.Delim('{') {
		return errors.New("expected JSON object")
	}
	seen := make(map[string]bool)
	for d.More() {
		token, err := d.Token()
		if err != nil {
			return err
		}
		key, ok := token.(string)
		if !ok || seen[key] {
			return errors.New("duplicate or invalid JSON key")
		}
		seen[key] = true
		if schema == "devices" {
			if err := checkJSONObject(d, "pairing"); err != nil {
				return err
			}
			continue
		}
		allowed := false
		switch schema {
		case "pairing":
			allowed = key == "version" || key == "deviceName" || key == "deviceToken" || key == "bundleID" || key == "environment"
		case "config":
			allowed = key == "keyID" || key == "teamID" || key == "keyPath"
		}
		if !allowed {
			return errors.New("unknown JSON field")
		}
		value, err := d.Token()
		if err != nil {
			return err
		}
		if _, nested := value.(json.Delim); nested || value == nil {
			return errors.New("expected non-null JSON field")
		}
	}
	_, err = d.Token()
	return err
}
