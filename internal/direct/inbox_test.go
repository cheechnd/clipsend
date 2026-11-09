package direct

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInboxRequiresValidatedCompleteItemsAndCleansPrivateData(t *testing.T) {
	root := filepath.Join(t.TempDir(), "inbox")
	b, e := NewInbox(root)
	if e != nil {
		t.Fatal(e)
	}
	data := []byte("fixture")
	sum := sha256.Sum256(data)
	a := IncomingAttachment{ID: "file1", Name: "../../display only", MIME: "text/plain", Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}
	x, e := b.Create("device", "text", []IncomingAttachment{a})
	if e != nil {
		t.Fatal(e)
	}
	if e = b.Complete(x.ID); e == nil {
		t.Fatal("incomplete transfer accepted")
	}
	if e = b.Put(x.ID, a.ID, strings.NewReader("bad")); e == nil {
		t.Fatal("bad hash accepted")
	}
	if e = b.Put(x.ID, a.ID, strings.NewReader(string(data))); e != nil {
		t.Fatal(e)
	}
	if e = b.Complete(x.ID); e != nil {
		t.Fatal(e)
	}
	p, e := b.File(x.ID, a.ID)
	if e != nil {
		t.Fatal(e)
	}
	st, e := os.Stat(p)
	if e != nil || st.Mode().Perm() != 0600 {
		t.Fatal("inbox file not private")
	}
	if e = b.Clear(x.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(p); !os.IsNotExist(e) {
		t.Fatal("clear retained file")
	}
	if e = b.Close(); e != nil {
		t.Fatal(e)
	}
}
func TestInboxReservesDeclaredQuotaAndRejectsUnsafeIDs(t *testing.T) {
	b, e := NewInbox(filepath.Join(t.TempDir(), "inbox"))
	if e != nil {
		t.Fatal(e)
	}
	defer b.Close()
	sum := strings.Repeat("0", 64)
	_, e = b.Create("device", "", []IncomingAttachment{{ID: "../x", Size: 1, SHA256: sum}})
	if e == nil {
		t.Fatal("unsafe ID accepted")
	}
	_, e = b.Create("device", "", []IncomingAttachment{{ID: "item1", Size: MaxItemBytes, SHA256: sum}, {ID: "item2", Size: MaxItemBytes, SHA256: sum}})
	if e != nil {
		t.Fatal(e)
	}
	_, e = b.Create("device", "", []IncomingAttachment{{ID: "another", Size: 1, SHA256: sum}})
	if e == nil {
		t.Fatal("declared pending quota was not reserved")
	}
}

func TestInboxNewestFirstStableAcrossRefreshAndClear(t *testing.T) {
	b, err := NewInbox(filepath.Join(t.TempDir(), "inbox"))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	var ids []string
	for _, device := range []string{"ipad", "iphone", "iphone"} {
		x, err := b.Create(device, "same clipboard text", nil)
		if err != nil {
			t.Fatal(err)
		}
		if err = b.Complete(x.ID); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, x.ID)
	}
	for range 50 {
		items := b.Items()
		for i, x := range items {
			if x.ID != ids[len(ids)-1-i] || x.ReceivedAt.IsZero() {
				t.Fatal("inbox order changed or receipt time missing")
			}
		}
		if items[0].DeviceID != "iphone" || items[2].DeviceID != "ipad" {
			t.Fatal("sender attribution changed")
		}
	}
	if err = b.Clear(ids[1]); err != nil {
		t.Fatal(err)
	}
	items := b.Items()
	if len(items) != 2 || items[0].ID != ids[2] || items[1].ID != ids[0] {
		t.Fatal("clear changed remaining order")
	}
}
