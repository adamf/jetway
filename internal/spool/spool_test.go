package spool

import (
	"bytes"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/adamf/jetway/internal/ulid"
)

func newSpool(t *testing.T) *Spool {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func entry(raw string) Entry {
	return Entry{ID: ulid.New(), Peer: "BA", Transport: "tcp", At: time.Now().UTC(), Raw: []byte(raw)}
}

func TestPutGetDone(t *testing.T) {
	s := newSpool(t)
	e := entry("QU LHRRMBA\r\n.LONRM1J 121430\r\nBA0175Y15JUNLHRJFKNN1\r\n")
	if err := s.Put(e); err != nil {
		t.Fatalf("Put: %v", err)
	}
	ids, err := s.List()
	if err != nil || len(ids) != 1 || ids[0] != e.ID {
		t.Fatalf("List = %v, %v", ids, err)
	}
	got, err := s.Get(e.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(got.Raw, e.Raw) {
		t.Errorf("raw bytes changed:\n got %q\nwant %q", got.Raw, e.Raw)
	}
	if got.Peer != "BA" || got.Transport != "tcp" {
		t.Errorf("metadata lost: %+v", got)
	}
	if err := s.Done(e.ID); err != nil {
		t.Fatalf("Done: %v", err)
	}
	if ids, _ := s.List(); len(ids) != 0 {
		t.Errorf("entry still pending after Done: %v", ids)
	}
	// Done must be idempotent: a drainer that crashes between the store write
	// and the removal will call it twice.
	if err := s.Done(e.ID); err != nil {
		t.Errorf("second Done: %v", err)
	}
}

func TestBinaryPayloadSurvives(t *testing.T) {
	s := newSpool(t)
	raw := []byte{0x00, 0x01, 0xff, 0xfe, '\n', '\r', 0x7f}
	e := entry("")
	e.Raw = raw
	if err := s.Put(e); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(e.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Raw, raw) {
		t.Errorf("binary payload corrupted: %v", got.Raw)
	}
}

// Ids are ULIDs, so listing order must be receive order. Draining out of order
// would apply a reply before the request that provoked it.
func TestListIsInReceiveOrder(t *testing.T) {
	s := newSpool(t)
	var want []string
	for i := 0; i < 25; i++ {
		e := entry("msg")
		if err := s.Put(e); err != nil {
			t.Fatal(err)
		}
		want = append(want, e.ID)
	}
	got, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("entry %d out of order: got %s, want %s", i, got[i], want[i])
		}
	}
}

// The point of the spool: entries survive the process that wrote them.
func TestEntriesSurviveReopen(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	e := entry("BA0175Y15JUNLHRJFKNN1")
	if err := s.Put(e); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	ids, _ := reopened.List()
	if len(ids) != 1 || ids[0] != e.ID {
		t.Fatalf("entry did not survive reopen: %v", ids)
	}
	got, err := reopened.Get(e.ID)
	if err != nil || string(got.Raw) != "BA0175Y15JUNLHRJFKNN1" {
		t.Errorf("payload after reopen: %+v, %v", got, err)
	}
}

// A half-written file is a write that never completed. It must never be
// mistaken for a pending message.
func TestPartialWritesAreNotVisibleAndAreCleanedUp(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a crash mid-Put: a file left behind in tmp.
	stray := filepath.Join(dir, "tmp", "01ABCDEF.json")
	if err := os.WriteFile(stray, []byte(`{"id":"01ABCDEF","raw":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if ids, _ := s.List(); len(ids) != 0 {
		t.Errorf("a tmp file must not appear as pending: %v", ids)
	}

	// Reopening clears it rather than letting the directory grow forever.
	if _, err := Open(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stray); !os.IsNotExist(err) {
		t.Errorf("stray tmp file was not cleaned up: %v", err)
	}
}

func TestRetryIncrementsAttempts(t *testing.T) {
	s := newSpool(t)
	e := entry("x")
	if err := s.Put(e); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(e.ID)
	if err := s.Retry(got); err != nil {
		t.Fatal(err)
	}
	again, _ := s.Get(e.ID)
	if again.Attempts != 1 {
		t.Errorf("Attempts = %d, want 1", again.Attempts)
	}
	if ids, _ := s.List(); len(ids) != 1 {
		t.Errorf("Retry must replace the entry, not duplicate it: %v", ids)
	}
}

func TestDepthAndOldest(t *testing.T) {
	s := newSpool(t)
	if n, _ := s.Depth(); n != 0 {
		t.Errorf("empty depth = %d", n)
	}
	if _, ok, _ := s.Oldest(); ok {
		t.Error("empty spool should report no oldest entry")
	}

	old := entry("old")
	old.At = time.Now().UTC().Add(-90 * time.Second)
	if err := s.Put(old); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(entry("new")); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.Depth(); n != 2 {
		t.Errorf("depth = %d, want 2", n)
	}
	age, ok, err := s.Oldest()
	if err != nil || !ok {
		t.Fatalf("Oldest: %v, %v", ok, err)
	}
	if age < 80*time.Second {
		t.Errorf("oldest age = %s, want ~90s", age)
	}
}

func TestConcurrentPuts(t *testing.T) {
	s := newSpool(t)
	const n = 60
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.Put(entry("concurrent")); err != nil {
				t.Errorf("Put: %v", err)
			}
		}()
	}
	wg.Wait()
	ids, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != n {
		t.Errorf("got %d entries, want %d", len(ids), n)
	}
}

func TestPutRejectsEntryWithoutID(t *testing.T) {
	s := newSpool(t)
	if err := s.Put(Entry{Raw: []byte("x")}); err == nil {
		t.Error("an entry with no id must be rejected")
	}
}
