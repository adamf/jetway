package gateway

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/adamf/jetway/pkg/airimp"
	"github.com/adamf/jetway/pkg/avail"
	"github.com/adamf/jetway/pkg/edifact"
	"github.com/adamf/jetway/pkg/fare"
	"github.com/adamf/jetway/pkg/padis"
	"github.com/adamf/jetway/pkg/pnr"
	"github.com/adamf/jetway/pkg/rescode"
	"github.com/adamf/jetway/pkg/store"
	"github.com/adamf/jetway/pkg/telemetry"
	"github.com/adamf/jetway/pkg/typeb"
)

// BookingPassenger is one traveller on a booking request.
type BookingPassenger struct {
	Surname string `json:"surname"`
	Given   string `json:"given"`
	Title   string `json:"title,omitempty"`
	Infant  bool   `json:"infant,omitempty"`
}

// BookingSegment is one requested flight.
type BookingSegment struct {
	Carrier   string `json:"carrier"`
	FlightNum string `json:"flight_num"`
	Class     string `json:"class"`
	// Date is DDMMM as an agent would enter it, e.g. "15JUN".
	Date  string `json:"date"`
	Board string `json:"board"`
	Off   string `json:"off"`
	Seats int    `json:"seats"`
	// DepartTime and ArriveTime are HHMM local to their station. Optional: a
	// sell is valid without them, and the carrier's own schedule is
	// authoritative either way.
	DepartTime string `json:"depart_time,omitempty"`
	ArriveTime string `json:"arrive_time,omitempty"`
}

// BookingSSR is a special service request on a booking.
type BookingSSR struct {
	Code    string `json:"code"`
	Carrier string `json:"carrier,omitempty"`
	Text    string `json:"text,omitempty"`
}

// BookingRequest is what the distribution side receives from an agent.
type BookingRequest struct {
	Passengers   []BookingPassenger `json:"passengers"`
	Segments     []BookingSegment   `json:"segments"`
	SSRs         []BookingSSR       `json:"ssrs,omitempty"`
	Contact      string             `json:"contact,omitempty"`
	ReceivedFrom string             `json:"received_from,omitempty"`
	Agent        string             `json:"agent,omitempty"`
	// Channel says where the booking came from: the console API, an NDC order,
	// a partner message. It is carried onto the record and onto the span,
	// because "how much comes through NDC" is a question somebody asks.
	Channel string `json:"channel,omitempty"`
	// Country is the point of sale's country and Account the corporate or
	// agency contract the booking is made under. With Agent and Channel
	// they are the point of sale the fares are priced for: a fare filed
	// for one country, one channel, one agency or one account sells only
	// there.
	Country string `json:"country,omitempty"`
	Account string `json:"account,omitempty"`
	// PricedAt is when the fare was quoted, when that was before the
	// sell: a seller shops, quotes, and books afterwards, and the quote
	// is what the traveller agreed to. The rules that count from the
	// purchase count from it. Zero is now. A moment in the future is
	// refused, and one older than the node's QuoteValidity is priced as
	// of now.
	PricedAt time.Time `json:"priced_at,omitempty"`
	// Quote is the fare as the seller's own engine priced it, for a node
	// that accepts quotes (Gateway.AcceptQuotes): the record carries it
	// as priced and the node does not price again. It is how a booking
	// made from a shopping engine's answer, or from an airline's offer,
	// keeps the price the traveller was shown. The quote must cover the
	// booking's air segments in order, in the classes booked.
	Quote *fare.Quote `json:"quote,omitempty"`
}

// nameOutsideWire reports the first character of s that cannot survive both
// wire charsets -- teletype ITA2 and EDIFACT level A. A name outside this
// repertoire encodes for nobody: the booking would be accepted, every request
// to a carrier would fail to build, and the record would sit at HN forever.
// Refusing at the counter is what every real distribution system does, and is
// why ticketed names are plain uppercase Latin the world over.
func nameOutsideWire(s string) (rune, bool) {
	for _, r := range strings.ToUpper(s) {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == ' ', r == '-', r == '\'', r == '.', r == '/':
		default:
			return r, true
		}
	}
	return 0, false
}

// Validate checks a request is coherent before anything is written.
func (r *BookingRequest) Validate() error { return r.validate(time.Now().UTC()) }

// validate is Validate against a caller-supplied clock, so a gateway driving
// time resolves booking dates in the same "today" it stamps everything else.
func (r *BookingRequest) validate(now time.Time) error {
	if len(r.Passengers) == 0 {
		return fmt.Errorf("booking needs at least one passenger")
	}
	if len(r.Segments) == 0 {
		return fmt.Errorf("booking needs at least one segment")
	}
	for i, p := range r.Passengers {
		if p.Surname == "" || p.Given == "" {
			return fmt.Errorf("passenger %d needs a surname and a given name", i+1)
		}
		for _, f := range []struct{ what, v string }{
			{"surname", p.Surname}, {"given name", p.Given}, {"title", p.Title},
		} {
			if bad, ok := nameOutsideWire(f.v); ok {
				return fmt.Errorf("passenger %d: %s contains %q, which no interline "+
					"wire format can carry; names are limited to letters, digits, "+
					"spaces, hyphens, apostrophes, periods and slashes", i+1, f.what, bad)
			}
		}
	}
	for i, s := range r.Segments {
		switch {
		case len(s.Carrier) != 2:
			return fmt.Errorf("segment %d: carrier must be two characters", i+1)
		case s.FlightNum == "":
			return fmt.Errorf("segment %d: flight number is required", i+1)
		case len(s.Class) != 1:
			return fmt.Errorf("segment %d: booking class must be one character", i+1)
		case len(s.Board) != 3 || len(s.Off) != 3:
			return fmt.Errorf("segment %d: board and off points must be three-letter codes", i+1)
		}
		if _, err := pnr.ResolveDate(s.Date, now); err != nil {
			return fmt.Errorf("segment %d: %w", i+1, err)
		}
	}
	return nil
}

// BookResult reports what a booking produced.
type BookResult struct {
	PNR      *pnr.PNR `json:"pnr"`
	Sent     []string `json:"sent"`
	Carriers []string `json:"carriers"`
}

// Book creates a record and requests its segments from the carriers that own
// them.
//
// The record is written before any message goes out. If a link is down, the
// booking exists at HN and the request can be retried; the alternative, sending
// first and storing after, produces seats sold against a record that does not
// exist.
func (g *Gateway) Book(ctx context.Context, req *BookingRequest) (*BookResult, error) {
	// The commercial span. Everything the revenue side asks about a booking is
	// answered from here: how many seats, on how many carriers, how much of it
	// sold without having to ask, and what the carriers said.
	channel := telemetry.ChannelAPI
	if req.Channel != "" {
		channel = req.Channel
	}
	ctx, span := telemetry.Start(ctx, "jetway.book",
		telemetry.AttrChannel.String(channel),
		telemetry.AttrPaxCount.Int(len(req.Passengers)),
		telemetry.AttrSegmentCount.Int(len(req.Segments)),
	)
	defer span.End()

	now := g.now()
	if err := req.validate(now); err != nil {
		telemetry.Fail(span, err)
		return nil, err
	}

	rec := &pnr.PNR{
		Status: pnr.StatusOpen, CreatedAt: now, UpdatedAt: now,
		ReceivedFrom: req.ReceivedFrom,
		Origin: pnr.Origin{
			Party: g.Identity.Designator, Agent: req.Agent, Channel: "api",
			Country: upper(req.Country), Account: upper(req.Account),
		},
	}
	rec.Origin.Channel = channel
	for _, p := range req.Passengers {
		rec.Passengers = append(rec.Passengers, pnr.Passenger{
			Surname: upper(p.Surname), Given: upper(p.Given),
			Title: upper(p.Title), Infant: p.Infant,
		})
	}
	var freeSold []pnr.Segment
	var sellReason []string
	for i, s := range req.Segments {
		depart, err := pnr.ResolveDate(s.Date, now)
		if err != nil {
			return nil, err
		}
		seats := s.Seats
		if seats <= 0 {
			seats = len(req.Passengers)
		}
		seg := pnr.Segment{
			Type: pnr.SegmentAir, Carrier: upper(s.Carrier), FlightNum: s.FlightNum,
			Class: upper(s.Class), Depart: depart, WireDate: pnr.FormatDate(depart),
			Board: upper(s.Board), Off: upper(s.Off),
			DepartTime: s.DepartTime, ArriveTime: s.ArriveTime,
			// HN: we have asked and not yet heard back. A segment starts here
			// unless the carrier has already granted free sale for it.
			Status: "HN", Seats: seats,
		}
		decision, why := g.decide(seg)
		switch decision {
		case avail.Refuse:
			// The carrier has said this class is closed. Sending a request we
			// know will be refused wastes a round trip and, on a busy link,
			// somebody else's.
			return nil, fmt.Errorf("segment %d: %s%s %s %s-%s is not available: %s",
				i+1, seg.Carrier, seg.FlightNum, seg.Class, seg.Board, seg.Off, why)
		case avail.FreeSale:
			// Free sale is the point of availability: the carrier granted
			// permission in advance, so the seat is held now and the carrier is
			// told afterwards.
			seg.Status = "HK"
			freeSold = append(freeSold, seg)
		}
		sellReason = append(sellReason, fmt.Sprintf("%s%s %s: %s (%s)",
			seg.Carrier, seg.FlightNum, seg.Class, decision, why))
		rec.Segments = append(rec.Segments, seg)
	}
	for _, s := range req.SSRs {
		rec.SSRs = append(rec.SSRs, pnr.SSR{
			Code: upper(s.Code), Carrier: upper(s.Carrier), Status: "NN",
			Count: len(req.Passengers), Text: s.Text,
			Sensitive: sensitiveSSR(upper(s.Code)),
		})
	}
	if req.Contact != "" {
		rec.Contacts = append(rec.Contacts, pnr.Contact{Type: "phone", Text: req.Contact})
	}
	priced, err := g.quoteTime(req, now)
	if err != nil {
		return nil, err
	}
	if req.Quote != nil && g.AcceptQuotes {
		if err := storeQuote(rec, req.Quote, now); err != nil {
			return nil, err
		}
	} else if err := g.price(rec, priced); err != nil {
		return nil, err
	}
	rec.Recompute()

	loc, err := g.newLocator(ctx)
	if err != nil {
		return nil, err
	}
	rec.RecordLocator = loc

	for _, r := range sellReason {
		g.Log.Debug("availability decision", "locator", loc, "detail", r)
	}
	// Commit the seats we free-sold, so two bookings a moment apart cannot both
	// sell the last seat on the strength of one broadcast.
	for _, seg := range freeSold {
		if g.Avail != nil {
			g.Avail.Sold(availKey(seg), seg.Seats)
		}
	}

	events := []store.Event{{Type: "created", Detail: "booking created via API", Actor: req.Agent, At: now}}
	for _, s := range rec.Segments {
		events = append(events, store.Event{
			Type: "add_segment", Detail: s.Describe(), Actor: req.Agent, At: now,
		})
	}
	if err := g.Store.CreatePNR(ctx, rec, events); err != nil {
		return nil, fmt.Errorf("gateway: create record: %w", err)
	}
	g.Bus.Publish(EvPNR, g.pnrView(rec))
	g.Log.Info("booking created", "locator", rec.RecordLocator, "segments", len(rec.Segments))

	// The node's own carrier's seats are its own to sell: decided here,
	// with nothing sent.
	if sold, err := g.sellOwn(ctx, rec.RecordLocator, req.Agent, nil); err != nil {
		g.Log.Warn("could not decide an own sale", "locator", rec.RecordLocator, "err", err)
	} else if sold != nil {
		rec = sold
	}

	res := &BookResult{PNR: rec, Carriers: rec.Carriers()}
	for _, carrier := range rec.Carriers() {
		if g.Own(carrier) {
			continue
		}
		id, err := g.RequestFromCarrier(ctx, rec, carrier)
		if err != nil {
			// A link being down must not undo a booking. The record stays at
			// HN and the request can be resent.
			g.Log.Warn("could not request segments", "carrier", carrier,
				"locator", rec.RecordLocator, "err", err)
			continue
		}
		res.Sent = append(res.Sent, id)
	}

	// What the booking actually is, commercially. Seats and carriers are the
	// volume; free_sale is the share that sold on the carrier's standing
	// permission rather than costing a round trip, which is the number a
	// distribution team watches.
	seats, free := 0, 0
	for _, sg := range rec.Segments {
		if sg.Type != pnr.SegmentAir {
			continue
		}
		seats += sg.Seats
		if sg.Status == "HK" {
			free++
		}
	}
	span.SetAttributes(
		telemetry.AttrLocator.String(rec.RecordLocator),
		telemetry.AttrRecordID.String(rec.ID),
		telemetry.AttrSeats.Int(seats),
		telemetry.AttrCarrier.String(strings.Join(rec.Carriers(), ",")),
		telemetry.AttrInterline.Bool(len(rec.Carriers()) > 1),
		telemetry.AttrFreeSale.Bool(free == len(rec.Segments) && free > 0),
		telemetry.AttrOutcome.String(bookingOutcome(rec)),
	)
	return res, nil
}

// bookingOutcome collapses a record to the answer somebody reports on.
func bookingOutcome(rec *pnr.PNR) string {
	held, waitlisted, refused, pending := 0, 0, 0, 0
	for _, s := range rec.Segments {
		if s.Type != pnr.SegmentAir {
			continue
		}
		code := rescode.ActionCode(s.Status)
		info, known := code.Info()
		switch {
		case known && info.Confirmed:
			held++
		case known && info.Waitlisted:
			waitlisted++
		case known && info.Category == rescode.CatReply:
			refused++
		default:
			pending++
		}
	}
	switch {
	case refused > 0:
		return telemetry.OutcomeRefused
	case waitlisted > 0:
		return telemetry.OutcomeWaitlisted
	case pending > 0:
		return telemetry.OutcomePending
	case held > 0:
		return telemetry.OutcomeConfirmed
	}
	return telemetry.OutcomePending
}

// decide asks the availability cache what to do about a segment.
func (g *Gateway) decide(s pnr.Segment) (avail.Decision, string) {
	if g.Avail == nil {
		return avail.Ask, "no availability cache"
	}
	return g.Avail.Decide(availKey(s), s.Seats)
}

func availKey(s pnr.Segment) avail.Key {
	return avail.NewKey(s.Carrier, s.FlightNum, s.Depart, s.Board, s.Off, s.Class)
}

// RequestFromCarrier sends a sell request for the record's segments operated by
// carrier, in whatever format that carrier's link speaks.
func (g *Gateway) RequestFromCarrier(ctx context.Context, rec *pnr.PNR, carrier string) (string, error) {
	return g.requestSegments(ctx, rec, carrier, nil)
}

// requestSegments asks a carrier for the record's segments that only
// admits, or all of the carrier's segments when only is nil. The message is
// built from a view of the record holding just those segments, so a carrier
// that already confirmed the rest is not asked to sell them again.
func (g *Gateway) requestSegments(ctx context.Context, rec *pnr.PNR, carrier string, only func(pnr.Segment) bool) (string, error) {
	if only != nil {
		view := *rec
		view.Segments = nil
		for _, s := range rec.Segments {
			if s.Carrier == carrier && only(s) {
				view.Segments = append(view.Segments, s)
			}
		}
		if len(view.Segments) == 0 {
			return "", fmt.Errorf("gateway: record has no %s segments to request", carrier)
		}
		rec = &view
	}
	peer := g.PeerForCarrier(carrier)
	if peer == nil {
		return "", fmt.Errorf("gateway: no link configured for carrier %q", carrier)
	}
	switch peer.Format {
	case store.FormatEDIFACT:
		ref := g.nextControlRef()
		ic, err := padis.BuildPAOREQ(rec, carrier, padis.BuildOptions{
			Sender:     edifact.Party{ID: g.Identity.Designator, Qualifier: "ZZ"},
			Recipient:  edifact.Party{ID: carrier, Qualifier: "ZZ"},
			ControlRef: ref, MessageRef: "1",
			Charset: edifact.CharsetUNOA,
		})
		if err != nil {
			return "", err
		}
		raw, err := ic.Encode(edifact.EncodeOptions{SegmentPerLine: true, Charset: edifact.CharsetUNOA})
		if err != nil {
			return "", err
		}
		return g.SendKeyed(ctx, peer, raw, padis.MsgPAOREQ, rec.ID, "", "unb:"+ref)

	default:
		// SS reports a sale made from availability; NN asks for one. Sending NN
		// for a seat already held would ask the carrier to sell it twice.
		action := airimp.ActionCode("NN")
		if g.allFreeSold(rec, carrier) {
			action = "SS"
		}
		text := airimp.BuildSell(rec, carrier, action)
		if text == "" {
			return "", fmt.Errorf("gateway: record has no %s segments to request", carrier)
		}
		dest, err := typeb.ParseAddress(peer.TTYAddress)
		if err != nil {
			return "", fmt.Errorf("gateway: peer %s has an unusable TTY address: %w", peer.Name, err)
		}
		out := &typeb.Message{
			Priority: "QU", Destinations: []typeb.Address{dest},
			Origin: mustAddress(g.Identity.TTYAddress), OriginTime: g.nowOriginTime(),
			Text: text,
		}
		raw, err := out.Encode(typeb.EncodeOptions{Charset: typeb.CharsetITA2, CRLF: true})
		if err != nil {
			return "", fmt.Errorf("gateway: encode sell: %w", err)
		}
		return g.Send(ctx, peer, raw, "AIRIMP/sell", rec.ID, "")
	}
}

// allFreeSold reports whether every segment for a carrier was taken on free
// sale, in which case the message reports rather than requests.
func (g *Gateway) allFreeSold(rec *pnr.PNR, carrier string) bool {
	n := 0
	for _, s := range rec.Segments {
		if s.Carrier != carrier || s.Type != pnr.SegmentAir {
			continue
		}
		if s.Status != "HK" {
			return false
		}
		n++
	}
	return n > 0
}

func upper(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'a' && b[i] <= 'z' {
			b[i] -= 32
		}
	}
	return string(b)
}

func sensitiveSSR(code string) bool {
	switch code {
	case "DOCS", "DOCA", "DOCO", "FOID":
		return true
	}
	return false
}

// price prices a new record against the node's tariff, when it has one.
// The fare basis lands on each air segment and the money on the record. A
// segment with no fare that may be sold in its class for this trip refuses
// the booking: the wrapped error is a *fare.ErrNoFare naming the rule, so a
// caller can offer another class.
func (g *Gateway) price(rec *pnr.PNR, now time.Time) error {
	if g.Tariff == nil {
		return nil
	}
	req := fare.Request{Purchased: now, Sale: fare.Sale{Country: rec.Origin.Country, Channel: rec.Origin.Channel, Agency: rec.Origin.Agent, Account: rec.Origin.Account}}
	if g.Miles != nil {
		req.Distance = g.Miles
	}
	var air []int
	for i, s := range rec.Segments {
		if s.Type != pnr.SegmentAir {
			continue
		}
		fs := fare.Segment{Carrier: s.Carrier, Origin: s.Board, Destination: s.Off, Class: s.Class, FlightNum: s.FlightNum,
			Depart: clockOn(s.Depart, s.DepartTime)}
		if s.ArriveTime != "" && s.DepartTime != "" {
			fs.Arrive = clockOn(s.Depart, s.ArriveTime)
			if fs.Arrive.Before(fs.Depart) {
				// Lands after midnight: the arrival is the next day's.
				fs.Arrive = fs.Arrive.AddDate(0, 0, 1)
			}
		}
		if g.Miles != nil {
			fs.Miles = g.Miles(s.Board, s.Off)
		}
		req.Segments = append(req.Segments, fs)
		air = append(air, i)
	}
	if len(req.Segments) == 0 {
		return nil
	}
	for _, p := range rec.Passengers {
		switch {
		case p.Infant || p.Type == pnr.PaxInfant:
			req.Passengers = append(req.Passengers, fare.Infant)
		case p.Type == pnr.PaxChild:
			req.Passengers = append(req.Passengers, fare.Child)
		default:
			req.Passengers = append(req.Passengers, fare.Adult)
		}
	}
	q, err := fare.Price(g.Tariff, req)
	if err != nil {
		return fmt.Errorf("gateway: pricing: %w", err)
	}
	return putQuote(rec, air, q, now)
}

// storeQuote puts a quote the seller priced on the record, after holding
// it against the record: a passenger's price for each passenger, a
// segment's fare for each air segment, in the class booked.
func storeQuote(rec *pnr.PNR, q *fare.Quote, now time.Time) error {
	var air []int
	for i, s := range rec.Segments {
		if s.Type == pnr.SegmentAir {
			air = append(air, i)
		}
	}
	if len(q.Passengers) != len(rec.Passengers) {
		return fmt.Errorf("gateway: the quote prices %d passengers and the booking has %d", len(q.Passengers), len(rec.Passengers))
	}
	for _, pq := range q.Passengers {
		if len(pq.Segments) != len(air) {
			return fmt.Errorf("gateway: the quote prices %d segments and the booking has %d", len(pq.Segments), len(air))
		}
		for k, i := range air {
			if c := pq.Segments[k].Class; c != "" && !strings.EqualFold(c, rec.Segments[i].Class) {
				return fmt.Errorf("gateway: the quote prices segment %d in %s and it is booked in %s", k+1, c, rec.Segments[i].Class)
			}
		}
	}
	return putQuote(rec, air, q, now)
}

// putQuote writes a quote onto a record: the money, the fare calculation,
// the components, and the fare basis on each air segment.
func putQuote(rec *pnr.PNR, air []int, q *fare.Quote, now time.Time) error {
	pr := &pnr.Pricing{Currency: q.Currency, Base: q.Base.Amount, Taxes: q.Taxes.Amount, Total: q.Total.Amount, PricedAt: now,
		CarrierFees: q.CarrierFees.Amount, Calc: q.Calc}
	for _, c := range q.Components {
		fc := pnr.FareComponent{Carrier: c.Fare.Carrier, Basis: c.Fare.Basis, From: c.From, To: c.To, Amount: c.Amount.Amount, Unit: c.Unit}
		if c.Unit < len(q.Units) {
			fc.Kind = string(q.Units[c.Unit].Kind)
		}
		pr.Components = append(pr.Components, fc)
	}
	for i, pq := range q.Passengers {
		pp := pnr.PassengerPricing{Ref: i + 1, Type: string(pq.Type), Base: pq.Base.Amount, Total: pq.Total.Amount}
		for _, tl := range pq.Taxes {
			pp.Taxes += tl.Amount.Amount
		}
		for _, sf := range pq.Segments {
			pp.Bases = append(pp.Bases, sf.Basis)
			pp.Segments = append(pp.Segments, sf.Base.Amount)
		}
		pr.Passengers = append(pr.Passengers, pp)
	}
	for k, i := range air {
		if len(q.Passengers) > 0 && k < len(q.Passengers[0].Segments) {
			rec.Segments[i].FareBasis = q.Passengers[0].Segments[k].Basis
		}
	}
	rec.Pricing = pr
	return nil
}

// clockOn is a date with a wire time of day (HHMM) on it; the date alone
// when the time is missing or malformed.
func clockOn(date time.Time, hhmm string) time.Time {
	if len(hhmm) != 4 {
		return date
	}
	h, m := 0, 0
	for i, c := range hhmm {
		if c < '0' || c > '9' {
			return date
		}
		if i < 2 {
			h = h*10 + int(c-'0')
		} else {
			m = m*10 + int(c-'0')
		}
	}
	if h > 23 || m > 59 {
		return date
	}
	return time.Date(date.Year(), date.Month(), date.Day(), h, m, 0, 0, date.Location())
}

// quoteTime is the moment a booking's fare is priced as of: the quote the
// seller gave, when the request carries one that is still good, else now.
func (g *Gateway) quoteTime(req *BookingRequest, now time.Time) (time.Time, error) {
	if req.PricedAt.IsZero() {
		return now, nil
	}
	if req.PricedAt.After(now.Add(time.Minute)) {
		return now, fmt.Errorf("the fare quote is dated in the future")
	}
	if g.QuoteValidity > 0 && now.Sub(req.PricedAt) > g.QuoteValidity {
		return now, nil
	}
	return req.PricedAt, nil
}

// couponValue is what a passenger's coupon on a segment is worth, from the
// record's pricing: the segment's base fare share, with the passenger's
// taxes spread across their coupons.
func couponValue(rec *pnr.PNR, paxRef, segRef int) (amount, currency string) {
	if rec.Pricing == nil {
		return "", ""
	}
	airIndex := -1
	k := 0
	for _, s := range rec.Segments {
		if s.Type != pnr.SegmentAir {
			continue
		}
		if s.Ref == segRef {
			airIndex = k
		}
		k++
	}
	if airIndex < 0 {
		return "", ""
	}
	for _, pp := range rec.Pricing.Passengers {
		if pp.Ref != paxRef || airIndex >= len(pp.Segments) {
			continue
		}
		v := pp.Segments[airIndex]
		if n := len(pp.Segments); n > 0 {
			v += pp.Taxes / int64(n)
		}
		return fmt.Sprintf("%d.%02d", v/100, v%100), rec.Pricing.Currency
	}
	return "", ""
}
