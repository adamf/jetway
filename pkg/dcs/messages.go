package dcs

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/adamf/jetway/pkg/pnl"
)

// Kind is the standard message identifier on the first line of a departure
// control message.
type Kind string

const (
	KindPFS Kind = "PFS" // passenger final sales, to reservations
	KindPTM Kind = "PTM" // passenger transfer, to the arrival station
	KindPSM Kind = "PSM" // passenger service, to the arrival station
	KindETL Kind = "ETL" // electronic ticket list, to revenue accounting
	KindLDM Kind = "LDM" // load, to the arrival station and operations
	KindCPM Kind = "CPM" // container/pallet distribution, to the arrival station
)

// Message is one decoded departure control message: which kind, and the
// body for that kind. It is what the gateway hands a consumer.
type Message struct {
	Kind   Kind   `json:"kind"`
	Flight string `json:"flight"`
	Date   string `json:"date,omitempty"`
	Board  string `json:"board,omitempty"`
	PFS    *PFS   `json:"pfs,omitempty"`
	PTM    *PTM   `json:"ptm,omitempty"`
	PSM    *PSM   `json:"psm,omitempty"`
	ETL    *ETL   `json:"etl,omitempty"`
	LDM    *LDM   `json:"ldm,omitempty"`
	CPM    *CPM   `json:"cpm,omitempty"`
}

// IsDepartureControl reports whether a Type B text is one of the messages
// this package reads.
func IsDepartureControl(text string) bool {
	switch Kind(firstLine(text)) {
	case KindPFS, KindPTM, KindPSM, KindETL, KindLDM, KindCPM:
		return true
	}
	return false
}

func firstLine(text string) string {
	for _, ln := range strings.Split(text, "\n") {
		if t := strings.TrimSpace(ln); t != "" {
			return t
		}
	}
	return ""
}

// Parse decodes any departure control message.
func Parse(text string) (*Message, error) {
	kind := Kind(firstLine(text))
	m := &Message{Kind: kind}
	var err error
	switch kind {
	case KindPFS:
		m.PFS, err = ParsePFS(text)
		if m.PFS != nil {
			m.Flight, m.Date, m.Board = m.PFS.Flight, m.PFS.Date, m.PFS.Board
		}
	case KindPTM:
		m.PTM, err = ParsePTM(text)
		if m.PTM != nil {
			m.Flight, m.Date, m.Board = m.PTM.Flight, m.PTM.Date, m.PTM.Board
		}
	case KindPSM:
		m.PSM, err = ParsePSM(text)
		if m.PSM != nil {
			m.Flight, m.Date, m.Board = m.PSM.Flight, m.PSM.Date, m.PSM.Board
		}
	case KindETL:
		m.ETL, err = ParseETL(text)
		if m.ETL != nil {
			m.Flight, m.Date, m.Board = m.ETL.Flight, m.ETL.Date, m.ETL.Board
		}
	case KindLDM:
		m.LDM, err = ParseLDM(text)
		if m.LDM != nil {
			m.Flight, m.Date = m.LDM.Flight, m.LDM.Day
		}
	case KindCPM:
		m.CPM, err = ParseCPM(text)
		if m.CPM != nil {
			m.Flight, m.Date = m.CPM.Flight, m.CPM.Day
		}
	default:
		return nil, fmt.Errorf("dcs: %q is not a departure control message", kind)
	}
	if err != nil {
		return nil, err
	}
	return m, nil
}

// lines splits a text into trimmed-right non-blank lines.
func lines(text string) []string {
	var out []string
	for _, ln := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if t := strings.TrimRight(ln, " "); strings.TrimSpace(t) != "" {
			out = append(out, t)
		}
	}
	return out
}

// linesPerPart keeps each part inside the Type B sixty-line envelope with
// room for the address block, the same budget pkg/pnl uses.
const linesPerPart = 50

// block is one run of lines under a heading path: a destination, or a
// destination and a category. Headings are a path so that a part which
// starts in the middle of a block can repeat exactly the context a reader
// needs and no more.
type block struct {
	head []string
	body []string
}

// paginate renders a list message in parts. When a block will not fit it is
// split and its heading path repeated at the top of the next part, so every
// part reads on its own. header is the flight line without the PART token.
func paginate(kind Kind, header string, blocks []block) []string {
	var parts []string
	var cur []string
	var ctx []string // headings in effect in the current part
	part := 1
	start := func() {
		cur = []string{string(kind), fmt.Sprintf("%s PART%d", header, part)}
		ctx = nil
	}
	flush := func(final bool) {
		if final {
			cur = append(cur, "END"+string(kind))
		} else {
			cur = append(cur, fmt.Sprintf("ENDPART%d", part))
		}
		parts = append(parts, strings.Join(cur, "\n"))
		part++
		start()
	}
	// enter emits the heading lines of b that the part is not already under.
	enter := func(b block) {
		common := 0
		for common < len(ctx) && common < len(b.head) && ctx[common] == b.head[common] {
			common++
		}
		cur = append(cur, b.head[common:]...)
		ctx = b.head
	}
	start()
	for _, b := range blocks {
		if len(b.head)+len(cur)+1 > linesPerPart {
			flush(false)
		}
		enter(b)
		for _, ln := range b.body {
			if len(cur)+1 > linesPerPart-1 {
				flush(false)
				enter(b)
			}
			cur = append(cur, ln)
		}
	}
	flush(true)
	return parts
}

// parseHeader reads "BA0117/16DEC LHR PART1" into its pieces. The third
// token is the boarding point, or board+destination on a PTM.
func parseHeader(ln string) (flight, date, station string, part int, err error) {
	fields := strings.Fields(ln)
	if len(fields) < 2 {
		return "", "", "", 0, fmt.Errorf("dcs: header %q lacks flight and station", ln)
	}
	fd := strings.SplitN(fields[0], "/", 2)
	if len(fd) != 2 {
		return "", "", "", 0, fmt.Errorf("dcs: header %q lacks a /date", ln)
	}
	part = 1
	for _, f := range fields[2:] {
		if strings.HasPrefix(f, "PART") {
			if n, e := strconv.Atoi(f[4:]); e == nil {
				part = n
			}
		}
	}
	return fd[0], fd[1], fields[1], part, nil
}

// ---------------------------------------------------------------- PFS

// PFS is a passenger final sales message: what departure control tells
// reservations about the passengers whose handling differed from the list.
//
// The layout is inferred (RP 1719 was not bought and has no free
// reproduction this project could find). It follows the PNL family it
// belongs to: the flight header, a destination heading, category markers,
// and name items in PNL form. The category vocabulary -- NOSHO, GOSHO,
// NOREC, OFFLD, IDPAD, INVOL -- is the published one; how the practice lays
// the items out around it is the guess.
type PFS struct {
	Flight string     `json:"flight"`
	Date   string     `json:"date"`
	Board  string     `json:"board"`
	Part   int        `json:"part"`
	Final  bool       `json:"final"`
	Groups []PFSGroup `json:"groups"`
}

// PFSGroup is one destination's report.
type PFSGroup struct {
	Dest  string    `json:"dest"`
	Items []PFSItem `json:"items"`
}

// PFSItem is one name under one category.
type PFSItem struct {
	Category string   `json:"category"`
	Name     pnl.Name `json:"name"`
}

// pfsCategories is the order categories are listed in.
var pfsCategories = []string{"NOSHO", "GOSHO", "NOREC", "OFFLD", "IDPAD", "INVOL"}

// pfsCategory classifies a passenger for the final sales report; empty
// means the passenger was handled as listed and is not reported.
func pfsCategory(p *Passenger) string {
	switch p.Status {
	case StatusNoShow:
		return "NOSHO"
	case StatusOffloaded:
		if p.Category == CategoryInvol {
			return "INVOL"
		}
		return "OFFLD"
	case StatusBoarded, StatusAccepted:
		if p.DeletedAfterAcceptance {
			return "GOSHO"
		}
		switch p.Category {
		case CategoryGoShow:
			return "GOSHO"
		case CategoryNoRec:
			return "NOREC"
		case CategoryIDPad:
			return "IDPAD"
		}
	}
	return ""
}

// nameItem renders a party's members who share a category as one name item.
func nameItem(members []*Passenger) pnl.Name {
	n := pnl.Name{Party: len(members), Surname: members[0].Surname}
	for _, p := range members {
		n.Givens = append(n.Givens, p.Given)
	}
	if loc := members[0].Locator; loc != "" {
		n.Elements = append(n.Elements, ".L/"+loc)
	}
	return n
}

// groupByParty gathers passengers into their name items, in list order.
func groupByParty(pax []*Passenger) [][]*Passenger {
	var order []string
	byKey := map[string][]*Passenger{}
	for _, p := range pax {
		if _, seen := byKey[p.Party]; !seen {
			order = append(order, p.Party)
		}
		byKey[p.Party] = append(byKey[p.Party], p)
	}
	out := make([][]*Passenger, 0, len(order))
	for _, k := range order {
		out = append(out, byKey[k])
	}
	return out
}

// BuildPFS renders the final sales message for a closed flight.
func BuildPFS(f *Flight) []string {
	byDest := map[string]map[string][]*Passenger{}
	var dests []string
	for _, p := range f.Passengers {
		cat := pfsCategory(p)
		if cat == "" {
			continue
		}
		dest := p.Dest
		if dest == "" {
			dest = f.Dest
		}
		if byDest[dest] == nil {
			byDest[dest] = map[string][]*Passenger{}
			dests = append(dests, dest)
		}
		byDest[dest][cat] = append(byDest[dest][cat], p)
	}
	sort.Strings(dests)
	var blocks []block
	for _, dest := range dests {
		for _, cat := range pfsCategories {
			pax := byDest[dest][cat]
			if len(pax) == 0 {
				continue
			}
			b := block{head: []string{"-" + dest, cat}}
			for _, party := range groupByParty(pax) {
				b.body = append(b.body, pnl.NameLine(nameItem(party)))
			}
			blocks = append(blocks, b)
		}
	}
	if len(blocks) == 0 {
		dest := f.Dest
		if dest == "" {
			dest = "XXX"
		}
		blocks = []block{{head: []string{"-" + dest}, body: []string{"NIL"}}}
	}
	return paginate(KindPFS, fmt.Sprintf("%s/%s %s", f.Flight, f.Date, f.Board), blocks)
}

// ParsePFS reads one part of a final sales message.
func ParsePFS(text string) (*PFS, error) {
	ls := lines(text)
	if len(ls) < 3 || ls[0] != string(KindPFS) {
		return nil, fmt.Errorf("dcs: not a PFS")
	}
	flight, date, board, part, err := parseHeader(ls[1])
	if err != nil {
		return nil, err
	}
	m := &PFS{Flight: flight, Date: date, Board: board, Part: part}
	var g *PFSGroup
	cat := ""
	for _, ln := range ls[2:] {
		t := strings.TrimSpace(ln)
		switch {
		case t == "ENDPFS":
			m.Final = true
			if g != nil {
				m.Groups = append(m.Groups, *g)
			}
			return m, nil
		case strings.HasPrefix(t, "ENDPART"):
			if g != nil {
				m.Groups = append(m.Groups, *g)
			}
			return m, nil
		case strings.HasPrefix(t, "-"):
			if g != nil {
				m.Groups = append(m.Groups, *g)
			}
			g = &PFSGroup{Dest: strings.TrimPrefix(t, "-")}
			cat = ""
		case t == "NIL":
		case isCategory(t):
			cat = t
		default:
			if g == nil {
				return nil, fmt.Errorf("dcs: PFS name %q before any destination", t)
			}
			n, err := pnl.ParseName(t)
			if err != nil {
				return nil, err
			}
			g.Items = append(g.Items, PFSItem{Category: cat, Name: n})
		}
	}
	return nil, fmt.Errorf("dcs: PFS has no END line")
}

func isCategory(t string) bool {
	for _, c := range pfsCategories {
		if t == c {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------- PTM

// PTM is a passenger transfer message: who on this flight connects onward
// at the arrival station, so it can plan the transfer desk and the bags.
//
// The layout follows the freely published worked examples (RP 1718): a
// header with the boarding point and destination run together, then one
// line per transferring party -- onward flight and day, onward destination,
// count and class, bag count, names -- and ENDPTM.
type PTM struct {
	Flight    string     `json:"flight"`
	Date      string     `json:"date"`
	Board     string     `json:"board"`
	Dest      string     `json:"dest"`
	Part      int        `json:"part"`
	Final     bool       `json:"final"`
	Transfers []Transfer `json:"transfers"`
}

// Transfer is one party's connection.
type Transfer struct {
	Onward  Connection `json:"onward"`
	Count   int        `json:"count"`
	Bags    int        `json:"bags"`
	Surname string     `json:"surname"`
	Givens  []string   `json:"givens"`
}

// BuildPTM renders the transfer message for the flight's boarded connecting
// passengers. Nothing to transfer yields no message at all: a NIL PTM is
// not a published form.
func BuildPTM(f *Flight) []string {
	var pax []*Passenger
	for _, p := range f.Passengers {
		if p.Status == StatusBoarded && p.Onward != nil {
			pax = append(pax, p)
		}
	}
	if len(pax) == 0 {
		return nil
	}
	var body []string
	for _, party := range groupByParty(pax) {
		lead := party[0]
		bags := 0
		names := lead.Surname
		for _, p := range party {
			bags += len(p.Bags)
			names += "/" + p.Given
		}
		day := lead.Onward.Date
		if len(day) > 2 {
			day = day[:2]
		}
		class := lead.Onward.Class
		if class == "" {
			class = lead.Class
		}
		body = append(body, fmt.Sprintf("%s/%s %s %d%s %dB %s",
			lead.Onward.Flight, day, lead.Onward.Dest, len(party), class, bags, names))
	}
	// The PTM has no group heading; the whole body is one group with no head
	// line, so paginate gets a heading-less group by using the first line.
	return paginateFlat(KindPTM, fmt.Sprintf("%s/%s %s%s", f.Flight, f.Date, f.Board, f.Dest), body)
}

// paginateFlat is paginate for a message with no group headings.
func paginateFlat(kind Kind, header string, body []string) []string {
	var parts []string
	part := 1
	for len(body) > 0 {
		take := min(len(body), linesPerPart-3)
		cur := []string{string(kind), fmt.Sprintf("%s PART%d", header, part)}
		cur = append(cur, body[:take]...)
		body = body[take:]
		if len(body) == 0 {
			cur = append(cur, "END"+string(kind))
		} else {
			cur = append(cur, fmt.Sprintf("ENDPART%d", part))
		}
		parts = append(parts, strings.Join(cur, "\n"))
		part++
	}
	return parts
}

var ptmLineRe = regexp.MustCompile(`^([A-Z0-9]{2}[A-Z]?\d{1,4}[A-Z]?)/(\d{1,2}[A-Z]{0,3}) ([A-Z]{3}) (\d+)([A-Z]) (\d+)B (.+)$`)

// ParsePTM reads one part of a transfer message.
func ParsePTM(text string) (*PTM, error) {
	ls := lines(text)
	if len(ls) < 3 || ls[0] != string(KindPTM) {
		return nil, fmt.Errorf("dcs: not a PTM")
	}
	flight, date, stations, part, err := parseHeader(ls[1])
	if err != nil {
		return nil, err
	}
	m := &PTM{Flight: flight, Date: date, Part: part}
	if len(stations) >= 6 {
		m.Board, m.Dest = stations[:3], stations[3:6]
	} else {
		m.Board = stations
	}
	for _, ln := range ls[2:] {
		t := strings.TrimSpace(ln)
		switch {
		case t == "ENDPTM":
			m.Final = true
			return m, nil
		case strings.HasPrefix(t, "ENDPART"):
			return m, nil
		}
		sm := ptmLineRe.FindStringSubmatch(t)
		if sm == nil {
			return nil, fmt.Errorf("dcs: PTM line %q is not a transfer", t)
		}
		count, _ := strconv.Atoi(sm[4])
		bags, _ := strconv.Atoi(sm[6])
		names := strings.Split(sm[7], "/")
		m.Transfers = append(m.Transfers, Transfer{
			Onward: Connection{Flight: sm[1], Date: sm[2], Dest: sm[3], Class: sm[5]},
			Count:  count, Bags: bags, Surname: names[0], Givens: names[1:],
		})
	}
	return nil, fmt.Errorf("dcs: PTM has no END line")
}

// ---------------------------------------------------------------- PSM

// PSM is a passenger service message: the passengers arriving who need
// assistance or special handling, with their seats, for the arrival station.
//
// The layout follows the practice's own worked examples (RP 1715), which
// are freely reproduced by airports: the header, a destination line with a
// recap of passengers and services, one count line per service code with
// counts per compartment, then per compartment the names with seats and
// their services indented beneath, an optional SI, and ENDPSM. A flight
// with nobody to report sends a NIL.
type PSM struct {
	Flight string     `json:"flight"`
	Date   string     `json:"date"`
	Board  string     `json:"board"`
	Part   int        `json:"part"`
	Final  bool       `json:"final"`
	Groups []PSMGroup `json:"groups"`
	SI     []string   `json:"si,omitempty"`
	Nil    bool       `json:"nil,omitempty"`
}

// PSMGroup is one destination's passengers.
type PSMGroup struct {
	Dest       string         `json:"dest"`
	Pax        int            `json:"pax"`
	SSRs       int            `json:"ssrs"`
	Passengers []PSMPassenger `json:"passengers"`
}

// PSMPassenger is one name with seat and services.
type PSMPassenger struct {
	Compartment string `json:"compartment"`
	Surname     string `json:"surname"`
	Given       string `json:"given"`
	Seat        string `json:"seat"`
	Onward      string `json:"onward,omitempty"`
	Services    []SSR  `json:"services"`
}

// psmCodes are the service codes the practice allows on a PSM.
var psmCodes = map[string]bool{
	"ASVC": true, "BLND": true, "DEAF": true, "DEPA": true, "DEPU": true, "DPNA": true,
	"EMIG": true, "INAD": true, "LANG": true, "MAAS": true, "MEDA": true, "MEQT": true,
	"PPOC": true, "STCR": true, "TWOV": true, "UMNR": true, "UPGR": true, "VIP": true,
	"WCHC": true, "WCHR": true, "WCHS": true, "WEAP": true,
}

func psmServices(p *Passenger) []SSR {
	var out []SSR
	for _, s := range p.SSRs {
		if psmCodes[s.Code] {
			out = append(out, s)
		}
	}
	return out
}

// BuildPSM renders the service message for the flight's boarded passengers.
func BuildPSM(f *Flight) []string {
	header := fmt.Sprintf("%s/%s %s", f.Flight, f.Date, f.Board)
	comps := []string{"Y"}
	if f.Cabin != nil {
		comps = f.Cabin.compartments()
	}
	byDest := map[string][]*Passenger{}
	var dests []string
	for _, p := range f.Passengers {
		if p.Status != StatusBoarded || len(psmServices(p)) == 0 {
			continue
		}
		dest := p.Dest
		if dest == "" {
			dest = f.Dest
		}
		if _, seen := byDest[dest]; !seen {
			dests = append(dests, dest)
		}
		byDest[dest] = append(byDest[dest], p)
	}
	sort.Strings(dests)
	if len(dests) == 0 {
		return []string{strings.Join([]string{string(KindPSM), header + " PART1", "NIL", "ENDPSM"}, "\n")}
	}
	var blocks []block
	for _, dest := range dests {
		pax := byDest[dest]
		total := 0
		codeCounts := map[string]map[string]int{}
		var codes []string
		for _, p := range pax {
			for _, s := range psmServices(p) {
				total++
				if codeCounts[s.Code] == nil {
					codeCounts[s.Code] = map[string]int{}
					codes = append(codes, s.Code)
				}
				codeCounts[s.Code][p.Compartment]++
			}
		}
		sort.Strings(codes)
		b := block{head: []string{fmt.Sprintf("-%s %dPAX/%dSSR", dest, len(pax), total)}}
		g := []string{}
		for _, code := range codes {
			ln := code
			for _, c := range comps {
				ln += fmt.Sprintf(" %03d%s", codeCounts[code][c], c)
			}
			g = append(g, ln)
		}
		for _, c := range comps {
			var inComp []*Passenger
			n := 0
			for _, p := range pax {
				if p.Compartment == c {
					inComp = append(inComp, p)
					n += len(psmServices(p))
				}
			}
			if len(inComp) == 0 {
				g = append(g, fmt.Sprintf("%s CLASS NIL", c))
				continue
			}
			g = append(g, fmt.Sprintf("%s CLASS %dPAX/%dSSR", c, len(inComp), n))
			sort.Slice(inComp, func(i, j int) bool {
				return inComp[i].Surname+inComp[i].Given < inComp[j].Surname+inComp[j].Given
			})
			for _, p := range inComp {
				g = append(g, fmt.Sprintf("1%s/%s %s", p.Surname, p.Given, p.Seat))
				if p.Onward != nil {
					day := p.Onward.Date
					if len(day) > 2 {
						day = day[:2]
					}
					class := p.Onward.Class
					if class == "" {
						class = p.Class
					}
					g = append(g, fmt.Sprintf("%s%s%s%s", p.Onward.Flight, class, day, p.Onward.Dest))
				}
				for _, s := range psmServices(p) {
					ln := " " + s.Code
					if s.Text != "" {
						ln += " " + s.Text
					}
					g = append(g, ln)
				}
			}
		}
		b.body = g
		blocks = append(blocks, b)
	}
	return paginate(KindPSM, header, blocks)
}

var (
	psmRecapRe  = regexp.MustCompile(`^-([A-Z]{3})(?: (\d+)PAX/(\d+)SSR| NIL)?$`)
	psmCountRe  = regexp.MustCompile(`^([A-Z]{3,4})((?: \d{3}[A-Z])+)$`)
	psmClassRe  = regexp.MustCompile(`^([A-Z]) CLASS (?:(\d+)PAX/(\d+)SSR|NIL)$`)
	psmNameRe   = regexp.MustCompile(`^(\d+)([A-Z' -]+)/([A-Z' -]+) (\d{1,3}[A-Z])$`)
	psmOnwardRe = regexp.MustCompile(`^([A-Z0-9]{2}[A-Z]?\d{1,4}[A-Z]?)([A-Z])(\d{2})([A-Z]{3})`)
)

// isServiceCode reports whether a token is a service code: one the practice
// lists for the PSM, or any four-letter code a house profile might add.
func isServiceCode(tok string) bool {
	if psmCodes[tok] {
		return true
	}
	if len(tok) != 4 {
		return false
	}
	for _, r := range tok {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return true
}

// ParsePSM reads one part of a service message.
func ParsePSM(text string) (*PSM, error) {
	ls := lines(text)
	if len(ls) < 3 || ls[0] != string(KindPSM) {
		return nil, fmt.Errorf("dcs: not a PSM")
	}
	flight, date, board, part, err := parseHeader(ls[1])
	if err != nil {
		return nil, err
	}
	m := &PSM{Flight: flight, Date: date, Board: board, Part: part}
	var g *PSMGroup
	var cur *PSMPassenger
	comp := ""
	inSI := false
	closePax := func() {
		if g != nil && cur != nil {
			g.Passengers = append(g.Passengers, *cur)
			cur = nil
		}
	}
	closeGroup := func() {
		closePax()
		if g != nil {
			m.Groups = append(m.Groups, *g)
			g = nil
		}
	}
	for _, ln := range ls[2:] {
		t := strings.TrimSpace(ln)
		indented := strings.HasPrefix(ln, " ")
		switch {
		case t == "ENDPSM":
			closeGroup()
			m.Final = true
			return m, nil
		case strings.HasPrefix(t, "ENDPART"):
			closeGroup()
			return m, nil
		case inSI:
			m.SI = append(m.SI, t)
		case t == "SI":
			closeGroup()
			inSI = true
		case t == "NIL":
			m.Nil = true
		case cur != nil && psmOnwardRe.MatchString(t):
			// The onward connection sits under the name, indented or not
			// in the published examples.
			cur.Onward = t
		case indented && cur != nil && isServiceCode(strings.Fields(t)[0]):
			// A service line under a name: code, then free text.
			code, rest, _ := strings.Cut(t, " ")
			cur.Services = append(cur.Services, SSR{Code: code, Text: rest})
		case indented && cur != nil && len(cur.Services) > 0:
			// A service text wrapped onto a second line.
			n := len(cur.Services)
			cur.Services[n-1].Text = strings.TrimSpace(cur.Services[n-1].Text + " " + t)
		case psmRecapRe.MatchString(t):
			closeGroup()
			sm := psmRecapRe.FindStringSubmatch(t)
			g = &PSMGroup{Dest: sm[1]}
			g.Pax, _ = strconv.Atoi(sm[2])
			g.SSRs, _ = strconv.Atoi(sm[3])
			comp = ""
		case psmClassRe.MatchString(t):
			closePax()
			comp = psmClassRe.FindStringSubmatch(t)[1]
		case psmNameRe.MatchString(t):
			closePax()
			sm := psmNameRe.FindStringSubmatch(t)
			cur = &PSMPassenger{Compartment: comp, Surname: sm[2], Given: sm[3], Seat: sm[4]}
		case psmCountRe.MatchString(t):
			// The recap counts are derivable from the names; they are read
			// and not stored.
		default:
			if cur != nil {
				// Continuation of a long service text.
				if n := len(cur.Services); n > 0 {
					cur.Services[n-1].Text = strings.TrimSpace(cur.Services[n-1].Text + " " + t)
				}
				continue
			}
			return nil, fmt.Errorf("dcs: PSM line %q not understood", t)
		}
	}
	return nil, fmt.Errorf("dcs: PSM has no END line")
}

// ---------------------------------------------------------------- ETL

// ETL is the electronic ticket list: the boarded passengers with the
// documents they flew on, for the systems that lift coupons and settle.
//
// The layout is inferred (RP 1719c was not bought). It follows the PNL
// family: header, destination heading, name items carrying the locator, the
// ticket as a TKNE element, and the seat.
type ETL struct {
	Flight string     `json:"flight"`
	Date   string     `json:"date"`
	Board  string     `json:"board"`
	Part   int        `json:"part"`
	Final  bool       `json:"final"`
	Groups []ETLGroup `json:"groups"`
}

// ETLGroup is one destination's boarded passengers.
type ETLGroup struct {
	Dest  string     `json:"dest"`
	Names []pnl.Name `json:"names"`
}

// BuildETL renders the ticket list for the flight's boarded passengers.
// Passengers who boarded without a ticket number known here are listed
// without a TKNE element: revenue accounting wants to know that too.
func BuildETL(f *Flight) []string {
	byDest := map[string][]*Passenger{}
	var dests []string
	for _, p := range f.Passengers {
		if p.Status != StatusBoarded {
			continue
		}
		dest := p.Dest
		if dest == "" {
			dest = f.Dest
		}
		if _, seen := byDest[dest]; !seen {
			dests = append(dests, dest)
		}
		byDest[dest] = append(byDest[dest], p)
	}
	if len(dests) == 0 {
		return nil
	}
	sort.Strings(dests)
	var blocks []block
	for _, dest := range dests {
		b := block{head: []string{"-" + dest}}
		for _, p := range byDest[dest] {
			n := pnl.Name{Party: 1, Surname: p.Surname, Givens: []string{p.Given}}
			if p.Locator != "" {
				n.Elements = append(n.Elements, ".L/"+p.Locator)
			}
			if p.Ticket != "" {
				n.Elements = append(n.Elements, ".R/TKNE "+p.Ticket)
			}
			if p.Seat != "" {
				n.Elements = append(n.Elements, ".S/"+p.Seat)
			}
			b.body = append(b.body, pnl.NameLine(n))
		}
		blocks = append(blocks, b)
	}
	return paginate(KindETL, fmt.Sprintf("%s/%s %s", f.Flight, f.Date, f.Board), blocks)
}

// ParseETL reads one part of a ticket list.
func ParseETL(text string) (*ETL, error) {
	ls := lines(text)
	if len(ls) < 3 || ls[0] != string(KindETL) {
		return nil, fmt.Errorf("dcs: not an ETL")
	}
	flight, date, board, part, err := parseHeader(ls[1])
	if err != nil {
		return nil, err
	}
	m := &ETL{Flight: flight, Date: date, Board: board, Part: part}
	var g *ETLGroup
	for _, ln := range ls[2:] {
		t := strings.TrimSpace(ln)
		switch {
		case t == "ENDETL":
			m.Final = true
			if g != nil {
				m.Groups = append(m.Groups, *g)
			}
			return m, nil
		case strings.HasPrefix(t, "ENDPART"):
			if g != nil {
				m.Groups = append(m.Groups, *g)
			}
			return m, nil
		case strings.HasPrefix(t, "-"):
			if g != nil {
				m.Groups = append(m.Groups, *g)
			}
			g = &ETLGroup{Dest: strings.TrimPrefix(t, "-")}
		default:
			if g == nil {
				return nil, fmt.Errorf("dcs: ETL name %q before any destination", t)
			}
			n, err := pnl.ParseName(t)
			if err != nil {
				return nil, err
			}
			g.Names = append(g.Names, n)
		}
	}
	return nil, fmt.Errorf("dcs: ETL has no END line")
}
