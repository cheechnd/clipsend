package direct

import (
	"path/filepath"
	"testing"
)

func TestReceivePreferencePersists(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "store")
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	enabled, err := store.ReceiveEnabled()
	if err != nil || enabled {
		t.Fatalf("fresh preference: %v %v", enabled, err)
	}
	identity := store.Identity().ComputerID
	for _, want := range []bool{true, false, true} {
		if err := store.SetReceiveEnabled(want); err != nil {
			t.Fatal(err)
		}
		store.Close()
		store, err = OpenStore(dir)
		if err != nil {
			t.Fatal(err)
		}
		got, err := store.ReceiveEnabled()
		if err != nil || got != want {
			t.Fatalf("restored %v %v; want %v", got, err, want)
		}
		if store.Identity().ComputerID != identity {
			t.Fatal("receiving preference changed identity")
		}
	}
	store.Close()
}
