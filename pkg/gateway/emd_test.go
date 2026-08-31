package gateway

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/adamf/jetway/pkg/store"
	"github.com/adamf/jetway/pkg/pnr"
)

func emdNode(t *testing.T) (*Gateway, *sentTo) { return ticketControlNode(t) }

func baggageEMD(segmentRef int) EMDRequest {
	return EMDRequest{
		PaxRef: 1, Type: pnr.DocEMDA, RFIC: pnr.RFICBaggage, AirlineCode: "125",
		IssuedBy: "adam",
		Coupons: []EMDCoupon{{
			RFISC: "0CZ", SegmentRef: segmentRef, Amount: "60.00", Currency: "GBP",
		}},
	}
}

func TestIssueEMDAssociatedToAFlightCoupon(t *testing.T) {
	gw, _ := emdNode(t)
	ctx := context.Background()
	rec := ticketedInterline(t, gw, "EMD001")
	seg := rec.Segments[0].Ref

	req := baggageEMD(seg)
	req.Locator = "EMD001"
	after, doc, err := gw.IssueEMD(ctx, req)
	if err != nil {
		t.Fatalf("IssueEMD: %v", err)
	}
	if doc.Type != pnr.DocEMDA || doc.RFIC != pnr.RFICBaggage {
		t.Errorf("document = %+v", doc)
	}
	if !doc.Number.CheckDigitOK() {
		t.Errorf("issued a number failing its own check digit: %s", doc.Number)
	}
	// Stapled to the flight coupon covering that segment.
	assoc := doc.Coupons[0].Association
	if assoc.IsZero() || assoc.Coupon != 1 {
		t.Errorf("association = %+v, want the flight coupon for segment %d", assoc, seg)
	}
	if assoc.Document.Compact() != rec.Tickets[0].Number.Compact() {
		t.Errorf("associated to %s, want the flight ticket", assoc.Document)
	}
	// The EMD must not make the record look ticketed on its own, and must not
	// stop it being ticketed either.
	if len(after.EMDs()) != 1 || len(after.FlightTickets()) != 1 {
		t.Errorf("documents = %d EMD, %d ticket", len(after.EMDs()), len(after.FlightTickets()))
	}
	if !after.Ticketed() {
		t.Error("the flight ticket still covers the itinerary")
	}
}

func TestEMDStructuralRulesAreEnforced(t *testing.T) {
	gw, _ := emdNode(t)
	ctx := context.Background()
	rec := ticketedInterline(t, gw, "EMD002")
	seg := rec.Segments[0].Ref

	// An EMD-A stapled to nothing is stapled to nothing.
	bad := baggageEMD(999)
	bad.Locator = "EMD002"
	if _, _, err := gw.IssueEMD(ctx, bad); !errors.Is(err, ErrNotAssociable) {
		t.Errorf("EMD-A on an unticketed segment = %v, want ErrNotAssociable", err)
	}

	// A sub-code is mandatory: without it the document says a fee was charged
	// without saying what for.
	noSub := baggageEMD(seg)
	noSub.Locator = "EMD002"
	noSub.Coupons[0].RFISC = ""
	if _, _, err := gw.IssueEMD(ctx, noSub); err == nil {
		t.Error("a coupon with no sub-code should be refused")
	}

	// One of the seven published reason groups, or nothing.
	badRFIC := baggageEMD(seg)
	badRFIC.Locator = "EMD002"
	badRFIC.RFIC = "Q"
	if _, _, err := gw.IssueEMD(ctx, badRFIC); err == nil {
		t.Error("a reason for issuance outside the published groups should be refused")
	}

	// A flight ticket is not an EMD type.
	notEMD := baggageEMD(seg)
	notEMD.Locator = "EMD002"
	notEMD.Type = pnr.DocTicket
	if _, _, err := gw.IssueEMD(ctx, notEMD); err == nil {
		t.Error("IssueEMD accepted a flight ticket type")
	}
}

func TestStandaloneEMDTakesNoAssociation(t *testing.T) {
	gw, _ := emdNode(t)
	ctx := context.Background()
	ticketedInterline(t, gw, "EMD003")

	// A residual balance is not attached to any flight.
	_, doc, err := gw.IssueEMD(ctx, EMDRequest{
		Locator: "EMD003", PaxRef: 1, Type: pnr.DocEMDS, RFIC: pnr.RFICFinancial,
		AirlineCode: "125", IssuedBy: "adam",
		Coupons: []EMDCoupon{{RFISC: "99I", Amount: "40.00", Currency: "GBP"}},
	})
	if err != nil {
		t.Fatalf("IssueEMD: %v", err)
	}
	if !doc.Coupons[0].Association.IsZero() {
		t.Errorf("a standalone document claimed an association: %+v", doc.Coupons[0].Association)
	}
}

func TestConsumedAtIssuanceClosesTheCoupon(t *testing.T) {
	gw, _ := emdNode(t)
	ctx := context.Background()
	ticketedInterline(t, gw, "EMD004")

	_, doc, err := gw.IssueEMD(ctx, EMDRequest{
		Locator: "EMD004", PaxRef: 1, Type: pnr.DocEMDS, RFIC: pnr.RFICAirport,
		AirlineCode: "125", IssuedBy: "adam",
		Coupons: []EMDCoupon{{RFISC: "0B3", ConsumedAtIssuance: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Delivered at the counter: there is no later event to close it, so it
	// closes now rather than sitting open forever.
	if doc.Coupons[0].Status != pnr.CouponFlown {
		t.Errorf("Status = %q, want F", doc.Coupons[0].Status)
	}
}

func TestFlownFlightCouponLiftsWhatIsStapledToIt(t *testing.T) {
	gw, _ := emdNode(t)
	ctx := context.Background()
	rec := ticketedInterline(t, gw, "EMD005")
	seg := rec.Segments[0].Ref // the AA segment, coupon 1

	req := baggageEMD(seg)
	req.Locator = "EMD005"
	if _, _, err := gw.IssueEMD(ctx, req); err != nil {
		t.Fatal(err)
	}

	// AA reports the passenger flew the segment the excess baggage was for.
	if _, err := gw.Ingest(ctx, "AA",
		tkcreq(t, rec.Tickets[0].Number, "AA", 1, pnr.CouponFlown)); err != nil {
		t.Fatal(err)
	}

	after, err := gw.Store.GetPNR(ctx, "EMD005")
	if err != nil {
		t.Fatal(err)
	}
	emds := after.EMDs()
	if len(emds) != 1 {
		t.Fatalf("EMDs = %d", len(emds))
	}
	// The whole point of stapling them together.
	if got := emds[0].Coupons[0].Status; got != pnr.CouponFlown {
		t.Errorf("the value coupon is %q; it should have been lifted with the flight coupon", got)
	}

	var lifted bool
	events, _ := gw.Store.Events(ctx, after.ID)
	for _, e := range events {
		if e.Type == "coupon_lifted" {
			lifted = true
		}
	}
	if !lifted {
		t.Error("the lift was not recorded as its own event")
	}
}

func TestDisassociateLeavesTheDocumentStanding(t *testing.T) {
	gw, _ := emdNode(t)
	ctx := context.Background()
	rec := ticketedInterline(t, gw, "EMD006")
	seg := rec.Segments[0].Ref

	req := baggageEMD(seg)
	req.Locator = "EMD006"
	_, doc, err := gw.IssueEMD(ctx, req)
	if err != nil {
		t.Fatal(err)
	}

	// The passenger checks in without the excess baggage they paid for: that
	// one coupon needs unstapling while the document stands.
	after, err := gw.AssociateEMD(ctx, "EMD006", doc.Number, 1, 0, "agent")
	if err != nil {
		t.Fatalf("AssociateEMD: %v", err)
	}
	if !after.EMDs()[0].Coupons[0].Association.IsZero() {
		t.Error("the coupon is still associated")
	}
	if len(after.EMDs()) != 1 {
		t.Error("disassociating must not remove the document")
	}

	// And now a flown flight coupon must not lift it.
	if _, err := gw.Ingest(ctx, "AA",
		tkcreq(t, rec.Tickets[0].Number, "AA", 1, pnr.CouponFlown)); err != nil {
		t.Fatal(err)
	}
	final, _ := gw.Store.GetPNR(ctx, "EMD006")
	if final.EMDs()[0].Coupons[0].Status != pnr.CouponOpen {
		t.Error("a disassociated coupon was lifted anyway")
	}

	// Re-stapling works while the coupon is still open.
	if _, err := gw.AssociateEMD(ctx, "EMD006", doc.Number, 1, rec.Segments[1].Ref, "agent"); err != nil {
		t.Errorf("re-associating an open coupon should work: %v", err)
	}
}

func TestEMDRefusesPrintStatuses(t *testing.T) {
	// The guide is explicit: an EMD supports neither print status, because an
	// EMD is never printed.
	for _, s := range []pnr.CouponStatus{pnr.CouponPrinted, pnr.CouponPrintExchange} {
		if pnr.DocEMDA.SupportsStatus(s) || pnr.DocEMDS.SupportsStatus(s) {
			t.Errorf("EMD should not support %s (%s)", s, s.Meaning())
		}
		if !pnr.DocTicket.SupportsStatus(s) {
			t.Errorf("a ticket does support %s", s)
		}
	}
	doc := pnr.Ticket{
		Number: pnr.TicketNumber{AirlineCode: "125", Serial: "1234567891"},
		Type:   pnr.DocEMDS, RFIC: pnr.RFICBaggage,
		Coupons: []pnr.Coupon{{Number: 1, RFISC: "0CZ", Status: pnr.CouponPrinted}},
	}
	if err := doc.Validate(); err == nil || !strings.Contains(err.Error(), "coupon status") {
		t.Errorf("Validate = %v, want a refusal naming the status", err)
	}
}

func TestEMDIssuanceTellsTheServiceProvider(t *testing.T) {
	gw, sent := emdNode(t)
	ctx := context.Background()
	rec := ticketedInterline(t, gw, "EMD007")
	before := sent.count("AA")

	req := baggageEMD(rec.Segments[0].Ref)
	req.Locator = "EMD007"
	if _, _, err := gw.IssueEMD(ctx, req); err != nil {
		t.Fatal(err)
	}
	// The carrier providing the service has to know the document exists, for
	// the same reason a flight ticket does.
	if sent.count("AA") <= before {
		t.Error("the operating carrier was not told about the document")
	}
	items, _ := gw.Store.ListQueue(ctx, store.QueueFilter{Queue: store.QueueDivergence})
	for _, it := range items {
		if strings.Contains(it.Code, "AA") {
			t.Errorf("AA is reachable and should not be a divergence: %s", it.Reason)
		}
	}
}
