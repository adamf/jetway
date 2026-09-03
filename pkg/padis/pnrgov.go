package padis

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/adamf/jetway/pkg/edifact"
	"github.com/adamf/jetway/pkg/pnr"
)

// PNRGOV is the passenger name record push a carrier makes to a state:
// every record holding a seat on a flight, with the reservation as booked
// and, once the flight is checked in, the seat, sequence and bags departure
// control gave each traveller.
//
// This part of the package is specified rather than inferred. IATA publishes
// the PADIS EDIFACT Implementation Guide for PNRGOV free of charge, and the
// segment layouts here follow it: MSG function 22 for the push, ORG and TVL
// naming the sender and the flight, EQN counting the records, SRC opening
// each one, and the check-in group of DAT, ORG, TRI, TIF, SSD and TBD under
// the travel segment. The tests carry the guide's own worked example
// verbatim. What the guide leaves to bilateral agreement -- the push times,
// the acknowledgement, history -- is left to the caller here too.
const (
	// MsgACKRES is the state's acknowledgement, sent only under a bilateral
	// agreement; this package recognises it and does not compose it.
	MsgACKRES = "ACKRES"

	// FuncPushToState is the MSG function of a PNR push (element 1225).
	FuncPushToState = "22"
	// FuncAcknowledge is the MSG function of an ACKRES.
	FuncAcknowledge = "23"

	// pnrgovVersion is the message version the guide's examples carry, the
	// 11.1 release of the PADIS directory.
	pnrgovVersion = "11"
)

// GovFlight is the flight a push is about: one leg, with the local departure
// and arrival as date and time.
type GovFlight struct {
	Carrier, Number  string
	Board, Off       string
	Departs, Arrives time.Time
}

// GovBag is one checked bag as the TBD segment carries it: the tag, the
// piece number within the passenger's bags, and where it is tagged to.
type GovBag struct {
	Tag         string
	Piece       int
	Destination string
}

// GovCheckIn is what departure control recorded for one traveller of the
// record on the pushed flight.
type GovCheckIn struct {
	// PaxRef is the traveller's reference within the record (TIF).
	PaxRef int
	// Station and Sequence make the boarding number, e.g. SIN-168.
	Station  string
	Sequence int
	Seat     string
	// Cabin is the compartment code the seat sits in, J or Y.
	Cabin       string
	Bags        []GovBag
	BagWeightKg int
}

// GovRecord is one record in a push, with the check-in data for the pushed
// flight when there is any.
type GovRecord struct {
	PNR     *pnr.PNR
	CheckIn []GovCheckIn
}

// GovPush is the content of a PNRGOV message in either direction.
type GovPush struct {
	// Sender is the system pushing, and Station its office (ORG).
	Sender, Station string
	Flight          GovFlight
	// Count is the EQN value on parse: the records the sender says the
	// message carries, which the check against len(Records) is for.
	Count   int
	Records []GovRecord
}

// Describe renders the push for logs.
func (p *GovPush) Describe() string {
	return fmt.Sprintf("PNRGOV %s%s %s %s-%s: %d record(s)", p.Flight.Carrier, p.Flight.Number,
		p.Flight.Departs.Format("02Jan"), p.Flight.Board, p.Flight.Off, len(p.Records))
}

// IsPNRGOV reports whether a message is a PNR push.
func IsPNRGOV(m edifact.Message) bool { return m.ID().Type == MsgPNRGOV }

// IsACKRES reports whether a message is a state's acknowledgement of a push.
func IsACKRES(m edifact.Message) bool { return m.ID().Type == MsgACKRES }

// BuildPNRGOV renders a push. Records with no passengers or no air segment
// are skipped: a state's PNR regime is about travellers on a flight, and a
// record without either is not one. The message reference (UNH 0068) is
// the common access reference the guide recommends, date/time/carrier/flight,
// so a state can tie a resend to the first attempt.
func BuildPNRGOV(p *GovPush, o BuildOptions) (*edifact.Interchange, error) {
	if p == nil || p.Flight.Carrier == "" || p.Flight.Number == "" {
		return nil, fmt.Errorf("padis: a PNR push names a flight")
	}
	fl := p.Flight
	body := []edifact.Segment{
		edifact.Seg("MSG", edifact.Comp("", FuncPushToState)),
		edifact.Seg("ORG", edifact.Comp(o.text(p.Sender), o.text(p.Station))),
		edifact.Seg("TVL",
			edifact.Comp(FormatTVLDate(fl.Departs), hhmm(fl.Departs), FormatTVLDate(fl.Arrives), hhmm(fl.Arrives)),
			edifact.Simple(fl.Board), edifact.Simple(fl.Off),
			edifact.Simple(fl.Carrier), edifact.Simple(fl.Number)),
	}
	var recs []edifact.Segment
	n := 0
	for _, r := range p.Records {
		segs := govRecordSegments(r, p, o)
		if segs == nil {
			continue
		}
		recs = append(recs, segs...)
		n++
	}
	body = append(body, edifact.Seg("EQN", edifact.Simple(strconv.Itoa(n))))
	body = append(body, recs...)

	ic := newInterchange(MsgPNRGOV, o)
	id := edifact.MessageID{Type: MsgPNRGOV, Version: pnrgovVersion, Release: "1", ControllingAgency: "IA"}
	ref := o.MessageRef
	if ref == "" {
		ref = "1"
	}
	ic.AddMessage(ref, id, body...)
	ic.Finalize()
	return ic, nil
}

// CommonAccessRef is the reference the guide's examples carry in the UNH:
// the push date and time, the carrier and the flight. Callers put it in
// BuildOptions.MessageRef when the state wants it.
func CommonAccessRef(fl GovFlight, at time.Time) string {
	return fmt.Sprintf("%s/%s/%s/%s", at.UTC().Format("020106"), at.UTC().Format("1504"), fl.Carrier, fl.Number)
}

func hhmm(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("1504")
}

func govRecordSegments(r GovRecord, p *GovPush, o BuildOptions) []edifact.Segment {
	rec := r.PNR
	if rec == nil || len(rec.Passengers) == 0 {
		return nil
	}
	air := 0
	for _, s := range rec.Segments {
		if s.Type == pnr.SegmentAir {
			air++
		}
	}
	if air == 0 {
		return nil
	}
	party := rec.Origin.Party
	if party == "" {
		party = p.Sender
	}
	created := ""
	ctime := ""
	if !rec.CreatedAt.IsZero() {
		created, ctime = rec.CreatedAt.UTC().Format("020106"), rec.CreatedAt.UTC().Format("1504")
	}
	out := []edifact.Segment{
		edifact.Seg("SRC"),
		edifact.Seg("RCI", edifact.Comp(o.text(party), rec.RecordLocator, "", created, ctime)),
	}
	if !rec.UpdatedAt.IsZero() {
		out = append(out, edifact.Seg("DAT", edifact.Comp("700", rec.UpdatedAt.UTC().Format("020106"), rec.UpdatedAt.UTC().Format("1504"))))
	}
	out = append(out, edifact.Seg("ORG", edifact.Comp(o.text(party), o.text(rec.Origin.Agent))))
	for _, pax := range rec.Passengers {
		out = append(out, tifOne(pax, o))
		for _, ff := range pax.FrequentFlyer {
			carrier, num, _ := strings.Cut(ff, ":")
			out = append(out, edifact.Seg("FTI", edifact.Comp(carrier, num)))
		}
		for _, s := range rec.SSRs {
			if s.PaxRef != pax.Ref || s.SegmentRef != 0 {
				continue
			}
			out = append(out, govSSR(s, p.Flight.Carrier, "", "", 0))
		}
	}
	for _, s := range rec.SSRs {
		if s.PaxRef == 0 && s.SegmentRef == 0 {
			out = append(out, govSSR(s, p.Flight.Carrier, "", "", 0))
		}
	}
	for _, s := range rec.Segments {
		if s.Type != pnr.SegmentAir {
			continue
		}
		out = append(out, tvlSegment(s))
		status := s.Status
		if status == "" {
			status = "HK"
		}
		out = append(out, edifact.Seg("RPI", edifact.Simple(strconv.Itoa(max(s.Seats, 1))), edifact.Simple(status)))
		for _, ssr := range rec.SSRs {
			if ssr.SegmentRef == s.Ref {
				out = append(out, govSSR(ssr, s.Carrier, s.Board, s.Off, ssr.PaxRef))
			}
		}
		if !onPushedFlight(s, p.Flight) {
			continue
		}
		for _, ci := range r.CheckIn {
			pax, ok := paxByRef(rec, ci.PaxRef)
			if !ok {
				continue
			}
			bn := ci.Station + "-" + strconv.Itoa(ci.Sequence)
			out = append(out,
				edifact.Seg("DAT"),
				edifact.Seg("ORG", edifact.Simple(p.Flight.Carrier), edifact.Simple(""), edifact.Simple(""), edifact.Simple(""), edifact.Simple("A")),
				edifact.Seg("TRI", edifact.Simple(""), edifact.Comp(bn, "", "", strconv.Itoa(pax.Ref))),
				tifOne(pax, o),
				edifact.Seg("SSD", edifact.Simple(ci.Seat), edifact.Simple(""), edifact.Simple(""), edifact.Simple(""), edifact.Simple(ci.Cabin)),
			)
			if len(ci.Bags) > 0 {
				els := []edifact.Element{
					edifact.Simple(""),
					edifact.Comp(strconv.Itoa(len(ci.Bags)), strconv.Itoa(ci.BagWeightKg), "700"),
					edifact.Simple(""),
					edifact.Comp("HP", bn),
				}
				for i, b := range ci.Bags {
					piece := b.Piece
					if piece == 0 {
						piece = i + 1
					}
					els = append(els, edifact.Comp("618", b.Tag, strconv.Itoa(piece), b.Destination))
				}
				out = append(out, edifact.Seg("TBD", els...))
			}
		}
	}
	return out
}

// tifOne is a single traveller's TIF: surname, then given name with the
// title run on, traveller type and reference, the way the guide carries a
// name in both the reservation and the check-in groups.
func tifOne(pax pnr.Passenger, o BuildOptions) edifact.Segment {
	t := pax.Type
	if t == "" {
		t = pnr.PaxAdult
		if pax.Infant {
			t = pnr.PaxInfant
		}
	}
	return edifact.Seg("TIF", edifact.Simple(o.text(pax.Surname)),
		edifact.Comp(o.text(pax.Given+pax.Title), string(t), strconv.Itoa(pax.Ref)))
}

// govSSR lays an SSR out as the guide does: the free text is the ninth
// component, after the board and off points, and the traveller reference
// rides in a second element when the SSR is one passenger's.
func govSSR(s pnr.SSR, carrier, board, off string, paxRef int) edifact.Segment {
	c := s.Carrier
	if c == "" {
		c = carrier
	}
	count := ""
	if s.Count > 0 {
		count = strconv.Itoa(s.Count)
	}
	els := []edifact.Element{edifact.Comp(s.Code, s.Status, count, c, "", "", board, off, s.Text)}
	if paxRef > 0 {
		els = append(els, edifact.Comp("", "", strconv.Itoa(paxRef)))
	}
	return edifact.Seg("SSR", els...)
}

func onPushedFlight(s pnr.Segment, fl GovFlight) bool {
	carrier := s.OperatingCarrier
	if carrier == "" {
		carrier = s.Carrier
	}
	if carrier != fl.Carrier && s.Carrier != fl.Carrier {
		return false
	}
	if strings.TrimLeft(s.FlightNum, "0") != strings.TrimLeft(fl.Number, "0") {
		return false
	}
	return fl.Board == "" || s.Board == fl.Board
}

func paxByRef(rec *pnr.PNR, ref int) (pnr.Passenger, bool) {
	for _, p := range rec.Passengers {
		if p.Ref == ref {
			return p, true
		}
	}
	return pnr.Passenger{}, false
}

// ParsePNRGOV reads a push. Reservation history (ABI, SAC, LTS) and the
// hotel and car groups are passed over: they are the record's past, and the
// receiver of a push wants who is on the flight now. Segments the layout
// does not know are ignored rather than rejected, because a state's own
// requirements (Appendix A of the guide lists them per state) add elements
// this profile has no view on.
func ParsePNRGOV(m edifact.Message) (*GovPush, error) {
	if !IsPNRGOV(m) {
		return nil, fmt.Errorf("padis: not a PNRGOV message: %s", m.ID().Type)
	}
	p := &GovPush{}
	var (
		rec      *GovRecord
		seg      *pnr.Segment
		ci       *GovCheckIn
		lastPax  int
		tail     bool // inside history or the other-record groups of a record
		checkin  bool // inside the check-in group under a travel segment
		orgSeen  bool // the record's originator ORG has been read
		paxCount int
	)
	finishRecord := func() {
		if rec != nil {
			p.Records = append(p.Records, *rec)
		}
		rec, seg, ci, lastPax, tail, checkin, orgSeen, paxCount = nil, nil, nil, 0, false, false, false, 0
	}
	for _, s := range m.Segments {
		switch s.Tag {
		case "ORG":
			if rec == nil {
				p.Sender, p.Station = s.Get(0, 0), s.Get(0, 1)
			} else if checkin {
				// ORG+carrier++++A opens one traveller's check-in data.
				rec.CheckIn = append(rec.CheckIn, GovCheckIn{})
				ci = &rec.CheckIn[len(rec.CheckIn)-1]
			} else if !tail && !orgSeen {
				orgSeen = true
				if party := s.Get(0, 0); party != "" {
					rec.PNR.Origin.Party = party
				}
				rec.PNR.Origin.Agent = s.Get(0, 1)
			}
		case "TVL":
			if rec == nil {
				p.Flight = govFlightFrom(s)
				continue
			}
			if tail {
				continue
			}
			checkin, ci = false, nil
			dep, wire, _ := parseTVLDate(s.Get(0, 0), time.Time{})
			ns := pnr.Segment{Ref: len(rec.PNR.Segments) + 1, Type: pnr.SegmentAir,
				Depart: dep, DepartTime: s.Get(0, 1), ArriveTime: s.Get(0, 3),
				Board: s.Value(1), Off: s.Value(2), Carrier: s.Get(3, 0), OperatingCarrier: s.Get(3, 1),
				FlightNum: s.Get(4, 0), Class: s.Get(4, 1), WireDate: wire}
			rec.PNR.Segments = append(rec.PNR.Segments, ns)
			seg = &rec.PNR.Segments[len(rec.PNR.Segments)-1]
		case "EQN":
			if rec == nil {
				p.Count = atoiOr(s.Value(0), 0)
			} else {
				tail = true
			}
		case "SRC":
			finishRecord()
			rec = &GovRecord{PNR: &pnr.PNR{}}
		case "RCI":
			if rec == nil || tail || rec.PNR.RecordLocator != "" {
				continue
			}
			if rec.PNR.Origin.Party == "" {
				rec.PNR.Origin.Party = s.Get(0, 0)
			}
			rec.PNR.RecordLocator = s.Get(0, 1)
			if d, t := s.Get(0, 3), s.Get(0, 4); d != "" {
				rec.PNR.CreatedAt = govTime(d, t)
			}
		case "DAT":
			if rec == nil || tail {
				continue
			}
			if s.Get(0, 0) == "700" {
				rec.PNR.UpdatedAt = govTime(s.Get(0, 1), s.Get(0, 2))
			} else if seg != nil {
				// DAT with no qualifier, or a check-in qualifier, under a
				// travel segment opens the check-in group.
				checkin = true
			}
		case "TIF":
			if rec == nil || tail {
				continue
			}
			ref := atoiOr(s.Get(1, 2), 0)
			if checkin && ci != nil {
				ci.PaxRef = ref
				continue
			}
			given, title := pnr.SplitTitle(s.Get(1, 0))
			paxCount++
			if ref == 0 {
				ref = paxCount
			}
			pt := pnr.PassengerType(s.Get(1, 1))
			rec.PNR.Passengers = append(rec.PNR.Passengers, pnr.Passenger{
				Ref: ref, Surname: s.Value(0), Given: given, Title: title, Type: pt, Infant: pt == pnr.PaxInfant,
			})
			lastPax = ref
		case "FTI":
			if rec != nil && !tail && !checkin && len(rec.PNR.Passengers) > 0 {
				pax := &rec.PNR.Passengers[len(rec.PNR.Passengers)-1]
				pax.FrequentFlyer = append(pax.FrequentFlyer, s.Get(0, 0)+":"+s.Get(0, 1))
			}
		case "RPI":
			if seg != nil && !tail && !checkin {
				seg.Seats = atoiOr(s.Value(0), 1)
				seg.Status = s.Value(1)
			}
		case "SSR":
			if rec == nil || tail || checkin {
				continue
			}
			ssr := pnr.SSR{Code: s.Get(0, 0), Status: s.Get(0, 1), Count: atoiOr(s.Get(0, 2), 0),
				Carrier: s.Get(0, 3), Text: s.Get(0, 8), Sensitive: sensitiveSSR(s.Get(0, 0))}
			if seg != nil {
				ssr.SegmentRef = seg.Ref
				ssr.PaxRef = atoiOr(s.Get(1, 2), 0)
			} else {
				ssr.PaxRef = lastPax
			}
			rec.PNR.SSRs = append(rec.PNR.SSRs, ssr)
		case "TRI":
			if ci != nil {
				bn := s.Get(1, 0)
				if st, seq, ok := strings.Cut(bn, "-"); ok {
					ci.Station, ci.Sequence = st, atoiOr(seq, 0)
				} else {
					ci.Sequence = atoiOr(bn, 0)
				}
				if r := atoiOr(s.Get(1, 3), 0); r != 0 {
					ci.PaxRef = r
				}
			}
		case "SSD":
			if ci != nil {
				ci.Seat, ci.Cabin = s.Value(0), s.Value(4)
			}
		case "TBD":
			if ci != nil {
				ci.BagWeightKg = atoiOr(s.Get(1, 1), 0)
				for i := 4; i < len(s.Elements); i++ {
					if s.Get(i, 0) != "618" {
						continue
					}
					ci.Bags = append(ci.Bags, GovBag{Tag: s.Get(i, 1), Piece: atoiOr(s.Get(i, 2), 0), Destination: s.Get(i, 3)})
				}
			}
		case "ABI", "MSG":
			if rec != nil {
				tail = true
			}
		}
	}
	finishRecord()
	return p, nil
}

func govFlightFrom(s edifact.Segment) GovFlight {
	fl := GovFlight{Board: s.Value(1), Off: s.Value(2), Carrier: s.Get(3, 0), Number: s.Get(4, 0)}
	fl.Departs = govTime(s.Get(0, 0), s.Get(0, 1))
	fl.Arrives = govTime(s.Get(0, 2), s.Get(0, 3))
	if fl.Arrives.IsZero() && s.Get(0, 3) != "" {
		fl.Arrives = govTime(s.Get(0, 0), s.Get(0, 3))
	}
	return fl
}

// govTime joins a DDMMYY date and an HHMM time; a missing time is midnight,
// a missing date is the zero time.
func govTime(d, t string) time.Time {
	if len(d) != 6 {
		return time.Time{}
	}
	day, err := time.Parse("020106", d)
	if err != nil {
		return time.Time{}
	}
	if len(t) == 4 {
		if hm, err := time.Parse("1504", t); err == nil {
			day = day.Add(time.Duration(hm.Hour())*time.Hour + time.Duration(hm.Minute())*time.Minute)
		}
	}
	return day
}
