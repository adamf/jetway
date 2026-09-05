package gateway

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/adamf/jetway/pkg/acars"
	"github.com/adamf/jetway/pkg/aftn"
	"github.com/adamf/jetway/pkg/atfm"
	"github.com/adamf/jetway/pkg/ats"
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
	oooi     []*acars.Message
	ats      []*ats.Message
	atfm     []*atfm.Message
	origins  []string
	refuseOn string
}

func (r *recorder) Datalink(ctx context.Context, m *acars.Message, o typeb.Address) error {
	r.oooi = append(r.oooi, m)
	r.origins = append(r.origins, o.String())
	return nil
}

func (r *recorder) ATS(ctx context.Context, m *ats.Message, env *aftn.Message) error {
	r.ats = append(r.ats, m)
	r.origins = append(r.origins, env.Originator)
	return nil
}

func (r *recorder) ATFM(ctx context.Context, m *atfm.Message, env *aftn.Message) error {
	r.atfm = append(r.atfm, m)
	r.origins = append(r.origins, env.Originator)
	return nil
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

// The other networks reach the ground seam: an aircraft's datalink report
// over Type B, and an air traffic services message over the AFTN.
func TestGroundReceivesDatalinkAndATS(t *testing.T) {
	gw, rec := carrierNode(t)
	gw.Identity.AFTNAddress = "EGLLBAWO"
	ctx := context.Background()

	res, err := gw.Ingest(ctx, "net", tty("LHRKLBA", "LONXSXS", "DEP\nFI BA117/AN G-BZHA/DA EGLL/DS KJFK/OT 1207/OF 1219/FB 62400\nDT SIT LHR 261219 M01A"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != store.StatusApplied {
		t.Errorf("OOOI status %s", res.Status)
	}
	if len(rec.oooi) != 1 || rec.oooi[0].Off != "1219" || rec.oooi[0].Registration != "G-BZHA" {
		t.Errorf("datalink report not handed over: %+v", rec.oooi)
	}
	if msg, _ := gw.Store.GetMessage(ctx, res.MessageID); msg == nil || msg.Kind != "ACARS/DEP/BA117" {
		t.Errorf("kind %+v", msg)
	}

	env := &aftn.Message{TransmissionID: "LPA001", Priority: aftn.PrioritySafety, Addressees: []string{"EGLLBAWO"},
		FilingTime: "261220", Originator: "EGLLZPZX", Text: "(DEP-BAW117-EGLL1219-KJFK-DOF/251126)"}
	raw, err := env.Encode(aftn.EncodeOptions{CRLF: true})
	if err != nil {
		t.Fatal(err)
	}
	res, err = gw.Ingest(ctx, "net", raw)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != store.StatusApplied {
		t.Errorf("ATS status %s", res.Status)
	}
	if len(rec.ats) != 1 || rec.ats[0].Type != ats.TypeDEP || rec.ats[0].EOBT != "1219" || rec.origins[len(rec.origins)-1] != "EGLLZPZX" {
		t.Errorf("ATS message not handed over: %+v %v", rec.ats, rec.origins)
	}
	if msg, _ := gw.Store.GetMessage(ctx, res.MessageID); msg == nil || msg.Format != store.FormatAFTN || msg.Kind != "ATS/DEP/BAW117" {
		t.Errorf("filed as %+v", msg)
	}
}

// A slot allocation from the Network Manager reaches the operations centre
// through the same seam: parsed, handed over with its envelope, filed under
// its own kind. A Ground that does not listen for flow management has it
// filed and nothing more.
func TestGroundReceivesSlotMessages(t *testing.T) {
	gw, rec := carrierNode(t)
	gw.Identity.AFTNAddress = "EGLLBAWO"
	ctx := context.Background()
	env := &aftn.Message{TransmissionID: "NMA001", Priority: aftn.PriorityRegularity, Addressees: []string{"EGLLBAWO"},
		FilingTime: "260600", Originator: "EUCHZMFP",
		Text: "-TITLE SAM\n-ARCID BAW117\n-IFPLID AA00000117\n-ADEP EGLL\n-ADES KJFK\n-EOBD 261126\n-EOBT 0800\n-CTOT 0855\n-REGUL KJFKA26M\n-TAXITIME 0020\n-REGCAUSE WA 84"}
	raw, err := env.Encode(aftn.EncodeOptions{CRLF: true})
	if err != nil {
		t.Fatal(err)
	}
	res, err := gw.Ingest(ctx, "net", raw)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != store.StatusApplied {
		t.Errorf("SAM status %s", res.Status)
	}
	if len(rec.atfm) != 1 || rec.atfm[0].CTOT != "0855" || rec.atfm[0].REGCAUSE == nil || rec.atfm[0].REGCAUSE.IATA != "84" || rec.origins[len(rec.origins)-1] != "EUCHZMFP" {
		t.Errorf("slot not handed over: %+v", rec.atfm)
	}
	if msg, _ := gw.Store.GetMessage(ctx, res.MessageID); msg == nil || msg.Format != store.FormatAFTN || msg.Kind != "ATFM/SAM/BAW117" {
		t.Errorf("filed as %+v", msg)
	}
	// Without a listener the slot is filed, not refused.
	gw.Ground = GroundFuncs{}
	if res, err := gw.Ingest(ctx, "net", raw); err != nil || res.Status != store.StatusApplied {
		t.Errorf("unlistened slot: %+v %v", res, err)
	}
}

// A switch carries AFTN traffic by indicator: an airline's flight plan to
// air traffic services, and the tower's departure message back to the
// airline by the designator in the addressee.
func TestSwitchRelaysAFTNByIndicator(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	gw := New(Identity{Designator: "1X", TTYAddress: "XCHDD1X", Name: "switch"}, store.NewMem(), NewBus(100), log, []byte("s"))
	gw.Relay = true
	gw.AddPeer(&Peer{Name: "BA", Carrier: "BA", ICAO: "BAW", Format: store.FormatTypeB, TTYAddress: "LHRRMBA"})
	gw.AddPeer(&Peer{Name: "ATC", AFTN: true, Format: store.FormatAFTN, TTYAddress: "XXXXXATC"})
	carried := map[string][][]byte{}
	gw.Sender = SenderFunc(func(ctx context.Context, peer string, raw []byte) error {
		carried[peer] = append(carried[peer], raw)
		return nil
	})
	ctx := context.Background()

	fpl := &aftn.Message{Priority: aftn.PrioritySafety, Addressees: []string{"EGLLZPZX", "KJFKZQZX"},
		FilingTime: "261100", Originator: "EGLLBAWO",
		Text: "(FPL-BAW117-IS\n-B772/H-SDE3FGHIRWXY/LB1\n-EGLL1200\n-N0480F350 DCT CPT UL9 KENET UN14 STU DCT\n-KJFK0700 KBOS\n-DOF/251126 REG/GBZHA)"}
	raw, _ := fpl.Encode(aftn.EncodeOptions{})
	res, err := gw.Ingest(ctx, "BA", raw)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != store.StatusApplied || len(carried["ATC"]) != 2 {
		t.Errorf("flight plan: status %s, ATC received %d copies (two indicators, one link)", res.Status, len(carried["ATC"]))
	}
	if len(carried["BA"]) != 0 {
		t.Error("the flight plan went back to the airline")
	}
	dep := &aftn.Message{Priority: aftn.PrioritySafety, Addressees: []string{"KJFKBAWX"},
		FilingTime: "261220", Originator: "EGLLZPZX", Text: "(DEP-BAW117-EGLL1219-KJFK-DOF/251126)"}
	raw, _ = dep.Encode(aftn.EncodeOptions{})
	if _, err := gw.Ingest(ctx, "ATC", raw); err != nil {
		t.Fatal(err)
	}
	if len(carried["BA"]) != 1 {
		t.Errorf("the departure message did not reach the airline by its designator: BA got %d", len(carried["BA"]))
	}
	msgs, _ := gw.Store.ListMessages(ctx, store.MessageFilter{Limit: 10, Peer: "BA"})
	found := false
	for _, m := range msgs {
		if m.Direction == store.Outbound && m.Kind == "relay/ATS/DEP/BAW117" {
			found = true
		}
	}
	if !found {
		t.Errorf("relayed copy not filed under its kind: %+v", msgs)
	}
	if p := gw.PeerByAddress("LFPGZPZX"); p == nil || p.Name != "ATC" {
		t.Errorf("an unknown ATS indicator should route to the AFTN link: %v", p)
	}
	if p := gw.PeerByAddress("LFPGBAWX"); p == nil || p.Name != "BA" {
		t.Errorf("BA's indicator at Paris should route to BA: %v", p)
	}
}
