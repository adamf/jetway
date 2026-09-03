package transport

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

// A peer that sends its first frames in the same burst as its hello loses
// none of them. The server read the hello through a buffered reader and
// then started a fresh one on the connection, so whatever the first reader
// had buffered past the hello was gone -- and a frame split across the two
// came out as garbage and closed the link. It showed as the outbox test
// receiving 188 of 200 frames, or the client's writes hitting a broken
// pipe, one run in ten.
func TestFramesInTheHelloBurstAreNotLost(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	var received atomic.Int64
	srv := &Server{Addr: "127.0.0.1:0", Framer: DefaultFramer(), Log: log}
	srv.OnMessage = func(ctx context.Context, peer string, raw []byte) error {
		received.Add(1)
		return nil
	}
	if err := srv.Listen(); err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ctx) //nolint:errcheck

	conn, err := net.Dial("tcp", srv.Addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// One write: the hello and fifty frames, as a fast peer's kernel would
	// coalesce them.
	var burst []byte
	hello, _ := json.Marshal(Hello{Peer: "XX", Role: "carrier", Format: "typeb"})
	burst = appendFrame(t, burst, hello)
	const n = 50
	for i := 0; i < n; i++ {
		burst = appendFrame(t, burst, []byte("frame"))
	}
	if _, err := conn.Write(burst); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for received.Load() < n && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := received.Load(); got != n {
		t.Fatalf("server received %d of %d frames sent in the hello's burst", got, n)
	}
}

func appendFrame(t *testing.T, buf, raw []byte) []byte {
	w := &sliceWriter{buf: buf}
	if err := DefaultFramer().WriteFrame(w, raw); err != nil {
		t.Fatal(err)
	}
	return w.buf
}

type sliceWriter struct{ buf []byte }

func (w *sliceWriter) Write(p []byte) (int, error) { w.buf = append(w.buf, p...); return len(p), nil }
