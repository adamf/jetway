package node

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/adamf/jetway/pkg/config"
	"github.com/adamf/jetway/pkg/egress"
	"github.com/adamf/jetway/pkg/store"
)

// LivePeers names the partners currently holding a session -- and only those.
// It once also listed every peer with a configured egress, which never gets
// unregistered, so a carrier that connected once read as live forever: the
// fleet dashboards built on this could not show a dead link, and wholesky's
// link-sever chaos was invisible on every dial.
func TestLivePeersExcludesConfiguredButDisconnectedPeers(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	r := egress.NewRouter(store.NewMem(), log)
	r.Register("BA", egress.SenderFunc{
		Fn:   func(ctx context.Context, raw []byte) error { return nil },
		Desc: "test sender",
	}, config.Retry{}, store.FormatTypeB)

	n := &Node{Router: r}
	if got := n.LivePeers(); len(got) != 0 {
		t.Errorf("LivePeers = %v, want none: a configured egress is not a session", got)
	}
}
