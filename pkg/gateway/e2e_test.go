package gateway

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/adamf/jetway/pkg/avail"
	"github.com/adamf/jetway/pkg/pnr"
	"github.com/adamf/jetway/pkg/store"
)

// node bundles a gateway with the store behind it, for assertions.
type node struct {
	gw *Gateway
	st *store.Mem
}

// wire builds a distribution node and one carrier node, connected by direct
// calls so the exchange is deterministic. Over a real link the same messages
// cross a socket; nothing in the pipeline depends on which it is.
func wire(t *testing.T, carrier string, format store.Format) (gds, air *node) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	gdsStore, airStore := store.NewMem(), store.NewMem()
	gdsGW := New(Identity{Designator: "1J", TTYAddress: "LONRM1J", Name: "jetway-gds"},
		gdsStore, NewBus(100), log, []byte("gds-secret"))
	airGW := New(Identity{Designator: carrier, TTYAddress: "LHRRM" + carrier, Name: carrier + "-res"},
		airStore, NewBus(100), log, []byte("carrier-secret"))

	inv := NewInventory()
	inv.Carrier = carrier
	inv.Capacity = 4
	airGW.Responder = inv

	gdsGW.AddPeer(&Peer{Name: carrier, Carrier: carrier, Format: format, TTYAddress: "LHRRM" + carrier})
	airGW.AddPeer(&Peer{Name: "1J", Carrier: "1J", Format: format, TTYAddress: "LONRM1J"})

	gdsGW.Sender = SenderFunc(func(ctx context.Context, peer string, raw []byte) error {
		_, err := airGW.Ingest(ctx, "1J", raw)
		return err
	})
	airGW.Sender = SenderFunc(func(ctx context.Context, peer string, raw []byte) error {
		_, err := gdsGW.Ingest(ctx, carrier, raw)
		return err
	})
	return &node{gdsGW, gdsStore}, &node{airGW, airStore}
}

func booking(class string, seats int) *BookingRequest {
	return &BookingRequest{
		Passengers: []BookingPassenger{{Surname: "smith", Given: "john", Title: "mr"}},
		Segments: []BookingSegment{{
			Carrier: "BA", FlightNum: "0175", Class: class,
			Date:  pnr.FormatDate(time.Now().UTC().AddDate(0, 0, 30)),
			Board: "lhr", Off: "jfk", Seats: seats,
		}},
		SSRs:    []BookingSSR{{Code: "VGML", Carrier: "BA"}},
		Contact: "LON 44-20-7777-7777",
		Agent:   "LON1A2B",
	}
}

func TestEndToEndTypeBConfirmation(t *testing.T) {
	ctx := context.Background()
	gds, air := wire(t, "BA", store.FormatTypeB)

	res, err := gds.gw.Book(ctx, booking("Y", 1))
	if err != nil {
		t.Fatalf("Book: %v", err)
	}
	if len(res.Sent) != 1 {
		t.Fatalf("expected one outbound request, got %d", len(res.Sent))
	}

	// The distribution side's record must now be confirmed by the reply that
	// came back during Book.
	rec, err := gds.st.GetPNR(ctx, res.PNR.RecordLocator)
	if err != nil {
		t.Fatalf("GetPNR: %v", err)
	}
	if len(rec.Segments) != 1 {
		t.Fatalf("segments = %d", len(rec.Segments))
	}
	if rec.Segments[0].Status != "HK" {
		t.Errorf("status = %q, want HK", rec.Segments[0].Status)
	}
	if rec.AwaitingReply() {
		t.Error("record should not still be awaiting a reply")
	}
	if _, ok := rec.LocatorFor("BA"); !ok {
		t.Errorf("carrier locator was not recorded: %+v", rec.Locators)
	}
	if len(rec.Unparsed) != 0 {
		t.Errorf("unparsed fragments on a clean exchange: %+v", rec.Unparsed)
	}

	// The carrier holds its own record for the same booking.
	airRecs, _ := air.st.ListPNRs(ctx, 10)
	if len(airRecs) != 1 {
		t.Fatalf("carrier records = %d, want 1", len(airRecs))
	}
	if got := airRecs[0].Segments[0].Status; got != "HK" {
		t.Errorf("carrier segment status = %q, want HK", got)
	}
	if len(airRecs[0].Passengers) != 1 || airRecs[0].Passengers[0].Surname != "SMITH" {
		t.Errorf("carrier did not receive the passenger: %+v", airRecs[0].Passengers)
	}

	// Both sides logged the exchange, with the exact bytes.
	gdsMsgs, _ := gds.st.ListMessages(ctx, store.MessageFilter{Limit: 50})
	if len(gdsMsgs) != 2 {
		t.Fatalf("gds messages = %d, want 2 (one out, one in)", len(gdsMsgs))
	}
	if gdsMsgs[0].Direction != store.Outbound || !strings.Contains(gdsMsgs[0].Kind, "sell") {
		t.Errorf("first gds message = %+v", gdsMsgs[0])
	}
	if gdsMsgs[1].Direction != store.Inbound || gdsMsgs[1].Status != store.StatusApplied {
		t.Errorf("second gds message = %+v", gdsMsgs[1])
	}
	if !strings.Contains(string(gdsMsgs[0].Raw), "BA0175Y") {
		t.Errorf("sell message does not carry the segment:\n%s", gdsMsgs[0].Raw)
	}
	if !strings.HasPrefix(string(gdsMsgs[0].Raw), "QU LHRRMBA") {
		t.Errorf("sell message is not addressed to the carrier:\n%s", gdsMsgs[0].Raw)
	}

	// Every change carries the id of the message that caused it.
	events, _ := gds.st.Events(ctx, rec.ID)
	if len(events) == 0 {
		t.Fatal("no events recorded")
	}
	var withProvenance int
	for _, e := range events {
		if e.MessageID != "" {
			withProvenance++
		}
	}
	if withProvenance == 0 {
		t.Error("no event names the message that caused it")
	}
}

func TestEndToEndEDIFACTConfirmation(t *testing.T) {
	ctx := context.Background()
	gds, air := wire(t, "BA", store.FormatEDIFACT)

	res, err := gds.gw.Book(ctx, booking("Y", 2))
	if err != nil {
		t.Fatalf("Book: %v", err)
	}
	rec, _ := gds.st.GetPNR(ctx, res.PNR.RecordLocator)
	if rec.Segments[0].Status != "HK" {
		t.Errorf("status = %q, want HK", rec.Segments[0].Status)
	}
	if rec.Segments[0].Seats != 2 {
		t.Errorf("seats = %d, want 2", rec.Segments[0].Seats)
	}

	msgs, _ := gds.st.ListMessages(ctx, store.MessageFilter{Limit: 10})
	if len(msgs) != 2 {
		t.Fatalf("messages = %d, want 2", len(msgs))
	}
	if msgs[0].Kind != "PAOREQ" || msgs[1].Kind != "PAORES" {
		t.Errorf("kinds = %q, %q; want PAOREQ, PAORES", msgs[0].Kind, msgs[1].Kind)
	}
	if !strings.HasPrefix(string(msgs[0].Raw), "UNB+UNOA:3+1J:ZZ+BA:ZZ") {
		t.Errorf("PAOREQ envelope wrong:\n%s", msgs[0].Raw)
	}
	for _, m := range msgs {
		for _, d := range m.Diagnostics {
			if d.Severity == "error" {
				t.Errorf("error diagnostic on a clean exchange: %+v", d)
			}
		}
	}
	airRecs, _ := air.st.ListPNRs(ctx, 10)
	if len(airRecs) != 1 || airRecs[0].Segments[0].Status != "HK" {
		t.Errorf("carrier record wrong: %+v", airRecs)
	}
}

// A booking class the carrier never sells must come back refused, and the
// record must reflect that rather than silently staying at HN.
func TestRefusalIsApplied(t *testing.T) {
	ctx := context.Background()
	gds, _ := wire(t, "BA", store.FormatTypeB)
	res, err := gds.gw.Book(ctx, booking("Z", 1))
	if err != nil {
		t.Fatalf("Book: %v", err)
	}
	rec, _ := gds.st.GetPNR(ctx, res.PNR.RecordLocator)
	if rec.Segments[0].Status != "UC" {
		t.Errorf("status = %q, want UC", rec.Segments[0].Status)
	}
	if rec.Status != pnr.StatusCancelled {
		t.Errorf("record status = %q, want cancelled", rec.Status)
	}
}

// Once capacity is gone the carrier waitlists, and the requester must record a
// waitlist holding rather than a confirmation.
func TestWaitlistWhenSoldOut(t *testing.T) {
	ctx := context.Background()
	gds, _ := wire(t, "BA", store.FormatTypeB)

	var last string
	for i := 0; i < 5; i++ {
		res, err := gds.gw.Book(ctx, booking("Y", 1))
		if err != nil {
			t.Fatalf("Book %d: %v", i, err)
		}
		last = res.PNR.RecordLocator
	}
	rec, _ := gds.st.GetPNR(ctx, last)
	if got := rec.Segments[0].Status; got != "HL" {
		t.Errorf("fifth booking status = %q, want HL once the four seats are gone", got)
	}
}

// A retransmission is the normal failure mode of a store-and-forward link. It
// must be recorded and refused, not applied a second time.
func TestDuplicateIsRejectedNotReapplied(t *testing.T) {
	ctx := context.Background()
	gds, air := wire(t, "BA", store.FormatTypeB)
	if _, err := gds.gw.Book(ctx, booking("Y", 1)); err != nil {
		t.Fatalf("Book: %v", err)
	}

	msgs, _ := air.st.ListMessages(ctx, store.MessageFilter{Limit: 10})
	var sell *store.Message
	for _, m := range msgs {
		if m.Direction == store.Inbound {
			sell = m
			break
		}
	}
	if sell == nil {
		t.Fatal("carrier has no inbound message to replay")
	}
	before, _ := air.st.ListPNRs(ctx, 10)

	res, err := air.gw.Ingest(ctx, "1J", sell.Raw)
	if err != nil {
		t.Fatalf("re-ingest: %v", err)
	}
	if !res.Duplicate {
		t.Error("replayed message was not recognised as a duplicate")
	}
	if res.Status != store.StatusRejected {
		t.Errorf("status = %q, want rejected", res.Status)
	}
	after, _ := air.st.ListPNRs(ctx, 10)
	if len(after) != len(before) {
		t.Errorf("duplicate created a second record: %d -> %d", len(before), len(after))
	}
	// The duplicate itself must still be in the log: it happened.
	all, _ := air.st.ListMessages(ctx, store.MessageFilter{Limit: 50})
	var rejected int
	for _, m := range all {
		if m.Status == store.StatusRejected {
			rejected++
		}
	}
	if rejected != 1 {
		t.Errorf("rejected messages = %d, want 1", rejected)
	}
}

// Undecodable bytes must land in the dead letter queue with the raw content
// intact, never be dropped.
func TestGarbageGoesToDeadLetterQueue(t *testing.T) {
	ctx := context.Background()
	_, air := wire(t, "BA", store.FormatTypeB)

	res, err := air.gw.Ingest(ctx, "1J", []byte("\x00\x01\x02"))
	if err != nil {
		t.Fatalf("Ingest returned a hard error: %v", err)
	}
	if res.Status != store.StatusDLQ {
		t.Errorf("status = %q, want dlq", res.Status)
	}
	m, err := air.st.GetMessage(ctx, res.MessageID)
	if err != nil {
		t.Fatalf("the message must still be retrievable: %v", err)
	}
	if len(m.Raw) != 3 {
		t.Errorf("raw bytes were not preserved: %q", m.Raw)
	}
	if m.Error == "" {
		t.Error("dead-lettered message should carry the reason")
	}
}

// A dialect the profile does not know must still produce a booking, with the
// unrecognised content attached to the record.
func TestUnknownDialectDegradesToFragment(t *testing.T) {
	ctx := context.Background()
	_, air := wire(t, "BA", store.FormatTypeB)

	raw := "QU LHRRMBA\r\n.LONRM1J 011200\r\n" +
		"BA0175Y" + pnr.FormatDate(time.Now().UTC().AddDate(0, 0, 30)) + "LHRJFKNN1\r\n" +
		"1SMITH/JOHNMR\r\n" +
		"XQ SOMETHING ONLY THIS CARRIER SENDS\r\n"
	res, err := air.gw.Ingest(ctx, "1J", []byte(raw))
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if res.Status != store.StatusApplied {
		t.Fatalf("status = %q, want applied", res.Status)
	}
	rec, _ := air.st.GetPNRByID(ctx, res.PNRID)
	if len(rec.Segments) != 1 {
		t.Errorf("the recognised segment should still be booked: %+v", rec.Segments)
	}
	if len(rec.Unparsed) != 1 {
		t.Fatalf("unparsed = %d, want 1: %+v", len(rec.Unparsed), rec.Unparsed)
	}
	if !strings.Contains(rec.Unparsed[0].Raw, "ONLY THIS CARRIER SENDS") {
		t.Errorf("fragment lost the original line: %+v", rec.Unparsed[0])
	}
}

func TestBookingValidation(t *testing.T) {
	ctx := context.Background()
	gds, _ := wire(t, "BA", store.FormatTypeB)
	bad := []*BookingRequest{
		{},
		{Passengers: []BookingPassenger{{Surname: "X", Given: "Y"}}},
		{Passengers: []BookingPassenger{{Surname: "X", Given: "Y"}},
			Segments: []BookingSegment{{Carrier: "BAA", FlightNum: "1", Class: "Y", Date: "15JUN", Board: "LHR", Off: "JFK"}}},
		{Passengers: []BookingPassenger{{Surname: "X", Given: "Y"}},
			Segments: []BookingSegment{{Carrier: "BA", FlightNum: "1", Class: "Y", Date: "99XXX", Board: "LHR", Off: "JFK"}}},
	}
	for i, b := range bad {
		if _, err := gds.gw.Book(ctx, b); err == nil {
			t.Errorf("case %d: expected validation failure", i)
		}
	}
}

// A cancellation must stay cancelled after the carrier hears about it. The
// EDIFACT path once failed this: the cancel went out stamped as a request, the
// carrier's responder had nothing to decide (an XX segment is not awaiting a
// decision), and the reply builder's fallback refused every undecided segment
// with NO -- which resurrected the cancelled record when it landed back at the
// distribution side. The message counts pin the conservation law: a cancel is
// an advisory, so nothing travels back.
func TestEndToEndCancelStaysCancelled(t *testing.T) {
	for _, format := range []store.Format{store.FormatTypeB, store.FormatEDIFACT} {
		t.Run(string(format), func(t *testing.T) {
			ctx := context.Background()
			gds, air := wire(t, "BA", format)

			res, err := gds.gw.Book(ctx, booking("Y", 1))
			if err != nil {
				t.Fatalf("Book: %v", err)
			}
			rec, _ := gds.st.GetPNR(ctx, res.PNR.RecordLocator)
			if rec.Segments[0].Status != "HK" {
				t.Fatalf("pre-cancel status = %q, want HK", rec.Segments[0].Status)
			}

			if _, err := gds.gw.Cancel(ctx, rec.RecordLocator, CancelOptions{
				By: "test", Reason: "regression",
			}); err != nil {
				t.Fatalf("Cancel: %v", err)
			}

			// The senders are direct calls, so by now any reply the carrier
			// was ever going to make has already been applied.
			rec, _ = gds.st.GetPNR(ctx, rec.RecordLocator)
			if rec.Status != pnr.StatusCancelled {
				t.Errorf("distribution record = %q, want cancelled", rec.Status)
			}
			for _, s := range rec.Segments {
				if s.Status != "XX" {
					t.Errorf("segment %d = %q, want XX", s.Ref, s.Status)
				}
			}

			airRecs, _ := air.st.ListPNRs(ctx, 10)
			if len(airRecs) != 1 {
				t.Fatalf("carrier records = %d, want 1", len(airRecs))
			}
			if airRecs[0].Status != pnr.StatusCancelled {
				t.Errorf("carrier record = %q, want cancelled", airRecs[0].Status)
			}

			// Book is two messages each side (request out, reply back); the
			// cancel is exactly one more. A fourth message is the bug.
			gdsMsgs, _ := gds.st.ListMessages(ctx, store.MessageFilter{Limit: 50})
			if len(gdsMsgs) != 3 {
				t.Errorf("distribution side logged %d messages, want 3 (sell, reply, cancel)", len(gdsMsgs))
				for _, m := range gdsMsgs {
					t.Logf("  %s %s %s", m.Direction, m.Kind, m.Status)
				}
			}
			airMsgs, _ := air.st.ListMessages(ctx, store.MessageFilter{Limit: 50})
			if len(airMsgs) != 3 {
				t.Errorf("carrier side logged %d messages, want 3 (sell, reply, cancel)", len(airMsgs))
			}
		})
	}
}

// A warm availability cache must never make service worse. With free sale
// exhausted the next booking falls back to asking the carrier -- which
// waitlists it -- rather than being refused on the strength of the GDS's own
// bookkeeping. Six bookings against four seats and a warm cache settle
// exactly as they would have cold: four confirmed, two waitlisted.
func TestFreeSaleExhaustionFallsBackToRequest(t *testing.T) {
	ctx := context.Background()
	gds, _ := wire(t, "BA", store.FormatTypeB)

	depart := time.Now().UTC().AddDate(0, 0, 30)
	gds.gw.Avail = avail.NewCache()
	gds.gw.Avail.Put(avail.Entry{
		Key:    avail.NewKey("BA", "0175", depart, "LHR", "JFK", "Y"),
		Status: avail.Open, Seats: 4, SeatsKnown: true,
		Source: avail.SourceAVS, AsOf: time.Now(),
	})

	statuses := map[string]int{}
	for i := 0; i < 6; i++ {
		res, err := gds.gw.Book(ctx, booking("Y", 1))
		if err != nil {
			t.Fatalf("booking %d refused: %v", i+1, err)
		}
		rec, _ := gds.st.GetPNR(ctx, res.PNR.RecordLocator)
		statuses[rec.Segments[0].Status]++
	}
	if statuses["HK"] != 4 || statuses["HL"] != 2 {
		t.Errorf("outcomes = %v, want 4 HK and 2 HL", statuses)
	}
}

// A PNL is for an airport and a BSM is about a bag; both must classify,
// file, and leave the record store alone. Feeding either to the booking
// grammar would spray unrecognised-element diagnostics and, worse, invent a
// record no passenger ever made.
func TestNameListAndBaggageClassifyWithoutBookings(t *testing.T) {
	ctx := context.Background()
	_, air := wire(t, "BA", store.FormatTypeB)

	wrap := func(text string) []byte {
		return []byte("QU LHRRMBA\n.LONRM1J 010900\n" + text)
	}
	pnlText := "PNL\nBA0117/16DEC LHR PART1\n-JFK02Y\n1SMITH/JOHNMR .L/AB12CD\n1JONES/AMYMS\nENDPNL"
	bsmText := "BSM\n.V/1LLHR\n.F/BA0117/16DEC/JFK/Y\n.N/0125999888001\n.P/SMITH/JOHN\nENDBSM"

	for _, tc := range []struct{ text, wantKind string }{
		{pnlText, "PNL/BA0117"},
		{bsmText, "BSM/BA0117"},
	} {
		res, err := air.gw.Ingest(ctx, "1J", wrap(tc.text))
		if err != nil {
			t.Fatalf("ingest %s: %v", tc.wantKind, err)
		}
		m, err := air.st.GetMessage(ctx, res.MessageID)
		if err != nil {
			t.Fatalf("GetMessage: %v", err)
		}
		if m.Kind != tc.wantKind {
			t.Errorf("kind = %q, want %q", m.Kind, tc.wantKind)
		}
	}
	recs, _ := air.st.ListPNRs(ctx, 10)
	if len(recs) != 0 {
		t.Errorf("a name list or bag message created %d records", len(recs))
	}
	msgs, _ := air.st.ListMessages(ctx, store.MessageFilter{Limit: 10})
	if len(msgs) != 2 {
		t.Errorf("messages filed = %d, want 2", len(msgs))
	}
	for _, m := range msgs {
		if m.Status == store.StatusDLQ || m.Status == store.StatusRejected {
			t.Errorf("message %s (%s) landed at %s", m.ID, m.Kind, m.Status)
		}
	}
}
