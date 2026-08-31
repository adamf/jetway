package gateway

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/adamf/jetway/pkg/airimp"
	"github.com/adamf/jetway/pkg/avail"
	"github.com/adamf/jetway/pkg/edifact"
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
}

// Validate checks a request is coherent before anything is written.
func (r *BookingRequest) Validate() error {
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
		if _, err := pnr.ResolveDate(s.Date, time.Now().UTC()); err != nil {
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

	if err := req.Validate(); err != nil {
		telemetry.Fail(span, err)
		return nil, err
	}
	now := time.Now().UTC()

	rec := &pnr.PNR{
		Status: pnr.StatusOpen, CreatedAt: now, UpdatedAt: now,
		ReceivedFrom: req.ReceivedFrom,
		Origin: pnr.Origin{
			Party: g.Identity.Designator, Agent: req.Agent, Channel: "api",
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

	res := &BookResult{PNR: rec, Carriers: rec.Carriers()}
	for _, carrier := range rec.Carriers() {
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
	peer := g.PeerForCarrier(carrier)
	if peer == nil {
		return "", fmt.Errorf("gateway: no link configured for carrier %q", carrier)
	}
	switch peer.Format {
	case store.FormatEDIFACT:
		ref := nextControlRef()
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
			Origin: mustAddress(g.Identity.TTYAddress), OriginTime: nowOriginTime(),
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
