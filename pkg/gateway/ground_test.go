package gateway

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/adamf/jetway/pkg/baggage"
	"github.com/adamf/jetway/pkg/dcs"
	"github.com/adamf/jetway/pkg/pnl"
	"github.com/adamf/jetway/pkg/store"
	"github.com/adamf/jetway/pkg/typeb"
)

// recorder is a Ground that remembers what it was handed.
type recorder struct {
	lists    []*pnl.Message
	bags     []*baggage.Message
	deps     []*dcs.Message
	origins  []string
	refuseOn string
}

func (r *recorder) NameList(ctx context.Context, m *pnl.Message, o typeb.Address) error {
	r.lists = append(r.lists, m)
	r.origins = append(r.origins, o.String())
	if r.refuseOn == "list" {
		return errors.New("acceptance has begun")
	}
	return nil
}

func (r *recorder) Baggage(ctx context.Context, m *baggage.Message, o typeb.Address) error {
	r.bags = append(r.bags, m)
	r.origins = append(r.origins, o.String())
	return nil
}

func (r *recorder) Departure(ctx context.Context, m *dcs.Message, o typeb.Address) error {
	r.deps = append(r.deps, m)
	r.origins = append(r.origins, o.String())
	return nil
}

func carrierNode(t *testing.T) (*Gateway, *recorder) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	gw := New(Identity{Designator: "BA", TTYAddress: "LHRRMBA", Name: "BA"},
		store.NewMem(), NewBus(100), log, []byte("secret"))
	gw.AddPeer(&Peer{Name: "net", Format: store.FormatTypeB, TTYAddress: "XCHDD1X"})
	gw.Sender = SenderFunc(func(context.Context, string, []byte) error { return nil })
	rec := &recorder{}
	gw.Ground = rec
	return gw, rec
}

func tty(dest, origin, text string) []byte {
	return []byte("QU " + dest + "\n." + origin + " 161200\n" + text + "\n")
}

func TestGroundReceivesNameListsBagsAndDepartureOutput(t *testing.T) {
	gw, rec := carrierNode(t)
	ctx := context.Background()

	res, err := gw.Ingest(ctx, "net", tty("LHRKPBA", "LHRRMBA", strings.Join([]string{
		"PNL", "BA0117/16DEC LHR PART1", "-JFK001Y", "1SMITH/JOHNMR .L/ABC123", "ENDPNL"}, "\n")))
	if err != nil {
		t.Fatalf("PNL: %v", err)
	}
	if res.Status != store.StatusApplied {
		t.Errorf("PNL status %s", res.Status)
	}
	if len(rec.lists) != 1 || rec.lists[0].Flight != "BA0117" || rec.origins[0] != "LHRRMBA" {
		t.Errorf("name list not handed over: %+v %v", rec.lists, rec.origins)
	}

	if _, err := gw.Ingest(ctx, "net", tty("LHRKBBA", "LHRKPBA", strings.Join([]string{
		"BSM", ".V/1LLHR", ".F/BA0117/16DEC/JFK/Y", ".N/0125000001001", ".P/SMITH/JOHNMR", "ENDBSM"}, "\n"))); err != nil {
		t.Fatalf("BSM: %v", err)
	}
	if len(rec.bags) != 1 || rec.bags[0].Tags[0].Number != "0125000001" {
		t.Errorf("bag message not handed over: %+v", rec.bags)
	}

	res, err = gw.Ingest(ctx, "net", tty("LHRRMBA", "LHRKPBA", strings.Join([]string{
		"PFS", "BA0117/16DEC LHR PART1", "-JFK", "NOSHO", "1SMITH/JOHNMR .L/ABC123", "ENDPFS"}, "\n")))
	if err != nil {
		t.Fatalf("PFS: %v", err)
	}
	if msg, _ := gw.Store.GetMessage(ctx, res.MessageID); msg == nil || msg.Kind != "PFS/BA0117" {
		t.Errorf("PFS filed under the wrong kind: %+v", msg)
	}
	if len(rec.deps) != 1 || rec.deps[0].Kind != dcs.KindPFS || rec.deps[0].PFS.Groups[0].Items[0].Category != "NOSHO" {
		t.Errorf("departure message not handed over: %+v", rec.deps)
	}

	if _, err := gw.Ingest(ctx, "net", tty("JFKKLBA", "LHRKLBA", strings.Join([]string{
		"LDM", "BA0117/16.GBZHA.Y180.2/6", "-JFK.150/0/0.T2850.1/1200.3/1650.PAX/150.PAD/0", "SI NIL"}, "\n"))); err != nil {
		t.Fatalf("LDM: %v", err)
	}
	if len(rec.deps) != 2 || rec.deps[1].Kind != dcs.KindLDM || rec.deps[1].LDM.Destinations[0].Total != 2850 {
		t.Errorf("LDM not handed over: %+v", rec.deps)
	}
}

func TestGroundRefusalIsRecordedNotDropped(t *testing.T) {
	gw, rec := carrierNode(t)
	rec.refuseOn = "list"
	ctx := context.Background()
	res, err := gw.Ingest(ctx, "net", tty("LHRKPBA", "LHRRMBA", strings.Join([]string{
		"PNL", "BA0117/16DEC LHR PART1", "-JFK001Y", "1SMITH/JOHNMR .L/ABC123", "ENDPNL"}, "\n")))
	if err != nil {
		t.Fatalf("a refusal must not fail the ingest: %v", err)
	}
	if res.Status != store.StatusRejected {
		t.Errorf("status %s, want rejected", res.Status)
	}
	msg, err := gw.Store.GetMessage(ctx, res.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Status != store.StatusRejected || !strings.Contains(msg.Error, "acceptance has begun") {
		t.Errorf("ledger says %s %q", msg.Status, msg.Error)
	}
	if len(msg.Raw) == 0 {
		t.Error("the bytes were lost")
	}
}

func TestWithoutGroundTheOlderBehaviourHolds(t *testing.T) {
	gw, _ := carrierNode(t)
	gw.Ground = nil
	res, err := gw.Ingest(context.Background(), "net", tty("LHRRMBA", "LHRKPBA", strings.Join([]string{
		"PFS", "BA0117/16DEC LHR PART1", "-JFK", "NIL", "ENDPFS"}, "\n")))
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != store.StatusApplied {
		t.Errorf("filed as %s", res.Status)
	}
}

func TestUnreadableDepartureOutputGoesToTheDLQWithItsReason(t *testing.T) {
	gw, rec := carrierNode(t)
	res, err := gw.Ingest(context.Background(), "net", tty("JFKKLBA", "LHRKLBA", "LDM\nthis is not an identification line\n-JFK"))
	if err != nil {
		t.Fatalf("Ingest must still succeed; the bytes are captured: %v", err)
	}
	if res.Status != store.StatusDLQ {
		t.Errorf("status %s, want dlq", res.Status)
	}
	if res.Err == nil || !strings.Contains(res.Err.Error(), "dcs:") {
		t.Errorf("the reason should be the parser's, got %v", res.Err)
	}
	if len(rec.deps) != 0 {
		t.Error("an unreadable message was handed to the ground")
	}
}
