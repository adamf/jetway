package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/adamf/jetway/pkg/dcs"
)

// Departure control over HTTP: the departures board, one flight's manifest
// and seat map, and the agent operations -- accept, go-show, board, offload,
// close check-in, close the flight. The console's Departures view is built
// on these; so could a kiosk or a gate reader be.

// dcsRoutes mounts the departure control endpoints. They answer 404 with a
// reason when the node runs no departure control, which a distribution
// system does not.
func (s *Server) dcsRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/dcs/flights", s.dcsFlights)
	mux.HandleFunc("GET /api/dcs/flight/{flight}/{date}", s.dcsFlight)
	mux.HandleFunc("POST /api/dcs/flight/{flight}/{date}/accept", s.dcsAccept)
	mux.HandleFunc("POST /api/dcs/flight/{flight}/{date}/goshow", s.dcsGoShow)
	mux.HandleFunc("POST /api/dcs/flight/{flight}/{date}/board", s.dcsBoard)
	mux.HandleFunc("POST /api/dcs/flight/{flight}/{date}/offload", s.dcsOffload)
	mux.HandleFunc("POST /api/dcs/flight/{flight}/{date}/close-checkin", s.dcsCloseCheckIn)
	mux.HandleFunc("POST /api/dcs/flight/{flight}/{date}/close", s.dcsClose)
}

// departureRow is one line of the departures board.
type departureRow struct {
	dcs.Key
	Dest         string     `json:"dest"`
	Equipment    string     `json:"equipment"`
	Registration string     `json:"registration,omitempty"`
	Version      string     `json:"version"`
	State        dcs.State  `json:"state"`
	Counts       dcs.Counts `json:"counts"`
	Alerts       int        `json:"alerts"`
	OpenedAt     string     `json:"opened_at"`
}

func (s *Server) dcsFlights(w http.ResponseWriter, r *http.Request) {
	if s.Ground == nil {
		http.Error(w, "this node runs no departure control", http.StatusNotFound)
		return
	}
	rows := []departureRow{}
	for _, f := range s.Ground.Flights() {
		rows = append(rows, departureRow{
			Key: f.Key, Dest: f.Dest, Equipment: f.Equipment, Registration: f.Registration,
			Version: f.Version, State: f.State, Counts: f.Counts(), Alerts: len(f.Alerts),
			OpenedAt: f.OpenedAt.UTC().Format("2006-01-02T15:04:05Z"),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"carrier": s.Ground.Carrier, "flights": rows})
}

// flightView is one flight with its seat map rendered for a display.
type flightView struct {
	*dcs.Flight
	Counts dcs.Counts `json:"counts"`
	Rows   []dcs.Row  `json:"rows"`
	// SeatHolders maps a seat to the passenger holding it, for the map.
	SeatHolders map[string]int `json:"seat_holders"`
}

func (s *Server) dcsFind(w http.ResponseWriter, r *http.Request) (*dcs.Flight, bool) {
	if s.Ground == nil {
		http.Error(w, "this node runs no departure control", http.StatusNotFound)
		return nil, false
	}
	flight := strings.ToUpper(r.PathValue("flight"))
	date := strings.ToUpper(r.PathValue("date"))
	f, ok := s.Ground.Find(flight, date)
	if !ok {
		http.Error(w, "flight not under control", http.StatusNotFound)
		return nil, false
	}
	return f, true
}

func (s *Server) dcsFlight(w http.ResponseWriter, r *http.Request) {
	f, ok := s.dcsFind(w, r)
	if !ok {
		return
	}
	v := flightView{Flight: f, Counts: f.Counts()}
	if f.Cabin != nil {
		v.Rows = f.Cabin.Rows()
		v.SeatHolders = f.Cabin.Occupied
	}
	writeJSON(w, http.StatusOK, v)
}

// dcsError maps a departure control refusal onto a status code: what the
// agent did wrong is a 409, what does not exist is a 404, and anything else
// is the server's fault.
func dcsError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, dcs.ErrFlightNotFound), errors.Is(err, dcs.ErrPassengerNotFound), errors.Is(err, dcs.ErrNoSuchSeat):
		http.Error(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, dcs.ErrFlightClosed), errors.Is(err, dcs.ErrCheckInClosed), errors.Is(err, dcs.ErrCheckInOpen),
		errors.Is(err, dcs.ErrAlreadyAccepted), errors.Is(err, dcs.ErrNotAccepted), errors.Is(err, dcs.ErrAlreadyBoarded),
		errors.Is(err, dcs.ErrSeatTaken), errors.Is(err, dcs.ErrNoSeat), errors.Is(err, dcs.ErrListAfterAccept):
		http.Error(w, err.Error(), http.StatusConflict)
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) dcsAccept(w http.ResponseWriter, r *http.Request) {
	f, ok := s.dcsFind(w, r)
	if !ok {
		return
	}
	var req dcs.AcceptRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	acc, err := s.Ground.Accept(r.Context(), f.Key, req)
	if err != nil {
		dcsError(w, err)
		return
	}
	if s.OnAccept != nil {
		s.OnAccept(r.Context(), acc)
	}
	writeJSON(w, http.StatusOK, acc)
}

func (s *Server) dcsGoShow(w http.ResponseWriter, r *http.Request) {
	f, ok := s.dcsFind(w, r)
	if !ok {
		return
	}
	var g dcs.GoShow
	if err := json.NewDecoder(r.Body).Decode(&g); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	acc, err := s.Ground.AcceptGoShow(r.Context(), f.Key, g)
	if err != nil {
		dcsError(w, err)
		return
	}
	if s.OnAccept != nil {
		s.OnAccept(r.Context(), acc)
	}
	writeJSON(w, http.StatusOK, acc)
}

type passengerAction struct {
	PassengerID int    `json:"passenger_id"`
	Reason      string `json:"reason,omitempty"`
}

func (s *Server) dcsBoard(w http.ResponseWriter, r *http.Request) {
	f, ok := s.dcsFind(w, r)
	if !ok {
		return
	}
	var a passengerAction
	if err := json.NewDecoder(r.Body).Decode(&a); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	p, err := s.Ground.Board(r.Context(), f.Key, a.PassengerID)
	if err != nil {
		dcsError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) dcsOffload(w http.ResponseWriter, r *http.Request) {
	f, ok := s.dcsFind(w, r)
	if !ok {
		return
	}
	var a passengerAction
	if err := json.NewDecoder(r.Body).Decode(&a); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if a.Reason == "" {
		a.Reason = "offloaded by agent"
	}
	p, err := s.Ground.Offload(r.Context(), f.Key, a.PassengerID, a.Reason)
	if err != nil {
		dcsError(w, err)
		return
	}
	if s.OnOffload != nil {
		s.OnOffload(r.Context(), f, p)
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) dcsCloseCheckIn(w http.ResponseWriter, r *http.Request) {
	f, ok := s.dcsFind(w, r)
	if !ok {
		return
	}
	fl, cleared, err := s.Ground.CloseCheckIn(r.Context(), f.Key)
	if err != nil {
		dcsError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"flight": fl, "cleared": cleared})
}

func (s *Server) dcsClose(w http.ResponseWriter, r *http.Request) {
	f, ok := s.dcsFind(w, r)
	if !ok {
		return
	}
	var opts dcs.CloseOptions
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&opts); err != nil {
			http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	cl, err := s.Ground.CloseFlight(r.Context(), f.Key, opts)
	if err != nil {
		dcsError(w, err)
		return
	}
	var sendErr string
	if s.OnClose != nil {
		if err := s.OnClose(r.Context(), cl); err != nil {
			// The flight is closed and the messages are built regardless;
			// what failed is transmission, and the agent should see that.
			sendErr = err.Error()
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"closure": cl, "send_error": sendErr})
}

// opsDesk is GET /api/ops: the operations desk's schedule as loaded, the
// slots flow management has given its flights, and how many movements it
// has filed. 404 on a node that is a gateway and nothing more.
func (s *Server) opsDesk(w http.ResponseWriter, r *http.Request) {
	if s.Ops == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "this node runs no operations desk"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"carrier": s.Ops.Carrier, "legs": s.Ops.Legs(), "slots": s.Ops.Slots(), "movements": s.Ops.Movements(),
		"movements_to": s.Ops.Config.MovementsTo, "via": s.Ops.Config.Via,
	})
}
