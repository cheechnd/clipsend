package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"image"
	"image/png"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"clipsend/internal/direct"
)

func TestDecodeUIBodyEscapedTextAndStrictJSON(t *testing.T) {
	raw := `{"version":1,"text":"` + strings.Repeat(`\u0001`, 64000) + `","attachments":[]}`
	var q struct {
		Version     int    `json:"version"`
		Text        string `json:"text"`
		Attachments []any  `json:"attachments"`
	}
	r := httptest.NewRequest("POST", "/v1/incoming", strings.NewReader(raw))
	w := httptest.NewRecorder()
	if !decodeUIBody(w, r, &q) || len(q.Text) != 64000 {
		t.Fatalf("escaped boundary rejected: %d", len(q.Text))
	}
	for _, bad := range []string{`{"version":1,"version":1,"text":"x","attachments":[]}`, `{"version":1,"text":"x","attachments":[]} junk`} {
		r = httptest.NewRequest("POST", "/v1/incoming", strings.NewReader(bad))
		w = httptest.NewRecorder()
		if decodeUIBody(w, r, &q) {
			t.Fatalf("accepted malformed JSON %q", bad)
		}
	}
}

func previewTestInbox(t *testing.T, name, mime string, data []byte, complete bool) (*direct.Inbox, string) {
	t.Helper()
	b, err := direct.NewInbox(filepath.Join(t.TempDir(), "inbox"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	sum := sha256.Sum256(data)
	x, err := b.Create("device", "", []direct.IncomingAttachment{{ID: "item", Name: name, MIME: mime, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Put(x.ID, "item", bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	if complete {
		if err := b.Complete(x.ID); err != nil {
			t.Fatal(err)
		}
	}
	return b, x.ID
}

func previewTestOpener(t *testing.T, body string) string {
	t.Helper()
	bin := t.TempDir()
	log := filepath.Join(t.TempDir(), "opener-arguments")
	t.Setenv("PATH", bin)
	t.Setenv("CLIPSEND_OPENER_LOG", log)
	if err := os.WriteFile(filepath.Join(bin, "xdg-open"), []byte("#!/bin/sh\n"+body), 0700); err != nil {
		t.Fatal(err)
	}
	return log
}

func previewTestCache(t *testing.T) *uiPreviews {
	t.Helper()
	t.Setenv("TMPDIR", t.TempDir())
	p := newUIPreviews()
	t.Cleanup(p.close)
	return p
}

func TestOpenInboxStagesPrivateTypedCopyAndReusesUntilExpiry(t *testing.T) {
	p := previewTestCache(t)
	log := previewTestOpener(t, "printf '%s\\n' \"$#\" \"$@\" >> \"$CLIPSEND_OPENER_LOG\"\n")
	var pngData bytes.Buffer
	if err := png.Encode(&pngData, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	b, id := previewTestInbox(t, "../../$(touch hacked);evil.desktop", "application/x-desktop", pngData.Bytes(), true)
	for i := range 2 {
		if err := p.openInbox(b, id, "item"); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			past := time.Now().Add(-uiPreviewRetention + time.Hour)
			if err := os.Chtimes(filepath.Dir(p.files[[2]string{id, "item"}]), past, past); err != nil {
				t.Fatal(err)
			}
		}
	}
	args, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(args)), "\n")
	if len(lines) != 4 || lines[0] != "1" || lines[2] != "1" || lines[1] != lines[3] {
		t.Fatalf("wrong/repeated opener arguments: %q", args)
	}
	preview := lines[1]
	if !filepath.IsAbs(preview) || filepath.Base(preview) != "attachment.png" {
		t.Fatalf("sender influenced preview name: %q", preview)
	}
	for path, mode := range map[string]os.FileMode{preview: 0600, filepath.Dir(preview): 0700} {
		st, err := os.Stat(path)
		if err != nil || st.Mode().Perm() != mode {
			t.Fatalf("unsafe preview permissions: %s %v", path, err)
		}
	}
	if err := b.Clear(id); err != nil {
		t.Fatal(err)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(preview)
	if err != nil || !bytes.Equal(data, pngData.Bytes()) {
		t.Fatal("clear/close removed or changed opened bytes")
	}
	if err := p.openInbox(b, id, "item"); err == nil {
		t.Fatal("cached preview bypassed inbox authorization")
	}
	p.cleanup(time.Now().Add(uiPreviewRetention - time.Minute))
	if _, err := os.Stat(preview); err != nil {
		t.Fatal("recent preview expired early")
	}
	p.cleanup(time.Now().Add(uiPreviewRetention + time.Minute))
	if _, err := os.Stat(preview); !os.IsNotExist(err) || len(p.files) != 0 {
		t.Fatal("expired preview/cache entry retained")
	}
}

func TestOpenInboxRejectsUnsafeContentAndUntrustedIDs(t *testing.T) {
	p := previewTestCache(t)
	log := previewTestOpener(t, "printf called >> \"$CLIPSEND_OPENER_LOG\"\n")
	for _, data := range [][]byte{
		[]byte("\x7fELF\x02\x01\x01\x00executable"), []byte("MZ\x00\x00executable"),
		[]byte("#!/bin/sh\ntouch /tmp/unsafe\n"),
		[]byte("[Desktop Entry]\nType=Application\nExec=touch /tmp/unsafe\n"),
		[]byte(strings.Repeat(" ", 600) + "[Desktop Entry]\nExec=unsafe"),
		[]byte("<html><script>alert(1)</script></html>"), []byte("<?xml version=\"1.0\"?><svg/>"),
		[]byte("<svg xmlns=\"http://www.w3.org/2000/svg\" onload=\"alert(1)\"/>"),
		append([]byte(strings.Repeat("a", 600)), 0), []byte("\x00\x01\x02"),
	} {
		b, id := previewTestInbox(t, "photo.png", "image/png", data, true)
		if err := p.openInbox(b, id, "item"); err == nil {
			t.Fatalf("opened disguised payload %q", data)
		}
	}
	b, id := previewTestInbox(t, "good.txt", "text/plain", []byte("hello"), false)
	for _, ids := range [][2]string{{id, "item"}, {"/etc/passwd", "item"}, {id, "../../etc/passwd"}, {id, "/etc/passwd"}, {id, ""}} {
		if err := p.openInbox(b, ids[0], ids[1]); err == nil {
			t.Fatalf("opened incomplete/arbitrary path: %q", ids)
		}
	}
	if err := p.openInbox(nil, id, "item"); err == nil {
		t.Fatal("opened without inbox")
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatal("unsafe request reached opener")
	}
}

func TestOpenInboxFailureCleansPreviewAndRejectsSymlink(t *testing.T) {
	p := previewTestCache(t)
	previewTestOpener(t, "exit 3\n")
	b, id := previewTestInbox(t, "note.txt", "text/plain", []byte("hello"), true)
	if err := p.openInbox(b, id, "item"); err == nil {
		t.Fatal("opener failure reported success")
	}
	entries, _ := os.ReadDir(os.TempDir())
	if len(entries) != 0 {
		t.Fatal("failed open retained a preview")
	}
	t.Setenv("PATH", t.TempDir())
	if err := p.openInbox(b, id, "item"); err == nil {
		t.Fatal("missing opener reported success")
	}
	original, _ := b.File(id, "item")
	if err := os.Remove(original); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", original); err != nil {
		t.Fatal(err)
	}
	if err := p.openInbox(b, id, "item"); err == nil {
		t.Fatal("followed symlink")
	}
}

func testFTYP(major string, compatible ...string) []byte {
	data := make([]byte, 16+4*len(compatible))
	binary.BigEndian.PutUint32(data[:4], uint32(len(data)))
	copy(data[4:8], "ftyp")
	copy(data[8:12], major)
	for i, brand := range compatible {
		copy(data[16+i*4:], brand)
	}
	return data
}

func TestInboxHEICContentDetectionAndMetadata(t *testing.T) {
	for _, tc := range []struct{ major, compatible, mime, suffix string }{
		{"heic", "mif1", "image/heic", ".heic"},
		{"mif1", "heic", "image/heic", ".heic"},
		{"mif1", "heix", "image/heic", ".heic"},
		{"mif1", "mif1", "image/heif", ".heif"},
		{"hevc", "msf1", "image/heic-sequence", ".heics"},
		{"avif", "mif1", "image/avif", ".avif"},
	} {
		data := testFTYP(tc.major, tc.compatible)
		mime, suffix := inboxPreviewType(data)
		if mime != tc.mime || suffix != tc.suffix {
			t.Fatalf("%s/%s: %s %s", tc.major, tc.compatible, mime, suffix)
		}
	}
	for _, data := range [][]byte{testFTYP("mp42"), testFTYP("heic")[:15], append([]byte{0, 0, 0, 100}, testFTYP("heic")[4:]...)} {
		if mime, _ := inboxHEIFType(data); mime != "" {
			t.Fatal("misidentified unrelated or truncated ftyp")
		}
	}
	p := previewTestCache(t)
	log := previewTestOpener(t, "printf '%s' \"$1\" > \"$CLIPSEND_OPENER_LOG\"\n")
	data := testFTYP("mif1", "heic")
	b, id := previewTestInbox(t, "Clipboard.heic", "application/octet-stream", data, true)
	x := b.Items()[0]
	if got := inboxAttachments(b, x)[0].MIME; got != "image/heic" {
		t.Fatalf("incoming HEIC remained %s", got)
	}
	if x.Attachments[0].MIME != "application/octet-stream" {
		t.Fatal("view mutated source manifest")
	}
	if err := p.openInbox(b, id, "item"); err != nil {
		t.Fatal(err)
	}
	opened, _ := os.ReadFile(log)
	if filepath.Ext(string(opened)) != ".heic" {
		t.Fatal("HEIC preview has wrong suffix")
	}
}

func TestUIPreviewStartupCleanupRetainsRecentAndIgnoresSymlinks(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	old, _ := os.MkdirTemp("", "clipsend-preview-")
	recent, _ := os.MkdirTemp("", "clipsend-preview-")
	unrelated, _ := os.MkdirTemp("", "other-")
	past := time.Now().Add(-uiPreviewRetention - time.Minute)
	for _, dir := range []string{old, unrelated} {
		if err := os.Chtimes(dir, past, past); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(os.TempDir(), "clipsend-preview-link")
	if err := os.Symlink(unrelated, link); err != nil {
		t.Fatal(err)
	}
	p := newUIPreviews()
	defer p.close()
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("startup retained expired preview")
	}
	for _, dir := range []string{recent, unrelated, link} {
		if _, err := os.Lstat(dir); err != nil {
			t.Fatalf("cleanup removed recent/unrelated path: %s", dir)
		}
	}
}
