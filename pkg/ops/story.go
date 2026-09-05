package ops

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/adamf/jetway/pkg/dcs"
	"github.com/adamf/jetway/pkg/pnl"
)

// The ground story a carrier's operations desk can run for one of its
// departures: the name list from the node's own book to its station, the
// passengers accepted, the counter closed, the cabin boarded, the door
// closed with the load. A real carrier's day does this at set hours
// (PNL three hours out, close at ten minutes); the desk runs each step
// when asked, so a timetable outside it -- a person, an agent, a world
// driving the node over HTTP -- can keep the hours.

// BuildNameList builds the PNL for a departure from the node's own records: one
// group per class, a party per record with its locator, service requests
// and ticket numbers as the airport reads them.
func (d *Desk) BuildNameList(ctx context.Context, flight, date, board string) ([]string, error) {
	if d.Gateway == nil || d.Gateway.Store == nil {
		return nil, fmt.Errorf("ops: no book of record to list from")
	}
	flight, date, board = strings.ToUpper(flight), strings.ToUpper(date), strings.ToUpper(board)
	leg, ok := d.leg(flight, board, -1)
	if !ok {
		return nil, fmt.Errorf("ops: %s from %s is not in the schedule", flight, board)
	}
	recs, err := d.Gateway.Store.FindPNRsByFlight(ctx, flight, date, 5000)
	if err != nil {
		return nil, err
	}
	type item struct {
		name  pnl.Name
		class string
	}
	items := map[string]item{}
	for _, r := range recs {
		if len(r.Passengers) == 0 {
			continue
		}
		class, onLeg := "Y", false
		for _, sg := range r.Segments {
			if sg.Carrier+sg.FlightNum != flight && sg.Carrier+strings.TrimLeft(sg.FlightNum, "0") != strings.TrimLeft(flight[:2]+flight[2:], "0") {
				if key(sg.Carrier, sg.FlightNum) != key(flight[:2], flight[2:]) {
					continue
				}
			}
			if sg.Board != "" && sg.Board != board {
				continue
			}
			onLeg = true
			if sg.Class != "" {
				class = sg.Class
			}
		}
		if !onLeg {
			continue
		}
		n := pnl.Name{Party: len(r.Passengers), Surname: r.Passengers[0].Surname}
		for _, p := range r.Passengers {
			n.Givens = append(n.Givens, p.Given+p.Title)
		}
		if r.RecordLocator != "" {
			n.Elements = append(n.Elements, ".L/"+r.RecordLocator)
		}
		for _, ssr := range r.SSRs {
			if ssr.Code == "" || ssr.Sensitive {
				continue
			}
			n.Elements = append(n.Elements, fmt.Sprintf(".R/%s HK%d", ssr.Code, max(1, ssr.Count)))
		}
		for _, tk := range r.Tickets {
			if tk.Type == "" {
				n.Elements = append(n.Elements, ".R/TKNE HK1 "+tk.Number.String()+"C1")
			}
		}
		items[r.RecordLocator+"/"+n.Surname] = item{name: n, class: class}
	}
	classes := map[string]bool{}
	for _, it := range items {
		classes[it.class] = true
	}
	var classList []string
	for c := range classes {
		classList = append(classList, c)
	}
	sort.Strings(classList)
	keys := make([]string, 0, len(items))
	for k := range items {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var groups []pnl.Group
	for _, class := range classList {
		g := pnl.Group{Dest: leg.Off, Class: class}
		for _, k := range keys {
			if items[k].class == class {
				g.Names = append(g.Names, items[k].name)
				g.Count += items[k].name.Party
			}
		}
		groups = append(groups, g)
	}
	if len(groups) == 0 {
		groups = []pnl.Group{{Dest: leg.Off, Class: "Y"}}
	}
	return pnl.BuildParts(pnl.KindPNL, flight, date, board, groups)
}

// SendNameList builds the PNL, opens the flight at this node's own station
// with it, and copies it to the configured addresses -- the world the
// carrier flies in, the handlers, whoever reads the carrier's lists.
func (d *Desk) SendNameList(ctx context.Context, flight, date, board string) (int, error) {
	parts, err := d.BuildNameList(ctx, flight, date, board)
	if err != nil {
		return 0, err
	}
	for _, part := range parts {
		m, err := pnl.Parse(part)
		if err != nil {
			return 0, err
		}
		if _, err := d.Station.ApplyNameList(ctx, m); err != nil {
			return 0, err
		}
		if err := d.send(ctx, part, "PNL"); err != nil {
			return 0, err
		}
	}
	return len(parts), nil
}

// Story is what the ground story produced for a departure.
type Story struct {
	Flight    string `json:"flight"`
	Date      string `json:"date"`
	Board     string `json:"board"`
	Listed    int    `json:"listed"`
	Accepted  int    `json:"accepted"`
	Boarded   int    `json:"boarded"`
	Bags      int    `json:"bags"`
	Closed    bool   `json:"closed"`
	Messages  int    `json:"messages"`
	Loadsheet bool   `json:"loadsheet"`
}

// Run runs the whole ground story for a departure now: the name list, every
// passenger accepted with a bag, the counter closed, the cabin boarded, the
// door closed; the closure's messages -- final sales, transfer and service
// lists, load and container messages, the loadsheet -- go to the configured
// addresses. It is the autopilot's version of a day at the airport, for a
// node whose operator would rather watch than work the counter.
func (d *Desk) Run(ctx context.Context, flight, date, board string) (*Story, error) {
	flight, date, board = strings.ToUpper(flight), strings.ToUpper(date), strings.ToUpper(board)
	st := &Story{Flight: flight, Date: date, Board: board}
	k := dcs.Key{Flight: flight, Date: date, Board: board}
	if _, err := d.Station.Flight(k); err != nil {
		n, err := d.SendNameList(ctx, flight, date, board)
		if err != nil {
			return st, err
		}
		st.Listed = n
	}
	fl, err := d.Station.Flight(k)
	if err != nil {
		return st, err
	}
	for _, p := range fl.Passengers {
		if p.Status != dcs.StatusListed {
			continue
		}
		if _, err := d.Station.Accept(ctx, k, dcs.AcceptRequest{PassengerID: p.ID, Bags: []int{18}}); err == nil {
			st.Accepted++
			st.Bags++
		}
	}
	if _, _, err := d.Station.CloseCheckIn(ctx, k); err != nil {
		return st, err
	}
	fl, _ = d.Station.Flight(k)
	for _, p := range fl.Passengers {
		if p.Status == dcs.StatusAccepted {
			if _, err := d.Station.Board(ctx, k, p.ID); err == nil {
				st.Boarded++
			}
		}
	}
	cl, err := d.Station.CloseFlight(ctx, k, dcs.CloseOptions{Force: true})
	if err != nil {
		return st, err
	}
	st.Closed = true
	send := func(kind string, texts ...string) {
		for _, text := range texts {
			if text == "" {
				continue
			}
			if err := d.send(ctx, text, kind); err == nil {
				st.Messages++
			}
		}
	}
	send("PFS", cl.PFS...)
	send("PTM", cl.PTM...)
	send("PSM", cl.PSM...)
	send("ETL", cl.ETL...)
	send("LDM", cl.LDM)
	send("CPM", cl.CPM)
	st.Loadsheet = cl.Loadsheet != ""
	return st, nil
}
