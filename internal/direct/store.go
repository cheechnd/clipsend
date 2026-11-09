// Package direct implements the native, pinned-TLS clipboard transfer protocol.
package direct

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"
)

const PairingTTL = 5 * time.Minute
const TransferTTL = 120 * time.Second
const MaxTextBytes = 64000
const maxStoreBytes int64 = 16 << 20

var errClosed = errors.New("direct store closed")

type Identity struct {
	ComputerID        string `json:"computerID"`
	CertificateSHA256 string `json:"certificateSHA256"`
	CertificatePEM    string `json:"certificatePEM"`
	PrivateKeyPEM     string `json:"privateKeyPEM"`
}
type Device struct {
	ID          string    `json:"deviceID"`
	Alias       string    `json:"alias"`
	Name        string    `json:"deviceName"`
	Secret      string    `json:"secret"`
	DeviceToken string    `json:"deviceToken,omitempty"`
	BundleID    string    `json:"bundleID,omitempty"`
	Environment string    `json:"environment,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
}
type state struct {
	Version  int               `json:"version"`
	Identity Identity          `json:"identity"`
	Devices  map[string]Device `json:"devices"`
}
type Invitation struct {
	Value     string    `json:"invitation"`
	ExpiresAt time.Time `json:"expiresAt"`
}
type inviteState struct {
	expires time.Time
	used    bool
}
type Store struct {
	dir, dataPath, lockPath string
	mu                      sync.Mutex
	invites                 map[string]*inviteState
	sessions                map[string]*Session
	closed                  bool
}

func OpenStore(dir string) (*Store, error) {
	if dir == "" {
		return nil, errors.New("empty store directory")
	}
	if err := rejectSymlinkComponents(dir); err != nil {
		return nil, err
	}
	di, err := os.Lstat(dir)
	createdDir := os.IsNotExist(err)
	if createdDir {
		if err = os.MkdirAll(dir, 0700); err != nil {
			return nil, err
		}
		di, err = os.Lstat(dir)
	}
	if err != nil {
		return nil, err
	}
	if !di.IsDir() || di.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("store path must be a real directory")
	}
	if err = rejectSymlinkComponents(dir); err != nil {
		return nil, err
	}
	if di.Mode().Perm() != 0700 {
		return nil, errors.New("direct store directory permissions are unsafe; repair with chmod 700")
	}
	s := &Store{dir: dir, dataPath: filepath.Join(dir, "store.json"), lockPath: filepath.Join(dir, "store.lock"), invites: map[string]*inviteState{}, sessions: map[string]*Session{}}
	_, stErr := os.Lstat(s.dataPath)
	initiallyExists := stErr == nil
	if stErr != nil && !os.IsNotExist(stErr) {
		return nil, stErr
	}
	flags := syscall.O_RDWR
	if !initiallyExists {
		flags |= syscall.O_CREAT
	}
	lf, err := openPrivate(s.lockPath, flags, 0600)
	if err != nil {
		return nil, err
	}
	defer lf.Close()
	if err = syscall.Flock(int(lf.Fd()), syscall.LOCK_EX); err != nil {
		return nil, err
	}
	defer syscall.Flock(int(lf.Fd()), syscall.LOCK_UN)
	b, err := readPrivate(s.dataPath, maxStoreBytes)
	if os.IsNotExist(err) {
		if initiallyExists {
			return nil, errors.New("direct store disappeared while opening")
		}
		id, e := randomToken(24)
		if e != nil {
			return nil, e
		}
		cert, key, fpr, e := makeIdentity()
		if e != nil {
			return nil, e
		}
		st := state{Version: 1, Identity: Identity{ComputerID: id, CertificateSHA256: fpr, CertificatePEM: cert, PrivateKeyPEM: key}, Devices: map[string]Device{}}
		if e = writeState(s.dir, s.dataPath, st); e != nil {
			return nil, e
		}
	} else if err != nil {
		return nil, err
	} else {
		var st state
		if err = json.Unmarshal(b, &st); err != nil {
			return nil, fmt.Errorf("read direct store: %w", err)
		}
		if err = validateState(st); err != nil {
			return nil, err
		}
	}
	return s, nil
}
func rejectSymlinkComponents(path string) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	vol := filepath.VolumeName(absolute)
	rest := strings.TrimPrefix(absolute, vol)
	current := vol + string(os.PathSeparator)
	for _, part := range strings.Split(strings.TrimPrefix(rest, string(os.PathSeparator)), string(os.PathSeparator)) {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		fi, e := os.Lstat(current)
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			return e
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return errors.New("direct store path cannot contain symlinks")
		}
	}
	return nil
}
func validateState(st state) error {
	if st.Version != 1 {
		return errors.New("unsupported or corrupt direct store version")
	}
	if err := validateIdentity(st.Identity); err != nil {
		return err
	}
	if st.Devices == nil {
		return errors.New("corrupt direct store: missing devices")
	}
	return validateDevices(st.Devices)
}
func writeState(dir, path string, st state) error {
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	if int64(len(data)) > maxStoreBytes {
		return errors.New("direct store exceeds size limit")
	}
	tmp, err := os.CreateTemp(dir, ".store-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err = tmp.Write(data); err == nil {
		err = tmp.Sync()
	}
	ce := tmp.Close()
	if err == nil {
		err = ce
	}
	if err != nil {
		return err
	}
	if err = os.Rename(name, path); err != nil {
		return err
	}
	df, err := os.Open(dir)
	if err == nil {
		err = df.Sync()
		df.Close()
	}
	return err
}
func (s *Store) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	list := make([]*Session, 0, len(s.sessions))
	for _, x := range s.sessions {
		list = append(list, x)
	}
	s.mu.Unlock()
	for _, x := range list {
		x.finish(Cancelled)
	}
	return nil
}
func (s *Store) Identity() Identity {
	st, e := s.read()
	if e != nil {
		return Identity{}
	}
	return st.Identity
}

// Snapshot reads identity and devices from one consistent store revision.
func (s *Store) Snapshot() (Identity, []Device, error) {
	st, err := s.read()
	if err != nil {
		return Identity{}, nil, err
	}
	out := make([]Device, 0, len(st.Devices))
	for _, d := range st.Devices {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return st.Identity, out, nil
}
func (s *Store) Devices() []Device {
	st, e := s.read()
	if e != nil {
		return nil
	}
	out := make([]Device, 0, len(st.Devices))
	for _, d := range st.Devices {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
func (s *Store) Device(id string) (Device, bool) {
	st, e := s.read()
	if e != nil {
		return Device{}, false
	}
	d, ok := st.Devices[id]
	return d, ok
}

// FindDevice returns the device whose user-selected alias matches alias.
func (s *Store) FindDevice(alias string) (Device, bool) {
	for _, d := range s.Devices() {
		if d.Alias == alias {
			return d, true
		}
	}
	return Device{}, false
}

// RenameDevice atomically changes a paired device's local alias.
func (s *Store) RenameDevice(id, alias string) error {
	alias = strings.TrimSpace(alias)
	if alias == "" || len(alias) > 128 {
		return errors.New("empty alias")
	}
	return s.transaction(func(st *state) error {
		d, ok := st.Devices[id]
		if !ok {
			return os.ErrNotExist
		}
		if aliasInUse(st.Devices, alias, id) {
			return errors.New("alias already in use")
		}
		d.Alias = alias
		st.Devices[id] = d
		return nil
	})
}
func (s *Store) NewInvitation() (Invitation, error) {
	v, e := randomToken(32)
	if e != nil {
		return Invitation{}, e
	}
	i := Invitation{Value: v, ExpiresAt: time.Now().UTC().Add(PairingTTL)}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Invitation{}, errClosed
	}
	for token, old := range s.invites {
		if old.used || !time.Now().Before(old.expires) {
			delete(s.invites, token)
		}
	}
	s.invites[v] = &inviteState{expires: i.ExpiresAt}
	return i, nil
}
func (s *Store) consumeInvitation(v string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	x := s.invites[v]
	if x == nil || x.used || !time.Now().Before(x.expires) {
		delete(s.invites, v)
		return errors.New("invalid invitation")
	}
	x.used = true
	return nil
}
func (s *Store) invitationDeadline(v string) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	if x := s.invites[v]; x != nil {
		return x.expires
	}
	return time.Time{}
}
func (s *Store) ApproveInvitation(v, alias string, approve bool, req PairRequest) (Device, error) {
	if err := s.consumeInvitation(v); err != nil {
		return Device{}, err
	}
	if !approve {
		return Device{}, errors.New("pairing denied")
	}
	return s.addApproved(alias, req)
}
func (s *Store) addApproved(alias string, req PairRequest) (Device, error) {
	alias = strings.TrimSpace(alias)
	if alias == "" || len(alias) > 128 || !validPairFields(req) {
		return Device{}, errors.New("empty alias")
	}
	id, e := randomToken(18)
	if e != nil {
		return Device{}, e
	}
	secret, e := randomToken(32)
	if e != nil {
		return Device{}, e
	}
	d := Device{ID: id, Alias: alias, Name: req.DeviceName, Secret: secret, DeviceToken: req.DeviceToken, BundleID: req.BundleID, Environment: req.Environment, CreatedAt: time.Now().UTC()}
	e = s.transaction(func(st *state) error {
		if aliasInUse(st.Devices, alias, "") {
			return errors.New("alias already in use")
		}
		st.Devices[id] = d
		return nil
	})
	return d, e
}
func (s *Store) RevokeDevice(id string) error {
	return s.transaction(func(st *state) error {
		if _, ok := st.Devices[id]; !ok {
			return os.ErrNotExist
		}
		delete(st.Devices, id)
		return nil
	})
}
func (s *Store) updateToken(id, token, bundle, env string) error {
	if !validDeviceToken(token) || !bundleIDPattern.MatchString(bundle) || (env != "sandbox" && env != "production") {
		return errors.New("invalid device routing")
	}
	return s.transaction(func(st *state) error {
		d, ok := st.Devices[id]
		if !ok {
			return os.ErrNotExist
		}
		d.DeviceToken = token
		d.BundleID = bundle
		d.Environment = env
		st.Devices[id] = d
		return nil
	})
}
func (s *Store) read() (*state, error) {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return nil, errClosed
	}
	lf, err := openPrivate(s.lockPath, syscall.O_RDONLY, 0600)
	if err != nil {
		return nil, err
	}
	defer lf.Close()
	if err = syscall.Flock(int(lf.Fd()), syscall.LOCK_SH); err != nil {
		return nil, err
	}
	defer syscall.Flock(int(lf.Fd()), syscall.LOCK_UN)
	b, err := readPrivate(s.dataPath, maxStoreBytes)
	if err != nil {
		return nil, err
	}
	var st state
	if err = json.Unmarshal(b, &st); err != nil {
		return nil, fmt.Errorf("read direct store: %w", err)
	}
	if err = validateState(st); err != nil {
		return nil, err
	}
	return &st, nil
}
func (s *Store) transaction(fn func(*state) error) error {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return errClosed
	}
	lf, err := openPrivate(s.lockPath, syscall.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lf.Close()
	if err = syscall.Flock(int(lf.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lf.Fd()), syscall.LOCK_UN)
	b, err := readPrivate(s.dataPath, maxStoreBytes)
	if err != nil {
		return err
	}
	var st state
	if err = json.Unmarshal(b, &st); err != nil {
		return fmt.Errorf("read direct store: %w", err)
	}
	if err = validateState(st); err != nil {
		return err
	}
	if err = fn(&st); err != nil {
		return err
	}
	if err = validateState(st); err != nil {
		return err
	}
	return writeState(s.dir, s.dataPath, st)
}
func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func openPrivate(path string, flags int, mode uint32) (*os.File, error) {
	fd, err := syscall.Open(path, flags|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, mode)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if !st.Mode().IsRegular() {
		f.Close()
		return nil, errors.New("private state path must be a regular file")
	}
	if st.Mode().Perm() != os.FileMode(mode) {
		f.Close()
		return nil, errors.New("direct store file permissions are unsafe; repair with chmod 600")
	}
	return f, nil
}
func readPrivate(path string, limit int64) ([]byte, error) {
	f, err := openPrivate(path, syscall.O_RDONLY, 0600)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, errors.New("direct store exceeds size limit")
	}
	return b, nil
}

var bundleIDPattern = regexp.MustCompile(`^[A-Za-z0-9.-]{1,255}$`)

func validPairFields(q PairRequest) bool {
	if strings.TrimSpace(q.DeviceName) == "" || len(q.DeviceName) > 128 || !utf8.ValidString(q.DeviceName) || hasControl(q.DeviceName) || !bundleIDPattern.MatchString(q.BundleID) || (q.Environment != "sandbox" && q.Environment != "production") {
		return false
	}
	return validDeviceToken(q.DeviceToken)
}
func hasControl(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}
func validDeviceToken(token string) bool {
	if len(token) == 0 || len(token) > 4096 || len(token)%2 != 0 {
		return false
	}
	_, err := hex.DecodeString(token)
	return err == nil
}
func validRandom(v string, n int) bool {
	b, e := base64.RawURLEncoding.DecodeString(v)
	return e == nil && len(b) == n
}
func validateIdentity(i Identity) error {
	if !validRandom(i.ComputerID, 24) {
		return errors.New("invalid direct store computer ID")
	}
	pair, err := tls.X509KeyPair([]byte(i.CertificatePEM), []byte(i.PrivateKeyPEM))
	if err != nil || len(pair.Certificate) == 0 {
		return errors.New("invalid direct store certificate identity")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil || leaf.CheckSignature(leaf.SignatureAlgorithm, leaf.RawTBSCertificate, leaf.Signature) != nil {
		return errors.New("direct store certificate is not self-signed")
	}
	sum := sha256.Sum256(pair.Certificate[0])
	if i.CertificateSHA256 != hex.EncodeToString(sum[:]) {
		return errors.New("direct store certificate fingerprint mismatch")
	}
	return nil
}
func aliasInUse(ds map[string]Device, alias, except string) bool {
	for id, d := range ds {
		if id != except && strings.EqualFold(strings.TrimSpace(d.Alias), strings.TrimSpace(alias)) {
			return true
		}
	}
	return false
}
func duplicateAliases(ds map[string]Device, except string) bool {
	seen := map[string]bool{}
	for id, d := range ds {
		if id == except {
			continue
		}
		k := strings.ToLower(strings.TrimSpace(d.Alias))
		if seen[k] {
			return true
		}
		seen[k] = true
	}
	return false
}
func validateDevices(ds map[string]Device) error {
	for id, d := range ds {
		if id != d.ID || !validRandom(d.ID, 18) || !validRandom(d.Secret, 32) || strings.TrimSpace(d.Alias) == "" || len(d.Alias) > 128 || !utf8.ValidString(d.Alias) || hasControl(d.Alias) || d.CreatedAt.IsZero() || !validPairFields(PairRequest{DeviceName: d.Name, DeviceToken: d.DeviceToken, BundleID: d.BundleID, Environment: d.Environment}) {
			return errors.New("corrupt direct store device record")
		}
	}
	if duplicateAliases(ds, "") {
		return errors.New("corrupt direct store: duplicate aliases")
	}
	return nil
}
func makeIdentity() (string, string, string, error) {
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		return "", "", "", e
	}
	serial, e := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if e != nil {
		return "", "", "", e
	}
	tmpl := x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "ClipSend Native"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().AddDate(10, 0, 0), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, BasicConstraintsValid: true}
	der, e := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if e != nil {
		return "", "", "", e
	}
	k, e := x509.MarshalPKCS8PrivateKey(key)
	if e != nil {
		return "", "", "", e
	}
	sum := sha256.Sum256(der)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: k})), hex.EncodeToString(sum[:]), nil
}
