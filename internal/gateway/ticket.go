package gateway

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/adamf/jetway/internal/store"
	"github.com/adamf/jetway/internal/telemetry"
	"github.com/adamf/jetway/pkg/edifact"
	"github.com/adamf/jetway/pkg/padis"
	"github.com/adamf/jetway/pkg/pnr"
)

// IssueOptions controls ticket issuance.
type IssueOptions struct {
	// AirlineCode is the three-digit numeric code whose stock is being issued
	// against, e.g. 125 for British Airways. It is not the two-letter
	// designator, and there is no reliable mapping between the two, so it has
	// to be supplied.
	AirlineCode string
	// IssuedBy names who issued, for the audit trail.
	IssuedBy string
}

// ErrNothingToTicket is returned when a record has no segment a ticket could
// cover.
var ErrNothingToTicket = errors.New("gateway: record has no live air segment to ticket")

// IssueTickets issues documents against a record, one set per passenger.
//
// Issuing is what a ticketing time limit is waiting for, so it is also what
// satisfies it: the deadline is cleared and any ticketing task on the queue is
// worked. Leaving the limit standing after issuance would have the sweeper
// raise a record that has already been dealt with, every pass, forever.
func (g *Gateway) IssueTickets(ctx context.Context, locator string, opts IssueOptions) (*pnr.PNR, error) {
	if len(opts.AirlineCode) != 3 {
		return nil, fmt.Errorf("gateway: airline code must be three digits, got %q", opts.AirlineCode)
	}
	ctx, span := telemetry.Start(ctx, "jetway.ticket.issue",
		telemetry.AttrLocator.String(locator),
		telemetry.AttrDocumentType.String(string(pnr.DocTicket)),
	)
	defer span.End()

	const maxAttempts = 5
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		rec, err := g.Store.GetPNR(ctx, locator)
		if err != nil {
			telemetry.Fail(span, err)
			return nil, err
		}
		if rec.Status == pnr.StatusCancelled {
			return nil, fmt.Errorf("gateway: %s is cancelled", rec.RecordLocator)
		}
		if rec.Ticketed() {
			// Issuing twice would put two live documents against the same
			// coupon, which is a refund problem rather than a booking one.
			return rec, nil
		}

		segs := ticketableSegments(rec)
		if len(segs) == 0 {
			return nil, ErrNothingToTicket
		}

		now := time.Now().UTC()
		expected := rec.Version
		var events []store.Event

		for _, pax := range rec.Passengers {
			if _, done := rec.TicketFor(pax.Ref); done {
				continue
			}
			tickets, err := g.issueFor(ctx, pax, segs, opts, now)
			if err != nil {
				return nil, err
			}
			rec.Tickets = append(rec.Tickets, tickets...)
			for _, t := range tickets {
				events = append(events, store.Event{
					Type: "ticket_issued", At: now, Actor: opts.IssuedBy,
					Detail: fmt.Sprintf("%s to %s/%s over %d coupon(s)",
						t.Number, pax.Surname, pax.Given, len(t.Coupons)),
				})
			}
		}
		if len(events) == 0 {
			return rec, nil
		}

		// The limit has been met, so it stops being a limit. The arrangement
		// text stays: it says how the booking was to be ticketed, which is
		// still true.
		for i := range rec.Ticketing {
			if rec.Ticketing[i].Deadline != nil {
				rec.Ticketing[i].Deadline = nil
				events = append(events, store.Event{
					Type: "tktl_satisfied", At: now, Actor: opts.IssuedBy,
					Detail: "ticketing time limit cleared by issuance",
				})
			}
		}
		rec.UpdatedAt = now
		rec.Status = pnr.StatusTicketed

		switch err := g.Store.UpdatePNR(ctx, rec, expected, events); {
		case err == nil:
			g.Bus.Publish(EvPNR, g.pnrView(rec))
			g.workTicketingQueue(ctx, rec, opts.IssuedBy)
			// A ticket the operating carrier does not know about is a ticket
			// that exists only here.
			g.notifyTicketed(ctx, rec, opts.IssuedBy)
			coupons := 0
			for _, t := range rec.FlightTickets() {
				coupons += len(t.Coupons)
			}
			span.SetAttributes(
				telemetry.AttrRecordID.String(rec.ID),
				telemetry.AttrCouponCount.Int(coupons),
				telemetry.AttrPaxCount.Int(len(rec.Passengers)),
			)
			return rec, nil
		case errors.Is(err, store.ErrConflict):
			lastErr = err
			continue
		default:
			return nil, fmt.Errorf("gateway: persist tickets: %w", err)
		}
	}
	return nil, fmt.Errorf("gateway: gave up issuing after %d attempts: %w", maxAttempts, lastErr)
}

// ticketableSegments returns the air segments a coupon can be written against.
func ticketableSegments(rec *pnr.PNR) []*pnr.Segment {
	var out []*pnr.Segment
	for i := range rec.Segments {
		s := &rec.Segments[i]
		if s.Type != pnr.SegmentAir || s.Status == "XX" {
			continue
		}
		out = append(out, s)
	}
	return out
}

// issueFor builds the document set covering one passenger's itinerary.
//
// A ticket carries four flight coupons. An itinerary longer than that spills
// onto conjunction documents, and each of them names the others so a partner
// holding one can find the rest.
func (g *Gateway) issueFor(ctx context.Context, pax pnr.Passenger, segs []*pnr.Segment,
	opts IssueOptions, now time.Time) ([]pnr.Ticket, error) {
	// Four documents of four coupons is the published ceiling for one
	// conjunction set. Beyond it the itinerary needs more than one set, which
	// is a fare construction decision rather than something to do silently.
	if len(segs) > pnr.MaxItinerary {
		return nil, fmt.Errorf(
			"gateway: %d segments needs more than one conjunction set; the limit is %d coupons across %d documents",
			len(segs), pnr.MaxItinerary, pnr.MaxConjunction)
	}
	var chunks [][]*pnr.Segment
	for i := 0; i < len(segs); i += pnr.MaxCoupons {
		end := min(i+pnr.MaxCoupons, len(segs))
		chunks = append(chunks, segs[i:end])
	}

	numbers := make([]pnr.TicketNumber, len(chunks))
	for i := range chunks {
		n, err := g.nextTicketNumber(ctx, opts.AirlineCode)
		if err != nil {
			return nil, err
		}
		numbers[i] = n
	}

	out := make([]pnr.Ticket, 0, len(chunks))
	for i, chunk := range chunks {
		t := pnr.Ticket{
			Number: numbers[i], PaxRef: pax.Ref,
			IssuedAt: now, IssuedBy: opts.IssuedBy,
		}
		for j, s := range chunk {
			t.Coupons = append(t.Coupons, pnr.Coupon{
				Number: j + 1, SegmentRef: s.Ref, Status: pnr.CouponOpen,
			})
		}
		for j, n := range numbers {
			if j != i {
				t.Conjunction = append(t.Conjunction, n)
			}
		}
		out = append(out, t)
	}
	return out, nil
}

// nextTicketNumber allocates a document number.
//
// The serial comes from the same counter that backs record locators. That
// counter's only contract is that it never returns a value twice, which is
// exactly what a document number needs; giving ticketing its own sequence would
// be a second thing to keep unique for no gain.
func (g *Gateway) nextTicketNumber(ctx context.Context, airlineCode string) (pnr.TicketNumber, error) {
	n, err := g.Store.NextLocatorCounter(ctx)
	if err != nil {
		return pnr.TicketNumber{}, fmt.Errorf("gateway: allocate a ticket serial: %w", err)
	}
	serial := fmt.Sprintf("%09d", n%1_000_000_000)
	return pnr.NewTicketNumber(airlineCode, serial)
}

// workTicketingQueue clears any ticketing task now that the record is ticketed.
func (g *Gateway) workTicketingQueue(ctx context.Context, rec *pnr.PNR, by string) {
	if g.Queues == nil {
		return
	}
	items, err := g.Store.ListQueue(ctx, store.QueueFilter{
		Queue: store.QueueTicketing, PNRID: rec.ID,
	})
	if err != nil {
		g.Log.Error("could not read the ticketing queue", "locator", rec.RecordLocator, "err", err)
		return
	}
	if by == "" {
		by = "issuance"
	}
	for _, it := range items {
		if err := g.Queues.Work(ctx, it.ID, by, "ticketed"); err != nil {
			g.Log.Error("could not clear a ticketing task",
				"locator", rec.RecordLocator, "item", it.ID, "err", err)
		}
	}
}

// TicketSummary renders a record's ticketing state for display.
func TicketSummary(rec *pnr.PNR) string {
	if len(rec.Tickets) == 0 {
		return "not ticketed"
	}
	var b strings.Builder
	for i, t := range rec.Tickets {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(t.Number.String())
	}
	return b.String()
}

// notifyTicketed tells each operating carrier that a document now covers their
// segment.
//
// Until this existed, a ticket was real only where it was issued. The carrier
// flying the passenger had no way to know a document backed the segment they
// were holding, which is the difference between a booking and a ticketed
// booking everywhere except in this node's own store.
func (g *Gateway) notifyTicketed(ctx context.Context, rec *pnr.PNR, by string) {
	for _, t := range rec.Tickets {
		byCarrier := map[string][]padis.CouponRef{}
		for _, c := range t.Coupons {
			seg := segmentByRef(rec, c.SegmentRef)
			if seg == nil {
				continue
			}
			carrier := seg.OperatingCarrier
			if carrier == "" {
				carrier = seg.Carrier
			}
			byCarrier[carrier] = append(byCarrier[carrier], padis.CouponRef{
				Number: c.Number, Status: c.Status, SegmentRef: c.SegmentRef,
			})
		}
		for carrier, coupons := range byCarrier {
			if err := g.sendTicketControl(ctx, rec, t, carrier, coupons); err != nil {
				// The ticket exists either way. What is lost is the carrier
				// knowing, so it goes in front of somebody rather than being
				// swallowed.
				g.Log.Error("could not tell a carrier about a ticket",
					"locator", rec.RecordLocator, "carrier", carrier,
					"ticket", t.Number.String(), "err", err)
				g.queueTicketDivergence(ctx, rec, carrier, t.Number, err, by)
			}
		}
	}
}

func segmentByRef(rec *pnr.PNR, ref int) *pnr.Segment {
	for i := range rec.Segments {
		if rec.Segments[i].Ref == ref {
			return &rec.Segments[i]
		}
	}
	return nil
}

func (g *Gateway) sendTicketControl(ctx context.Context, rec *pnr.PNR, t pnr.Ticket,
	carrier string, coupons []padis.CouponRef) error {
	peer := g.PeerForCarrier(carrier)
	if peer == nil {
		return fmt.Errorf("no link configured for carrier %q", carrier)
	}
	if peer.Format != store.FormatEDIFACT {
		// Ticket control is an EDIFACT message. A teletype link has no
		// equivalent here, and pretending otherwise would send a carrier
		// something they cannot read.
		return fmt.Errorf("peer %s is a teletype link and carries no ticket control", peer.Name)
	}
	ref := nextControlRef()
	ic, err := padis.BuildTKCREQ(rec, t.Number, len(t.Coupons), coupons, padis.BuildOptions{
		Sender:     edifact.Party{ID: g.Identity.Designator, Qualifier: "ZZ"},
		Recipient:  edifact.Party{ID: carrier, Qualifier: "ZZ"},
		ControlRef: ref, MessageRef: "1",
	})
	if err != nil {
		return err
	}
	raw, err := ic.Encode(edifact.EncodeOptions{SegmentPerLine: true, Charset: edifact.CharsetUNOA})
	if err != nil {
		return err
	}
	_, err = g.SendKeyed(ctx, peer, raw, padis.MsgTKCREQ, rec.ID, "", "unb:"+ref)
	return err
}

func (g *Gateway) queueTicketDivergence(ctx context.Context, rec *pnr.PNR, carrier string,
	number pnr.TicketNumber, cause error, by string) {
	if g.Queues == nil {
		return
	}
	if _, err := g.Queues.Place(ctx, &store.QueueItem{
		Queue: store.QueueDivergence, PNRID: rec.ID, Locator: rec.RecordLocator,
		Code: "ticket_not_advised_" + carrier,
		Reason: fmt.Sprintf("%s was not told that %s covers their segment: %v",
			carrier, number, cause),
		PlacedBy: by,
	}); err != nil {
		g.Log.Error("could not queue an unadvised ticket", "locator", rec.RecordLocator, "err", err)
	}
}

// applyTicketControl handles a partner's ticket control message.
//
// A request is a carrier saying what became of a coupon on a document this node
// issued: checked in, flown, not accepted. It is applied and answered. A
// response is their acknowledgement of something we said, and is recorded.
func (g *Gateway) applyTicketControl(ctx context.Context, peer *Peer, msg *store.Message,
	dec *decoded, res *Result) error {
	tc := dec.TicketControl
	msg.Kind = padis.MsgTKCREQ
	if tc.Response {
		msg.Kind = padis.MsgTKCRES
	}
	g.trace(msg.ID, "ticket", tc.Describe())

	rec, coupIdx, err := g.findTicket(ctx, tc.Number)
	if err != nil {
		return err
	}
	if rec == nil {
		// Not a status change on a document we issued. It may instead be the
		// validating carrier advising us that a document now covers a segment
		// we operate, which is the other half of interline ticketing and the
		// half a node only sees when it is the carrier rather than the issuer.
		if !tc.Response {
			if advised, err := g.acceptTicketAdvice(ctx, peer, msg, tc); err != nil {
				return err
			} else if advised != nil {
				msg.Status = store.StatusApplied
				msg.PNRID = advised.ID
				res.Status = store.StatusApplied
				res.PNRID = advised.ID
				res.Locator = advised.RecordLocator
				return g.answerTicketControl(ctx, peer, msg, tc, tc.Coupons, len(tc.Coupons), "")
			}
		}
		msg.Status = store.StatusRejected
		msg.Error = "no record holds document " + tc.Number.String()
		res.Status = store.StatusRejected
		if !tc.Response {
			return g.answerTicketControl(ctx, peer, msg, tc, nil, 0,
				"no record holds this document")
		}
		return nil
	}
	msg.PNRID = rec.ID
	res.PNRID = rec.ID
	res.Locator = rec.RecordLocator

	if tc.Response {
		msg.Status = store.StatusApplied
		res.Status = store.StatusApplied
		if tc.Refusal != "" {
			msg.Error = "carrier refused: " + tc.Refusal
			g.queueTicketDivergence(ctx, rec, peer.Carrier, tc.Number,
				errors.New(tc.Refusal), "partner")
		}
		return nil
	}

	applied, refusal := g.applyCouponChanges(ctx, rec, coupIdx, peer, tc, msg)
	msg.Status = store.StatusApplied
	res.Status = store.StatusApplied
	if refusal != "" {
		msg.Error = refusal
	}
	return g.answerTicketControl(ctx, peer, msg, tc, applied, len(rec.Tickets[coupIdx].Coupons), refusal)
}

// findTicket locates the record holding a document, and the index of the
// ticket within it.
// findTicket locates the record holding a document, and the document's index
// within it.
//
// This asks the store rather than walking a page of recent records. The
// difference is not performance: a document issued last month is still valid,
// and a scan bounded by "most recently touched" answered "no record holds this
// document" about documents this node had itself issued -- in a message sent to
// the carrier that asked.
func (g *Gateway) findTicket(ctx context.Context, number pnr.TicketNumber) (*pnr.PNR, int, error) {
	rec, err := g.Store.FindPNRByDocument(ctx, number.Compact())
	if err != nil {
		return nil, 0, fmt.Errorf("gateway: look up a document: %w", err)
	}
	if rec == nil {
		return nil, 0, nil
	}
	for i, t := range rec.Tickets {
		if t.Number.Compact() == number.Compact() {
			return rec, i, nil
		}
	}
	// The store matched the record but the document is not in it, which means
	// the projection and the index disagree. Report absence rather than a
	// coupon index that does not exist.
	return nil, 0, nil
}

// applyCouponChanges folds a carrier's coupon status changes into the record.
//
// Two things are refused. A coupon already at a final status cannot move,
// because no follow-up is permitted on one. And a carrier may only touch a
// coupon covering a segment they operate: letting any partner move any coupon
// would make the document worth nothing.
func (g *Gateway) applyCouponChanges(ctx context.Context, rec *pnr.PNR, ticketIdx int,
	peer *Peer, tc *padis.TicketControl, msg *store.Message) ([]padis.CouponRef, string) {
	now := time.Now().UTC()
	var applied []padis.CouponRef
	var refusal string
	var events []store.Event
	expected := rec.Version
	t := &rec.Tickets[ticketIdx]

	for _, want := range tc.Coupons {
		var c *pnr.Coupon
		for i := range t.Coupons {
			if t.Coupons[i].Number == want.Number {
				c = &t.Coupons[i]
				break
			}
		}
		if c == nil {
			refusal = fmt.Sprintf("document has no coupon %d", want.Number)
			continue
		}
		if c.Status.Final() {
			refusal = fmt.Sprintf("coupon %d is %s (%s) and no follow-up is permitted",
				c.Number, c.Status, c.Status.Meaning())
			continue
		}
		if want.Status.Class() == pnr.ClassUnknown {
			refusal = fmt.Sprintf("coupon status %q is not in the published list", want.Status)
			continue
		}
		if seg := segmentByRef(rec, c.SegmentRef); seg != nil && !operates(peer, seg) {
			refusal = fmt.Sprintf("%s does not operate the segment coupon %d covers",
				peer.Carrier, c.Number)
			continue
		}
		if c.Status == want.Status {
			applied = append(applied, padis.CouponRef{Number: c.Number, Status: c.Status})
			continue
		}
		events = append(events, store.Event{
			Type: "coupon_status", At: now, Actor: peer.Name, MessageID: msg.ID,
			Detail: fmt.Sprintf("coupon %d of %s: %s -> %s (%s)",
				c.Number, t.Number, c.Status, want.Status, want.Status.Meaning()),
		})
		c.Status = want.Status
		applied = append(applied, padis.CouponRef{Number: c.Number, Status: c.Status})

		// Anything stapled to this flight coupon is lifted with it. That is
		// what associating an EMD-A meant in the first place, and a value
		// coupon left open behind a flown flight is revenue nobody accounts
		// for.
		if !t.Type.IsEMD() {
			events = append(events,
				liftAssociated(rec, t.Number, c.Number, want.Status, now, peer.Name)...)
		}
	}

	if len(events) > 0 {
		rec.UpdatedAt = now
		if err := g.Store.UpdatePNR(ctx, rec, expected, events); err != nil {
			g.Log.Error("could not record a coupon status change",
				"locator", rec.RecordLocator, "err", err)
			return applied, "could not record the change"
		}
		g.Bus.Publish(EvPNR, g.pnrView(rec))
	}
	return applied, refusal
}

// operates reports whether a peer flies a segment.
func operates(peer *Peer, seg *pnr.Segment) bool {
	carrier := seg.OperatingCarrier
	if carrier == "" {
		carrier = seg.Carrier
	}
	return peer.Carrier == carrier
}

func (g *Gateway) answerTicketControl(ctx context.Context, peer *Peer, msg *store.Message,
	tc *padis.TicketControl, applied []padis.CouponRef, total int, refusal string) error {
	ref := nextControlRef()
	ic, err := padis.BuildTKCRES(tc.Number, total, applied, refusal, padis.BuildOptions{
		Sender:     edifact.Party{ID: g.Identity.Designator, Qualifier: "ZZ"},
		Recipient:  edifact.Party{ID: peer.Carrier, Qualifier: "ZZ"},
		ControlRef: ref, MessageRef: "1",
	})
	if err != nil {
		return fmt.Errorf("gateway: build ticket control response: %w", err)
	}
	raw, err := ic.Encode(edifact.EncodeOptions{SegmentPerLine: true, Charset: edifact.CharsetUNOA})
	if err != nil {
		return fmt.Errorf("gateway: encode ticket control response: %w", err)
	}
	_, err = g.SendKeyed(ctx, peer, raw, padis.MsgTKCRES, msg.PNRID, msg.ID, "unb:"+ref)
	return err
}

// acceptTicketAdvice records a document the validating carrier says covers a
// segment this node operates.
//
// It is matched by the sender's own record locator, which they put in RCI,
// because their document number means nothing here yet and their locator is the
// one reference both sides already share. Returns nil when no record matches,
// which leaves the caller to refuse.
func (g *Gateway) acceptTicketAdvice(ctx context.Context, peer *Peer, msg *store.Message,
	tc *padis.TicketControl) (*pnr.PNR, error) {
	if tc.Locator == "" {
		return nil, nil
	}
	rec, err := g.findByExternalLocator(ctx, tc.Party, tc.Locator)
	if err != nil || rec == nil {
		return nil, err
	}

	now := time.Now().UTC()
	expected := rec.Version
	t := pnr.Ticket{
		Number: tc.Number, IssuedAt: now, IssuedBy: tc.Party,
	}
	if len(rec.Passengers) > 0 {
		t.PaxRef = rec.Passengers[0].Ref
	}
	// The coupons are theirs; only the ones covering a segment this node
	// actually holds are worth recording against it.
	for _, c := range tc.Coupons {
		t.Coupons = append(t.Coupons, pnr.Coupon{
			Number: c.Number, SegmentRef: firstSegmentFor(rec, peer), Status: c.Status,
		})
	}
	rec.Tickets = append(rec.Tickets, t)
	rec.UpdatedAt = now

	events := []store.Event{{
		Type: "ticket_advised", At: now, Actor: peer.Name, MessageID: msg.ID,
		Detail: fmt.Sprintf("%s advised %s covers this booking over %d coupon(s)",
			tc.Party, tc.Number, len(t.Coupons)),
	}}
	if err := g.Store.UpdatePNR(ctx, rec, expected, events); err != nil {
		return nil, fmt.Errorf("gateway: record an advised ticket: %w", err)
	}
	g.Bus.Publish(EvPNR, g.pnrView(rec))
	g.trace(msg.ID, "ticket", "recorded "+tc.Number.String()+" against "+rec.RecordLocator)
	return rec, nil
}

// findByExternalLocator locates a record by another system's locator for it.
// findByExternalLocator locates the record a partner knows by its own locator.
//
// Also a whole-store lookup, and for the same reason: a partner referring to a
// booking made months ago is the ordinary case, not the exotic one.
func (g *Gateway) findByExternalLocator(ctx context.Context, owner, value string) (*pnr.PNR, error) {
	rec, err := g.Store.FindPNRByExternalLocator(ctx, owner, value)
	if err != nil {
		return nil, fmt.Errorf("gateway: look up a partner locator: %w", err)
	}
	return rec, nil
}

// firstSegmentFor returns the reference of the first segment a peer operates.
func firstSegmentFor(rec *pnr.PNR, peer *Peer) int {
	for i := range rec.Segments {
		if rec.Segments[i].Type == pnr.SegmentAir && operates(peer, &rec.Segments[i]) {
			return rec.Segments[i].Ref
		}
	}
	if len(rec.Segments) > 0 {
		return rec.Segments[0].Ref
	}
	return 0
}
