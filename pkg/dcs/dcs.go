// Package dcs is a departure control system: the system that turns a
// reservation into a passenger on an aircraft.
//
// A reservations system knows who booked. Departure control knows who turned
// up, where they sit, what they checked into the hold, whether they boarded,
// and what the aircraft weighs when the door closes. It is fed by the
// passenger name list (PNL) and its amendments (ADL) from reservations, and it
// feeds everyone else: final sales back to reservations (PFS), transfer and
// service lists to the arrival station (PTM, PSM), the boarded ticket list to
// revenue (ETL), and the load and container distribution to the arrival
// station and the operations centre (LDM, CPM). The loadsheet is the document
// the captain signs.
//
// The package is a library. A Station holds the open flights of one carrier
// at one node; the operations on it -- accept, seat, tag a bag, board, offload,
// close -- are the ones an agent's screen exposes, and they refuse with the
// reasons a real DCS gives (flight closed, seat taken, passenger not on the
// list). Messages are built and parsed as text; transmitting them is the
// gateway's job. Persistence is behind a Store seam whose in-memory
// implementation ships here; a durable one is a straight port because every
// flight is written as one JSON document.
//
// Message formats. The PNL/ADL side uses pkg/pnl. The messages this package
// builds are from the IATA Passenger Services Conference Resolutions Manual
// (PSM RP 1715, PTM RP 1718, PFS RP 1719, ETL RP 1719c) and the Airport
// Handling Manual (LDM AHM 583, CPM AHM 587). None of those documents was
// bought. PSM, PTM, LDM and CPM are reconstructed from freely published
// reproductions with verbatim worked examples and follow them exactly. PFS and
// ETL have no free reproduction this project could find; their layouts here
// are inferred from the PNL family they belong to and are labelled as such in
// their builders. Per this repository's rule that makes every format inferred
// rather than conformant: a line the parsers do not recognise is kept, never
// dropped.
//
// Weight and balance follows the AHM 560/565 method -- index units from arms,
// a converted %MAC, a per-type envelope -- with aircraft data supplied by the
// operator. The representative data in DefaultFleet exists so the package
// works out of the box; it is not any operator's AHM 560 and says so.
package dcs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/adamf/jetway/pkg/baggage"
	"github.com/adamf/jetway/pkg/pnl"
)

// Key identifies one departure: the flight as written on the wire (BA0117),
// the date as DDMMM, and the boarding point.
type Key struct {
	Flight string `json:"flight"`
	Date   string `json:"date"`
	Board  string `json:"board"`
}

// String renders the key the way a PNL header does.
func (k Key) String() string { return k.Flight + "/" + k.Date + " " + k.Board }

// Status is where a passenger is in the departure.
type Status string

const (
	// StatusListed is on the name list and not yet seen at the airport.
	StatusListed Status = "listed"
	// StatusAccepted is checked in: seated, sequence number issued, bags tagged.
	StatusAccepted Status = "accepted"
	// StatusBoarded is through the gate.
	StatusBoarded Status = "boarded"
	// StatusStandby is a passenger waiting for a seat -- a go-show without a
	// confirmed booking, or staff travel. The industry calls them PADs.
	StatusStandby Status = "standby"
	// StatusNoShow is listed, never accepted, and the flight has closed.
	StatusNoShow Status = "noshow"
	// StatusOffloaded was accepted and then removed: did not board, or was
	// taken off deliberately. Their bags come off with them.
	StatusOffloaded Status = "offloaded"
	// StatusDeleted was removed from the list by an ADL before acceptance.
	StatusDeleted Status = "deleted"
)

// Category is how the final sales message classifies a passenger relative to
// the name list. Empty is the ordinary case: listed and handled as listed.
type Category string

const (
	CategoryListed Category = ""
	// CategoryGoShow was accepted with a booking reservations had not listed.
	CategoryGoShow Category = "GOSHO"
	// CategoryNoRec was accepted holding a ticket with no record at all.
	CategoryNoRec Category = "NOREC"
	// CategoryIDPad is staff travel accepted from standby.
	CategoryIDPad Category = "IDPAD"
	// CategoryInvol was denied boarding involuntarily: oversold.
	CategoryInvol Category = "INVOL"
)

// PassengerType is the weight-and-balance class of a traveller.
type PassengerType string

const (
	PaxAdult  PassengerType = "A"
	PaxChild  PassengerType = "C"
	PaxInfant PassengerType = "I"
)

// Bag is one piece of hold baggage.
type Bag struct {
	// Tag is the ten-digit licence plate.
	Tag    string `json:"tag"`
	Weight int    `json:"weight"` // kilos
	// Loaded is the sortation system's word that the bag is on board.
	Loaded bool `json:"loaded,omitempty"`
	// Offloaded marks a bag pulled because its passenger did not fly.
	Offloaded bool `json:"offloaded,omitempty"`
	// Position is where load planning put it: a compartment or a ULD.
	Position string `json:"position,omitempty"`
}

// Connection is a flight the passenger arrives on or continues to.
type Connection struct {
	Flight string `json:"flight"`
	Date   string `json:"date"` // DDMMM, or DD as the PTM carries it
	// Station is where the connection happens; Dest is where it goes.
	Station string `json:"station,omitempty"`
	Dest    string `json:"dest,omitempty"`
	Class   string `json:"class,omitempty"`
	Bags    int    `json:"bags,omitempty"`
}

// SSR is one special service request carried on the name list.
type SSR struct {
	Code string `json:"code"`
	Text string `json:"text,omitempty"`
}

// Passenger is one traveller on a departure.
type Passenger struct {
	ID      int    `json:"id"`
	Surname string `json:"surname"`
	// Given is the given name and title run exactly as the wire carries it:
	// RUIMR, ANAMRS, TIAGOMSTR.
	Given   string `json:"given"`
	Locator string `json:"locator,omitempty"`
	// Party groups the names that arrived on one name item. They check in
	// and sit together unless the agent says otherwise.
	Party string `json:"party"`
	// Class is the booking class from the group heading; Compartment is the
	// cabin it maps to (F, C or Y).
	Class       string        `json:"class"`
	Compartment string        `json:"compartment"`
	Dest        string        `json:"dest"`
	Type        PassengerType `json:"type"`
	SSRs        []SSR         `json:"ssrs,omitempty"`
	Ticket      string        `json:"ticket,omitempty"`
	// Elements keeps every dotted element the list carried that nothing here
	// interprets, verbatim.
	Elements []string `json:"elements,omitempty"`

	Status   Status   `json:"status"`
	Category Category `json:"category,omitempty"`
	Seat     string   `json:"seat,omitempty"`
	// Sequence is the boarding sequence number, issued at acceptance.
	Sequence   int         `json:"sequence,omitempty"`
	AcceptedAt *time.Time  `json:"accepted_at,omitempty"`
	BoardedAt  *time.Time  `json:"boarded_at,omitempty"`
	Bags       []Bag       `json:"bags,omitempty"`
	Onward     *Connection `json:"onward,omitempty"`
	Inbound    *Connection `json:"inbound,omitempty"`
	// DeletedAfterAcceptance records that an ADL deleted this passenger after
	// check-in had accepted them. The passenger stays; the conflict is the
	// point, and the PFS reports them as a go-show because reservations no
	// longer holds them.
	DeletedAfterAcceptance bool `json:"deleted_after_acceptance,omitempty"`
	// OffloadReason says why an offloaded passenger came off.
	OffloadReason string `json:"offload_reason,omitempty"`
}

// Display renders the passenger as the wire does: 1SMITH/JOHNMR.
func (p *Passenger) Display() string { return p.Surname + "/" + p.Given }

// Flying reports whether the passenger currently holds a seat on the flight.
func (p *Passenger) Flying() bool {
	return p.Status == StatusAccepted || p.Status == StatusBoarded
}

// Active reports whether the passenger is still part of the departure: on
// the list or through it, not removed.
func (p *Passenger) Active() bool {
	switch p.Status {
	case StatusDeleted, StatusOffloaded, StatusNoShow:
		return false
	}
	return true
}

// State is where a flight is in its departure.
type State string

const (
	// StateOpen accepts passengers.
	StateOpen State = "open"
	// StateCheckInClosed accepts no more passengers; boarding proceeds.
	StateCheckInClosed State = "checkin_closed"
	// StateClosed is final: the messages have been built and the loadsheet
	// produced. Nothing changes after this.
	StateClosed State = "closed"
)

// Alert is something the flight wants a supervisor to see.
type Alert struct {
	At     time.Time `json:"at"`
	Code   string    `json:"code"`
	Detail string    `json:"detail"`
}

// Flight is one departure under control.
type Flight struct {
	Key
	Carrier      string `json:"carrier"`
	Dest         string `json:"dest"`
	Equipment    string `json:"equipment"`
	Registration string `json:"registration,omitempty"`
	// Version is the cabin configuration as the LDM names it: Y180, C48Y312.
	Version string `json:"version"`
	Cabin   *Cabin `json:"cabin"`
	// Crew is the cockpit/cabin crew complement for the LDM, e.g. "3/12".
	Crew string `json:"crew,omitempty"`

	State           State      `json:"state"`
	OpenedAt        time.Time  `json:"opened_at"`
	CheckInClosedAt *time.Time `json:"checkin_closed_at,omitempty"`
	ClosedAt        *time.Time `json:"closed_at,omitempty"`

	Passengers []*Passenger `json:"passengers"`
	// PartsSeen tracks which PNL parts arrived; Complete says the last one did.
	PartsSeen map[int]bool `json:"parts_seen,omitempty"`
	Complete  bool         `json:"complete"`
	ADLs      int          `json:"adls"`
	Alerts    []Alert      `json:"alerts,omitempty"`

	// Load and Loadsheet are produced at close.
	Load      *Load  `json:"load,omitempty"`
	Loadsheet string `json:"loadsheet,omitempty"`

	// Version is the optimistic concurrency token, bumped on every change.
	Revision int64 `json:"revision"`

	nextID  int
	nextSeq int
}

// Counts is the summary a departures board shows.
type Counts struct {
	Listed   int `json:"listed"`
	Accepted int `json:"accepted"`
	Boarded  int `json:"boarded"`
	Standby  int `json:"standby"`
	NoShow   int `json:"noshow"`
	Offload  int `json:"offloaded"`
	Bags     int `json:"bags"`
	BagKilos int `json:"bag_kilos"`
	Seats    int `json:"seats"`
}

// Counts summarises the manifest.
func (f *Flight) Counts() Counts {
	var c Counts
	for _, p := range f.Passengers {
		switch p.Status {
		case StatusListed:
			c.Listed++
		case StatusAccepted:
			c.Accepted++
		case StatusBoarded:
			c.Boarded++
		case StatusStandby:
			c.Standby++
		case StatusNoShow:
			c.NoShow++
		case StatusOffloaded:
			c.Offload++
		}
		if p.Flying() {
			for _, b := range p.Bags {
				if !b.Offloaded {
					c.Bags++
					c.BagKilos += b.Weight
				}
			}
		}
	}
	if f.Cabin != nil {
		c.Seats = f.Cabin.Seats()
	}
	return c
}

func (f *Flight) alert(at time.Time, code, detail string) {
	f.Alerts = append(f.Alerts, Alert{At: at, Code: code, Detail: detail})
}

// passenger finds by ID.
func (f *Flight) passenger(id int) *Passenger {
	for _, p := range f.Passengers {
		if p.ID == id {
			return p
		}
	}
	return nil
}

// party lists the active members of one name item.
func (f *Flight) party(key string) []*Passenger {
	var out []*Passenger
	for _, p := range f.Passengers {
		if p.Party == key && p.Status != StatusDeleted {
			out = append(out, p)
		}
	}
	return out
}

// Errors an agent's screen shows. They are values so an embedder can map
// them to its own codes; the text is what a DCS says.
var (
	ErrFlightNotFound    = errors.New("dcs: flight not under control")
	ErrFlightClosed      = errors.New("dcs: flight closed")
	ErrCheckInClosed     = errors.New("dcs: check-in closed")
	ErrCheckInOpen       = errors.New("dcs: check-in still open")
	ErrPassengerNotFound = errors.New("dcs: passenger not on this flight")
	ErrAlreadyAccepted   = errors.New("dcs: passenger already accepted")
	ErrNotAccepted       = errors.New("dcs: passenger not accepted")
	ErrAlreadyBoarded    = errors.New("dcs: passenger already boarded")
	ErrSeatTaken         = errors.New("dcs: seat occupied")
	ErrNoSeat            = errors.New("dcs: no seat available")
	ErrNoSuchSeat        = errors.New("dcs: no such seat")
	ErrListAfterAccept   = errors.New("dcs: name list received after acceptance began")
	ErrWrongFlight       = errors.New("dcs: message is for another flight")
	ErrNotFound          = errors.New("dcs: not found")
	ErrRevisionConflict  = errors.New("dcs: flight changed underneath the write")
)

// Store persists flights. Every flight is one document; Save replaces it.
type Store interface {
	SaveFlight(ctx context.Context, f *Flight) error
	LoadFlight(ctx context.Context, k Key) (*Flight, error)
	ListFlights(ctx context.Context) ([]Key, error)
	DeleteFlight(ctx context.Context, k Key) error
}

// MemStore keeps flights as JSON documents in memory. It is the shape a
// durable store takes: the document is the unit, and a Postgres table with a
// key and a jsonb column is the whole port.
type MemStore struct {
	mu   sync.Mutex
	docs map[Key][]byte
}

// NewMemStore returns an empty store.
func NewMemStore() *MemStore { return &MemStore{docs: map[Key][]byte{}} }

// SaveFlight implements Store.
func (m *MemStore) SaveFlight(ctx context.Context, f *Flight) error {
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.docs[f.Key] = b
	return nil
}

// LoadFlight implements Store.
func (m *MemStore) LoadFlight(ctx context.Context, k Key) (*Flight, error) {
	m.mu.Lock()
	b, ok := m.docs[k]
	m.mu.Unlock()
	if !ok {
		return nil, ErrNotFound
	}
	var f Flight
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	f.restoreCounters()
	return &f, nil
}

// ListFlights implements Store.
func (m *MemStore) ListFlights(ctx context.Context) ([]Key, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Key, 0, len(m.docs))
	for k := range m.docs {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out, nil
}

// DeleteFlight implements Store.
func (m *MemStore) DeleteFlight(ctx context.Context, k Key) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.docs, k)
	return nil
}

// restoreCounters rebuilds the unexported counters after a load, so IDs and
// sequence numbers continue rather than repeat.
func (f *Flight) restoreCounters() {
	for _, p := range f.Passengers {
		if p.ID >= f.nextID {
			f.nextID = p.ID + 1
		}
		if p.Sequence >= f.nextSeq {
			f.nextSeq = p.Sequence + 1
		}
	}
	if f.nextID == 0 {
		f.nextID = 1
	}
	if f.nextSeq == 0 {
		f.nextSeq = 1
	}
	if f.Cabin != nil {
		f.Cabin.reindex(f.Passengers)
	}
}

// Equipment is what a station needs to know about a departure to open it:
// which aircraft, so the cabin and the weight data are right.
type Equipment struct {
	Type         string // aircraft type code in the fleet, e.g. "320"
	Registration string
	Dest         string
	Crew         string // cockpit/cabin, e.g. "2/4"
}

// Station is one carrier's departure control at one node.
type Station struct {
	// Carrier is the two-character designator whose flights this handles.
	Carrier string
	// AccountingCode is the carrier's three-digit numeric code, which leads
	// the bag tag licence plate. Zero-padded; "000" when unknown.
	AccountingCode string
	// Store persists flights. Nil means a fresh MemStore.
	Store Store
	// Fleet is the aircraft data. Nil means DefaultFleet.
	Fleet *FleetData
	// Weights are the standard passenger weights for load control.
	Weights StandardWeights
	// Equipment resolves a departure to its aircraft when a name list opens a
	// flight nothing has opened explicitly. Nil opens with the fleet's
	// default type and no registration, which is enough to check in and
	// wrong for a loadsheet, and is recorded as an alert on the flight.
	Equipment func(k Key) (Equipment, bool)
	// Now is the clock; nil is time.Now.
	Now func() time.Time
	Log *slog.Logger

	mu      sync.Mutex
	flights map[Key]*Flight
	tagSeq  int
}

// NewStation returns a station ready to receive name lists.
func NewStation(carrier string) *Station {
	return &Station{Carrier: carrier, flights: map[Key]*Flight{}}
}

func (s *Station) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *Station) store() Store {
	if s.Store == nil {
		s.Store = NewMemStore()
	}
	return s.Store
}

func (s *Station) fleet() *FleetData {
	if s.Fleet == nil {
		s.Fleet = DefaultFleet()
	}
	return s.Fleet
}

func (s *Station) log() *slog.Logger {
	if s.Log == nil {
		return slog.Default()
	}
	return s.Log
}

// Restore loads every persisted flight into memory. Call once at start; a
// station that skips it begins empty and overwrites what the store held as
// flights re-open.
func (s *Station) Restore(ctx context.Context) error {
	keys, err := s.store().ListFlights(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.flights == nil {
		s.flights = map[Key]*Flight{}
	}
	for _, k := range keys {
		f, err := s.store().LoadFlight(ctx, k)
		if err != nil {
			return fmt.Errorf("dcs: restore %s: %w", k, err)
		}
		s.flights[k] = f
	}
	return nil
}

// Flights lists every flight under control, oldest first.
func (s *Station) Flights() []*Flight {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Flight, 0, len(s.flights))
	for _, f := range s.flights {
		out = append(out, f.snapshot())
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].OpenedAt.Equal(out[j].OpenedAt) {
			return out[i].OpenedAt.Before(out[j].OpenedAt)
		}
		return out[i].Key.String() < out[j].Key.String()
	})
	return out
}

// Flight returns a copy of one flight, or ErrFlightNotFound.
func (s *Station) Flight(k Key) (*Flight, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.flights[k]
	if !ok {
		return nil, ErrFlightNotFound
	}
	return f.snapshot(), nil
}

// Find locates a flight by designator and date without the boarding point,
// for callers that know the flight but not the station.
func (s *Station) Find(flight, date string) (*Flight, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, f := range s.flights {
		if k.Flight == flight && (date == "" || k.Date == date) {
			return f.snapshot(), true
		}
	}
	return nil, false
}

// Forget drops a closed flight from memory and the store. Departure control
// keeps a flight for the day; the ledger of what was sent lives in the
// gateway's message log, not here.
func (s *Station) Forget(ctx context.Context, k Key) error {
	s.mu.Lock()
	delete(s.flights, k)
	s.mu.Unlock()
	return s.store().DeleteFlight(ctx, k)
}

// snapshot deep-copies a flight for callers outside the lock.
func (f *Flight) snapshot() *Flight {
	b, _ := json.Marshal(f)
	var c Flight
	_ = json.Unmarshal(b, &c)
	c.restoreCounters()
	return &c
}

// Open puts a flight under control explicitly, ahead of its name list. A
// flight already open is returned unchanged.
func (s *Station) Open(ctx context.Context, k Key, eq Equipment) (*Flight, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if f, ok := s.flights[k]; ok {
		return f.snapshot(), nil
	}
	f, err := s.open(k, eq)
	if err != nil {
		return nil, err
	}
	if err := s.store().SaveFlight(ctx, f); err != nil {
		return nil, err
	}
	return f.snapshot(), nil
}

func (s *Station) open(k Key, eq Equipment) (*Flight, error) {
	if s.flights == nil {
		s.flights = map[Key]*Flight{}
	}
	fl := s.fleet()
	t, ok := fl.Type(eq.Type)
	now := s.now()
	f := &Flight{
		Key: k, Carrier: carrierOf(k.Flight), Dest: eq.Dest,
		Equipment: eq.Type, Registration: eq.Registration, Crew: eq.Crew,
		State: StateOpen, OpenedAt: now, PartsSeen: map[int]bool{},
		nextID: 1, nextSeq: 1,
	}
	if !ok {
		t = fl.Default()
		f.Equipment = t.Code
		if eq.Type != "" {
			f.alert(now, "unknown_equipment", fmt.Sprintf("aircraft type %q is not in the fleet data; opened as %s", eq.Type, t.Code))
		}
	}
	f.Version = t.Version()
	f.Cabin = t.Cabin.instance()
	if f.Carrier == "" {
		f.Carrier = s.Carrier
	}
	s.flights[k] = f
	return f, nil
}

// carrierOf takes the designator off a flight as written: BA0117 -> BA.
func carrierOf(flight string) string {
	if len(flight) >= 2 {
		return flight[:2]
	}
	return ""
}

// ApplyNameList folds a PNL or ADL part into the flight it names, opening
// the flight if this is the first word of it.
//
// A PNL is the list; parts accumulate until the final one. A PNL arriving
// after acceptance has begun is refused: replacing the manifest under a
// checked-in passenger is how people end up boarded with no record, and the
// practice reserves that for a new flight. An ADL is applied in the order the
// practice fixes -- deletions, additions, changes -- and a deletion of an
// accepted passenger is kept as an alert rather than obeyed.
func (s *Station) ApplyNameList(ctx context.Context, m *pnl.Message) (*Flight, error) {
	if m == nil {
		return nil, errors.New("dcs: nil name list")
	}
	k := Key{Flight: m.Flight, Date: m.Date, Board: m.Board}
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.flights[k]
	if !ok {
		var eq Equipment
		if s.Equipment != nil {
			eq, _ = s.Equipment(k)
		}
		var err error
		f, err = s.open(k, eq)
		if err != nil {
			return nil, err
		}
		if s.Equipment == nil {
			f.alert(s.now(), "no_equipment", "opened by name list with no equipment lookup; load control will be wrong")
		}
	}
	if f.State == StateClosed {
		return nil, ErrFlightClosed
	}
	now := s.now()
	switch m.Kind {
	case pnl.KindPNL:
		if err := f.applyPNL(m, now); err != nil {
			return nil, err
		}
	case pnl.KindADL:
		f.applyADL(m, now)
	default:
		return nil, fmt.Errorf("dcs: %q is not a name list", m.Kind)
	}
	if f.Dest == "" {
		for _, g := range m.Groups {
			if g.Dest != "" {
				f.Dest = g.Dest
				break
			}
		}
	}
	f.Revision++
	if err := s.store().SaveFlight(ctx, f); err != nil {
		return nil, err
	}
	return f.snapshot(), nil
}

func (f *Flight) applyPNL(m *pnl.Message, now time.Time) error {
	for _, p := range f.Passengers {
		if p.Status != StatusListed && p.Status != StatusDeleted {
			f.alert(now, "pnl_after_acceptance", fmt.Sprintf("PNL part %d refused: acceptance has begun", m.Part))
			return ErrListAfterAccept
		}
	}
	if f.PartsSeen[m.Part] {
		f.alert(now, "duplicate_pnl_part", fmt.Sprintf("PNL part %d received twice; the repeat was ignored", m.Part))
		return nil
	}
	if f.Complete {
		f.alert(now, "pnl_after_end", fmt.Sprintf("PNL part %d arrived after the final part; ignored", m.Part))
		return nil
	}
	f.PartsSeen[m.Part] = true
	for _, g := range m.Groups {
		for _, n := range g.Names {
			f.addNames(g, n, now)
		}
	}
	if m.Final {
		f.Complete = true
		for i := 1; i < m.Part; i++ {
			if !f.PartsSeen[i] {
				f.alert(now, "pnl_part_missing", fmt.Sprintf("PNL part %d never arrived", i))
			}
		}
	}
	return nil
}

func (f *Flight) applyADL(m *pnl.Message, now time.Time) {
	f.ADLs++
	if !f.Complete && len(f.PartsSeen) == 0 {
		f.alert(now, "adl_before_pnl", "ADL received before any PNL; applied to an empty list")
	}
	// Deletions first, then additions, then changes: the practice's order,
	// and the one that makes a same-message delete-and-add a move.
	for _, want := range []pnl.Change{pnl.ChangeDEL, pnl.ChangeADD, pnl.ChangeCHG} {
		for _, g := range m.Groups {
			for _, sec := range g.Sections {
				if sec.Change != want {
					continue
				}
				for _, n := range sec.Names {
					switch want {
					case pnl.ChangeDEL:
						f.deleteNames(n, now)
					case pnl.ChangeADD:
						f.addNames(g, n, now)
					case pnl.ChangeCHG:
						f.changeNames(g, n, now)
					}
				}
			}
		}
	}
}

// partyKey is how a name item is recognised across the PNL and its ADLs:
// the locator when there is one, else the surname and party. The locator
// is what the practice keys on; the fallback is for lists without one.
func partyKey(n pnl.Name) string {
	if loc := elementValue(n.Elements, ".L/"); loc != "" {
		return loc + "/" + n.Surname
	}
	return n.Surname + "/" + strings.Join(n.Givens, "/")
}

func elementValue(elements []string, prefix string) string {
	for _, e := range elements {
		if strings.HasPrefix(e, prefix) {
			return strings.Fields(strings.TrimPrefix(e, prefix))[0]
		}
	}
	return ""
}

// addNames turns one name item into passengers.
func (f *Flight) addNames(g pnl.Group, n pnl.Name, now time.Time) {
	key := partyKey(n)
	// An ADD for a party already listed is a repeat, not a second party:
	// listing a passenger twice sells the seat twice.
	if existing := f.party(key); len(existing) > 0 {
		f.alert(now, "duplicate_name", fmt.Sprintf("%s already listed; addition ignored", key))
		return
	}
	// A party deleted earlier and added back is reinstated rather than
	// duplicated, so their history survives the round trip.
	for _, p := range f.Passengers {
		if p.Party == key && p.Status == StatusDeleted {
			p.Status = StatusListed
			p.Class, p.Compartment, p.Dest = g.Class, f.compartmentFor(g.Class), g.Dest
			return
		}
	}
	party := n.Party
	if party <= 0 {
		party = max(1, len(n.Givens))
	}
	givens := n.Givens
	for len(givens) < party {
		// A party count larger than the names given happens on lists that
		// carry only the lead name. The seat is real; the name is not known.
		givens = append(givens, "TBA")
	}
	loc := elementValue(n.Elements, ".L/")
	ssrs, ticket, rest := parseElements(n.Elements)
	for i := 0; i < party; i++ {
		p := &Passenger{
			ID: f.nextID, Surname: n.Surname, Given: givens[i], Locator: loc, Party: key,
			Class: g.Class, Compartment: f.compartmentFor(g.Class), Dest: g.Dest,
			Type: typeOf(givens[i], ssrs), SSRs: ssrs, Ticket: ticket, Elements: rest,
			Status: StatusListed,
		}
		f.nextID++
		f.Passengers = append(f.Passengers, p)
	}
}

// parseElements reads the dotted elements a name item carries: service
// requests and the ticket number are interpreted, everything else is kept.
func parseElements(elements []string) (ssrs []SSR, ticket string, rest []string) {
	for _, e := range elements {
		switch {
		case strings.HasPrefix(e, ".L/"):
			// The locator is read by partyKey.
		case strings.HasPrefix(e, ".R/"):
			// .R/WCHR HK1, .R/TKNE HK1 1251234567890C1, .R/VGML
			fields := strings.Fields(strings.TrimPrefix(e, ".R/"))
			if len(fields) == 0 {
				continue
			}
			code := fields[0]
			text := strings.Join(fields[1:], " ")
			if code == "TKNE" {
				// The ticket number is the last token that looks like one:
				// thirteen digits, optionally with a coupon suffix.
				for i := len(fields) - 1; i > 0; i-- {
					if isTicketNumber(fields[i]) {
						ticket = fields[i]
						break
					}
				}
				continue
			}
			ssrs = append(ssrs, SSR{Code: code, Text: text})
		default:
			rest = append(rest, e)
		}
	}
	return ssrs, ticket, rest
}

func isTicketNumber(s string) bool {
	digits := 0
	for i, r := range s {
		switch {
		case r >= '0' && r <= '9':
			digits++
		case r == 'C' && i >= 13:
			// coupon suffix: 1251234567890C1
		default:
			return false
		}
	}
	return digits >= 13
}

// typeOf infers the weight class from the title run and the service codes.
func typeOf(given string, ssrs []SSR) PassengerType {
	for _, s := range ssrs {
		switch s.Code {
		case "INFT":
			return PaxInfant
		case "CHLD", "UMNR":
			return PaxChild
		}
	}
	if strings.HasSuffix(given, "MSTR") {
		return PaxChild
	}
	return PaxAdult
}

// compartmentFor maps a booking class to the cabin that seats it.
func (f *Flight) compartmentFor(class string) string {
	if f.Cabin == nil {
		return "Y"
	}
	return f.Cabin.CompartmentFor(class)
}

func (f *Flight) deleteNames(n pnl.Name, now time.Time) {
	key := partyKey(n)
	found := false
	for _, p := range f.party(key) {
		found = true
		switch p.Status {
		case StatusListed, StatusStandby:
			p.Status = StatusDeleted
		case StatusAccepted, StatusBoarded:
			// Reservations no longer holds them; the airport does. The
			// passenger keeps the seat and the flight keeps the conflict.
			p.DeletedAfterAcceptance = true
			f.alert(now, "adl_deletes_accepted", fmt.Sprintf("ADL deleted %s after acceptance; passenger kept, reported as go-show", p.Display()))
		}
	}
	if !found {
		f.alert(now, "adl_deletes_unknown", fmt.Sprintf("ADL deleted %s, who was never listed", key))
	}
}

// changeNames applies a CHG item: the same party with new elements. The
// practice carries the whole revised item, so the change replaces the
// elements and keeps the passengers.
func (f *Flight) changeNames(g pnl.Group, n pnl.Name, now time.Time) {
	key := partyKey(n)
	members := f.party(key)
	if len(members) == 0 {
		f.addNames(g, n, now)
		return
	}
	ssrs, ticket, rest := parseElements(n.Elements)
	for _, p := range members {
		p.SSRs, p.Elements = ssrs, rest
		if ticket != "" {
			p.Ticket = ticket
		}
		if g.Class != "" && p.Status == StatusListed {
			p.Class, p.Compartment = g.Class, f.compartmentFor(g.Class)
		}
	}
}

// BSMFor builds the baggage source message for one passenger's bags: what
// check-in tells the sortation system when a bag is tagged, and -- with a
// change of DEL -- when it must come off again.
func (s *Station) BSMFor(f *Flight, p *Passenger, change string) *baggage.Message {
	m := &baggage.Message{
		Kind:    baggage.KindBSM,
		Change:  change,
		Version: "1L" + f.Board,
		Outbound: &baggage.FlightLeg{
			Flight: f.Flight, Date: f.Date, City: f.Dest, Class: p.Compartment,
		},
		Surname: p.Surname,
		Givens:  []string{p.Given},
	}
	if p.Onward != nil {
		m.Version = "1T" + f.Board
		m.Onward = &baggage.FlightLeg{Flight: p.Onward.Flight, Date: p.Onward.Date, City: p.Onward.Dest}
	}
	if p.Inbound != nil {
		m.Inbound = &baggage.FlightLeg{Flight: p.Inbound.Flight, Date: p.Inbound.Date, City: p.Inbound.Station}
	}
	for _, b := range p.Bags {
		m.Tags = append(m.Tags, baggage.Tag{Number: b.Tag, Count: 1})
	}
	if p.Seat != "" {
		m.Elements = append(m.Elements, fmt.Sprintf(".S/Y/%s/%03d", p.Seat, p.Sequence))
	}
	total := 0
	for _, b := range p.Bags {
		total += b.Weight
	}
	if total > 0 {
		m.Elements = append(m.Elements, fmt.Sprintf(".W/K/%d/%d", len(p.Bags), total))
	}
	return m
}

// ApplyBagReport folds a BPM -- the sortation system saying what it did with
// the bags -- into the flight. A tag it does not know is recorded, because a
// bag loaded onto a flight that has no passenger for it is exactly what
// reconciliation exists to catch.
func (s *Station) ApplyBagReport(ctx context.Context, m *baggage.Message) (*Flight, []string, error) {
	if m == nil || m.Outbound == nil {
		return nil, nil, errors.New("dcs: bag report names no flight")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var f *Flight
	for k, cand := range s.flights {
		if k.Flight == m.Outbound.Flight && k.Date == m.Outbound.Date {
			f = cand
			break
		}
	}
	if f == nil {
		return nil, nil, ErrFlightNotFound
	}
	var unknown []string
	for _, t := range m.Tags {
		count := max(1, t.Count)
		for i := 0; i < count; i++ {
			tag := bumpTag(t.Number, i)
			if !f.markLoaded(tag) {
				unknown = append(unknown, tag)
			}
		}
	}
	if len(unknown) > 0 {
		f.alert(s.now(), "unaccompanied_bag", fmt.Sprintf("%d bag(s) reported loaded that no passenger here checked: %s", len(unknown), strings.Join(unknown, " ")))
	}
	f.Revision++
	if err := s.store().SaveFlight(ctx, f); err != nil {
		return nil, nil, err
	}
	return f.snapshot(), unknown, nil
}

func (f *Flight) markLoaded(tag string) bool {
	for _, p := range f.Passengers {
		for i := range p.Bags {
			if p.Bags[i].Tag == tag {
				p.Bags[i].Loaded = true
				return true
			}
		}
	}
	return false
}

// bumpTag adds i to a licence plate's sequence, for consecutive-tag runs.
func bumpTag(number string, i int) string {
	if i == 0 || len(number) != 10 {
		return number
	}
	var n int
	fmt.Sscanf(number[4:], "%d", &n)
	return fmt.Sprintf("%s%06d", number[:4], n+i)
}

// nextTag issues a licence plate: a leading 0, the carrier's accounting
// code, a six-digit sequence. Callers hold s.mu.
func (s *Station) nextTag() string {
	s.tagSeq++
	code := s.AccountingCode
	if len(code) != 3 {
		code = "000"
	}
	return fmt.Sprintf("0%s%06d", code, s.tagSeq%1000000)
}
