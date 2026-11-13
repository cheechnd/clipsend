package direct

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestReadAliasesDoesNotInitializeOrLockStore(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "store")
	if _, err := ReadAliases(dir); !os.IsNotExist(err) {
		t.Fatalf("missing store: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("read-only lookup created directory")
	}
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, alias := range []string{"iphone", "ipad"} {
		inv, err := store.NewInvitation()
		if err != nil {
			t.Fatal(err)
		}
		_, err = store.ApproveInvitation(inv.Value, alias, true, PairRequest{DeviceName: "Fixture", DeviceToken: "aabb", BundleID: "dev.example.test", Environment: "sandbox"})
		if err != nil {
			t.Fatal(err)
		}
	}
	// Completion works without the normal store opener's lock file and does not
	// recreate it. Only the atomically replaced data file is needed.
	if err := os.Remove(filepath.Join(dir, "store.lock")); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(filepath.Join(dir, "store.json"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := ReadAliases(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"ipad", "iphone"}) {
		t.Fatalf("aliases: %q", got)
	}
	after, _ := os.Stat(filepath.Join(dir, "store.json"))
	if !after.ModTime().Equal(before.ModTime()) || after.Mode() != before.Mode() {
		t.Fatal("lookup changed pairing state")
	}
	if _, err := os.Stat(filepath.Join(dir, "store.lock")); !os.IsNotExist(err) {
		t.Fatal("lookup recreated lock")
	}
}
