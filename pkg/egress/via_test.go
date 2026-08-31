package egress

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"

	"github.com/adamf/jetway/pkg/config"
	"github.com/adamf/jetway/pkg/store"
)

// A "via" peer's traffic goes down the transit peer's link, resolved at send
// time -- so the transit link may be registered after the via peer, and a
// reconnected transit link is picked up without rebuilding anything.
func TestViaEgressRoutesThroughTransitPeer(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	r := NewRouter(store.NewMem(), log)

	var carried [][]byte
	// The via sender is built while the transit peer is still unregistered.
	s, err := BuildWith(config.Peer{
		Name:   "BA",
		Egress: config.Egress{Type: "via", Via: "SITA"},
	}, nil, r, log)
	if err != nil {
		t.Fatalf("BuildWith: %v", err)
	}

	if err := s.Send(context.Background(), []byte("early")); err == nil {
		t.Fatal("sending before the transit link exists must fail, not vanish")
	}

	r.Register("SITA", SenderFunc{
		Fn:   func(ctx context.Context, raw []byte) error { carried = append(carried, raw); return nil },
		Desc: "test link",
	}, config.Retry{}, store.FormatTypeB)

	if err := s.Send(context.Background(), []byte("QU LHRRMBA ...")); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(carried) != 1 || string(carried[0]) != "QU LHRRMBA ..." {
		t.Fatalf("the transit link did not carry the bytes: %q", carried)
	}
	if s.Describe() != "via SITA" {
		t.Errorf("Describe = %q", s.Describe())
	}
}

func TestViaEgressConfigErrors(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	r := NewRouter(store.NewMem(), log)
	if _, err := BuildWith(config.Peer{Name: "BA", Egress: config.Egress{Type: "via"}}, nil, r, log); err == nil {
		t.Error("via with no transit peer must be a configuration error")
	}
	if _, err := BuildWith(config.Peer{Name: "BA", Egress: config.Egress{Type: "via", Via: "SITA"}}, nil, nil, log); err == nil {
		t.Error("via with no resolver must be a configuration error")
	}
	_ = fmt.Sprint()
}
