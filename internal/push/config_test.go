package push

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func testPairing() pairing {
	return pairing{1, "Private phone name", "a1b2c3d4", "dev.cheechnd.clipsend", "sandbox"}
}

func pairingJSON(t *testing.T, p pairing) []byte {
	t.Helper()
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func writePrivate(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestPairUnpairAtomicAndPrivate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "new", "push")
	p := testPairing()
	if err := Pair(dir, "iPhone", pairingJSON(t, p)); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]os.FileMode{dir: 0700, filepath.Join(dir, "devices.json"): 0600} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != want {
			t.Fatalf("private mode: %v, %v", info, err)
		}
	}
	old, err := os.Open(filepath.Join(dir, "devices.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	before, _ := io.ReadAll(old)
	p.DeviceToken = "DEADBEEF"
	if err := Pair(dir, "IPHONE", pairingJSON(t, p)); err != nil {
		t.Fatal(err)
	}
	if _, err := old.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	stillOld, _ := io.ReadAll(old)
	if !bytes.Equal(before, stillOld) {
		t.Fatal("pairing overwrote the old inode instead of atomically replacing it")
	}
	devices, err := loadDevices(dir)
	if err != nil || len(devices) != 1 || devices["iPhone"].DeviceToken != "deadbeef" {
		t.Fatalf("replacement: %#v, %v", devices, err)
	}
	if err := Pair(dir, "iPad", pairingJSON(t, testPairing())); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := Unpair(dir, "iphone"); err != nil {
		t.Fatal(err)
	}
	devices, err = loadDevices(dir)
	if err != nil || len(devices) != 1 || devices["iPad"].Version != 1 {
		t.Fatalf("unpair: %#v, %v", devices, err)
	}
	if info, _ := os.Stat(dir); info.Mode().Perm() != 0700 {
		t.Fatal("unpair did not secure the directory")
	}
	if err := Unpair(dir, "iPad"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "devices.json"))
	if strings.TrimSpace(string(data)) != "{}" {
		t.Fatal("unpair retained pairing metadata")
	}
	for i := 0; i < 2; i++ {
		if err := Unpair(dir, "absent"); err != nil {
			t.Fatal(err)
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || entries[0].Name() != "devices.json" {
		t.Fatalf("unexpected backup or temporary files: %v", entries)
	}
}

func TestPairValidatesBeforeWriting(t *testing.T) {
	valid := string(pairingJSON(t, testPairing()))
	cases := map[string]string{
		"bad version":         strings.Replace(valid, `"version":1`, `"version":2`, 1),
		"missing version":     strings.Replace(valid, `"version":1,`, "", 1),
		"empty name":          strings.Replace(valid, "Private phone name", "", 1),
		"long name":           strings.Replace(valid, "Private phone name", strings.Repeat("x", 257), 1),
		"control name":        strings.Replace(valid, "Private phone name", `hello\nprivate`, 1),
		"empty token":         strings.Replace(valid, "a1b2c3d4", "", 1),
		"odd token":           strings.Replace(valid, "a1b2c3d4", "abc", 1),
		"nonhex token":        strings.Replace(valid, "a1b2c3d4", "aaxx", 1),
		"long token":          strings.Replace(valid, "a1b2c3d4", strings.Repeat("ab", 2049), 1),
		"url environment":     strings.Replace(valid, "sandbox", "https://attacker.invalid", 1),
		"unknown environment": strings.Replace(valid, "sandbox", "development", 1),
		"header injection":    strings.Replace(valid, "dev.cheechnd.clipsend", `app\r\nX-Token: secret`, 1),
		"empty bundle":        strings.Replace(valid, "dev.cheechnd.clipsend", "", 1),
		"double dot":          strings.Replace(valid, "dev.cheechnd.clipsend", "dev..app", 1),
		"unknown field":       strings.TrimSuffix(valid, "}") + `,"url":"secret"}`,
		"duplicate field":     strings.TrimSuffix(valid, "}") + `,"version":1}`,
		"wrong case":          strings.Replace(valid, "deviceToken", "DeviceToken", 1),
		"null field":          strings.Replace(valid, `"version":1`, `"version":null`, 1),
		"trailing":            valid + `{}`,
		"null":                `null`,
		"array":               `[]`,
		"deep object":         strings.Repeat(`{"x":`, 10000) + `1` + strings.Repeat(`}`, 10000),
		"oversized":           strings.Repeat(" ", maxPairBytes+1),
		"invalid utf8":        string([]byte{'{', '"', 0xff, '"', ':', '1', '}'}),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "must-not-exist")
			if err := Pair(dir, "phone", []byte(data)); err == nil {
				t.Fatal("invalid pairing accepted")
			}
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Fatal("invalid pairing created configuration")
			}
		})
	}
	for _, alias := range []string{"", " ", " phone", "phone ", "../phone", `a\b`, ".", "..", "phone\n", "a\u202eb", strings.Repeat("a", 129), string([]byte{0xff})} {
		if err := Pair(t.TempDir(), alias, []byte(valid)); err == nil {
			t.Fatalf("invalid alias %q accepted", alias)
		}
		if err := Unpair(t.TempDir(), alias); err == nil {
			t.Fatalf("invalid unpair alias %q accepted", alias)
		}
	}
	if err := Pair("", "phone", []byte(valid)); err == nil {
		t.Fatal("empty directory accepted")
	}
}

func TestTokenLengthsAreNotFixed(t *testing.T) {
	for _, size := range []int{2, 8, 64, 512, 4096} {
		p := testPairing()
		p.DeviceToken = strings.Repeat("A", size)
		p.Environment = "production"
		if err := Pair(t.TempDir(), "Téléphone", pairingJSON(t, p)); err != nil {
			t.Fatalf("token length %d: %v", size, err)
		}
	}
}

func TestInvalidExistingRegistryIsPreserved(t *testing.T) {
	p := string(pairingJSON(t, testPairing()))
	for name, content := range map[string]string{
		"malformed":       `secret-invalid-json`,
		"null":            `null`,
		"invalid entry":   `{"other":{}}`,
		"duplicate alias": `{"phone":` + p + `,"phone":` + p + `}`,
		"folded alias":    `{"phone":` + p + `,"PHONE":` + p + `}`,
		"invalid alias":   `{"../secret":` + p + `}`,
		"oversized":       strings.Repeat(" ", maxDevicesBytes+1),
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "devices.json")
			writePrivate(t, path, []byte(content))
			if err := Pair(dir, "new", []byte(p)); err == nil {
				t.Fatal("pair ignored invalid registry")
			}
			if err := Unpair(dir, "other"); err == nil {
				t.Fatal("unpair ignored invalid registry")
			}
			after, _ := os.ReadFile(path)
			if string(after) != content {
				t.Fatal("failed update altered registry")
			}
			entries, _ := os.ReadDir(dir)
			if len(entries) != 1 {
				t.Fatal("failed update left temporary files")
			}
		})
	}
}

func TestPrivateFilesAndDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "devices.json")
	writePrivate(t, path, []byte(`{}`))
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if err := Pair(dir, "phone", pairingJSON(t, testPairing())); err == nil {
		t.Fatal("public registry accepted")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	writePrivate(t, outside, []byte(`{}`))
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	if err := Pair(dir, "phone", pairingJSON(t, testPairing())); err == nil {
		t.Fatal("symlink registry accepted")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := Pair(dir, "phone", pairingJSON(t, testPairing())); err == nil {
		t.Fatal("non-file registry accepted")
	}
	link := filepath.Join(t.TempDir(), "linked-config")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Fatal(err)
	}
	if err := Pair(link, "phone", pairingJSON(t, testPairing())); err == nil {
		t.Fatal("symlink config directory accepted")
	}
	absent := filepath.Join(t.TempDir(), "absent")
	if err := Unpair(absent, "phone"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(absent); !os.IsNotExist(err) {
		t.Fatal("no-op unpair created directory")
	}
}

func TestConcurrentPairUpdatesAndReaders(t *testing.T) {
	dir := t.TempDir()
	data := pairingJSON(t, testPairing())
	if err := Pair(dir, "initial", data); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := Pair(dir, fmt.Sprintf("phone-%d", i), data); err != nil {
				t.Error(err)
			}
			content, err := os.ReadFile(filepath.Join(dir, "devices.json"))
			var devices map[string]pairing
			if err != nil || json.Unmarshal(content, &devices) != nil {
				t.Error("reader observed an incomplete atomic update")
			}
		}(i)
	}
	wg.Wait()
	devices, err := loadDevices(dir)
	if err != nil || len(devices) != 21 {
		t.Fatalf("lost concurrent update: %d, %v", len(devices), err)
	}
}

func TestFailedAtomicReplacementCleansTempFile(t *testing.T) {
	dir := t.TempDir()
	// A directory at the target forces rename to fail after the temporary
	// file has been written and synced, without relying on user privileges.
	target := filepath.Join(dir, "devices.json")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	if err := saveDevices(dir, map[string]pairing{"phone": testPairing()}); err == nil {
		t.Fatal("replacement unexpectedly succeeded")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "devices.json" || !entries[0].IsDir() {
		t.Fatalf("failed replacement altered target or left a temporary file: %v, %v", entries, err)
	}
}
