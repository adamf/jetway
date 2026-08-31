package gateway

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/adamf/jetway/pkg/edifact"
	"github.com/adamf/jetway/pkg/padis"
	"github.com/adamf/jetway/pkg/pnr"
	"github.com/adamf/jetway/pkg/store"
)

// edifactSwitch builds a relay-mode gateway with two subscribers and a sender
// that records what went down which link.
func edifactSwitch(t *testing.T) (*Gateway, map[string][][]byte) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st := store.NewMem()
	gw := New(Identity{Designator: "1X", TTYAddress: "XCHDD1X", Name: "switch"},
		st, NewBus(64), log, []byte("secret"))
	gw.Relay = true
	gw.AddPeer(&Peer{Name: "AA", Carrier: "AA", Format: store.FormatEDIFACT, TTYAddress: "DFWRMAA"})
	gw.AddPeer(&Peer{Name: "1G", Carrier: "", Format: store.FormatEDIFACT, TTYAddress: "LONDD1G"})
	carried := map[string][][]byte{}
	gw.Sender = SenderFunc(func(ctx context.Context, peer string, raw []byte) error {
		carried[peer] = append(carried[peer], raw)
		return nil
	})
	return gw, carried
}

func interchangeFor(t *testing.T, sender, recipient string) []byte {
	t.Helper()
	rec := &pnr.PNR{
		RecordLocator: "RELAY1", Status: pnr.StatusOpen,
		Passengers: []pnr.Passenger{{Ref: 1, Surname: "SMITH", Given: "JOHN", Title: "MR"}},
		Segments: []pnr.Segment{{
			Ref: 1, Type: pnr.SegmentAir, Carrier: recipient, FlightNum: "0100",
			Board: "JFK", Off: "DFW", Status: "NN", Seats: 1, WireDate: "15JUN",
		}},
	}
	ic, err := padis.BuildPAOREQ(rec, recipient, padis.BuildOptions{
		Sender:     edifact.Party{ID: sender, Qualifier: "ZZ"},
		Recipient:  edifact.Party{ID: recipient, Qualifier: "ZZ"},
		ControlRef: "R1", MessageRef: "1",
		Charset: edifact.CharsetUNOA,
	})
	if err != nil {
		t.Fatalf("build interchange: %v", err)
	}
	raw, err := ic.Encode(edifact.EncodeOptions{SegmentPerLine: true, Charset: edifact.CharsetUNOA})
	if err != nil {
		t.Fatalf("encode interchange: %v", err)
	}
	return raw
}

// The switch's whole job: an interchange from the GDS link addressed to AA
// goes down AA's link, byte for byte, and is not applied at the switch.
func TestEDIFACTRelayForwardsByUNBRecipient(t *testing.T) {
	gw, carried := edifactSwitch(t)
	raw := interchangeFor(t, "1G", "AA")

	res, err := gw.Ingest(context.Background(), "1G", raw)
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if res.Status != store.StatusApplied {
		t.Fatalf("transit should read as applied; got %s", res.Status)
	}
	if len(carried["AA"]) != 1 {
		t.Fatalf("AA's link carried %d messages, want 1", len(carried["AA"]))
	}
	if string(carried["AA"][0]) != string(raw) {
		t.Error("the relayed bytes differ from what arrived; a switch carries, it does not rewrite")
	}
	// Pure transit must create no booking at the switch.
	recs, _ := gw.Store.ListPNRs(context.Background(), 5)
	if len(recs) != 0 {
		t.Errorf("transit traffic created %d records at the switch", len(recs))
	}
}

func TestEDIFACTRelayUnknownRecipientIsUndeliverable(t *testing.T) {
	gw, carried := edifactSwitch(t)
	raw := interchangeFor(t, "1G", "ZZ")

	res, err := gw.Ingest(context.Background(), "1G", raw)
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if res.Status != store.StatusUndeliverable {
		t.Fatalf("an unknown subscriber must be undeliverable, loudly; got %s", res.Status)
	}
	for peer, msgs := range carried {
		if len(msgs) != 0 {
			t.Errorf("%s's link carried %d messages for an unknown recipient", peer, len(msgs))
		}
	}
}

func TestEDIFACTRelayNeverBouncesToSender(t *testing.T) {
	gw, carried := edifactSwitch(t)
	// AA sends an interchange addressed to AA: misconfigured, and the one
	// thing the switch must not do is reflect it.
	raw := interchangeFor(t, "AA", "AA")

	res, err := gw.Ingest(context.Background(), "AA", raw)
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if res.Status != store.StatusUndeliverable {
		t.Fatalf("got %s, want undeliverable", res.Status)
	}
	if len(carried["AA"]) != 0 {
		t.Error("the switch bounced a message back down the link it arrived on")
	}
}

// Addressed to the switch itself, an interchange is not transit: it falls
// through to normal processing.
func TestEDIFACTForSelfIsNotRelayed(t *testing.T) {
	gw, carried := edifactSwitch(t)
	raw := interchangeFor(t, "AA", "1X")

	if _, err := gw.Ingest(context.Background(), "AA", raw); err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	for peer, msgs := range carried {
		for _, m := range msgs {
			if string(m) == string(raw) {
				t.Errorf("an interchange for this node was relayed to %s", peer)
			}
		}
	}
}
