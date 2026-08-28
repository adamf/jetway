// Package api exposes the gateway over HTTP: a booking endpoint, read access to
// the message log and the PNR store, and a live event stream that the console
// uses to show traffic as it happens.
package api

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/adamf/jetway/internal/demo"
	"github.com/adamf/jetway/internal/gateway"
	"github.com/adamf/jetway/internal/store"
	"github.com/adamf/jetway/internal/transport"
)

//go:embed console.html
var consoleFS embed.FS

// Server serves the API and the console.
type Server struct {
	Gateway *gateway.Gateway
	Store   store.Store
	Bus     *gateway.Bus
	Log     *slog.Logger
	// Links reports which peers are currently connected.
	Links *transport.Server
	// Fleet is the simulated carrier fleet, when one is running. Nil in a
	// deployment with real partners.
	Fleet *demo.RunningFleet
}

// Handler returns the HTTP routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /{$}", s.console)
	mux.HandleFunc("GET /api/status", s.status)
	mux.HandleFunc("GET /api/flights", s.flights)
	mux.HandleFunc("POST /api/book", s.book)
	mux.HandleFunc("GET /api/pnrs", s.listPNRs)
	mux.HandleFunc("GET /api/pnr/{locator}", s.getPNR)
	mux.HandleFunc("GET /api/messages", s.listMessages)
	mux.HandleFunc("GET /api/message/{id}", s.getMessage)
	mux.HandleFunc("POST /api/message/{id}/replay", s.replay)
	mux.HandleFunc("GET /api/carrier/{designator}/pnrs", s.carrierPNRs)
	mux.HandleFunc("GET /api/carrier/{designator}/inventory", s.carrierInventory)
	mux.HandleFunc("GET /api/stream", s.stream)

	return logRequests(s.Log, mux)
}

func logRequests(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		// The event stream is long lived; logging its duration on completion is
		// noise, not signal.
		if r.URL.Path != "/api/stream" {
			log.Debug("http", "method", r.Method, "path", r.URL.Path, "dur", time.Since(start))
		}
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// The response is already committed; nothing useful remains to be done.
		return
	}
}

func writeErr(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

func (s *Server) console(w http.ResponseWriter, r *http.Request) {
	b, err := consoleFS.ReadFile("console.html")
	if err != nil {
		http.Error(w, "console unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(b)
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	connected := map[string]bool{}
	if s.Links != nil {
		for _, p := range s.Links.Peers() {
			connected[p] = true
		}
	}
	type peerView struct {
		Name      string `json:"name"`
		Carrier   string `json:"carrier"`
		Format    string `json:"format"`
		TTY       string `json:"tty_address"`
		Connected bool   `json:"connected"`
		FullName  string `json:"full_name,omitempty"`
	}
	var peers []peerView
	for _, p := range s.Gateway.Peers() {
		v := peerView{Name: p.Name, Carrier: p.Carrier, Format: string(p.Format),
			TTY: p.TTYAddress, Connected: connected[p.Name]}
		if c, ok := demo.CarrierByDesignator(p.Carrier); ok {
			v.FullName = c.Name
		}
		peers = append(peers, v)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"identity": map[string]string{
			"designator": s.Gateway.Identity.Designator,
			"tty":        s.Gateway.Identity.TTYAddress,
			"name":       s.Gateway.Identity.Name,
		},
		"peers": peers,
		"now":   time.Now().UTC(),
	})
}

func (s *Server) flights(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"flights":      demo.Flights(),
		"classes":      demo.BookingClasses,
		"default_date": demo.DefaultDate().Format("02Jan"),
	})
}

func (s *Server) book(w http.ResponseWriter, r *http.Request) {
	var req gateway.BookingRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("malformed booking request: %w", err))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	res, err := s.Gateway.Book(ctx, &req)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) listPNRs(w http.ResponseWriter, r *http.Request) {
	recs, err := s.Store.ListPNRs(r.Context(), intParam(r, "limit", 50))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"pnrs": recs})
}

func (s *Server) getPNR(w http.ResponseWriter, r *http.Request) {
	loc := r.PathValue("locator")
	rec, err := s.Store.GetPNR(r.Context(), loc)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	events, _ := s.Store.Events(r.Context(), rec.ID)
	msgs, _ := s.Store.ListMessages(r.Context(), store.MessageFilter{PNRID: rec.ID, Limit: 200})
	writeJSON(w, http.StatusOK, map[string]any{
		"pnr": rec, "events": events, "messages": msgs,
	})
}

func (s *Server) listMessages(w http.ResponseWriter, r *http.Request) {
	msgs, err := s.Store.ListMessages(r.Context(), store.MessageFilter{
		Limit:   intParam(r, "limit", 100),
		Peer:    r.URL.Query().Get("peer"),
		SinceID: r.URL.Query().Get("since"),
		Status:  store.Status(r.URL.Query().Get("status")),
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": msgs})
}

func (s *Server) getMessage(w http.ResponseWriter, r *http.Request) {
	st, err := s.storeFor(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	m, err := st.GetMessage(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"message":   m,
		"raw":       string(m.Raw),
		"explained": Explain(m.Raw),
	})
}

// replay reprocesses a stored message through the pipeline.
//
// This is the payoff for capturing raw bytes before interpreting them: a parser
// fix can be applied to traffic that already failed, rather than requiring the
// partner to retransmit something they consider delivered.
func (s *Server) replay(w http.ResponseWriter, r *http.Request) {
	m, err := s.Store.GetMessage(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	if m.Direction != store.Inbound {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("only inbound messages can be replayed"))
		return
	}
	res, err := s.Gateway.Ingest(r.Context(), m.Peer, m.Raw)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"replayed_from": m.ID, "message_id": res.MessageID,
		"status": res.Status, "duplicate": res.Duplicate, "changes": res.Changes,
	})
}

func (s *Server) carrierNode(r *http.Request) *demo.Node {
	if s.Fleet == nil {
		return nil
	}
	return s.Fleet.Node(r.PathValue("designator"))
}

// storeFor resolves which node's message log a request refers to, so the
// console can inspect a message on either side of a link.
//
// An unknown node is an error rather than a silent fall back to our own log:
// answering from the wrong store turns a routing mistake into a confusing
// "message not found" at best, and the wrong message at worst.
func (s *Server) storeFor(r *http.Request) (store.Store, error) {
	node := r.URL.Query().Get("node")
	if node == "" || node == s.Gateway.Identity.Designator {
		return s.Store, nil
	}
	if s.Fleet != nil {
		if n := s.Fleet.Node(node); n != nil {
			return n.Store, nil
		}
	}
	return nil, fmt.Errorf("no node %q is running here", node)
}

func (s *Server) carrierPNRs(w http.ResponseWriter, r *http.Request) {
	n := s.carrierNode(r)
	if n == nil {
		writeErr(w, http.StatusNotFound, fmt.Errorf("no simulated carrier %q is running", r.PathValue("designator")))
		return
	}
	recs, err := n.Store.ListPNRs(r.Context(), intParam(r, "limit", 50))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"pnrs": recs, "carrier": n.Carrier})
}

func (s *Server) carrierInventory(w http.ResponseWriter, r *http.Request) {
	n := s.carrierNode(r)
	if n == nil {
		writeErr(w, http.StatusNotFound, fmt.Errorf("no simulated carrier %q is running", r.PathValue("designator")))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"inventory": n.Inventory.Snapshot()})
}

// stream is the live event feed the console renders.
func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ch, cancel := s.Bus.Subscribe()
	defer cancel()

	// Replay the backlog so a console opened after traffic started still shows
	// what happened.
	for _, ev := range s.Bus.History() {
		if !writeEvent(w, ev) {
			return
		}
	}
	flusher.Flush()

	// A periodic comment keeps intermediaries from closing an idle stream.
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			if !writeEvent(w, ev) {
				return
			}
			flusher.Flush()
		case <-ticker.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func writeEvent(w http.ResponseWriter, ev gateway.Event) bool {
	b, err := json.Marshal(ev)
	if err != nil {
		return true // skip an unserialisable event rather than kill the stream
	}
	_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, b)
	return err == nil
}

func intParam(r *http.Request, name string, def int) int {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	return n
}
