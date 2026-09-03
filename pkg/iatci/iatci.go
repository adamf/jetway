// Package iatci is inter-airline through check-in: the EDIFACT dialogue by
// which the departure-control system of one carrier checks a connecting
// passenger in on another carrier's flight, so the passenger is accepted
// once and boards both.
//
// Standing: inferred, closely. The message structures and segment layouts
// (DCQCKI request, DCRCKA response; LOR, FDQ, PPD, PRD, PSD, PBD, PSI, PAP,
// FDR, RAD, ERD, WAD, PFD, FSD) are those of the IATA PADIS EDIFACT message
// standards release 01.1 as mirrored publicly by EDI schema vendors, element
// by element; the IATCI Implementation Guide that says how carriers use them
// is distributed to members only and was not consulted. What is here is
// therefore a profile of the standard's structure, not a conformance claim:
// the element positions are the standard's, the usage is this package's.
//
// The exchange, in the shape wholesky drives it: the delivering carrier's
// DCS, accepting a passenger whose onward segment is another carrier's,
// sends DCQCKI naming the inbound flight it holds and the outbound flight
// it asks for; the receiving carrier's DCS accepts the passenger on the
// outbound flight and answers DCRCKA with the seat and boarding data, or
// the reason it could not.
package iatci

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/adamf/jetway/pkg/edifact"
	"github.com/adamf/jetway/pkg/padis"
)

// Message types, UNH S009/0065.
const (
	MsgDCQCKI = "DCQCKI" // through check-in request
	MsgDCRCKA = "DCRCKA" // through check-in response
	MsgDCQCKX = "DCQCKX" // through check-in cancel request
	MsgDCRCKX = "DCRCKX" // through check-in cancel response
)

// Release is the PADIS release the structures here follow, as declared in
// UNH S009: version 01, release 1, controlling agency IA.
var Release = edifact.MessageID{Version: "01", Release: "1", ControllingAgency: "IA"}

// Flight is one leg as the dialogue names it: the marketing carrier, the
// number, the departure date, board and off points.
type Flight struct {
	Carrier   string
	Operating string // operating carrier when it differs, FDQ C013 second code
	Number    string
	Date      time.Time // departure date; time of day ignored when zero
	Board     string
	Off       string
	// Arrives is the inbound flight's arrival, when known (DCQCKI FDQ 2107).
	Arrives time.Time
}

// Tag is one bag tag range: carrier, first serial, how many consecutive,
// and where the bags are tagged to.
type Tag struct {
	Carrier string
	Serial  string
	Count   int
	Dest    string
}

// Passenger is one passenger in the request.
type Passenger struct {
	Surname string
	Given   string
	// Type is A adult, C child; Infant marks an accompanying infant on the
	// same name (PPD C017).
	Type   string
	Infant bool
	// Ref is the requesting DCS's own reference for the passenger (PPD C692),
	// echoed in the response so the two sides agree who was accepted.
	Ref string
	// Class is the booking class on the outbound flight and Locator the
	// receiving carrier's record locator for it, when the delivering
	// carrier knows it (PRD).
	Class    string
	Status   string // PRD 9868-ish reservation status: OK, RQ, WL, SA
	Locator  string
	Ticket   string
	SeatWant string // a requested seat (PSD 9809), or "" for any
	// Bags checked through: pieces, kilos, and the tags issued.
	Pieces int
	Weight int
	Tags   []Tag
	// SSRs are the special service requests the receiving carrier should
	// honour (PSI), and FrequentFlyer the number to credit.
	SSRs          []string
	FrequentFlyer string
	// Document is the travel document the receiving carrier's border rules
	// may need (PAP): number and issuing country.
	Document        string
	DocumentCountry string
	DateOfBirth     time.Time
}

// CheckInRequest is a DCQCKI.
type CheckInRequest struct {
	// Requestor is the carrier and station asking (LOR).
	Requestor        string
	RequestorStation string
	// Inbound is the flight the requestor holds the passenger on; Outbound is
	// the receiving carrier's flight the passenger is to be checked in on.
	Inbound    Flight
	Outbound   Flight
	Passengers []Passenger
}

// Result is what the receiving carrier did for one passenger.
type Result struct {
	Ref     string
	Surname string
	Given   string
	// Status is RAD 9869: H granted, I not granted, and the data-required
	// codes when a border rule wants more.
	Status string
	Seat   string
	Cabin  string // cabin class designator (PFD 9854), e.g. Y
	// Sequence is the boarding sequence the receiving carrier issued, carried
	// as the boarding security identifier (PFD 9874).
	Sequence int
	// BoardingPass says the receiving carrier expects the requestor to print
	// the pass (PFD 9850: Y issue, N do not).
	BoardingPass bool
	Pieces       int
	Weight       int
	Tags         []Tag
	Errors       []Error
}

// Error is one ERD or WAD entry.
type Error struct {
	Level string // 0 system, 1 application
	Code  string
	Text  string
}

// CheckInResponse is a DCRCKA.
type CheckInResponse struct {
	Flight Flight // FDR: the outbound flight answered for
	// Status is the RAD processing status for the whole request: H granted,
	// I not granted, O processed with data following, X non-recoverable.
	Status string
	// Gate, Terminal and BoardingTime are what the receiving carrier
	// publishes for the flight (FSD).
	Gate         string
	Terminal     string
	BoardingTime string // HHMM
	Errors       []Error
	Passengers   []Result
}

// Granted reports whether the response accepted every passenger.
func (r *CheckInResponse) Granted() bool {
	if r.Status != "H" && r.Status != "O" && r.Status != "P" {
		return false
	}
	for _, p := range r.Passengers {
		if p.Status != "" && p.Status != "H" {
			return false
		}
	}
	return len(r.Errors) == 0
}

// Describe renders the request for logs.
func (r *CheckInRequest) Describe() string {
	return fmt.Sprintf("through check-in %d pax from %s%s %s to %s%s %s %s-%s", len(r.Passengers),
		r.Inbound.Carrier, r.Inbound.Number, r.Inbound.Board, r.Outbound.Carrier, r.Outbound.Number,
		wireDate(r.Outbound.Date), r.Outbound.Board, r.Outbound.Off)
}

// IsCheckIn reports whether m is a through check-in request.
func IsCheckIn(m edifact.Message) bool { return m.ID().Type == MsgDCQCKI }

// IsCheckInResponse reports whether m is a through check-in response.
func IsCheckInResponse(m edifact.Message) bool { return m.ID().Type == MsgDCRCKA }

func id(t string) edifact.MessageID {
	return edifact.MessageID{Type: t, Version: Release.Version, Release: Release.Release, ControllingAgency: Release.ControllingAgency}
}

// dateTime renders 2281/2107 as DDMMYY[HHMM], the PADIS date form.
func dateTime(t time.Time, withTime bool) string {
	if t.IsZero() {
		return ""
	}
	if withTime && (t.Hour() != 0 || t.Minute() != 0) {
		return t.Format("0201061504")
	}
	return t.Format("020106")
}

func wireDate(t time.Time) string { return strings.ToUpper(t.Format("02Jan")) }

func parseDateTime(s string) time.Time {
	switch len(s) {
	case 6:
		t, _ := time.Parse("020106", s)
		return t
	case 10:
		t, _ := time.Parse("0201061504", s)
		return t
	}
	return time.Time{}
}

// BuildDCQCKI renders a through check-in request. The FDQ carries the
// outbound flight first (C013/C014/2281/3215/3259) and the inbound flight
// second (C015/C016/2281/2107/3215/3259), which is the standard's order:
// the receiving carrier reads what it is being asked for before what the
// passenger is arriving on.
func BuildDCQCKI(req *CheckInRequest, o padis.BuildOptions) (*edifact.Interchange, error) {
	if req.Outbound.Carrier == "" || req.Outbound.Number == "" || req.Outbound.Board == "" {
		return nil, errors.New("iatci: the outbound flight needs carrier, number and board point")
	}
	if len(req.Passengers) == 0 {
		return nil, errors.New("iatci: no passengers to check in")
	}
	body := []edifact.Segment{
		edifact.Seg("LOR", edifact.Comp(req.Requestor, req.RequestorStation)),
		fdq(req.Outbound, req.Inbound),
	}
	for _, p := range req.Passengers {
		body = append(body, ppd(p))
		if p.Class != "" || p.Locator != "" || p.Ticket != "" {
			status := p.Status
			if status == "" {
				status = "OK"
			}
			body = append(body, edifact.Seg("PRD", edifact.Comp(p.Class), edifact.Simple(status),
				edifact.Comp(), edifact.Simple(p.Locator), edifact.Simple(""),
				edifact.Comp(req.Outbound.Carrier, p.Locator), edifact.Simple(p.Ticket)))
		}
		if p.SeatWant != "" {
			body = append(body, edifact.Seg("PSD", edifact.Comp(), edifact.Simple(p.SeatWant)))
		}
		if p.Pieces > 0 || len(p.Tags) > 0 {
			body = append(body, pbd(p.Pieces, p.Weight, p.Tags))
		}
		for _, code := range p.SSRs {
			body = append(body, edifact.Seg("PSI", edifact.Simple(""), edifact.Comp(code, req.Outbound.Carrier)))
		}
		if p.FrequentFlyer != "" {
			body = append(body, edifact.Seg("PSI", edifact.Simple(""), edifact.Comp("FQTV", req.Outbound.Carrier, p.FrequentFlyer)))
		}
		if p.Document != "" || !p.DateOfBirth.IsZero() {
			body = append(body, edifact.Seg("PAP", edifact.Simple(p.Type), edifact.Simple(p.Surname), edifact.Simple(p.Given),
				edifact.Simple(dateTime(p.DateOfBirth, false)), edifact.Simple(""), edifact.Simple(""), edifact.Comp(),
				edifact.Comp("PT", p.Document, p.DocumentCountry)))
		}
	}
	ic := newInterchange(MsgDCQCKI, o)
	ic.AddMessage(msgRef(o), id(MsgDCQCKI), body...)
	ic.Finalize()
	return ic, nil
}

func newInterchange(t string, o padis.BuildOptions) *edifact.Interchange {
	now := o.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	v := o.SyntaxVersion
	if v == 0 {
		v = 3
	}
	return edifact.NewInterchange(edifact.UNBParams{
		CharsetID: "UNOA", SyntaxVersion: v,
		Sender: o.Sender, Recipient: o.Recipient,
		Date: now.UTC().Format("060102"), Time: now.UTC().Format("1504"),
		ControlRef: o.ControlRef, AppRef: t, Test: o.Test,
	})
}

func msgRef(o padis.BuildOptions) string {
	if o.MessageRef == "" {
		return "1"
	}
	return o.MessageRef
}

func fdq(out, in Flight) edifact.Segment {
	return edifact.Seg("FDQ",
		edifact.Comp(out.Carrier, out.Operating),
		edifact.Comp(out.Number, ""),
		edifact.Simple(dateTime(out.Date, true)),
		edifact.Simple(out.Board),
		edifact.Simple(out.Off),
		edifact.Simple(""),
		edifact.Comp(in.Carrier, in.Operating),
		edifact.Comp(in.Number, ""),
		edifact.Simple(dateTime(in.Date, true)),
		edifact.Simple(dateTime(in.Arrives, true)),
		edifact.Simple(in.Board),
		edifact.Simple(in.Off),
	)
}

func ppd(p Passenger) edifact.Segment {
	typ := p.Type
	if typ == "" {
		typ = "A"
	}
	inf := "N"
	if p.Infant {
		inf = "Y"
	}
	return edifact.Seg("PPD",
		edifact.Simple(p.Surname),
		edifact.Comp(typ, inf),
		edifact.Comp(p.Ref),
		edifact.Simple(p.Given),
	)
}

func pbd(pieces, weight int, tags []Tag) edifact.Segment {
	elems := []edifact.Element{
		edifact.Comp(strconv.Itoa(pieces), itoaOrEmpty(weight)),
		edifact.Comp(),
		edifact.Comp(),
	}
	var occ []edifact.Composite
	for _, t := range tags {
		occ = append(occ, edifact.Composite{t.Carrier, t.Serial, strconv.Itoa(max(1, t.Count)), t.Dest})
	}
	if len(occ) > 0 {
		elems = append(elems, edifact.Repeat(occ...))
	}
	return edifact.Seg("PBD", elems...)
}

func itoaOrEmpty(n int) string {
	if n == 0 {
		return ""
	}
	return strconv.Itoa(n)
}

// ParseDCQCKI reads a through check-in request.
func ParseDCQCKI(m edifact.Message) (*CheckInRequest, error) {
	if !IsCheckIn(m) {
		return nil, fmt.Errorf("iatci: %s is not a %s", m.ID().Type, MsgDCQCKI)
	}
	req := &CheckInRequest{}
	var cur *Passenger
	for _, seg := range m.Segments {
		switch seg.Tag {
		case "LOR":
			req.Requestor, req.RequestorStation = seg.Get(0, 0), seg.Get(0, 1)
		case "FDQ":
			req.Outbound = Flight{Carrier: seg.Get(0, 0), Operating: seg.Get(0, 1), Number: seg.Get(1, 0),
				Date: parseDateTime(seg.Value(2)), Board: seg.Value(3), Off: seg.Value(4)}
			req.Inbound = Flight{Carrier: seg.Get(6, 0), Operating: seg.Get(6, 1), Number: seg.Get(7, 0),
				Date: parseDateTime(seg.Value(8)), Arrives: parseDateTime(seg.Value(9)), Board: seg.Value(10), Off: seg.Value(11)}
		case "PPD":
			req.Passengers = append(req.Passengers, Passenger{Surname: seg.Value(0), Type: seg.Get(1, 0),
				Infant: seg.Get(1, 1) == "Y", Ref: seg.Get(2, 0), Given: seg.Value(3)})
			cur = &req.Passengers[len(req.Passengers)-1]
		case "PRD":
			if cur == nil {
				continue
			}
			cur.Class, cur.Status = seg.Get(0, 0), seg.Value(1)
			cur.Locator = seg.Value(3)
			if cur.Locator == "" {
				cur.Locator = seg.Get(5, 1)
			}
			cur.Ticket = seg.Value(6)
		case "PSD":
			if cur != nil {
				cur.SeatWant = seg.Value(1)
			}
		case "PBD":
			if cur == nil {
				continue
			}
			cur.Pieces, _ = strconv.Atoi(seg.Get(0, 0))
			cur.Weight, _ = strconv.Atoi(seg.Get(0, 1))
			cur.Tags = parseTags(seg.Elem(3))
		case "PSI":
			if cur == nil {
				continue
			}
			code := seg.Get(1, 0)
			switch {
			case code == "FQTV":
				cur.FrequentFlyer = seg.Get(1, 2)
			case code != "":
				cur.SSRs = append(cur.SSRs, code)
			}
		case "PAP":
			if cur == nil {
				continue
			}
			cur.DateOfBirth = parseDateTime(seg.Value(3))
			if seg.Get(7, 0) == "PT" {
				cur.Document, cur.DocumentCountry = seg.Get(7, 1), seg.Get(7, 2)
			}
		}
	}
	if req.Outbound.Carrier == "" {
		return nil, errors.New("iatci: request names no outbound flight")
	}
	if len(req.Passengers) == 0 {
		return nil, errors.New("iatci: request names no passenger")
	}
	return req, nil
}

func parseTags(e edifact.Element) []Tag {
	var out []Tag
	for _, c := range e {
		if c.Get(1) == "" {
			continue
		}
		n, _ := strconv.Atoi(c.Get(2))
		out = append(out, Tag{Carrier: c.Get(0), Serial: c.Get(1), Count: max(1, n), Dest: c.Get(3)})
	}
	return out
}

// BuildDCRCKA renders the receiving carrier's answer.
func BuildDCRCKA(res *CheckInResponse, o padis.BuildOptions) (*edifact.Interchange, error) {
	f := res.Flight
	body := []edifact.Segment{
		edifact.Seg("FDR", edifact.Comp(f.Carrier, f.Operating), edifact.Comp(f.Number, ""),
			edifact.Simple(dateTime(f.Date, true)), edifact.Simple(f.Board), edifact.Simple(f.Off)),
		edifact.Seg("RAD", edifact.Simple("I"), edifact.Simple(res.Status)),
	}
	for _, e := range res.Errors {
		body = append(body, edifact.Seg("ERD", edifact.Comp(e.Level, e.Code, e.Text)))
	}
	if res.Gate != "" || res.Terminal != "" || res.BoardingTime != "" {
		body = append(body, edifact.Seg("FSD", edifact.Simple(res.BoardingTime), edifact.Simple(res.Terminal), edifact.Simple(res.Gate)))
	}
	for _, p := range res.Passengers {
		body = append(body, edifact.Seg("PPD", edifact.Simple(p.Surname), edifact.Comp(), edifact.Comp(p.Ref), edifact.Simple(p.Given)))
		if p.Seat != "" || p.Sequence > 0 || p.Cabin != "" {
			issue := "N"
			if p.BoardingPass {
				issue = "Y"
			}
			body = append(body, edifact.Seg("PFD",
				edifact.Comp(p.Seat),
				edifact.Comp("", p.Cabin),
				edifact.Comp(itoaOrEmpty(p.Sequence)),
				edifact.Simple(issue)))
		}
		if p.Pieces > 0 || len(p.Tags) > 0 {
			body = append(body, pbd(p.Pieces, p.Weight, p.Tags))
		}
		for _, e := range p.Errors {
			body = append(body, edifact.Seg("WAD", edifact.Comp(e.Level, e.Code, e.Text)))
		}
		if p.Status != "" {
			body = append(body, edifact.Seg("PAP", edifact.Simple(""), edifact.Simple(p.Surname), edifact.Simple(p.Given),
				edifact.Simple(""), edifact.Simple(p.Status)))
		}
	}
	ic := newInterchange(MsgDCRCKA, o)
	ic.AddMessage(msgRef(o), id(MsgDCRCKA), body...)
	ic.Finalize()
	return ic, nil
}

// ParseDCRCKA reads the receiving carrier's answer.
func ParseDCRCKA(m edifact.Message) (*CheckInResponse, error) {
	if !IsCheckInResponse(m) {
		return nil, fmt.Errorf("iatci: %s is not a %s", m.ID().Type, MsgDCRCKA)
	}
	res := &CheckInResponse{}
	var cur *Result
	for _, seg := range m.Segments {
		switch seg.Tag {
		case "FDR":
			res.Flight = Flight{Carrier: seg.Get(0, 0), Operating: seg.Get(0, 1), Number: seg.Get(1, 0),
				Date: parseDateTime(seg.Value(2)), Board: seg.Value(3), Off: seg.Value(4)}
		case "RAD":
			res.Status = seg.Value(1)
		case "ERD":
			res.Errors = append(res.Errors, Error{Level: seg.Get(0, 0), Code: seg.Get(0, 1), Text: seg.Get(0, 2)})
		case "FSD":
			res.BoardingTime, res.Terminal, res.Gate = seg.Value(0), seg.Value(1), seg.Value(2)
		case "PPD":
			res.Passengers = append(res.Passengers, Result{Surname: seg.Value(0), Ref: seg.Get(2, 0), Given: seg.Value(3)})
			cur = &res.Passengers[len(res.Passengers)-1]
		case "PFD":
			if cur == nil {
				continue
			}
			cur.Seat = seg.Get(0, 0)
			cur.Cabin = seg.Get(1, 1)
			cur.Sequence, _ = strconv.Atoi(seg.Get(2, 0))
			cur.BoardingPass = seg.Value(3) == "Y"
		case "PBD":
			if cur == nil {
				continue
			}
			cur.Pieces, _ = strconv.Atoi(seg.Get(0, 0))
			cur.Weight, _ = strconv.Atoi(seg.Get(0, 1))
			cur.Tags = parseTags(seg.Elem(3))
		case "WAD":
			if cur != nil {
				cur.Errors = append(cur.Errors, Error{Level: seg.Get(0, 0), Code: seg.Get(0, 1), Text: seg.Get(0, 2)})
			}
		case "PAP":
			if cur != nil {
				cur.Status = seg.Value(4)
			}
		}
	}
	if res.Status == "" {
		return nil, errors.New("iatci: response carries no RAD status")
	}
	return res, nil
}

// Refusal codes from ERD/WAD element 9845 that this package uses.
const (
	ErrSurnameNotFound = "1"
	ErrSeatUnavailable = "2"
	ErrInvalidFlight   = "5"
	ErrFlightCancelled = "15"
	ErrFlightClosed    = "35"
	ErrFlightFull      = "29"
	ErrFlightDeparted  = "97"
	ErrNotSupported    = "702"
)
