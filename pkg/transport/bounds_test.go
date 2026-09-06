package transport

import (
	"bufio"
	"bytes"
	"errors"
	"strings"
	"testing"
)

// A peer that never sends the terminator is caught at the bound, not
// buffered whole: the check has to happen while reading, because the
// stream may be as long as the peer likes.
func TestSentinelStopsBufferingAtTheBound(t *testing.T) {
	f := Sentinel{Terminator: []byte("\nNNNN\n"), Max: 1024}
	stream := strings.Repeat("Q", 8<<20) // no newline anywhere
	r := bufio.NewReaderSize(strings.NewReader(stream), 4096)
	raw, err := f.ReadFrame(r)
	if !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("err = %v, want ErrFrameTooLarge", err)
	}
	if len(raw) > 0 {
		t.Fatalf("returned %d bytes of an oversized frame", len(raw))
	}
	// Buffered at most a little over the bound plus one reader buffer.
	if n := r.Buffered(); n > 4096 {
		t.Fatalf("reader still holds %d bytes", n)
	}

	// And a proper frame, longer than one reader buffer, still arrives whole.
	msg := strings.Repeat("A", 6000) + "\nNNNN\n"
	r = bufio.NewReaderSize(strings.NewReader(msg), 4096)
	raw, err = Sentinel{Terminator: []byte("\nNNNN\n"), Max: 8192}.ReadFrame(r)
	if err != nil || !bytes.Equal(raw, []byte(msg)) {
		t.Fatalf("frame across buffers: err=%v len=%d want %d", err, len(raw), len(msg))
	}
}
