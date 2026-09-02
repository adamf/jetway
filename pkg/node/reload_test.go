package node

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/adamf/jetway/pkg/config"
	"github.com/adamf/jetway/pkg/store"
)

// A partner added to the config is picked up without a restart; the peers
// already there are untouched.
func TestReloadAddsNewPeersOnly(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := leasedConfig("host-a")
	cfg.Lease.Enabled = false
	n, err := Build(context.Background(), cfg, log, Options{Store: store.NewMem(), LocatorSecret: []byte("s"), SkipConsole: true})
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	peers := append([]config.Peer{}, cfg.Peers...)
	peers = append(peers, config.Peer{Name: "LH", Carrier: "LH", TTYAddress: "FRARMLH", Format: "typeb", Egress: config.Egress{Type: "tcp_accept"}})
	added, err := n.ReloadPeers(peers)
	if err != nil || added != 1 {
		t.Fatalf("reload: added %d, %v", added, err)
	}
	if n.Gateway.Peer("LH") == nil || n.Gateway.Peer("1G") == nil {
		t.Fatal("both the old and the new peer should be configured")
	}
	if added, _ := n.ReloadPeers(peers); added != 0 {
		t.Errorf("a second reload of the same list added %d", added)
	}
}
