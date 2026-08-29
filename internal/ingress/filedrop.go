package ingress

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/adamf/jetway/internal/config"
	"github.com/adamf/jetway/internal/metrics"
)

// FileDrop watches a directory for messages left by a partner.
//
// This is how batch traffic actually arrives: the partner's system writes into
// a directory an SFTP daemon serves, on their schedule, and nothing negotiates.
// Serving SFTP itself is out of scope — run a real SFTP server and point this
// at the directory it writes to, so authentication and key management stay with
// software built for them.
type FileDrop struct {
	name       string
	dir        string
	archiveDir string
	pattern    string
	peer       string
	poll       time.Duration
	stableFor  time.Duration
	log        *slog.Logger

	// sizes remembers what each file measured last sweep, so a file still
	// being uploaded is not read half-written.
	sizes    map[string]fileState
	inflight sync.WaitGroup
	mu       sync.Mutex
	closed   bool
}

type fileState struct {
	size int64
	seen time.Time
}

// NewFileDrop builds a directory watcher.
func NewFileDrop(c config.Ingress, log *slog.Logger) (*FileDrop, error) {
	if c.Identify.Peer == "" {
		return nil, fmt.Errorf("ingress %s: a file carries no identity, so identify.peer is required", c.Name)
	}
	f := &FileDrop{
		name: c.Name, dir: c.Dir, archiveDir: c.ArchiveDir, pattern: c.Pattern,
		peer: c.Identify.Peer, poll: c.Poll, stableFor: c.StableFor,
		log: log.With("ingress", c.Name), sizes: map[string]fileState{},
	}
	if f.archiveDir == "" {
		f.archiveDir = filepath.Join(c.Dir, ".processed")
	}
	for _, d := range []string{f.dir, f.archiveDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, fmt.Errorf("ingress %s: create %s: %w", c.Name, d, err)
		}
	}
	return f, nil
}

func (f *FileDrop) Name() string { return f.name }
func (f *FileDrop) Addr() string { return f.dir }

func (f *FileDrop) Start(ctx context.Context, h Handler) error {
	f.log.Info("file drop watching", "dir", f.dir, "pattern", f.pattern, "peer", f.peer)
	t := time.NewTicker(f.poll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			if err := f.sweep(ctx, h); err != nil {
				f.log.Error("sweep failed", "err", err)
			}
		}
	}
}

func (f *FileDrop) sweep(ctx context.Context, h Handler) error {
	matches, err := filepath.Glob(filepath.Join(f.dir, f.pattern))
	if err != nil {
		return err
	}
	// Deterministic order so a batch arriving as several files is processed the
	// way the partner named it, not in whatever order the filesystem returns.
	sort.Strings(matches)

	now := time.Now()
	for _, path := range matches {
		if ctx.Err() != nil {
			return nil
		}
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			continue
		}
		// A file is only read once its size has stopped changing. Reading a
		// partner's upload mid-write yields a truncated message that looks like
		// a protocol error and is not one.
		prev, seen := f.sizes[path]
		if !seen || prev.size != info.Size() {
			f.sizes[path] = fileState{size: info.Size(), seen: now}
			continue
		}
		if now.Sub(prev.seen) < f.stableFor {
			continue
		}

		raw, err := os.ReadFile(path)
		if err != nil {
			f.log.Error("could not read dropped file", "path", path, "err", err)
			continue
		}
		if len(raw) == 0 {
			f.archive(path)
			delete(f.sizes, path)
			continue
		}

		f.inflight.Add(1)
		_, herr := h(ctx, Message{
			Peer: f.peer, Transport: f.name, Remote: "file:" + filepath.Base(path), Raw: raw,
			FromFile: true,
		})
		f.inflight.Done()
		if herr != nil {
			// Leave the file where it is. It will be retried next sweep, which
			// is the correct behaviour for a drop directory: the partner has
			// gone, and the file is the only copy.
			f.log.Error("could not accept dropped file; leaving it for retry",
				"path", path, "err", herr)
			metrics.Counter("jetway_ingress_refused_total", "messages the pipeline would not accept",
				metrics.Labels{"ingress": f.name, "peer": f.peer})
			continue
		}
		metrics.Counter("jetway_ingress_accepted_total", "messages accepted",
			metrics.Labels{"ingress": f.name, "peer": f.peer})
		f.archive(path)
		delete(f.sizes, path)
	}

	// Forget files that have gone away, so the map does not grow forever.
	present := map[string]bool{}
	for _, m := range matches {
		present[m] = true
	}
	for p := range f.sizes {
		if !present[p] {
			delete(f.sizes, p)
		}
	}
	metrics.Gauge("jetway_filedrop_pending", "files waiting in a drop directory",
		metrics.Labels{"ingress": f.name}, float64(len(matches)))
	return nil
}

// archive moves a processed file aside rather than deleting it. The bytes are
// already in the message log, but a partner asking "did you get our file" is
// answered faster by looking in a directory.
func (f *FileDrop) archive(path string) {
	dest := filepath.Join(f.archiveDir, filepath.Base(path))
	if _, err := os.Stat(dest); err == nil {
		dest = fmt.Sprintf("%s.%d", dest, time.Now().UnixNano())
	}
	if err := os.Rename(path, dest); err != nil {
		f.log.Error("could not archive processed file", "path", path, "err", err)
	}
}

// Drain waits for in-flight files.
func (f *FileDrop) Drain(ctx context.Context) error {
	done := make(chan struct{})
	go func() { f.inflight.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
	return f.Close()
}

func (f *FileDrop) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}
