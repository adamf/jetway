package gateway

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/adamf/jetway/pkg/edifact"
	"github.com/adamf/jetway/pkg/padis"
	"github.com/adamf/jetway/pkg/pnr"
	"github.com/adamf/jetway/pkg/store"
)

// ticketControlNode is a gateway with two EDIFACT carriers, so a coupon can be
// offered to the wrong one.
func ticketControlNode(t *testing.T) (*Gateway, *sentTo) {
	t.Helper()
	gw, sent := cancelNode(t)
	// cancelNode makes BA teletype; ticket control is EDIFACT only.
	gw.AddPeer(&Peer{Name: "BA", Carrier: "BA", Format: store.FormatEDIFACT, TTYAddress: "LHRRMBA"})
	return gw, sent
}

func ticketedInterline(t *testing.T, gw *Gateway, locator string) *pnr.PNR {
	t.Helper()
	interlineRecord(t, gw, locator)
	rec, err := gw.IssueTickets(context.Background(), locator,
		IssueOptions{AirlineCode: "125", IssuedBy: "adam"})
	if err != nil {
		t.Fatalf("IssueTickets: %v", err)
	}
	return rec
}

func TestIssuanceTellsTheOperatingCarriers(t *testing.T) {
	gw, sent := ticketControlNode(t)
	rec := ticketedInterline(t, gw, "TKC001")

	// One document, two coupons, two operating carriers: each hears about its
	// own coupon and no other.
	for _, carrier := range []string{"AA", "BA"} {
		found := false
		for _, raw := range sent.msgs[carrier] {
			ic, err := edifact.Parse(raw, edifact.ParseOptions{})
			if err != nil || len(ic.Messages) == 0 {
				continue
			}
			if ic.Messages[0].ID().Type != padis.MsgTKCREQ {
				continue
			}
			found = true
			tc, err := padis.ParseTicketControl(ic.Messages[0])
			if err != nil {
				t.Fatalf("ParseTicketControl: %v", err)
			}
			if tc.Number.Compact() != rec.Tickets[0].Number.Compact() {
				t.Errorf("%s was told about %s, want %s", carrier, tc.Number, rec.Tickets[0].Number)
			}
			if len(tc.Coupons) != 1 {
				t.Errorf("%s was told about %d coupons, want only its own", carrier, len(tc.Coupons))
			}
			if tc.Coupons[0].Status != pnr.CouponOpen {
				t.Errorf("a freshly issued coupon should be open, got %q", tc.Coupons[0].Status)
			}
		}
		if !found {
			t.Errorf("%s was never told a ticket covers its segment", carrier)
		}
	}
}

// tkcreq builds what a carrier would send to report a coupon status.
func tkcreq(t *testing.T, number pnr.TicketNumber, from string, coupon int, status pnr.CouponStatus) []byte {
	t.Helper()
	ic, err := padis.BuildTKCREQ(nil, number, 2,
		[]padis.CouponRef{{Number: coupon, Status: status}}, padis.BuildOptions{
			Sender: edifact.Party{ID: from, Qualifier: "ZZ"}, Recipient: edifact.Party{ID: "1J", Qualifier: "ZZ"},
			// A distinct control reference per call: identical ones make the
			// second message a retransmission and it is deduplicated before it
			// is ever applied.
			ControlRef: (&Gateway{}).nextControlRef(), MessageRef: "1",
		})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := ic.Encode(edifact.EncodeOptions{SegmentPerLine: true})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func lastTKCRES(t *testing.T, sent *sentTo, peer string) *padis.TicketControl {
	t.Helper()
	sent.mu.Lock()
	defer sent.mu.Unlock()
	for i := len(sent.msgs[peer]) - 1; i >= 0; i-- {
		ic, err := edifact.Parse(sent.msgs[peer][i], edifact.ParseOptions{})
		if err != nil || len(ic.Messages) == 0 || ic.Messages[0].ID().Type != padis.MsgTKCRES {
			continue
		}
		tc, err := padis.ParseTicketControl(ic.Messages[0])
		if err != nil {
			t.Fatalf("ParseTicketControl: %v", err)
		}
		return tc
	}
	return nil
}

func TestCarrierReportsAFlownCoupon(t *testing.T) {
	gw, sent := ticketControlNode(t)
	ctx := context.Background()
	rec := ticketedInterline(t, gw, "TKC002")
	number := rec.Tickets[0].Number

	// Coupon 1 covers the AA segment. AA reports the passenger flew.
	if _, err := gw.Ingest(ctx, "AA", tkcreq(t, number, "AA", 1, pnr.CouponFlown)); err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	after, err := gw.Store.GetPNR(ctx, "TKC002")
	if err != nil {
		t.Fatal(err)
	}
	if got := after.Tickets[0].Coupons[0].Status; got != pnr.CouponFlown {
		t.Errorf("coupon 1 = %q, want F", got)
	}
	// The record is no longer fully covered once a coupon is flown.
	if after.Tickets[0].Covers(1) {
		t.Error("a flown coupon no longer covers its segment")
	}

	res := lastTKCRES(t, sent, "AA")
	if res == nil {
		t.Fatal("no response was sent")
	}
	if res.Refusal != "" {
		t.Errorf("a valid change was refused: %q", res.Refusal)
	}
	if len(res.Coupons) != 1 || res.Coupons[0].Status != pnr.CouponFlown {
		t.Errorf("response = %+v", res.Coupons)
	}
}

func TestFinalCouponCannotMove(t *testing.T) {
	gw, sent := ticketControlNode(t)
	ctx := context.Background()
	rec := ticketedInterline(t, gw, "TKC003")
	number := rec.Tickets[0].Number

	if _, err := gw.Ingest(ctx, "AA", tkcreq(t, number, "AA", 1, pnr.CouponRefunded)); err != nil {
		t.Fatal(err)
	}
	// Refunded is final; no follow-up is permitted.
	if _, err := gw.Ingest(ctx, "AA", tkcreq(t, number, "AA", 1, pnr.CouponFlown)); err != nil {
		t.Fatal(err)
	}

	after, _ := gw.Store.GetPNR(ctx, "TKC003")
	if got := after.Tickets[0].Coupons[0].Status; got != pnr.CouponRefunded {
		t.Errorf("coupon moved off a final status to %q", got)
	}
	res := lastTKCRES(t, sent, "AA")
	if res == nil || res.Refusal == "" {
		t.Fatalf("the second change should have been refused, got %+v", res)
	}
	if !containsFold(res.Refusal, "no follow-up is permitted") {
		t.Errorf("the refusal should say why: %q", res.Refusal)
	}
}

func TestCarrierCannotTouchAnothersCoupon(t *testing.T) {
	gw, sent := ticketControlNode(t)
	ctx := context.Background()
	rec := ticketedInterline(t, gw, "TKC004")
	number := rec.Tickets[0].Number

	// Coupon 2 covers the BA segment. AA has no business moving it; letting
	// any partner move any coupon would make the document worth nothing.
	if _, err := gw.Ingest(ctx, "AA", tkcreq(t, number, "AA", 2, pnr.CouponFlown)); err != nil {
		t.Fatal(err)
	}

	after, _ := gw.Store.GetPNR(ctx, "TKC004")
	if got := after.Tickets[0].Coupons[1].Status; got != pnr.CouponOpen {
		t.Errorf("coupon 2 = %q, want it untouched", got)
	}
	res := lastTKCRES(t, sent, "AA")
	if res == nil || !containsFold(res.Refusal, "does not operate") {
		t.Fatalf("expected a refusal naming the reason, got %+v", res)
	}
}

func TestUnknownDocumentIsRefusedNotInvented(t *testing.T) {
	gw, sent := ticketControlNode(t)
	ctx := context.Background()
	stranger, err := pnr.NewTicketNumber("999", "123456789")
	if err != nil {
		t.Fatal(err)
	}

	res, err := gw.Ingest(ctx, "AA", tkcreq(t, stranger, "AA", 1, pnr.CouponFlown))
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != store.StatusRejected {
		t.Errorf("Status = %q, want rejected", res.Status)
	}
	answer := lastTKCRES(t, sent, "AA")
	if answer == nil || !containsFold(answer.Refusal, "no record holds") {
		t.Errorf("a document we never issued must be refused plainly, got %+v", answer)
	}
	recs, _ := gw.Store.ListPNRs(ctx, 10)
	for _, r := range recs {
		if len(r.Tickets) > 0 && r.Tickets[0].Number.Compact() == stranger.Compact() {
			t.Error("a ticket control message invented a document")
		}
	}
}

func TestUnknownCouponStatusIsRefused(t *testing.T) {
	gw, sent := ticketControlNode(t)
	ctx := context.Background()
	rec := ticketedInterline(t, gw, "TKC005")

	if _, err := gw.Ingest(ctx, "AA", tkcreq(t, rec.Tickets[0].Number, "AA", 1, "Q")); err != nil {
		t.Fatal(err)
	}
	after, _ := gw.Store.GetPNR(ctx, "TKC005")
	if after.Tickets[0].Coupons[0].Status != pnr.CouponOpen {
		t.Error("a status outside the published list must not be applied")
	}
	res := lastTKCRES(t, sent, "AA")
	if res == nil || !containsFold(res.Refusal, "not in the published list") {
		t.Errorf("expected a refusal naming the reason, got %+v", res)
	}
}

func TestTicketNotAdvisedBecomesADivergence(t *testing.T) {
	gw, _ := cancelNode(t) // BA stays a teletype link here
	ctx := context.Background()
	interlineRecord(t, gw, "TKC006")

	if _, err := gw.IssueTickets(ctx, "TKC006", IssueOptions{AirlineCode: "125", IssuedBy: "adam"}); err != nil {
		t.Fatal(err)
	}
	items, err := gw.Store.ListQueue(ctx, store.QueueFilter{Queue: store.QueueDivergence})
	if err != nil {
		t.Fatal(err)
	}
	// BA cannot be told over teletype, and a ticket the operating carrier does
	// not know about is a ticket that exists only here.
	if len(items) != 1 {
		t.Fatalf("expected one divergence for the teletype carrier, got %d", len(items))
	}
	if !strings.Contains(items[0].Reason, "was not told") {
		t.Errorf("reason = %q", items[0].Reason)
	}
	_ = time.Now
}

// containsFold matches ignoring case. A refusal reason is free text this node
// wrote, and it reaches the partner uppercased because UNOA has no lowercase.
func containsFold(haystack, needle string) bool {
	return strings.Contains(strings.ToUpper(haystack), strings.ToUpper(needle))
}

// The other half of interline ticketing, and the half a node only meets when it
// is the operating carrier rather than the issuer: being told that a document
// somebody else issued covers a segment you hold.
func TestOperatingCarrierRecordsAnAdvisedTicket(t *testing.T) {
	gw, sent := ticketControlNode(t)
	ctx := context.Background()

	// A booking this node holds, with the issuer's own locator against it --
	// which is what the advice will be matched on, since their document number
	// means nothing here yet.
	rec := interlineRecord(t, gw, "ADV001")
	rec.Locators = append(rec.Locators, pnr.ExternalLocator{Owner: "1V", Value: "ISS999"})
	if err := gw.Store.UpdatePNR(ctx, rec, rec.Version, nil); err != nil {
		t.Fatal(err)
	}

	number, err := pnr.NewTicketNumber("125", "555555555")
	if err != nil {
		t.Fatal(err)
	}
	ic, err := padis.BuildTKCREQ(
		&pnr.PNR{RecordLocator: "ISS999"}, number, 2,
		[]padis.CouponRef{{Number: 1, Status: pnr.CouponOpen}}, padis.BuildOptions{
			Sender: edifact.Party{ID: "1V", Qualifier: "ZZ"}, Recipient: edifact.Party{ID: "1J", Qualifier: "ZZ"},
			ControlRef: (&Gateway{}).nextControlRef(), MessageRef: "1",
		})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := ic.Encode(edifact.EncodeOptions{SegmentPerLine: true})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := gw.Ingest(ctx, "AA", raw); err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	after, err := gw.Store.GetPNR(ctx, "ADV001")
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Tickets) != 1 {
		t.Fatalf("the advised document was not recorded: %+v", after.Tickets)
	}
	if after.Tickets[0].Number.Compact() != number.Compact() {
		t.Errorf("recorded %s, want %s", after.Tickets[0].Number, number)
	}
	if after.Tickets[0].IssuedBy != "1V" {
		t.Errorf("IssuedBy = %q, want the issuer", after.Tickets[0].IssuedBy)
	}

	// And it is accepted, not refused: refusing would leave the issuer
	// believing this carrier does not know about a ticket it is flying.
	res := lastTKCRES(t, sent, "AA")
	if res == nil {
		t.Fatal("no response was sent")
	}
	if res.Refusal != "" {
		t.Errorf("the advice was refused: %q", res.Refusal)
	}
}

func TestAdviceForAnUnknownBookingIsStillRefused(t *testing.T) {
	gw, sent := ticketControlNode(t)
	ctx := context.Background()
	number, _ := pnr.NewTicketNumber("125", "777777777")

	ic, err := padis.BuildTKCREQ(
		&pnr.PNR{RecordLocator: "NOSUCH"}, number, 1,
		[]padis.CouponRef{{Number: 1, Status: pnr.CouponOpen}}, padis.BuildOptions{
			Sender: edifact.Party{ID: "1V", Qualifier: "ZZ"}, Recipient: edifact.Party{ID: "1J", Qualifier: "ZZ"},
			ControlRef: (&Gateway{}).nextControlRef(), MessageRef: "1",
		})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := ic.Encode(edifact.EncodeOptions{SegmentPerLine: true})
	if _, err := gw.Ingest(ctx, "AA", raw); err != nil {
		t.Fatal(err)
	}

	// A locator this node has never seen is not licence to invent a booking.
	recs, _ := gw.Store.ListPNRs(ctx, 10)
	if len(recs) != 0 {
		t.Errorf("advice for an unknown booking created %d records", len(recs))
	}
	res := lastTKCRES(t, sent, "AA")
	if res == nil || !containsFold(res.Refusal, "no record holds") {
		t.Errorf("expected a plain refusal, got %+v", res)
	}
}
