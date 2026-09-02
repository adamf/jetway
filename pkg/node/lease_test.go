package node

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/adamf/jetway/pkg/config"
	"github.com/adamf/jetway/pkg/store"
)

func leasedConfig(holder string) *config.Config {
	cfg := config.Default()
	cfg.Identity = config.Identity{Designator: "BA", TTYAddress: "LHRRMBA", Name: "BA " + holder}
	cfg.HTTP.Addr = "127.0.0.1:0"
	cfg.Demo.Carriers = false
	cfg.Spool.Enabled = false
	cfg.Lease = config.Lease{Enabled: true, TTL: 600 * time.Millisecond, Holder: holder}
	cfg.Ingress = []config.Ingress{{Name: "link-1g", Type: "tcp", Addr: "127.0.0.1:0", Identify: config.Identify{Peer: "1G"}}}
	cfg.Peers = []config.Peer{{Name: "1G", Carrier: "1G", TTYAddress: "LONRM1G", Format: "typeb", Egress: config.Egress{Type: "tcp_accept"}}}
	return cfg
}

// Two processes for one system: the first holds the lease and binds its
// links, the second stands by and is not ready; when the first goes, the
// second takes the system within a renewal interval.
func TestOneWriterPerSystem(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	shared := store.NewMem()
	ctxA, cancelA := context.WithCancel(context.Background())
	defer cancelA()
	ctxB, cancelB := context.WithCancel(context.Background())
	defer cancelB()

	a, err := Build(ctxA, leasedConfig("host-a"), log, Options{Store: shared, LocatorSecret: []byte("a"), SkipConsole: true})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err := a.Start(ctxA); err != nil {
		t.Fatal(err)
	}
	waitFor := func(what string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	waitFor("the first process to hold the system", a.Holding)

	b, err := Build(ctxB, leasedConfig("host-b"), log, Options{Store: shared, LocatorSecret: []byte("b"), SkipConsole: true})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if err := b.Start(ctxB); err != nil {
		t.Fatal(err)
	}
	time.Sleep(700 * time.Millisecond)
	if b.Holding() {
		t.Fatal("a second process took a held system")
	}
	if b.API != nil {
		if err := b.API.Ready(context.Background()); err == nil {
			t.Error("a standby should not report ready")
		}
	}
	// The holder shuts down: it drains, then releases; the standby takes over.
	cancelA()
	a.Drain(context.Background(), nil)
	waitFor("the standby to take the system", b.Holding)
	if b.API != nil {
		if err := b.API.Ready(context.Background()); err != nil {
			t.Errorf("the new holder should be ready: %v", err)
		}
	}
}
