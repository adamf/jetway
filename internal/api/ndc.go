package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/adamf/jetway/internal/gateway"
	"github.com/adamf/jetway/pkg/ndc"
	"github.com/adamf/jetway/pkg/pnr"
)

// ndcHandler serves the NDC order endpoint.
//
// Orders are turned into ordinary bookings and run through the same pipeline as
// everything else: availability, free sale, carrier messaging, queues. A
// parallel path would be a second set of rules to keep in step with the first.
func (s *Server) ndcOrder(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		writeNDCError(w, http.StatusBadRequest, "400", "Malformed request", err.Error())
		return
	}

	// Before anything else, and before the bytes reach any log: the pipeline
	// makes raw input durable, and a card number must not be made durable in a
	// store with no encryption at rest.
	if ndc.CarriesCardData(raw) {
		writeNDCError(w, http.StatusUnprocessableEntity, "422", "Payment not accepted",
			ndc.ErrCardData.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	switch ndc.MessageType(raw) {
	case ndc.MsgOrderCreateRQ:
		s.ndcCreate(ctx, w, raw)
	case ndc.MsgOrderRetrieveRQ:
		s.ndcRetrieve(ctx, w, raw)
	case ndc.MsgOrderCancelRQ:
		s.ndcCancel(ctx, w, raw)
	default:
		writeNDCError(w, http.StatusBadRequest, "400", "Unsupported message",
			"this endpoint serves OrderCreateRQ, OrderRetrieveRQ and OrderCancelRQ")
	}
}

func (s *Server) ndcCreate(ctx context.Context, w http.ResponseWriter, raw []byte) {
	m, err := ndc.ParseOrderCreate(raw)
	if err != nil {
		writeNDCError(w, http.StatusBadRequest, "400", "Malformed OrderCreateRQ", err.Error())
		return
	}
	req, err := bookingFromOrder(m)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, ndc.ErrOfferOnly) {
			// Not the requester's mistake: this node holds no offers because it
			// prices nothing, and saying so is more useful than a bare 400.
			status = http.StatusNotImplemented
		}
		writeNDCError(w, status, "400", "Order cannot be created", err.Error())
		return
	}
	res, err := s.Gateway.Book(ctx, req)
	if err != nil {
		writeNDCError(w, http.StatusBadRequest, "400", "Order refused", err.Error())
		return
	}
	writeOrderView(w, http.StatusOK, res.PNR, s.Gateway.Identity.Designator)
}

func (s *Server) ndcRetrieve(ctx context.Context, w http.ResponseWriter, raw []byte) {
	m, err := ndc.ParseOrderRetrieve(raw)
	if err != nil {
		writeNDCError(w, http.StatusBadRequest, "400", "Malformed OrderRetrieveRQ", err.Error())
		return
	}
	rec, err := s.Store.GetPNR(ctx, m.OrderID())
	if err != nil {
		writeNDCError(w, http.StatusNotFound, "404", "Unknown order",
			"no record matches order "+m.OrderID())
		return
	}
	writeOrderView(w, http.StatusOK, rec, s.Gateway.Identity.Designator)
}

func (s *Server) ndcCancel(ctx context.Context, w http.ResponseWriter, raw []byte) {
	m, err := ndc.ParseOrderCancel(raw)
	if err != nil {
		writeNDCError(w, http.StatusBadRequest, "400", "Malformed OrderCancelRQ", err.Error())
		return
	}
	rec, err := s.Store.GetPNR(ctx, m.OrderID())
	if err != nil {
		writeNDCError(w, http.StatusNotFound, "404", "Unknown order",
			"no record matches order "+m.OrderID())
		return
	}
	// Cancelling the record is the local half. Telling the carriers is a
	// message this build does not send yet, and claiming otherwise would leave
	// seats held that the requester believes are released.
	if rec.Status == pnr.StatusCancelled {
		writeOrderView(w, http.StatusOK, rec, s.Gateway.Identity.Designator)
		return
	}
	writeNDCError(w, http.StatusNotImplemented, "501", "Cancellation not available",
		"this node can cancel its own record but does not yet send the cancellation to the carriers, "+
			"so the order is left as it is rather than diverging from what they hold")
}

// bookingFromOrder maps an order onto the booking request the gateway takes.
func bookingFromOrder(m *ndc.OrderCreateRQ) (*gateway.BookingRequest, error) {
	// ToRecord carries the validation and the refusal for offer-only orders,
	// so the mapping is done once there rather than twice.
	rec, err := m.ToRecord(time.Now().UTC())
	if err != nil {
		return nil, err
	}
	req := &gateway.BookingRequest{
		ReceivedFrom: m.Party.AgencyName,
		Agent:        m.Party.ID(),
	}
	for _, p := range rec.Passengers {
		req.Passengers = append(req.Passengers, gateway.BookingPassenger{
			Surname: p.Surname, Given: p.Given, Title: p.Title,
		})
	}
	for _, s := range rec.Segments {
		req.Segments = append(req.Segments, gateway.BookingSegment{
			Carrier: s.Carrier, FlightNum: s.FlightNum, Class: s.Class,
			Date: s.WireDate, Board: s.Board, Off: s.Off, Seats: s.Seats,
			DepartTime: s.DepartTime, ArriveTime: s.ArriveTime,
		})
	}
	for _, c := range rec.Contacts {
		if req.Contact == "" {
			req.Contact = c.Text
		}
	}
	if len(req.Segments) == 0 {
		return nil, fmt.Errorf("order carries no flights")
	}
	return req, nil
}

func writeOrderView(w http.ResponseWriter, code int, rec *pnr.PNR, owner string) {
	body, err := ndc.BuildOrderView(rec, owner)
	if err != nil {
		writeNDCError(w, http.StatusInternalServerError, "500", "Cannot render order", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(code)
	_, _ = w.Write(body)
}

func writeNDCError(w http.ResponseWriter, code int, errType, shortText, detail string) {
	body, err := ndc.BuildError(errType, shortText, detail)
	if err != nil {
		http.Error(w, "cannot render error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(code)
	_, _ = w.Write(body)
}
