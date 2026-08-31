package pnr

import (
	"testing"
	"time"
)

func airSeg(status string) Segment {
	return Segment{
		Type: SegmentAir, Carrier: "BA", FlightNum: "0117", Class: "Y",
		Board: "LHR", Off: "JFK", Status: status, Seats: 1,
		Depart: time.Now().UTC().AddDate(0, 1, 0),
	}
}

// A refusal arriving after a cancellation must not revive the record. This is
// the exact sequence an EDIFACT cancel round-trip once produced: the record
// cancelled to XX, then a stray NO reply landed on a segment, and because NO
// was not on Recompute's hand-written dead list the record rose from the dead.
func TestRecomputeRefusalDoesNotReviveCancelled(t *testing.T) {
	p := &PNR{Status: StatusOpen, Segments: []Segment{airSeg("XX")}}
	p.Recompute()
	if p.Status != StatusCancelled {
		t.Fatalf("after XX: status = %q, want cancelled", p.Status)
	}
	p.Segments[0].Status = "NO"
	p.Recompute()
	if p.Status != StatusCancelled {
		t.Errorf("after a late NO reply: status = %q, want still cancelled", p.Status)
	}
}

// Every refusal leaves nothing held; a record of nothing but refusals is a
// cancelled record. UC and UN were always treated so; NO must be too.
func TestRecomputeRefusalsAreNotLive(t *testing.T) {
	for _, code := range []string{"NO", "UC", "UN"} {
		p := &PNR{Status: StatusOpen, Segments: []Segment{airSeg(code)}}
		p.Recompute()
		if p.Status != StatusCancelled {
			t.Errorf("all-%s record: status = %q, want cancelled", code, p.Status)
		}
	}
}

// A waitlist answer holds something, so the record stays open.
func TestRecomputeWaitlistIsLive(t *testing.T) {
	for _, code := range []string{"US", "UU", "HL", "TL"} {
		p := &PNR{Status: StatusOpen, Segments: []Segment{airSeg(code)}}
		p.Recompute()
		if p.Status != StatusOpen {
			t.Errorf("%s record: status = %q, want open", code, p.Status)
		}
	}
}

// An ARNK is a placeholder, not a holding: a cancelled itinerary with a
// surface gap in it is still cancelled.
func TestRecomputeSurfaceSegmentIsNotAHolding(t *testing.T) {
	p := &PNR{Status: StatusOpen, Segments: []Segment{
		airSeg("XX"),
		{Type: SegmentSurface, Board: "JFK", Off: "EWR"},
		airSeg("XX"),
	}}
	p.Recompute()
	if p.Status != StatusCancelled {
		t.Errorf("XX + ARNK + XX: status = %q, want cancelled", p.Status)
	}
}

// Unknown codes must hold the record open: a code we cannot read is not a
// reason to cancel somebody's booking.
func TestRecomputeUnknownCodeStaysLive(t *testing.T) {
	p := &PNR{Status: StatusOpen, Segments: []Segment{airSeg("ZQ")}}
	p.Recompute()
	if p.Status != StatusOpen {
		t.Errorf("unknown code: status = %q, want open", p.Status)
	}
}
