package padis

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/adamf/jetway/pkg/edifact"
	"github.com/adamf/jetway/pkg/pnr"
)

// Ticket control messages: TKCREQ asks about or changes the status of a
// document's coupons, TKCRES answers.
//
// This is the interline half of electronic ticketing. A ticket is issued by one
// carrier and flown on another's aircraft, so the carrier that takes the
// passenger has to be able to tell the carrier that issued the document what
// became of the coupon -- checked in, flown, or not accepted. Without it a
// ticket exists only where it was issued, which is where this build was until
// now.
//
// # Standing
//
// A profile. The PADIS message directories are paid publications and were not
// bought, so the segment layout here is inferred: TKT carrying the document
// number, CPN carrying coupon number and status, alongside the MSG, ORG and TIF
// segments the reservation messages already use.
//
// What is *not* inferred is the coupon status vocabulary. IATA publishes it in
// the Airline Guide to EMD Implementation, which is free, and pkg/pnr takes it
// from there. A carrier link that writes the segments differently should get a
// profile; one that disagrees about what "F" means would be a different problem
// entirely, and does not happen.
const (
	// TagTKT carries a document number.
	TagTKT = "TKT"
	// TagCPN carries a coupon number and its status.
	TagCPN = "CPN"
	// TagERC carries an error or refusal reason.
	TagERC = "ERC"
)

// CouponRef is one coupon of a document.
type CouponRef struct {
	Number int
	Status pnr.CouponStatus
	// SegmentRef ties the coupon to a segment on the sender's record, where
	// they stated one.
	SegmentRef int
}

// TicketControl is a decoded TKCREQ or TKCRES.
type TicketControl struct {
	// Response reports whether this is the answer rather than the request.
	Response bool
	// Number is the document the message is about.
	Number pnr.TicketNumber
	// Party is who sent it.
	Party   string
	Coupons []CouponRef
	// Refusal is set on a response that declined the change.
	Refusal string
	// Locator is the sender's record locator, where they gave one.
	Locator string
}

// Describe renders the message as one line of operator-facing text.
func (t *TicketControl) Describe() string {
	kind := "TKCREQ"
	if t.Response {
		kind = "TKCRES"
	}
	parts := make([]string, 0, len(t.Coupons))
	for _, c := range t.Coupons {
		parts = append(parts, fmt.Sprintf("%d:%s", c.Number, c.Status))
	}
	s := kind + " " + t.Number.String() + " coupon " + strings.Join(parts, ",")
	if t.Refusal != "" {
		s += " refused: " + t.Refusal
	}
	return s
}

func tktSegment(n pnr.TicketNumber, coupons int) edifact.Segment {
	// T: an electronic flight document. The count is how many coupons the
	// document carries, which is not the same as how many this message is
	// about.
	return edifact.Seg(TagTKT, edifact.Comp(n.Compact(), "T", strconv.Itoa(coupons)))
}

func cpnSegments(coupons []CouponRef) []edifact.Segment {
	out := make([]edifact.Segment, 0, len(coupons))
	for _, c := range coupons {
		out = append(out, edifact.Seg(TagCPN,
			edifact.Comp(strconv.Itoa(c.Number), string(c.Status))))
	}
	return out
}

// BuildTKCREQ renders a ticket control request.
//
// coupons carries the status being asserted or asked for. Telling the issuing
// carrier that a coupon is now flown is the same message shape as asking them
// what it is; the difference is which side has the authority, not the syntax.
func BuildTKCREQ(rec *pnr.PNR, number pnr.TicketNumber, totalCoupons int,
	coupons []CouponRef, o BuildOptions) (*edifact.Interchange, error) {
	if number.IsZero() {
		return nil, fmt.Errorf("padis: ticket control needs a document number")
	}
	if len(coupons) == 0 {
		return nil, fmt.Errorf("padis: ticket control names no coupons")
	}
	body := []edifact.Segment{
		edifact.Seg("MSG", edifact.Comp("", "11")),
		edifact.Seg("ORG", edifact.Comp(o.Sender.ID, "")),
		tktSegment(number, totalCoupons),
	}
	body = append(body, cpnSegments(coupons)...)
	if rec != nil {
		if rec.RecordLocator != "" {
			body = append(body, edifact.Seg("RCI", edifact.Comp(o.Sender.ID, rec.RecordLocator, "")))
		}
		body = append(body, tifSegments(rec)...)
	}

	ic := newInterchange(MsgTKCREQ, o)
	ic.AddMessage(o.msgRef(), messageID(MsgTKCREQ), body...)
	ic.Finalize()
	return ic, nil
}

// BuildTKCRES renders the answer to a ticket control request.
//
// refusal, when set, means the change was not accepted and says why. A response
// that carried the requested status back regardless would tell the requester
// their update landed when it did not.
func BuildTKCRES(number pnr.TicketNumber, totalCoupons int,
	coupons []CouponRef, refusal string, o BuildOptions) (*edifact.Interchange, error) {
	body := []edifact.Segment{
		edifact.Seg("MSG", edifact.Comp("", "22")),
		edifact.Seg("ORG", edifact.Comp(o.Sender.ID, "")),
		tktSegment(number, totalCoupons),
	}
	body = append(body, cpnSegments(coupons)...)
	if refusal != "" {
		// The reason is text this node wrote, and it has to survive the link's
		// repertoire. UNOA has no lowercase, so a plainly-worded refusal would
		// otherwise fail to encode and the partner would learn nothing.
		body = append(body, edifact.Seg(TagERC,
			edifact.Comp("", edifact.CharsetUNOA.Sanitise(refusal))))
	}

	ic := newInterchange(MsgTKCRES, o)
	ic.AddMessage(o.msgRef(), messageID(MsgTKCRES), body...)
	ic.Finalize()
	return ic, nil
}

// IsTicketControl reports whether a message is TKCREQ or TKCRES.
func IsTicketControl(m edifact.Message) bool {
	switch m.ID().Type {
	case MsgTKCREQ, MsgTKCRES:
		return true
	}
	return false
}

// ParseTicketControl decodes a TKCREQ or TKCRES.
func ParseTicketControl(m edifact.Message) (*TicketControl, error) {
	if !IsTicketControl(m) {
		return nil, fmt.Errorf("padis: %s is not a ticket control message", m.ID())
	}
	t := &TicketControl{Response: m.ID().Type == MsgTKCRES}

	tkt, ok := m.First(TagTKT)
	if !ok {
		return nil, fmt.Errorf("padis: ticket control carries no %s segment", TagTKT)
	}
	n, err := pnr.ParseTicketNumber(tkt.Get(0, 0))
	if err != nil {
		return nil, fmt.Errorf("padis: %w", err)
	}
	t.Number = n

	if org, ok := m.First("ORG"); ok {
		t.Party = org.Get(0, 0)
	}
	if rci, ok := m.First("RCI"); ok {
		t.Locator = rci.Get(0, 1)
	}
	if erc, ok := m.First(TagERC); ok {
		t.Refusal = erc.Get(0, 1)
		if t.Refusal == "" {
			t.Refusal = erc.Value(0)
		}
	}
	for _, seg := range m.Find(TagCPN) {
		num, err := strconv.Atoi(seg.Get(0, 0))
		if err != nil {
			continue
		}
		t.Coupons = append(t.Coupons, CouponRef{
			Number: num, Status: pnr.CouponStatus(strings.ToUpper(seg.Get(0, 1))),
		})
	}
	// A request that names no coupon says nothing. A response may legitimately
	// name none: it refused everything asked of it, and the reason is in ERC.
	if len(t.Coupons) == 0 && !t.Response {
		return nil, fmt.Errorf("padis: ticket control request names no coupons")
	}
	if t.Response && len(t.Coupons) == 0 && t.Refusal == "" {
		return nil, fmt.Errorf("padis: ticket control response neither applied a coupon nor gave a reason")
	}
	return t, nil
}
