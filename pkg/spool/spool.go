// Package spool is a durable write-ahead buffer for inbound messages.
//
// It exists to break one dependency: without it, acknowledging a peer requires
// the database to be up, because capture writes straight through. That is
// correct but fragile -- a Postgres failover turns into refused acknowledgements
// and relies on every partner's retransmission behaviour being sane, which it
// is not.
//
// With a spool, ingest fsyncs the raw bytes to local disk and acknowledges. A
// drainer moves them into the store afterwards, retrying for as long as it
// takes. The peer's job is done the moment the bytes are on our disk, which is
// the guarantee a store-and-forward partner expects.
//
// One file per message, not a segment log. Airline messaging is thousands of
// messages a day, not millions a second, and one file per message removes
// compaction, torn-record recovery and offset bookkeeping -- three things that
// are easy to get subtly wrong and hard to test.
package spool

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Entry is one spooled message.
type Entry struct {
	// ID is the message identifier, and also the filename, so entries replay in
	// receive order.
	ID string `json:"id"`
	// Peer is the link it arrived on.
	Peer string `json:"peer"`
	// Transport names the ingress that accepted it.
	Transport string `json:"transport"`
	// At is when it was received.
	At time.Time `json:"at"`
	// Raw is the exact bytes.
	Raw []byte `json:"raw"`
	// Attempts counts how many times draining has been tried.
	Attempts int `json:"attempts"`
}

// Spool is a directory of pending messages.
type Spool struct {
	dir     string
	pending string
	tmp     string

	mu sync.Mutex
	// syncDir controls whether the directory itself is fsynced after a rename.
	// Enabled by default: without it, a crash can lose the rename even though
	// the file contents are durable.
	syncDir bool
}

// Open prepares a spool directory, creating it if needed.
func Open(dir string) (*Spool, error) {
	s := &Spool{
		dir:     dir,
		pending: filepath.Join(dir, "pending"),
		tmp:     filepath.Join(dir, "tmp"),
		syncDir: true,
	}
	for _, d := range []string{s.dir, s.pending, s.tmp} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, fmt.Errorf("spool: create %s: %w", d, err)
		}
	}
	// A tmp file is a write that never completed. Removing them on open keeps
	// the directory from growing after repeated crashes.
	if err := s.clearTmp(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Spool) clearTmp() error {
	entries, err := os.ReadDir(s.tmp)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := os.Remove(filepath.Join(s.tmp, e.Name())); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// Dir returns the spool's root directory.
func (s *Spool) Dir() string { return s.dir }

// Put writes an entry durably and returns once it is on disk.
//
// The sequence is write, fsync file, rename into place, fsync directory. Every
// step matters: without the file fsync the contents may be lost, and without
// the directory fsync the rename itself may be, leaving a message that was
// acknowledged and then vanished.
func (s *Spool) Put(e Entry) error {
	if e.ID == "" {
		return errors.New("spool: entry has no id")
	}
	b, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("spool: encode: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	tmpPath := filepath.Join(s.tmp, e.ID+".json")
	f, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("spool: create: %w", err)
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("spool: write: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("spool: fsync: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("spool: close: %w", err)
	}

	final := filepath.Join(s.pending, e.ID+".json")
	if err := os.Rename(tmpPath, final); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("spool: rename: %w", err)
	}
	if s.syncDir {
		if err := syncDir(s.pending); err != nil {
			return fmt.Errorf("spool: fsync dir: %w", err)
		}
	}
	return nil
}

func syncDir(path string) error {
	d, err := os.Open(path)
	if err != nil {
		return err
	}
	defer d.Close()
	// Directory fsync is not supported on every filesystem; a failure here is
	// not worth refusing the message over, since the contents are already
	// durable.
	if err := d.Sync(); err != nil && !errors.Is(err, os.ErrInvalid) {
		return err
	}
	return nil
}

// List returns pending entry ids in receive order. Ids are ULIDs, so
// lexicographic order is chronological.
func (s *Spool) List() ([]string, error) {
	entries, err := os.ReadDir(s.pending)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		ids = append(ids, strings.TrimSuffix(e.Name(), ".json"))
	}
	sort.Strings(ids)
	return ids, nil
}

// Get reads one pending entry.
func (s *Spool) Get(id string) (Entry, error) {
	var e Entry
	b, err := os.ReadFile(filepath.Join(s.pending, id+".json"))
	if err != nil {
		return e, err
	}
	if err := json.Unmarshal(b, &e); err != nil {
		return e, fmt.Errorf("spool: decode %s: %w", id, err)
	}
	return e, nil
}

// Done removes an entry that has been persisted downstream.
func (s *Spool) Done(id string) error {
	err := os.Remove(filepath.Join(s.pending, id+".json"))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// Retry rewrites an entry with an incremented attempt count, so that a message
// failing repeatedly is visible rather than silently spinning.
func (s *Spool) Retry(e Entry) error {
	e.Attempts++
	return s.Put(e)
}

// Depth returns the number of pending entries. It is the single most important
// number to alert on: a rising spool means the store is not keeping up, or is
// down.
func (s *Spool) Depth() (int, error) {
	ids, err := s.List()
	return len(ids), err
}

// Oldest returns the age of the oldest pending entry, and whether there is one.
func (s *Spool) Oldest() (time.Duration, bool, error) {
	ids, err := s.List()
	if err != nil || len(ids) == 0 {
		return 0, false, err
	}
	e, err := s.Get(ids[0])
	if err != nil {
		return 0, false, err
	}
	return time.Since(e.At), true, nil
}
