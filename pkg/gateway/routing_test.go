package gateway

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"

	"github.com/adamf/jetway/pkg/store"
	"github.com/adamf/jetway/pkg/typeb"
)

// sentTo records what reached each link, so fan-out can be asserted on
// delivery rather than on intent.
type sentTo struct {
	mu   sync.Mutex
	msgs map[string][][]byte
}

func newSentTo() *sentTo { return &sentTo{msgs: map[string][][]byte{}} }

func (s *sentTo) sender() Sender {
	return SenderFunc(func(ctx context.Context, peer string, raw []byte) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.msgs[peer] = append(s.msgs[peer], append([]byte(nil), raw...))
		return nil
	})
}

func (s *sentTo) count(peer string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.msgs[peer])
}

func switchNode(t *testing.T, relay bool) (*Gateway, *sentTo) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	gw := New(Identity{Designator: "1J", TTYAddress: "LONRM1J", Name: "jetway"},
		store.NewMem(), NewBus(100), log, []byte("secret"))
	gw.Relay = relay
	gw.AddPeer(&Peer{Name: "BA", Carrier: "BA", Format: store.FormatTypeB,
		TTYAddress: "LHRRMBA", Addresses: []string{"LHRRSBA"}})
	gw.AddPeer(&Peer{Name: "LH", Carrier: "LH", Format: store.FormatTypeB,
		TTYAddress: "FRARMLH"})
	sent := newSentTo()
	gw.Sender = sent.sender()
	return gw, sent
}

func TestPeerByAddress(t *testing.T) {
	gw, _ := switchNode(t, false)

	if p := gw.PeerByAddress("LHRRMBA"); p == nil || p.Name != "BA" {
		t.Errorf("primary address did not resolve: %v", p)
	}
	// A carrier commonly has more than one address on one circuit.
	if p := gw.PeerByAddress("LHRRSBA"); p == nil || p.Name != "BA" {
		t.Errorf("secondary address did not resolve: %v", p)
	}
	if p := gw.PeerByAddress("  lhrrmba "); p == nil || p.Name != "BA" {
		t.Errorf("address lookup must normalise case and spacing: %v", p)
	}
	if p := gw.PeerByAddress("JFKRMAA"); p != nil {
		t.Errorf("an unserved address resolved to %s", p.Name)
	}
	// An address nobody registered goes to the carrier whose code it
	// carries: BA's check-in at Heathrow comes down BA's circuit.
	if p := gw.PeerByAddress("LHRKPBA"); p == nil || p.Name != "BA" {
		t.Errorf("a carrier's unregistered address did not fall back to its link: %v", p)
	}
	if !gw.IsSelf("LONRM1J") || gw.IsSelf("LHRRMBA") {
		t.Error("IsSelf does not recognise this node's own address")
	}
}

func TestFanoutDeliversToEveryAddressee(t *testing.T) {
	gw, sent := switchNode(t, false)
	raw := []byte("QU LHRRMBA FRARMLH LONRM1J JFKRMAA\n.NYCRM1A 121430\nHELLO\n")
	tb, err := typeb.Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	got := gw.Fanout(context.Background(), tb, raw, "test", "", "")
	if len(got) != 4 {
		t.Fatalf("got %d deliveries, want one per addressee", len(got))
	}

	by := map[string]Delivery{}
	for _, d := range got {
		by[d.Address] = d
	}
	if d := by["LHRRMBA"]; d.Peer != "BA" || d.Err != "" || d.MessageID == "" {
		t.Errorf("BA delivery = %+v", d)
	}
	if d := by["FRARMLH"]; d.Peer != "LH" || d.Err != "" {
		t.Errorf("LH delivery = %+v", d)
	}
	// Our own address terminates here; forwarding it to ourselves is a loop.
	if d := by["LONRM1J"]; !d.Self || d.MessageID != "" {
		t.Errorf("self address should terminate, got %+v", d)
	}
	// An address nothing serves is reported, not silently dropped.
	if d := by["JFKRMAA"]; d.Err == "" || d.Peer != "" {
		t.Errorf("unroutable address should carry an error, got %+v", d)
	}

	if sent.count("BA") != 1 || sent.count("LH") != 1 {
		t.Errorf("delivery counts BA=%d LH=%d, want 1 each", sent.count("BA"), sent.count("LH"))
	}
	// The bytes go out unchanged; rewriting per recipient would make each copy
	// a different message from the one in the log.
	if got := string(sent.msgs["BA"][0]); got != string(raw) {
		t.Errorf("BA received rewritten bytes:\n got %q\nwant %q", got, raw)
	}
}

func TestRelayForwardsTransitTraffic(t *testing.T) {
	gw, sent := switchNode(t, true)
	// BA sends something addressed to LH. We are not an addressee.
	raw := []byte("QU FRARMLH\n.LHRRMBA 121430\nSSR VGML LH HK1\n")

	res, err := gw.Ingest(context.Background(), "BA", raw)
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if sent.count("LH") != 1 {
		t.Fatalf("LH received %d messages, want the relayed one", sent.count("LH"))
	}
	if res.Status != store.StatusApplied {
		t.Errorf("Status = %q, want applied for successfully relayed transit", res.Status)
	}
	// Pure transit must not manufacture a record here.
	recs, _ := gw.Store.ListPNRs(context.Background(), 10)
	if len(recs) != 0 {
		t.Errorf("relaying created %d records; transit traffic is not ours to hold", len(recs))
	}
}

func TestRelayNeverReflectsToTheOrigin(t *testing.T) {
	gw, sent := switchNode(t, true)
	// Addressed to the very address it came from. Forwarding it would return
	// it to its sender, and on a store-and-forward network that loop
	// survives restarts.
	raw := []byte("QU LHRRMBA\n.LHRRMBA 121430\nHELLO\n")

	res, err := gw.Ingest(context.Background(), "BA", raw)
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if n := sent.count("BA"); n != 0 {
		t.Errorf("reflected %d messages to their origin, want 0", n)
	}
	if res.Status != store.StatusUndeliverable {
		t.Errorf("Status = %q, want undeliverable", res.Status)
	}
}

func TestRelayDeliversToAnotherAddressOnTheArrivalLink(t *testing.T) {
	gw, sent := switchNode(t, true)
	// BA's reservations system tells BA's check-in at Heathrow about a
	// departure. Both addresses live on BA's one circuit, and the network
	// delivers down it: that is not a loop, it is the job. Until the DCS
	// existed the switch refused this as a bounce, and a carrier could not
	// reach its own airport.
	raw := []byte("QU LHRKPBA LHRRSBA\n.LHRRMBA 121430\nPNL\nBA0117/16DEC LHR PART1\n-JFK001Y\n1SMITH/JOHNMR .L/ABC123\nENDPNL\n")

	res, err := gw.Ingest(context.Background(), "BA", raw)
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if n := sent.count("BA"); n != 2 {
		t.Errorf("delivered %d copies down BA's link, want one per address", n)
	}
	if res.Status != store.StatusApplied {
		t.Errorf("Status = %q", res.Status)
	}
}

func TestRelayOffByDefault(t *testing.T) {
	gw, sent := switchNode(t, false)
	raw := []byte("QU FRARMLH\n.LHRRMBA 121430\nSSR VGML LH HK1\n")

	if _, err := gw.Ingest(context.Background(), "BA", raw); err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if n := sent.count("LH"); n != 0 {
		t.Errorf("forwarded %d messages with relay off; an open relay must be opt-in", n)
	}
}

func TestRelayReportsWhenNoAddresseeIsRoutable(t *testing.T) {
	gw, _ := switchNode(t, true)
	raw := []byte("QU JFKRMAA\n.LHRRMBA 121430\nHELLO\n")

	res, err := gw.Ingest(context.Background(), "BA", raw)
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if res.Status != store.StatusUndeliverable {
		t.Errorf("Status = %q, want undeliverable when nothing could be routed", res.Status)
	}
}
