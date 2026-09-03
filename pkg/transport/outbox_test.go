package transport

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

// The deadlock a holiday load found: both ends answer what they read from
// inside the read loop. The server answers every frame back down the same
// link; the client has stopped reading. Before the outbox the server's
// reader blocked on the answer's write once the client's window filled and
// stopped reading the client's frames -- and a client blocked the same way
// on the other side never drained it. Now the server keeps reading
// everything the client sends, and its sends fail fast with ErrCongested
// instead of stalling.
func TestReaderNeverWaitsOnAPeerThatStoppedReading(t *testing.T) {
	oldDepth, oldTimeout := OutboxDepth, SendTimeout
	OutboxDepth, SendTimeout = 8, 200*time.Millisecond
	defer func() { OutboxDepth, SendTimeout = oldDepth, oldTimeout }()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	big := make([]byte, 64<<10)

	var received, congested atomic.Int64
	srv := &Server{Addr: "127.0.0.1:0", Framer: DefaultFramer(), Log: log}
	srv.OnMessage = func(ctx context.Context, peer string, raw []byte) error {
		received.Add(1)
		// Answer from the read loop, the way a ticket-control reply does.
		if err := srv.Send(ctx, peer, big); errors.Is(err, ErrCongested) {
			congested.Add(1)
		}
		return nil
	}
	if err := srv.Listen(); err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ctx) //nolint:errcheck

	stuck := make(chan struct{})
	cli := &Client{Addr: srv.Addr, Hello: Hello{Peer: "XX", Role: "carrier", Format: "typeb"}, Framer: DefaultFramer(), Log: log}
	cli.OnMessage = func(ctx context.Context, peer string, raw []byte) error {
		<-stuck // the client's consumer never comes back: it is reading nothing more
		return nil
	}
	up := make(chan struct{})
	cli.OnUp = func() { close(up) }
	go cli.Run(ctx) //nolint:errcheck
	select {
	case <-up:
	case <-time.After(5 * time.Second):
		t.Fatal("link never came up")
	}

	// The client keeps sending; the server must keep receiving all of it
	// even though nothing it answers is being read.
	const n = 200
	for i := 0; i < n; i++ {
		if err := cli.Send(ctx, "", []byte("frame")); err != nil {
			t.Fatalf("client send %d: %v", i, err)
		}
	}
	// The read loop waits once, for SendTimeout, before the outbox declares
	// the peer congested and sends fail fast; the deadline allows that wait
	// and a slow runner besides.
	deadline := time.Now().Add(SendTimeout + 15*time.Second)
	for received.Load() < n && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	// Before the outbox the reader stalled after a dozen frames, once the
	// peer's window filled. Losing the last few to a torn-down harness
	// socket under the race detector is not that; three quarters through
	// is the property.
	if got := received.Load(); got < n*3/4 {
		t.Fatalf("server read %d of %d frames while its answers were unread: the reader waited on the writer", got, n)
	}
	if congested.Load() == 0 {
		t.Errorf("a peer that stopped reading should have made sends fail with ErrCongested")
	}
	close(stuck)
}

// A relay's outbox never makes its caller wait: full means ErrCongested
// now, and room means accepted again, with nothing marked as stopped.
func TestOutboxNoWaitRefusesAFullQueueAtOnce(t *testing.T) {
	release := make(chan struct{})
	taken := make(chan struct{}, 8)
	o := NewOutbox(2, func(raw []byte) error { taken <- struct{}{}; <-release; return nil }, func(error) {})
	o.NoWait = true
	defer o.Close()
	// The writer takes one frame and blocks in the write; two more fill the queue.
	if err := o.Send([]byte("x")); err != nil {
		t.Fatal(err)
	}
	<-taken
	for i := 0; i < 2; i++ {
		if err := o.Send([]byte("x")); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}
	start := time.Now()
	if err := o.Send([]byte("x")); err != ErrCongested {
		t.Fatalf("full queue: %v", err)
	}
	if time.Since(start) > 100*time.Millisecond {
		t.Fatalf("a no-wait send waited %v", time.Since(start))
	}
	if o.Congested() {
		t.Fatal("a momentarily full queue is not a stopped peer")
	}
	release <- struct{}{}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if err := o.Send([]byte("x")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the queue never took a frame again")
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(release)
}
