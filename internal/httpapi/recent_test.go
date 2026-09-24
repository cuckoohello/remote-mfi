package httpapi

import (
	"fmt"
	"testing"
	"time"
)

func TestRecentStoreKeepsNewestEntries(t *testing.T) {
	store := newRecentStore(20, time.UTC)
	for index := range 25 {
		store.add("POST", fmt.Sprintf("/request/%02d", index), 200, time.Duration(index)*time.Millisecond, "ok")
	}
	entries := store.listNewestFirst()
	if len(entries) != 20 {
		t.Fatalf("entry count = %d, want 20", len(entries))
	}
	if entries[0].Path != "/request/24" || entries[len(entries)-1].Path != "/request/05" {
		t.Fatalf("unexpected ordering: first=%q last=%q", entries[0].Path, entries[len(entries)-1].Path)
	}
}
