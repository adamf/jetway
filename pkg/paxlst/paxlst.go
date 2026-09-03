// Package paxlst is Advance Passenger Information: the UN/EDIFACT PAXLST
// message an airline sends a border control agency before an aircraft
// departs, naming everyone on board with the data from their travel
// documents.
//
// Standing: specified. The WCO/IATA/ICAO Passenger List Message (PAXLST)
// Implementation Guide is public (Appendix IIA to the API Guidelines,
// version 6.0, November 2016), and this package follows its segment tables
// and is tested against its own worked examples verbatim: the single-sector
// passenger list, the crew report, and the progressive flight. The UN/EDIFACT
// directory version is D.15B as the guide's examples use; agencies fix the
// version bilaterally.
//
// The guide's air-mode subset, in message order:
//
//	UNH BGM RFF? NAD+MS COM? { TDT { LOC DTM? } } { NAD+FL|FM|DDT|DDU ATT DTM MEA GEI FTX LOC.. COM EMP NAT RFF.. { DOC DTM LOC } } CNT UNT
package paxlst

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/adamf/jetway/pkg/edifact"
)

// MsgPAXLST is the message type.
const MsgPAXLST = "PAXLST"

// ID is the UNH message identifier the guide's examples carry:
// PAXLST:D:15B:UN:IATA.
var ID = edifact.MessageID{Type: MsgPAXLST, Version: "D", Release: "15B", ControllingAgency: "UN", AssociationCode: "IATA"}

// BGM document codes (1001).
const (
	ListPassengers = "745" // passenger list
	ListCrew       = "250" // crew list declaration
	ListFlightStat = "266" // change in flight status (interactive API)
	ListMasterCrew = "336" // master crew list declaration
)

// BGM document identifiers (1004), Table 4.5.1.
const (
	FuncChangePassenger = "CP"
	FuncCancelReserv    = "XR"
	FuncReduction       = "RP"
	FuncFlightClose     = "CL"
	FuncCloseNotOnBoard = "CLNB"
	FuncCloseOnBoard    = "CLOB"
	FuncCancelFlight    = "XF"
	FuncChangeFlight    = "CF"
)

// Party qualifiers on NAD (3035).
const (
	PartyPassenger      = "FL"
	PartyCrew           = "FM"
	PartyTransitCrew    = "DDT"
	PartyTransitPax     = "DDU"
	PartyMessageContact = "MS"
)

// Leg is one sector of the flight as the TDT/LOC/DTM group carries it.
type Leg struct {
	// Carrier and Number identify the flight: TDT 8028 is carrier+number.
	Carrier string
	Number  string
	// Overflight marks TDT 8051 34 rather than 20.
	Overflight bool
	// From and To are the sector's airports, with the LOC qualifiers the
	// guide assigns: 125 last foreign departure, 87 first arrival in the
	// destination country, 92 subsequent/intermediate, 130 final destination.
	From, To         string
	FromQual, ToQual string
	Departs, Arrives time.Time // local
	DepartsHasTime   bool
	ArrivesHasTime   bool
}

// Document is an official travel document (GR.5).
type Document struct {
	Type    string // P passport, V visa, I identity, A, C, AC, IP, F
	Number  string
	Expires time.Time
	Issuer  string // ICAO 9303 / ISO 3166 alpha-3, LOC+91
	Issued  time.Time
	Place   string // place of issue when the issuer is not a state
}

// Person is one passenger or crew member (GR.4).
type Person struct {
	Party       string // FL, FM, DDT, DDU
	Surname     string
	Given       string
	Second      string
	Gender      string // F, M, X, U
	DateOfBirth time.Time
	// Address, when reported: street, city, sub-division code/name, postcode,
	// country.
	Street, City, Region, RegionName, Postcode, Country string
	Bags                                                int
	BagWeightKg                                         int
	Verified                                            *bool // GEI 173/174
	BagTags                                             []BagTag
	// Journey airports: 178 original embarkation, 179 final destination,
	// 22 clearance, 174 country of residence, 180 place of birth.
	Embarked, Destination, Clearance, Residence, BirthPlace string
	Contacts                                                []Contact
	Crew                                                    string // EMP: CR1..CR5
	Nationality                                             string
	// References: AVF the PNR locator, ABO the unique passenger reference,
	// SEA the seat, AEA an agency reference, CR a customer reference.
	Locator, PassengerRef, Seat, AgencyRef, CustomerRef string
	Documents                                           []Document
}

// BagTag is one FTX+BAG entry: a tag number and how many consecutive.
type BagTag struct {
	Number string
	Count  int
}

// Contact is a COM entry.
type Contact struct {
	Value string
	Kind  string // TE, FX, EM
}

// Message is one PAXLST.
type Message struct {
	Ref                          string // UNH 0062
	List                         string // BGM 1001: ListPassengers, ListCrew...
	Function                     string // BGM 1004, optional
	TxnRef                       string // RFF+TN
	TxnRev                       string // RFF+TN revision
	Contact                      string // NAD+MS party identifier (profile), or
	ContactSurname, ContactGiven string
	ContactComs                  []Contact
	Legs                         []Leg
	People                       []Person
	// Total is CNT: 42 passengers or 41 crew on the whole flight.
	Total     int
	TotalKind string
}

// Describe renders the message for logs.
func (m *Message) Describe() string {
	f := ""
	if len(m.Legs) > 0 {
		l := m.Legs[0]
		f = l.Carrier + l.Number + " " + l.From + "-" + l.To
	}
	return fmt.Sprintf("PAXLST %s %s: %d persons", m.List, f, len(m.People))
}

// IsPAXLST reports whether an EDIFACT message is a PAXLST.
func IsPAXLST(m edifact.Message) bool { return m.ID().Type == MsgPAXLST }

// BuildOptions parameterise the interchange envelope.
type BuildOptions struct {
	Sender, Recipient edifact.Party
	ControlRef        string
	Now               time.Time
	// Group emits UNG/UNE around the message with this group reference; the
	// guide leaves the functional group to bilateral agreement.
	Group    bool
	GroupRef string
	Test     bool
}

// Build renders a PAXLST interchange. Dates are local per the guide;
// callers pass them as the times they are.
func Build(m *Message, o BuildOptions) (*edifact.Interchange, error) {
	if m.List == "" {
		return nil, errors.New("paxlst: BGM document code is required (745 passengers, 250 crew)")
	}
	if len(m.Legs) == 0 {
		return nil, errors.New("paxlst: a flight is required")
	}
	now := o.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	ic := edifact.NewInterchange(edifact.UNBParams{
		CharsetID: "UNOA", SyntaxVersion: 4, Sender: o.Sender, Recipient: o.Recipient,
		Date: now.Format("060102"), Time: now.Format("1504"), ControlRef: o.ControlRef, Test: o.Test,
	})
	var body []edifact.Segment
	if m.Function != "" {
		body = append(body, edifact.Seg("BGM", edifact.Simple(m.List), edifact.Simple(m.Function)))
	} else {
		body = append(body, edifact.Seg("BGM", edifact.Simple(m.List)))
	}
	if m.TxnRef != "" {
		if m.TxnRev != "" {
			body = append(body, edifact.Seg("RFF", edifact.Comp("TN", m.TxnRef, "", "", m.TxnRev)))
		} else {
			body = append(body, edifact.Seg("RFF", edifact.Comp("TN", m.TxnRef)))
		}
	}
	switch {
	case m.Contact != "":
		body = append(body, edifact.Seg("NAD", edifact.Simple(PartyMessageContact), edifact.Simple(m.Contact)))
	case m.ContactSurname != "":
		body = append(body, edifact.Seg("NAD", edifact.Simple(PartyMessageContact), edifact.Simple(""), edifact.Simple(""),
			edifact.Comp(m.ContactSurname, m.ContactGiven)))
	}
	if len(m.ContactComs) > 0 {
		body = append(body, com(m.ContactComs))
	}
	for _, l := range m.Legs {
		body = append(body, legSegments(l)...)
	}
	for _, p := range m.People {
		body = append(body, personSegments(p)...)
	}
	kind := m.TotalKind
	if kind == "" {
		kind = "42"
		if m.List == ListCrew || m.List == ListMasterCrew {
			kind = "41"
		}
	}
	total := m.Total
	if total == 0 {
		total = len(m.People)
	}
	body = append(body, edifact.Seg("CNT", edifact.Comp(kind, strconv.Itoa(total))))
	ref := m.Ref
	if ref == "" {
		ref = "1"
	}
	ic.AddMessage(ref, ID, body...)
	if o.Group {
		// UNG..UNE around the one message: UNG+PAXLST+sender+recipient+
		// date:time+ref+UN+D:15B, as the guide's examples frame it.
		gref := o.GroupRef
		if gref == "" {
			gref = o.ControlRef
		}
		ung := edifact.Seg(edifact.TagUNG, edifact.Simple(MsgPAXLST), edifact.Simple(o.Sender.ID), edifact.Simple(o.Recipient.ID),
			edifact.Comp(now.Format("060102"), now.Format("1504")), edifact.Simple(gref), edifact.Simple("UN"), edifact.Comp(ID.Version, ID.Release))
		msgs := ic.Messages
		ic.Messages = nil
		ic.Groups = []edifact.Group{{Header: ung, Messages: msgs}}
	}
	ic.Finalize()
	return ic, nil
}

// com renders up to three contacts, each its own data element -- the guide's
// COM+tel:TE+fax:FX+mail:EM -- not repetitions of one.
func com(cs []Contact) edifact.Segment {
	seg := edifact.Segment{Tag: "COM"}
	for i, c := range cs {
		if i == 3 {
			break
		}
		seg.Elements = append(seg.Elements, edifact.Comp(c.Value, c.Kind))
	}
	return seg
}

func dtm(qual string, t time.Time, withTime bool) edifact.Segment {
	if withTime {
		return edifact.Seg("DTM", edifact.Comp(qual, t.Format("0601021504"), "201"))
	}
	return edifact.Seg("DTM", edifact.Comp(qual, t.Format("060102")))
}

func legSegments(l Leg) []edifact.Segment {
	stage := "20"
	if l.Overflight {
		stage = "34"
	}
	fq, tq := l.FromQual, l.ToQual
	if fq == "" {
		fq = "125"
	}
	if tq == "" {
		tq = "87"
	}
	out := []edifact.Segment{
		edifact.Seg("TDT", edifact.Simple(stage), edifact.Simple(l.Carrier+l.Number), edifact.Comp(), edifact.Comp(), edifact.Comp(l.Carrier)),
		edifact.Seg("LOC", edifact.Simple(fq), edifact.Simple(l.From)),
	}
	if !l.Departs.IsZero() {
		out = append(out, dtm("189", l.Departs, l.DepartsHasTime))
	}
	out = append(out, edifact.Seg("LOC", edifact.Simple(tq), edifact.Simple(l.To)))
	if !l.Arrives.IsZero() {
		out = append(out, dtm("232", l.Arrives, l.ArrivesHasTime))
	}
	return out
}

func personSegments(p Person) []edifact.Segment {
	party := p.Party
	if party == "" {
		party = PartyPassenger
	}
	name := edifact.Composite{p.Surname, p.Given}
	if p.Second != "" {
		name = append(name, p.Second)
	}
	nad := edifact.Seg("NAD", edifact.Simple(party), edifact.Simple(""), edifact.Simple(""), edifact.Element{name})
	if p.Street != "" || p.City != "" || p.Country != "" || p.Postcode != "" {
		region := edifact.Composite{p.Region}
		if p.RegionName != "" {
			region = edifact.Composite{p.Region, "", "", p.RegionName}
		}
		nad.Elements = append(nad.Elements, edifact.Simple(p.Street), edifact.Simple(p.City), edifact.Element{region},
			edifact.Simple(p.Postcode), edifact.Simple(p.Country))
	}
	out := []edifact.Segment{nad}
	if p.Gender != "" {
		out = append(out, edifact.Seg("ATT", edifact.Simple("2"), edifact.Comp(), edifact.Comp(p.Gender)))
	}
	if !p.DateOfBirth.IsZero() {
		out = append(out, edifact.Seg("DTM", edifact.Comp("329", p.DateOfBirth.Format("060102"))))
	}
	if p.Bags > 0 {
		out = append(out, edifact.Seg("MEA", edifact.Simple("CT"), edifact.Comp(), edifact.Comp("", strconv.Itoa(p.Bags))))
	}
	if p.BagWeightKg > 0 {
		out = append(out, edifact.Seg("MEA", edifact.Simple("WT"), edifact.Comp(), edifact.Comp("KGM", strconv.Itoa(p.BagWeightKg))))
	}
	if p.Verified != nil {
		code := "174"
		if *p.Verified {
			code = "173"
		}
		out = append(out, edifact.Seg("GEI", edifact.Simple("4"), edifact.Comp(code)))
	}
	for _, t := range p.BagTags {
		if t.Count > 1 {
			out = append(out, edifact.Seg("FTX", edifact.Simple("BAG"), edifact.Simple(""), edifact.Comp(), edifact.Comp(t.Number, strconv.Itoa(t.Count))))
		} else {
			out = append(out, edifact.Seg("FTX", edifact.Simple("BAG"), edifact.Simple(""), edifact.Comp(), edifact.Comp(t.Number)))
		}
	}
	for _, q := range []struct{ qual, v string }{{"22", p.Clearance}, {"174", p.Residence}, {"178", p.Embarked}, {"179", p.Destination}} {
		if q.v != "" {
			out = append(out, edifact.Seg("LOC", edifact.Simple(q.qual), edifact.Simple(q.v)))
		}
	}
	if p.BirthPlace != "" {
		out = append(out, edifact.Seg("LOC", edifact.Simple("180"), edifact.Comp("", "", "", p.BirthPlace)))
	}
	if len(p.Contacts) > 0 {
		out = append(out, com(p.Contacts))
	}
	if p.Crew != "" {
		out = append(out, edifact.Seg("EMP", edifact.Simple("1"), edifact.Comp(p.Crew, "110", "111")))
	}
	if p.Nationality != "" {
		out = append(out, edifact.Seg("NAT", edifact.Simple("2"), edifact.Comp(p.Nationality)))
	}
	for _, r := range []struct{ qual, v string }{{"AVF", p.Locator}, {"ABO", p.PassengerRef}, {"SEA", p.Seat}, {"AEA", p.AgencyRef}, {"CR", p.CustomerRef}} {
		if r.v != "" {
			out = append(out, edifact.Seg("RFF", edifact.Comp(r.qual, r.v)))
		}
	}
	for _, d := range p.Documents {
		out = append(out, edifact.Seg("DOC", edifact.Comp(d.Type), edifact.Comp(d.Number)))
		if !d.Expires.IsZero() {
			out = append(out, edifact.Seg("DTM", edifact.Comp("36", d.Expires.Format("060102"))))
		}
		if !d.Issued.IsZero() {
			out = append(out, edifact.Seg("DTM", edifact.Comp("182", d.Issued.Format("060102"))))
		}
		switch {
		case d.Issuer != "":
			out = append(out, edifact.Seg("LOC", edifact.Simple("91"), edifact.Simple(d.Issuer)))
		case d.Place != "":
			out = append(out, edifact.Seg("LOC", edifact.Simple("91"), edifact.Comp("", "", "", d.Place)))
		}
	}
	return out
}

// Parse reads a PAXLST.
func Parse(m edifact.Message) (*Message, error) {
	if !IsPAXLST(m) {
		return nil, fmt.Errorf("paxlst: %s is not a PAXLST", m.ID().Type)
	}
	out := &Message{Ref: m.Header.Value(0)}
	var leg *Leg
	var person *Person
	var doc *Document
	for _, seg := range m.Segments {
		switch seg.Tag {
		case "BGM":
			out.List, out.Function = seg.Value(0), seg.Value(1)
		case "RFF":
			q, v := seg.Get(0, 0), seg.Get(0, 1)
			if person == nil {
				if q == "TN" {
					out.TxnRef, out.TxnRev = v, seg.Get(0, 4)
				}
				continue
			}
			switch q {
			case "AVF":
				person.Locator = v
			case "ABO":
				person.PassengerRef = v
			case "SEA":
				person.Seat = v
			case "AEA":
				person.AgencyRef = v
			case "CR":
				person.CustomerRef = v
			}
		case "NAD":
			party := seg.Value(0)
			if party == PartyMessageContact {
				out.Contact = seg.Value(1)
				out.ContactSurname, out.ContactGiven = seg.Get(3, 0), seg.Get(3, 1)
				continue
			}
			leg, doc = nil, nil
			out.People = append(out.People, Person{Party: party, Surname: seg.Get(3, 0), Given: seg.Get(3, 1), Second: seg.Get(3, 2),
				Street: seg.Get(4, 0), City: seg.Value(5), Region: seg.Get(6, 0), RegionName: seg.Get(6, 3), Postcode: seg.Value(7), Country: seg.Value(8)})
			person = &out.People[len(out.People)-1]
		case "COM":
			var cs []Contact
			for i := range seg.Elements {
				if v := seg.Get(i, 0); v != "" {
					cs = append(cs, Contact{Value: v, Kind: seg.Get(i, 1)})
				}
			}
			if person != nil {
				person.Contacts = cs
			} else {
				out.ContactComs = cs
			}
		case "TDT":
			person = nil
			ident := seg.Value(1)
			carrier := seg.Get(4, 0)
			number := ident
			if carrier != "" && strings.HasPrefix(ident, carrier) {
				number = strings.TrimPrefix(ident, carrier)
			} else if carrier == "" && len(ident) > 2 {
				carrier, number = ident[:2], ident[2:]
			}
			out.Legs = append(out.Legs, Leg{Carrier: carrier, Number: number, Overflight: seg.Value(0) == "34"})
			leg = &out.Legs[len(out.Legs)-1]
		case "LOC":
			q, v, name := seg.Value(0), seg.Get(1, 0), seg.Get(1, 3)
			switch {
			case doc != nil && q == "91":
				doc.Issuer, doc.Place = v, name
			case person != nil:
				switch q {
				case "22":
					person.Clearance = v
				case "174":
					person.Residence = v
				case "178":
					person.Embarked = v
				case "179":
					person.Destination = v
				case "180":
					person.BirthPlace = name
				}
			case leg != nil:
				if leg.From == "" {
					leg.From, leg.FromQual = v, q
				} else {
					leg.To, leg.ToQual = v, q
				}
			}
		case "DTM":
			q, v := seg.Get(0, 0), strings.TrimSpace(seg.Get(0, 1))
			t, hasTime := parseDTM(v)
			switch {
			case doc != nil && q == "36":
				doc.Expires = t
			case doc != nil && q == "182":
				doc.Issued = t
			case person != nil && q == "329":
				person.DateOfBirth = t
			case leg != nil && q == "189":
				leg.Departs, leg.DepartsHasTime = t, hasTime
			case leg != nil && q == "232":
				leg.Arrives, leg.ArrivesHasTime = t, hasTime
			}
		case "ATT":
			if person != nil && seg.Value(0) == "2" {
				person.Gender = seg.Get(2, 0)
			}
		case "MEA":
			if person == nil {
				continue
			}
			n, _ := strconv.Atoi(seg.Get(2, 1))
			if seg.Value(0) == "CT" {
				person.Bags = n
			} else if seg.Value(0) == "WT" {
				person.BagWeightKg = n
			}
		case "GEI":
			if person != nil {
				v := seg.Get(1, 0) == "173"
				person.Verified = &v
			}
		case "FTX":
			if person != nil && seg.Value(0) == "BAG" {
				n, _ := strconv.Atoi(seg.Get(3, 1))
				person.BagTags = append(person.BagTags, BagTag{Number: seg.Get(3, 0), Count: max(1, n)})
			}
		case "EMP":
			if person != nil {
				person.Crew = seg.Get(1, 0)
			}
		case "NAT":
			if person != nil {
				person.Nationality = seg.Get(1, 0)
			}
		case "DOC":
			if person != nil {
				person.Documents = append(person.Documents, Document{Type: seg.Get(0, 0), Number: seg.Get(1, 0)})
				doc = &person.Documents[len(person.Documents)-1]
			}
		case "CNT":
			out.TotalKind = seg.Get(0, 0)
			out.Total, _ = strconv.Atoi(seg.Get(0, 1))
		}
	}
	if out.List == "" {
		return nil, errors.New("paxlst: no BGM")
	}
	return out, nil
}

func parseDTM(v string) (time.Time, bool) {
	switch len(v) {
	case 6:
		t, _ := time.Parse("060102", v)
		return t, false
	case 10:
		t, _ := time.Parse("0601021504", v)
		return t, true
	}
	return time.Time{}, false
}

// DOCS is the travel document a reservations record carries as SSR DOCS,
// the way check-in and API read it. The SSR's free text is slash-delimited:
//
//	P/GBR/P123456/GBR/14MAY80/F/31JAN30/SMITH/JANE
//
// document type, issuing state, number, nationality, date of birth, gender,
// expiry, surname, given names, with an optional trailing H for the primary
// passport holder. The layout is the one airlines and agencies publish for
// entering DOCS; the IATA resolution that defines it is not free, so this is
// inferred from those publications and tolerant of missing tail fields.
type DOCS struct {
	Type, Issuer, Number, Nationality string
	DateOfBirth, Expires              time.Time
	Gender, Surname, Given            string
}

// ParseDOCS reads the free text of an SSR DOCS. It returns false when the text
// does not have the document's first three fields.
func ParseDOCS(text string) (DOCS, bool) {
	f := strings.Split(strings.TrimSpace(text), "/")
	if len(f) < 3 {
		return DOCS{}, false
	}
	get := func(i int) string {
		if i < len(f) {
			return strings.TrimSpace(f[i])
		}
		return ""
	}
	d := DOCS{Type: get(0), Issuer: get(1), Number: get(2), Nationality: get(3), Gender: get(5), Surname: get(7), Given: get(8)}
	d.DateOfBirth = parseDDMMMYY(get(4), true)
	d.Expires = parseDDMMMYY(get(6), false)
	if d.Type == "" || d.Number == "" {
		return DOCS{}, false
	}
	return d, true
}

// parseDDMMMYY reads 14MAY80 or 14MAY1980. A two-digit year is read the way
// the field is meant: a date of birth (past) is never in the future, so a
// year that would be is a century earlier; an expiry is taken as Go reads
// it, 69-99 last century and 00-68 this one.
func parseDDMMMYY(s string, past bool) time.Time {
	s = strings.ToUpper(strings.TrimSpace(s))
	if len(s) < 7 {
		return time.Time{}
	}
	// Go's month names are title case; the wire's are upper.
	s = s[:3] + strings.ToLower(s[3:5]) + s[5:]
	for _, layout := range []string{"02Jan06", "02Jan2006"} {
		if t, err := time.Parse(layout, s); err == nil {
			if past && layout == "02Jan06" && t.Year() > time.Now().Year() {
				t = t.AddDate(-100, 0, 0)
			}
			return t
		}
	}
	return time.Time{}
}
