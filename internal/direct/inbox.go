package direct

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"
)

type IncomingAttachment struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	MIME   string `json:"mime"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}
type Incoming struct {
	ID          string               `json:"transferId"`
	DeviceID    string               `json:"-"`
	Text        string               `json:"text,omitempty"`
	Attachments []IncomingAttachment `json:"attachments"`
	ExpiresAt   time.Time            `json:"expiresAt"`
	dir         string
	received    map[string]bool
	complete    bool
}
type Inbox struct {
	mu      sync.Mutex
	root    string
	base    string
	lock    *os.File
	pending map[string]*Incoming
	items   map[string]*Incoming
	closed  bool
	done    chan struct{}
	sweeper sync.WaitGroup
}

func NewInbox(root string) (*Inbox, error) {
	if err := rejectSymlinkComponents(root); err != nil {
		return nil, err
	}
	parent := filepath.Dir(root)
	if err := os.MkdirAll(parent, 0700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	st, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if sys, ok := st.Sys().(*syscall.Stat_t); !ok || sys.Uid != uint32(os.Getuid()) {
		return nil, errors.New("inbox directory owner is unsafe")
	}
	if st.Mode().Perm()&0077 != 0 {
		if err = os.Chmod(root, 0700); err != nil {
			return nil, err
		}
	}
	pst, err := os.Stat(parent)
	if err != nil {
		return nil, err
	}
	if ps, ok := pst.Sys().(*syscall.Stat_t); !ok || ps.Uid != uint32(os.Getuid()) {
		return nil, errors.New("inbox parent owner is unsafe")
	}
	if pst.Mode().Perm()&0077 != 0 {
		if err = os.Chmod(parent, 0700); err != nil {
			return nil, err
		}
	}
	lockPath := filepath.Join(root, "inbox.lock")
	fd, e := syscall.Open(lockPath, syscall.O_CREAT|syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0600)
	if e != nil {
		return nil, e
	}
	lock := os.NewFile(uintptr(fd), lockPath)
	lockInfo, e := lock.Stat()
	if e != nil || !lockInfo.Mode().IsRegular() || lockInfo.Mode().Perm() != 0600 {
		lock.Close()
		return nil, errors.New("inbox lock permissions are unsafe")
	}
	if sys, ok := lockInfo.Sys().(*syscall.Stat_t); !ok || sys.Uid != uint32(os.Getuid()) {
		lock.Close()
		return nil, errors.New("inbox lock owner is unsafe")
	}
	if e = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		lock.Close()
		return nil, errors.New("inbox is active in another process")
	}
	es, _ := os.ReadDir(root)
	for _, entry := range es {
		if strings.HasPrefix(entry.Name(), "session-") {
			_ = os.RemoveAll(filepath.Join(root, entry.Name()))
		}
	}
	base := root
	root, err = os.MkdirTemp(root, "session-")
	if err != nil {
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		_ = lock.Close()
		return nil, err
	}
	if err = os.Chmod(root, 0700); err != nil {
		_ = os.RemoveAll(root)
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		_ = lock.Close()
		return nil, err
	}
	b := &Inbox{root: root, base: base, lock: lock, pending: map[string]*Incoming{}, items: map[string]*Incoming{}, done: make(chan struct{})}
	b.sweeper.Add(1)
	go b.sweep()
	return b, nil
}
func (b *Inbox) sweep() {
	defer b.sweeper.Done()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-b.done:
			return
		case <-ticker.C:
			b.mu.Lock()
			b.expireLocked()
			b.mu.Unlock()
		}
	}
}
func (b *Inbox) Create(device, text string, as []IncomingAttachment) (*Incoming, error) {
	if len(as) > MaxItems || (text == "" && len(as) == 0) || len(text) > MaxTextBytes {
		return nil, errors.New("invalid incoming manifest")
	}
	if text != "" && !utf8.ValidString(text) {
		return nil, errors.New("invalid text")
	}
	seen := map[string]bool{}
	var total int64
	for _, a := range as {
		if !validItemID(a.ID) || seen[a.ID] || a.Size < 0 || a.Size > MaxItemBytes || len(a.SHA256) != 64 {
			return nil, errors.New("invalid attachment declaration")
		}
		seen[a.ID] = true
		if _, e := hex.DecodeString(a.SHA256); e != nil {
			return nil, errors.New("invalid attachment hash")
		}
		total += a.Size
	}
	if total > MaxTransferBytes {
		return nil, errors.New("attachments exceed transfer limit")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, errClosed
	}
	b.expireLocked()
	if len(b.items)+len(b.pending) >= 32 {
		return nil, errors.New("inbox is full")
	}
	disk, e := b.diskUsageLocked()
	if e != nil {
		return nil, e
	}
	if disk+total > MaxTransferBytes {
		return nil, errors.New("inbox storage is full")
	}
	id, e := randomToken(18)
	if e != nil {
		return nil, e
	}
	dir := filepath.Join(b.root, ".incoming-"+id)
	if e = os.Mkdir(dir, 0700); e != nil {
		return nil, e
	}
	x := &Incoming{ID: id, DeviceID: device, Text: text, Attachments: as, ExpiresAt: time.Now().Add(TransferTTL), dir: dir, received: map[string]bool{}}
	b.pending[id] = x
	return cloneIncoming(x), nil
}
func (b *Inbox) Put(id, item string, r io.Reader) error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return errors.New("inbox closed")
	}
	x := b.pending[id]
	if x == nil || !time.Now().Before(x.ExpiresAt) {
		b.mu.Unlock()
		return errors.New("incoming transfer expired")
	}
	var a *IncomingAttachment
	for i := range x.Attachments {
		if x.Attachments[i].ID == item {
			a = &x.Attachments[i]
			break
		}
	}
	if a == nil || x.received[item] {
		b.mu.Unlock()
		return errors.New("unknown or duplicate item")
	}
	decl := *a
	dir := x.dir
	b.mu.Unlock()
	f, e := os.OpenFile(filepath.Join(dir, item), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	h := sha256.New()
	n, e := io.Copy(io.MultiWriter(f, h), io.LimitReader(r, decl.Size+1))
	ce := f.Close()
	if e != nil || ce != nil || n != decl.Size || hex.EncodeToString(h.Sum(nil)) != decl.SHA256 {
		_ = os.Remove(f.Name())
		return errors.New("attachment size or hash mismatch")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.pending[id] != x {
		_ = os.Remove(f.Name())
		return errors.New("incoming transfer cancelled")
	}
	x.received[item] = true
	return nil
}
func (b *Inbox) Complete(id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	x := b.pending[id]
	if x == nil || !time.Now().Before(x.ExpiresAt) {
		return errors.New("incoming transfer expired")
	}
	if len(x.received) != len(x.Attachments) {
		return errors.New("incoming transfer is incomplete")
	}
	if len(b.items) >= 32 {
		return errors.New("inbox is full")
	}
	name := filepath.Join(b.root, ".item-"+id)
	if e := os.Rename(x.dir, name); e != nil {
		return e
	}
	x.dir = name
	x.complete = true
	delete(b.pending, id)
	x.ExpiresAt = time.Time{}
	b.items[id] = x
	return nil
}
func (b *Inbox) Items() []*Incoming {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.expireLocked()
	out := make([]*Incoming, 0, len(b.items))
	for _, x := range b.items {
		out = append(out, cloneIncoming(x))
	}
	return out
}
func (b *Inbox) Clear(id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	x := b.items[id]
	if x == nil {
		return os.ErrNotExist
	}
	delete(b.items, id)
	return os.RemoveAll(x.dir)
}
func (b *Inbox) Cancel(id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	x := b.pending[id]
	if x == nil {
		return os.ErrNotExist
	}
	delete(b.pending, id)
	return os.RemoveAll(x.dir)
}
func (b *Inbox) File(id, item string) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	x := b.items[id]
	if x == nil {
		return "", os.ErrNotExist
	}
	for _, a := range x.Attachments {
		if a.ID == item {
			return filepath.Join(x.dir, item), nil
		}
	}
	return "", os.ErrNotExist
}
func (b *Inbox) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	close(b.done)
	b.mu.Unlock()
	b.sweeper.Wait()
	b.mu.Lock()
	e := os.RemoveAll(b.root)
	b.mu.Unlock()
	_ = syscall.Flock(int(b.lock.Fd()), syscall.LOCK_UN)
	ce := b.lock.Close()
	if e != nil {
		return e
	}
	return ce
}

func (b *Inbox) expireLocked() {
	now := time.Now()
	for id, x := range b.pending {
		if !now.Before(x.ExpiresAt) {
			delete(b.pending, id)
			_ = os.RemoveAll(x.dir)
		}
	}

}
func (b *Inbox) diskUsageLocked() (int64, error) {
	var n int64
	for _, m := range []map[string]*Incoming{b.pending, b.items} {
		for _, x := range m {
			for _, a := range x.Attachments {
				n += a.Size
			}
		}
	}
	return n, nil
}
func cloneIncoming(x *Incoming) *Incoming {
	y := *x
	y.Attachments = append([]IncomingAttachment(nil), x.Attachments...)
	y.received = nil
	y.dir = ""
	return &y
}
func validItemID(s string) bool {
	if len(s) < 1 || len(s) > 128 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}
