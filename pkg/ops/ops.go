// Package ops is a carrier's operations desk in a node that runs the
// carrier for real: the schedule it flies, departure control at its
// stations, and the movement messages it owes the network. A gateway
// alone captures and books; a carrier also has to answer the airport's
// name lists with a departure control system, turn the aircraft's OOOI
// reports from the datalink provider into the MVTs its partners and the
// distribution systems read, and file what the towers and the Network
// Manager tell it. The desk is that: a gateway.Ground built from an SSIM
// schedule, with a dcs.Station behind it and a place to send movements.
//
// It is the part of a carrier the world simulator's tenants had and a bare
// jetwayd did not, which is why a node dialling a world as one of its
// carriers flew no aircraft anyone could see.
package ops

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/adamf/jetway/pkg/acars"
	"github.com/adamf/jetway/pkg/aftn"
	"github.com/adamf/jetway/pkg/atfm"
	"github.com/adamf/jetway/pkg/ats"
	"github.com/adamf/jetway/pkg/baggage"
	"github.com/adamf/jetway/pkg/dcs"
	"github.com/adamf/jetway/pkg/gateway"
	"github.com/adamf/jetway/pkg/mvt"
	"github.com/adamf/jetway/pkg/pnl"
	"github.com/adamf/jetway/pkg/ssim"
	"github.com/adamf/jetway/pkg/typeb"
)

// Leg is one scheduled departure the desk knows: what the SSIM file said.
type Leg struct {
	Carrier, Number string
	Board, Off      string
	// STD and STA are minutes after 0000 in the schedule's time mode.
	STD, STA  int
	Equipment string
	Days      string
	From, To  time.Time
}

// Config is what the desk needs beyond the schedule.
type Config struct {
	// Via is the link name outbound operational messages take: the switch.
	Via string
	// MovementsTo are the Type B addresses movement messages go to: the
	// distribution systems, partners, an operations centre.
	MovementsTo []string
	// AccountingCode leads the carrier's bag tags.
	AccountingCode string
}

// Desk is the operations desk.
type Desk struct {
	Gateway *gateway.Gateway
	Station *dcs.Station
	Carrier string
	Config  Config
	Now     func() time.Time
	Log     *slog.Logger

	legs map[string][]Leg // carrier + number, leading zeros trimmed
	mu   sync.Mutex
	ats  map[ats.Type]int
	atfm map[atfm.Title]int
	// slots is the current slot line per flight and date.
	slots     map[string]string
	movements int
}

// LoadSchedule reads an SSIM chapter 7 file into legs.
func LoadSchedule(path string) ([]Leg, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	file, err := ssim.ParseFile(f)
	if err != nil {
		return nil, fmt.Errorf("ops: reading the schedule: %w", err)
	}
	return Legs(file), nil
}

// Legs turns a parsed file into the desk's legs.
func Legs(file *ssim.File) []Leg {
	var out []Leg
	for _, fl := range file.Legs {
		out = append(out, Leg{
			Carrier: fl.Carrier, Number: fl.Number, Board: fl.Board, Off: fl.Off,
			STD: hhmmMin(fl.STD), STA: hhmmMin(fl.STA), Equipment: fl.Equipment, Days: fl.Days, From: fl.From, To: fl.To,
		})
	}
	return out
}

func hhmmMin(s string) int {
	s = strings.TrimSpace(s)
	if len(s) != 4 {
		return 0
	}
	var h, m int
	fmt.Sscanf(s, "%02d%02d", &h, &m)
	return h*60 + m
}

// New builds a desk for a carrier from its legs.
func New(gw *gateway.Gateway, carrier string, legs []Leg, cfg Config, log *slog.Logger) *Desk {
	if log == nil {
		log = slog.Default()
	}
	d := &Desk{Gateway: gw, Carrier: strings.ToUpper(carrier), Config: cfg, Now: time.Now, Log: log,
		legs: map[string][]Leg{}, ats: map[ats.Type]int{}, atfm: map[atfm.Title]int{}, slots: map[string]string{}}
	for _, l := range legs {
		d.legs[key(l.Carrier, l.Number)] = append(d.legs[key(l.Carrier, l.Number)], l)
	}
	st := dcs.NewStation(d.Carrier)
	st.AccountingCode = cfg.AccountingCode
	st.Log = log
	st.Equipment = func(k dcs.Key) (dcs.Equipment, bool) {
		if l, ok := d.leg(k.Flight, k.Board, -1); ok {
			return dcs.Equipment{Type: l.Equipment, Dest: l.Off}, true
		}
		return dcs.Equipment{}, false
	}
	d.Station = st
	return d
}

func key(carrier, number string) string {
	return strings.ToUpper(carrier) + strings.TrimLeft(strings.TrimSpace(number), "0")
}

// leg is the scheduled departure for a flight as written (BA0117 or
// BAW117-style already normalised by the caller) from a boarding point;
// with several legs a day and no board, the one nearest the minute given.
func (d *Desk) leg(flight, board string, nearMin int) (Leg, bool) {
	flight = strings.ToUpper(strings.TrimSpace(flight))
	if len(flight) < 3 {
		return Leg{}, false
	}
	legs := d.legs[key(flight[:2], flight[2:])]
	if len(legs) == 0 {
		return Leg{}, false
	}
	best, found := Leg{}, false
	for _, l := range legs {
		if board != "" && !strings.EqualFold(l.Board, board) {
			continue
		}
		if !found || (nearMin >= 0 && abs(l.STD-nearMin) < abs(best.STD-nearMin)) {
			best, found = l, true
		}
	}
	return best, found
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// Legs is the schedule as loaded.
func (d *Desk) Legs() []Leg {
	var out []Leg
	for _, ls := range d.legs {
		out = append(out, ls...)
	}
	return out
}

// NameList implements gateway.Ground: the airport's departure control
// opens the flight from the carrier's own name list.
func (d *Desk) NameList(ctx context.Context, m *pnl.Message, origin typeb.Address) error {
	_, err := d.Station.ApplyNameList(ctx, m)
	return err
}

// Baggage implements gateway.Ground: the hold's report reconciles against
// the cabin; tag and rush messages are the sortation system's and are filed.
func (d *Desk) Baggage(ctx context.Context, m *baggage.Message, origin typeb.Address) error {
	if m.Kind == baggage.KindBPM {
		_, _, err := d.Station.ApplyBagReport(ctx, m)
		return err
	}
	return nil
}

// Departure implements gateway.Ground: departure control output from
// another station is filed.
func (d *Desk) Departure(ctx context.Context, m *dcs.Message, origin typeb.Address) error {
	return nil
}

// Datalink implements gateway.Ground: the aircraft's OOOI report becomes
// the movement message the network reads.
func (d *Desk) Datalink(ctx context.Context, m *acars.Message, origin typeb.Address) error {
	msg, ok := d.MovementFor(m)
	if !ok {
		return nil
	}
	text, err := msg.Build()
	if err != nil {
		return err
	}
	d.mu.Lock()
	d.movements++
	d.mu.Unlock()
	return d.send(ctx, text, "MVT")
}

// MovementFor is the MVT an OOOI report calls for: an AD with the ETA for
// a departure once the aircraft is off, an AA for an arrival once it is
// in, nothing for half a movement. The delay against schedule is coded 93
// (late aircraft) when the report does not say.
func (d *Desk) MovementFor(m *acars.Message) (*mvt.Message, bool) {
	now := d.Now().UTC()
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	flight := strings.ToUpper(m.Flight)
	switch {
	case m.Kind == acars.KindDEP && m.Off != "":
		l, ok := d.leg(flight, "", hhmmMin(m.Off))
		if !ok {
			return nil, false
		}
		out := m.Out
		if out == "" {
			out = m.Off
		}
		delay := minutesBetween(fmt.Sprintf("%02d%02d", l.STD/60%24, l.STD%60), out)
		eta := day.Add(time.Duration(l.STA+max(delay, 0)) * time.Minute)
		msg := &mvt.Message{Kind: mvt.KindMVT, Flight: l.Carrier + strings.TrimLeft(l.Number, "0"), Day: fmt.Sprintf("%02d", day.Day()),
			Registration: m.Registration, Station: l.Board,
			AD: &mvt.TimePair{First: out, Second: m.Off}, EA: &mvt.ETA{Time: eta.Format("1504"), Airport: l.Off}}
		if delay > 0 {
			msg.Delays = []mvt.Delay{{Code: "93", Duration: fmt.Sprintf("%02d%02d", delay/60, delay%60)}}
		}
		return msg, true
	case m.Kind == acars.KindARR && m.In != "":
		l, ok := d.leg(flight, "", hhmmMin(m.In)-90)
		if !ok {
			return nil, false
		}
		on := m.On
		if on == "" {
			on = m.In
		}
		return &mvt.Message{Kind: mvt.KindMVT, Flight: l.Carrier + strings.TrimLeft(l.Number, "0"), Day: fmt.Sprintf("%02d", day.Day()),
			Registration: m.Registration, Station: l.Off, AA: &mvt.TimePair{First: on, Second: m.In}}, true
	}
	return nil, false
}

// minutesBetween is the signed minutes from a scheduled HHMM to an actual
// one, across midnight the short way.
func minutesBetween(sched, actual string) int {
	if len(sched) != 4 || len(actual) != 4 {
		return 0
	}
	d := hhmmMin(actual) - hhmmMin(sched)
	if d > 720 {
		d -= 1440
	}
	if d < -720 {
		d += 1440
	}
	return d
}

// ATS implements gateway.Ground: the towers' messages are counted.
func (d *Desk) ATS(ctx context.Context, m *ats.Message, env *aftn.Message) error {
	d.mu.Lock()
	d.ats[m.Type]++
	d.mu.Unlock()
	return nil
}

// ATFM implements gateway.ATFMReceiver: the Network Manager's slot is filed
// against the flight.
func (d *Desk) ATFM(ctx context.Context, m *atfm.Message, env *aftn.Message) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.atfm[m.Title]++
	k := m.ARCID + "/" + m.EOBD
	switch m.Title {
	case atfm.TitleSAM:
		d.slots[k] = "CTOT " + m.CTOT + " " + strings.Join(m.REGUL, " ") + " " + m.REGCAUSE.String()
	case atfm.TitleSRM:
		d.slots[k] = "CTOT " + m.NEWCTOT + " " + strings.Join(m.REGUL, " ") + " " + m.REGCAUSE.String()
	case atfm.TitleSLC, atfm.TitleDES:
		delete(d.slots, k)
	case atfm.TitleFLS:
		d.slots[k] = "suspended: " + m.COMMENT
	}
	return nil
}

// Slots is what flow management has told the desk, by callsign and date.
func (d *Desk) Slots() map[string]string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make(map[string]string, len(d.slots))
	for k, v := range d.slots {
		out[k] = v
	}
	return out
}

// Movements is how many movement messages the desk has filed.
func (d *Desk) Movements() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.movements
}

// send puts operational text in a Type B envelope to the configured
// addresses and hands it to the outbound link.
func (d *Desk) send(ctx context.Context, text, kind string) error {
	if d.Gateway == nil || len(d.Config.MovementsTo) == 0 {
		return nil
	}
	peer := d.Gateway.Peer(d.Config.Via)
	if peer == nil {
		return fmt.Errorf("ops: no link %q to send %s on", d.Config.Via, kind)
	}
	dests := make([]typeb.Address, 0, len(d.Config.MovementsTo))
	for _, a := range d.Config.MovementsTo {
		addr, err := typeb.ParseAddress(a)
		if err != nil {
			return fmt.Errorf("ops: address %q: %w", a, err)
		}
		dests = append(dests, addr)
	}
	origin, err := typeb.ParseAddress(d.Gateway.Identity.TTYAddress)
	if err != nil {
		return fmt.Errorf("ops: own address: %w", err)
	}
	now := d.Now().UTC()
	out := &typeb.Message{Priority: "QU", Destinations: dests, Origin: origin,
		OriginTime: typeb.OriginTime{Present: true, Day: now.Day(), Hour: now.Hour(), Minute: now.Minute()}, Text: text}
	raw, err := out.Encode(typeb.EncodeOptions{Charset: typeb.CharsetITA2, CRLF: true})
	if err != nil {
		return err
	}
	_, err = d.Gateway.Send(ctx, peer, raw, kind, "", "")
	return err
}
