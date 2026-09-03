// Package api exposes the gateway over HTTP: a booking endpoint, read access to
// the message log and the PNR store, and a live event stream that the console
// uses to show traffic as it happens.
package api

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/adamf/jetway/pkg/dcs"
	"github.com/adamf/jetway/pkg/demo"
	"github.com/adamf/jetway/pkg/gateway"
	"github.com/adamf/jetway/pkg/metrics"
	"github.com/adamf/jetway/pkg/pnr"
	"github.com/adamf/jetway/pkg/store"
	"github.com/adamf/jetway/pkg/transport"
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

	// The last insights snapshot, served to repeat requests inside a short
	// TTL so open consoles share one computation.
	insightsMu   sync.Mutex
	insightsAt   time.Time
	insightsFor  int
	insightsBody []byte

	// Console serves the operations console. It is unauthenticated, so a
	// deployment reachable beyond a trusted network should turn it off.
	Console bool
	// Metrics serves /metrics.
	Metrics bool
	// Ready reports whether dependencies are usable. A nil Ready means always
	// ready, which is only right for the demo.
	Ready func(ctx context.Context) error
	// LinkPeers lists peers with a live link, for /api/status.
	LinkPeers func() []string

	// Extend, when set, is called with the mux before the built-in routes are
	// registered, letting an embedder add pages and endpoints to the same
	// listener. Paths under /api/ and the root are taken; pick a prefix.
	Extend func(mux *http.ServeMux)

	// Ground is this node's departure control, when it runs one. Nil hides
	// the departures endpoints and the console's Departures view.
	Ground *dcs.Station
	// OnAccept, OnOffload and OnClose let the embedder transmit what an
	// agent's action produced: the bag messages at acceptance, the bag pull
	// at offload, the whole message set at close. The API itself only
	// changes the flight; the gateway owns the wire.
	OnAccept  func(ctx context.Context, acc *dcs.Acceptance)
	OnOffload func(ctx context.Context, f *dcs.Flight, p *dcs.Passenger)
	OnClose   func(ctx context.Context, cl *dcs.Closure) error
}

// Handler returns the HTTP routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	// An embedder's routes first, so they cannot be shadowed by a later
	// addition here. The console grew up as jetwayd's own; a node embedded in
	// something larger -- a simulator with a map, an operator with their own
	// pages -- gets to extend the same mux on the same port rather than
	// running a second listener nothing else knows about.
	if s.Extend != nil {
		s.Extend(mux)
	}

	// Liveness never depends on anything external: a process that answers is
	// alive, and restarting it because a database blipped makes the outage
	// worse. Readiness is where dependencies belong.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok") //nolint:errcheck
	})
	mux.HandleFunc("GET /readyz", s.readyz)
	mux.HandleFunc("POST /api/admin/retire", s.retire)
	mux.HandleFunc("GET /api/admin/export", s.export)
	if s.Metrics {
		mux.HandleFunc("GET /metrics", s.metrics)
	}
	if s.Console {
		mux.HandleFunc("GET /{$}", s.console)
	}
	mux.HandleFunc("GET /api/status", s.status)
	mux.HandleFunc("GET /api/flights", s.flights)
	mux.HandleFunc("GET /api/journeys", s.journeys)
	mux.HandleFunc("POST /api/book", s.book)
	mux.HandleFunc("GET /api/pnrs", s.listPNRs)
	mux.HandleFunc("GET /api/pnr/{locator}", s.getPNR)
	mux.HandleFunc("GET /api/messages", s.listMessages)
	mux.HandleFunc("GET /api/message/{id}", s.getMessage)
	mux.HandleFunc("POST /api/message/{id}/replay", s.replay)
	mux.HandleFunc("POST /api/pnr/{locator}/ticket", s.issueTickets)
	mux.HandleFunc("POST /api/pnr/{locator}/cancel", s.cancelRecord)
	mux.HandleFunc("POST /api/pnr/{locator}/emd", s.issueEMD)
	mux.HandleFunc("POST /api/pnr/{locator}/split", s.splitRecord)
	mux.HandleFunc("GET /api/carrier/{designator}/pnrs", s.carrierPNRs)
	mux.HandleFunc("GET /api/carrier/{designator}/inventory", s.carrierInventory)
	mux.HandleFunc("GET /api/availability", s.availability)
	mux.HandleFunc("GET /api/insights", s.insights)
	mux.HandleFunc("GET /api/queues", s.listQueues)
	mux.HandleFunc("GET /api/queue/{name}", s.getQueue)
	mux.HandleFunc("POST /api/queue/item/{id}/work", s.workQueueItem)
	// NDC lives outside /api because it is a partner-facing endpoint carrying a
	// standard message, not part of this console's own interface.
	mux.HandleFunc("POST /ndc", s.ndcOrder)
	mux.HandleFunc("GET /api/stream", s.stream)
	s.dcsRoutes(mux)

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

func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	// The store is the dependency every answer needs: a node that cannot
	// write a record cannot acknowledge a message, and must not be sent one.
	if p, ok := s.Store.(store.Pinger); ok && s.Store != nil {
		if err := p.Ping(ctx); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprintf(w, "not ready: store: %v\n", err) //nolint:errcheck
			return
		}
	}
	if s.Ready != nil {
		if err := s.Ready(ctx); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprintf(w, "not ready: %v\n", err) //nolint:errcheck
			return
		}
	}
	w.WriteHeader(http.StatusOK)
	fmt.Fprintln(w, "ready") //nolint:errcheck
}

// retire is the operator's retention run: POST {"before": "2025-11-27T00:00:00Z"}
// retires every record whose day is before the cutoff, as partitions where
// the store keeps them. A store that cannot retire by day says so.
func (s *Server) retire(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Before time.Time `json:"before"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil || req.Before.IsZero() {
		http.Error(w, `{"error":"body wants {\"before\": RFC3339}"}`, http.StatusBadRequest)
		return
	}
	rt, ok := s.Store.(store.Retirer)
	if !ok {
		http.Error(w, `{"error":"this node's store does not retire by day"}`, http.StatusNotImplemented)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	got, err := rt.RetireBefore(ctx, req.Before)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusInternalServerError)
		return
	}
	s.Log.Info("records retired", "before", req.Before.Format(time.RFC3339), "partitions", got.Partitions, "records", got.Records, "queue_items", got.QueueItems)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(got) //nolint:errcheck
}

func (s *Server) metrics(w http.ResponseWriter, r *http.Request) {
	var b strings.Builder
	metrics.Default.Write(&b)
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	io.WriteString(w, b.String()) //nolint:errcheck
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
	if s.LinkPeers != nil {
		for _, p := range s.LinkPeers() {
			connected[p] = true
		}
	}
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
		AFTN      bool   `json:"aftn,omitempty"`
	}
	var peers []peerView
	for _, p := range s.Gateway.Peers() {
		v := peerView{Name: p.Name, Carrier: p.Carrier, Format: string(p.Format),
			TTY: p.TTYAddress, Connected: connected[p.Name], AFTN: p.AFTN}
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

// journeys lists the itineraries the demo schedule can sell, interline first.
func (s *Server) journeys(w http.ResponseWriter, r *http.Request) {
	all := demo.Journeys()
	if r.URL.Query().Get("interline") == "true" {
		all = demo.InterlineJourneys()
	}
	type view struct {
		demo.Journey
		Label string `json:"label"`
	}
	out := make([]view, 0, len(all))
	for _, j := range all {
		out = append(out, view{Journey: j, Label: j.Label()})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"journeys": out, "classes": demo.BookingClasses,
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

// availability reports what the gateway believes is sellable, with the age of
// each belief. Age is shown because an operator cannot judge availability
// without it: the same status means different things fresh and stale.
func (s *Server) availability(w http.ResponseWriter, r *http.Request) {
	if s.Gateway.Avail == nil {
		writeJSON(w, http.StatusOK, map[string]any{"entries": []any{}, "held": 0})
		return
	}
	now := time.Now().UTC()
	type row struct {
		Carrier    string  `json:"carrier"`
		FlightNum  string  `json:"flight_num"`
		Date       string  `json:"date"`
		Board      string  `json:"board"`
		Off        string  `json:"off"`
		Class      string  `json:"class"`
		Status     string  `json:"status"`
		Seats      int     `json:"seats"`
		SeatsKnown bool    `json:"seats_known"`
		Source     string  `json:"source"`
		AgeSeconds float64 `json:"age_seconds"`
		Fresh      bool    `json:"fresh"`
	}
	entries := s.Gateway.Avail.Snapshot()
	rows := make([]row, 0, len(entries))
	for _, e := range entries {
		_, _, fresh := s.Gateway.Avail.Lookup(e.Key)
		rows = append(rows, row{
			Carrier: e.Key.Carrier, FlightNum: e.Key.FlightNum, Date: e.Key.Date,
			Board: e.Key.Board, Off: e.Key.Off, Class: e.Key.Class,
			Status: string(e.Status), Seats: e.Seats, SeatsKnown: e.SeatsKnown,
			Source: string(e.Source), AgeSeconds: e.Age(now).Seconds(), Fresh: fresh,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"entries": rows, "held": len(rows),
		"trust_window_seconds": s.Gateway.Avail.StaleAfter.Seconds(),
	})
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

// listQueues returns the pending count for every queue, including the empty
// ones. A console that only showed non-empty queues would make "nothing is
// waiting" indistinguishable from "the queue does not exist".
func (s *Server) listQueues(w http.ResponseWriter, r *http.Request) {
	counts, err := s.Store.QueueCounts(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	type row struct {
		Name    string `json:"name"`
		Pending int    `json:"pending"`
	}
	out := make([]row, 0, len(store.Queues))
	total := 0
	for _, name := range store.Queues {
		out = append(out, row{Name: name, Pending: counts[name]})
		total += counts[name]
	}
	// A queue name we do not know about is still real work: show it rather
	// than let a bilateral or future placement vanish from the console.
	for name, n := range counts {
		if !knownQueue(name) {
			out = append(out, row{Name: name, Pending: n})
			total += n
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"queues": out, "pending": total})
}

func knownQueue(name string) bool {
	for _, q := range store.Queues {
		if q == name {
			return true
		}
	}
	return false
}

func (s *Server) getQueue(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "all" {
		name = ""
	}
	items, err := s.Store.ListQueue(r.Context(), store.QueueFilter{
		Queue:         name,
		PNRID:         r.URL.Query().Get("pnr"),
		IncludeWorked: r.URL.Query().Get("worked") == "1",
		Limit:         intParam(r, "limit", 100),
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"queue": name, "items": items})
}

func (s *Server) workQueueItem(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	by := r.URL.Query().Get("by")
	if by == "" {
		by = "console"
	}
	err := s.Store.WorkQueueItem(r.Context(), id, by, r.URL.Query().Get("note"))
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusNotFound, err)
		return
	case errors.Is(err, store.ErrConflict):
		// Someone else cleared it first. That is not a failure the operator
		// needs to retry, but they should know their click did nothing.
		writeErr(w, http.StatusConflict, err)
		return
	case err != nil:
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	counts, _ := s.Store.QueueCounts(r.Context())
	s.Bus.Publish(gateway.EvQueue, map[string]any{"worked": id, "by": by})
	writeJSON(w, http.StatusOK, map[string]any{"worked": id, "counts": counts})
}

// issueTickets issues documents against a record.
//
// The airline code is required and not derived: it is a three-digit numeric
// stock code, the two-letter designator is a different namespace, and there is
// no reliable mapping between them. Guessing one would put a booking on
// somebody else's stock.
func (s *Server) issueTickets(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("airline_code")
	if code == "" {
		writeErr(w, http.StatusBadRequest,
			errors.New("airline_code is required: the three-digit numeric stock code, not the two-letter designator"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	by := r.URL.Query().Get("by")
	if by == "" {
		by = "console"
	}
	rec, err := s.Gateway.IssueTickets(ctx, r.PathValue("locator"), gateway.IssueOptions{
		AirlineCode: code, IssuedBy: by,
	})
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, store.ErrNotFound) {
			status = http.StatusNotFound
		}
		writeErr(w, status, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"pnr": rec, "tickets": rec.Tickets})
}

// cancelRecord cancels a booking and tells the carriers holding it.
func (s *Server) cancelRecord(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	by := r.URL.Query().Get("by")
	if by == "" {
		by = "console"
	}
	res, err := s.Gateway.Cancel(ctx, r.PathValue("locator"), gateway.CancelOptions{
		By: by, Reason: r.URL.Query().Get("reason"),
	})
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, store.ErrNotFound) {
			status = http.StatusNotFound
		}
		writeErr(w, status, err)
		return
	}
	// Notified and unreachable are reported separately because they are
	// different facts: a carrier that was not told still holds the seats.
	writeJSON(w, http.StatusOK, map[string]any{
		"pnr": res.PNR, "notified": res.Notified, "unreachable": res.Unreachable,
	})
}

// issueEMD issues a miscellaneous document against a record.
func (s *Server) issueEMD(w http.ResponseWriter, r *http.Request) {
	var body struct {
		PaxRef      int    `json:"pax_ref"`
		Type        string `json:"type"`
		RFIC        string `json:"rfic"`
		AirlineCode string `json:"airline_code"`
		Coupons     []struct {
			RFISC              string `json:"rfisc"`
			SegmentRef         int    `json:"segment_ref"`
			Amount             string `json:"amount"`
			Currency           string `json:"currency"`
			ConsumedAtIssuance bool   `json:"consumed_at_issuance"`
		} `json:"coupons"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("malformed request: %w", err))
		return
	}
	req := gateway.EMDRequest{
		Locator: r.PathValue("locator"), PaxRef: body.PaxRef,
		Type:        pnr.DocumentType(strings.ToUpper(body.Type)),
		RFIC:        pnr.RFIC(strings.ToUpper(body.RFIC)),
		AirlineCode: body.AirlineCode, IssuedBy: queryOr(r, "by", "console"),
	}
	if req.PaxRef == 0 {
		req.PaxRef = 1
	}
	for _, c := range body.Coupons {
		req.Coupons = append(req.Coupons, gateway.EMDCoupon{
			RFISC: strings.ToUpper(c.RFISC), SegmentRef: c.SegmentRef,
			Amount: c.Amount, Currency: strings.ToUpper(c.Currency),
			ConsumedAtIssuance: c.ConsumedAtIssuance,
		})
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	rec, doc, err := s.Gateway.IssueEMD(ctx, req)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, store.ErrNotFound) {
			status = http.StatusNotFound
		}
		writeErr(w, status, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"pnr": rec, "document": doc})
}

func queryOr(r *http.Request, key, fallback string) string {
	if v := r.URL.Query().Get(key); v != "" {
		return v
	}
	return fallback
}

// splitRecord divides passengers onto their own record.
func (s *Server) splitRecord(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Passengers []int  `json:"passengers"`
		Reason     string `json:"reason"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("malformed request: %w", err))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	res, err := s.Gateway.Split(ctx, gateway.SplitRequest{
		Locator: r.PathValue("locator"), Passengers: body.Passengers,
		By: queryOr(r, "by", "console"), Reason: body.Reason,
	})
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, store.ErrNotFound) {
			status = http.StatusNotFound
		}
		writeErr(w, status, err)
		return
	}
	// Unadvised is reported because it is the state of the world, not an
	// error: the carriers still hold one record covering both halves.
	writeJSON(w, http.StatusOK, map[string]any{
		"parent": res.Parent, "child": res.Child,
		"advised": res.Advised, "unadvised": res.Unadvised,
	})
}

// export streams every record this node holds as newline-delimited JSON,
// one record a line, oldest first. It is the weekly archive: the live book
// purges at retirement, the regulator asks four years later, and a PITR
// window does not answer that.
func (s *Server) export(w http.ResponseWriter, r *http.Request) {
	ex, ok := s.Store.(store.Exporter)
	if !ok {
		http.Error(w, `{"error":"this node's store does not export"}`, http.StatusNotImplemented)
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	enc := json.NewEncoder(w)
	n := 0
	flusher, _ := w.(http.Flusher)
	err := ex.ExportPNRs(r.Context(), func(p *pnr.PNR) error {
		if err := enc.Encode(p); err != nil {
			return err
		}
		n++
		if n%1000 == 0 && flusher != nil {
			flusher.Flush()
		}
		return nil
	})
	if err != nil && r.Context().Err() == nil {
		s.Log.Warn("export ended early", "records", n, "err", err)
	}
	s.Log.Info("records exported", "records", n)
}
