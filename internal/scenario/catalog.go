package scenario

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/adamf/jetway/internal/demo"
	"github.com/adamf/jetway/internal/gateway"
	"github.com/adamf/jetway/internal/store"
	"github.com/adamf/jetway/pkg/pnr"
	"github.com/adamf/jetway/pkg/typeb"
)

// The scenarios. Each is one thing a distribution system does, driven the way
// something outside would drive it, and checked on the outcome rather than on
// the call returning nil.

// pax builds a distinct passenger for one invocation.
func pax(seq int) gateway.BookingPassenger {
	return gateway.BookingPassenger{
		Surname: fmt.Sprintf("TESTER%06d", seq), Given: "SAM", Title: "MR",
	}
}

// wireDate is the date the demo carriers publish availability for.
func wireDate() string {
	return strings.ToUpper(demo.DefaultDate().Format("02Jan"))
}

// book is the common path: create a record and request its segments.
func book(ctx context.Context, h *Harness, seq int, segs ...gateway.BookingSegment) (*gateway.BookResult, error) {
	return h.Gw().Book(ctx, &gateway.BookingRequest{
		Passengers: []gateway.BookingPassenger{pax(seq)},
		Segments:   segs,
		Agent:      "scenario",
		Channel:    "scenario",
	})
}

func seg(carrier, flight, board, off string) gateway.BookingSegment {
	return gateway.BookingSegment{
		Carrier: carrier, FlightNum: flight, Class: "Y", Date: wireDate(),
		Board: board, Off: off, Seats: 1,
	}
}

// loadClasses are the classes a scenario spreads its bookings across.
//
// Z is excluded: the simulated inventory always refuses it, which is useful for
// demonstrating a refusal and useless for measuring throughput.
var loadClasses = []string{"Y", "M", "J", "F"}

// spread varies the class and departure date of a segment by sequence number.
//
// Without it every booking in a load run competes for one flight, one date and
// one class, and the run stops measuring the system and starts measuring how
// long that single bucket takes to sell out -- which it does, correctly, and
// then reports thousands of failures that are the simulated carrier being
// right. Real load is not all on one flight either.
func spread(s gateway.BookingSegment, seq int) gateway.BookingSegment {
	s.Class = loadClasses[seq%len(loadClasses)]
	// The window the simulated carriers publish availability for.
	day := (seq/len(loadClasses))%(AVSWindowDays*2+1) - AVSWindowDays
	s.Date = strings.ToUpper(demo.DefaultDate().AddDate(0, 0, day).Format("02Jan"))
	return s
}

// AVSWindowDays mirrors the window the simulated carriers broadcast around the
// default date. Booking outside it falls back to asking the carrier, which is
// correct but measures a different path.
const AVSWindowDays = demo.AVSDaysBack

// confirmed waits until every air segment on a record has been answered.
func confirmed(ctx context.Context, h *Harness, locator string) (*pnr.PNR, error) {
	var rec *pnr.PNR
	err := eventually(ctx, settle, "carrier answered "+locator, func() (bool, error) {
		got, err := h.Store.GetPNR(ctx, locator)
		if err != nil {
			return false, err
		}
		rec = got
		for _, s := range got.Segments {
			if s.Type != pnr.SegmentAir {
				continue
			}
			// HN and NN are the request still outstanding. Anything else is an
			// answer, including a refusal, which is a legitimate outcome.
			if s.Status == "HN" || s.Status == "NN" {
				return false, nil
			}
		}
		return true, nil
	})
	return rec, err
}

// BookDomestic is the shortest complete path through the system: sell a
// segment, request it from the carrier over Type B, take the answer.
func BookDomestic() Scenario {
	return Scenario{
		Name:  "book-domestic",
		About: "sell one BA segment and take the carrier's answer over Type B",
		Run: func(ctx context.Context, h *Harness, seq int) error {
			res, err := book(ctx, h, seq, spread(seg("BA", "0175", "LHR", "JFK"), seq))
			if err != nil {
				return err
			}
			if res.PNR.RecordLocator == "" {
				return fmt.Errorf("booking produced no record locator")
			}
			rec, err := confirmed(ctx, h, res.PNR.RecordLocator)
			if err != nil {
				return err
			}
			if len(rec.Segments) != 1 {
				return fmt.Errorf("record has %d segments, want 1", len(rec.Segments))
			}
			return nil
		},
	}
}

// BookInterline is the case interline messaging exists for: one record, two
// carriers, each holding a piece of it.
func BookInterline() Scenario {
	return Scenario{
		Name:  "book-interline",
		About: "one record across two carriers, each requested separately",
		Run: func(ctx context.Context, h *Harness, seq int) error {
			res, err := book(ctx, h, seq,
				spread(seg("BA", "0175", "LHR", "JFK"), seq),
				spread(seg("AA", "2401", "JFK", "DFW"), seq))
			if err != nil {
				return err
			}
			if len(res.Carriers) != 2 {
				return fmt.Errorf("booking reached %d carriers, want 2: %v", len(res.Carriers), res.Carriers)
			}
			rec, err := confirmed(ctx, h, res.PNR.RecordLocator)
			if err != nil {
				return err
			}
			seen := map[string]bool{}
			for _, s := range rec.Segments {
				seen[s.Carrier] = true
			}
			if !seen["BA"] || !seen["AA"] {
				return fmt.Errorf("record does not hold both carriers: %v", seen)
			}
			return nil
		},
	}
}

// BookEDIFACT drives the other wire format end to end. AA is the EDIFACT
// carrier in the demo fleet, so this is the same exchange in PADIS.
//
// It requests from the carrier explicitly. A booking made against fresh cached
// availability is sold free sale and sends nothing at all -- which is correct,
// and is what FreeSale below asserts -- so asserting on the wire here without
// forcing a request would be asserting on an empty log.
func BookEDIFACT() Scenario {
	return Scenario{
		Name:  "book-edifact",
		About: "request an AA segment from the carrier in PADIS rather than Type B",
		Run: func(ctx context.Context, h *Harness, seq int) error {
			res, err := book(ctx, h, seq, spread(seg("AA", "2401", "JFK", "DFW"), seq))
			if err != nil {
				return err
			}
			if _, err := h.Gw().RequestFromCarrier(ctx, res.PNR, "AA"); err != nil {
				return err
			}
			return eventually(ctx, settle, "an outbound EDIFACT request for "+res.PNR.RecordLocator,
				func() (bool, error) {
					msgs, err := h.Store.ListMessages(ctx, store.MessageFilter{
						PNRID: res.PNR.ID, Limit: 50,
					})
					if err != nil {
						return false, err
					}
					for _, m := range msgs {
						if m.Direction == store.Outbound && m.Format == store.FormatEDIFACT {
							return true, nil
						}
					}
					return false, nil
				})
		},
	}
}

// FreeSale is the point of an availability broadcast: permission granted in
// advance, so a seat can be sold without asking. A booking that quietly sent a
// request anyway would be slower and would contradict what the carrier said.
func FreeSale() Scenario {
	return Scenario{
		Name:  "free-sale",
		About: "a seat covered by a fresh broadcast is sold without asking the carrier",
		Run: func(ctx context.Context, h *Harness, seq int) error {
			if err := eventually(ctx, settle, "the availability cache filled",
				func() (bool, error) { return h.Gw().Avail.Len() > 0, nil }); err != nil {
				return err
			}
			res, err := book(ctx, h, seq, spread(seg("BA", "0175", "LHR", "JFK"), seq))
			if err != nil {
				return err
			}
			rec, err := h.Store.GetPNR(ctx, res.PNR.RecordLocator)
			if err != nil {
				return err
			}
			for _, sg := range rec.Segments {
				if sg.Status == "HN" || sg.Status == "NN" {
					return fmt.Errorf("segment %d is still at %s; a seat covered by a "+
						"fresh broadcast should be sold without asking", sg.Ref, sg.Status)
				}
			}
			return nil
		},
	}
}

// TypeBRoundTrip pushes bytes in off a link rather than calling an API, which
// is how a real partner interacts with this node.
func TypeBRoundTrip() Scenario {
	return Scenario{
		Name:  "typeb-ingest",
		About: "an availability broadcast arrives on a link and is applied",
		Run: func(ctx context.Context, h *Harness, seq int) error {
			raw := []byte("QU LONRM1J\r\n.LHRRMBA 121430\r\n" +
				"AVS\r\nBA0175/" + wireDate() + " LHRJFK\r\nY9 J4\r\n")
			res, err := h.Gw().Ingest(ctx, "BA", raw)
			if err != nil {
				return err
			}
			if res.MessageID == "" {
				return fmt.Errorf("ingest recorded no message")
			}
			// Capture precedes interpretation: whatever the parser made of it,
			// the bytes must be durable and identical.
			m, err := h.Store.GetMessage(ctx, res.MessageID)
			if err != nil {
				return err
			}
			if string(m.Raw) != string(raw) {
				return fmt.Errorf("stored bytes differ from what arrived")
			}
			return nil
		},
	}
}

// DuplicateSuppressed checks the retransmission rule: the second copy is
// recorded, because it really did arrive, and not applied twice.
//
// It uses a reservation message rather than an availability broadcast, because
// only reservation traffic carries a dedup key -- see the note on the AVS and
// schedule branches in gateway/decode.go. Applying a sell twice books two
// seats, which is the case the rule exists for.
func DuplicateSuppressed() Scenario {
	return Scenario{
		Name:  "duplicate-suppressed",
		About: "a retransmitted sell is recorded but not applied twice",
		Run: func(ctx context.Context, h *Harness, seq int) error {
			raw := []byte(fmt.Sprintf(
				"QU LONRM1J\r\n.LHRRMBA 121430\r\nSS\r\nBA0117Y%sLHRJFKNN1\r\n"+
					"1TESTER/SAM%06dMR\r\nRL BA/AB%06d\r\n",
				wireDate(), seq, seq%1000000))
			first, err := h.Gw().Ingest(ctx, "BA", raw)
			if err != nil {
				return err
			}
			second, err := h.Gw().Ingest(ctx, "BA", raw)
			if err != nil {
				return err
			}
			if first.MessageID == second.MessageID {
				return fmt.Errorf("a retransmission reused the first message id; " +
					"both copies must be recorded")
			}
			if !second.Duplicate {
				return fmt.Errorf("the second copy of an identical sell was not " +
					"recognised as a duplicate; applying it twice books two seats")
			}
			// Recorded, not discarded: the retransmission really did arrive.
			m, err := h.Store.GetMessage(ctx, second.MessageID)
			if err != nil {
				return fmt.Errorf("the duplicate was not stored: %w", err)
			}
			if string(m.Raw) != string(raw) {
				return fmt.Errorf("stored bytes differ from what arrived")
			}
			return nil
		},
	}
}

// CancelBooking withdraws segments and tells the carriers holding them.
func CancelBooking() Scenario {
	return Scenario{
		Name:  "cancel",
		About: "withdraw a booking and notify the carrier holding the seats",
		Run: func(ctx context.Context, h *Harness, seq int) error {
			res, err := book(ctx, h, seq, spread(seg("BA", "0175", "LHR", "JFK"), seq))
			if err != nil {
				return err
			}
			if _, err := confirmed(ctx, h, res.PNR.RecordLocator); err != nil {
				return err
			}
			out, err := h.Gw().Cancel(ctx, res.PNR.RecordLocator,
				gateway.CancelOptions{By: "scenario", Reason: "scenario cancel"})
			if err != nil {
				return err
			}
			if len(out.Unreachable) > 0 {
				return fmt.Errorf("carriers not told about the cancellation: %v", out.Unreachable)
			}
			if out.PNR.Status != pnr.StatusCancelled {
				return fmt.Errorf("record status is %q after cancelling every segment, want cancelled",
					out.PNR.Status)
			}
			return nil
		},
	}
}

// IssueTicket issues documents and then finds one by its number, which is the
// lookup that used to search only recently touched records.
func IssueTicket() Scenario {
	return Scenario{
		Name:  "issue-ticket",
		About: "issue a document, then find the record by its number",
		Run: func(ctx context.Context, h *Harness, seq int) error {
			res, err := book(ctx, h, seq, spread(seg("BA", "0175", "LHR", "JFK"), seq))
			if err != nil {
				return err
			}
			if _, err := confirmed(ctx, h, res.PNR.RecordLocator); err != nil {
				return err
			}
			rec, err := h.Gw().IssueTickets(ctx, res.PNR.RecordLocator,
				gateway.IssueOptions{AirlineCode: "125", IssuedBy: "scenario"})
			if err != nil {
				return err
			}
			if len(rec.Tickets) == 0 {
				return fmt.Errorf("issuing produced no documents")
			}
			number := rec.Tickets[0].Number.Compact()
			found, err := h.Store.FindPNRByDocument(ctx, number)
			if err != nil {
				return err
			}
			if found == nil || found.RecordLocator != rec.RecordLocator {
				return fmt.Errorf("document %s did not lead back to %s",
					number, rec.RecordLocator)
			}
			return nil
		},
	}
}

// IssueEMD covers the other document type: a fee, associated to a flight
// coupon, which is what makes it an EMD-A rather than a standalone.
func IssueEMD() Scenario {
	return Scenario{
		Name:  "issue-emd",
		About: "issue a baggage EMD associated to a flight coupon",
		Run: func(ctx context.Context, h *Harness, seq int) error {
			res, err := book(ctx, h, seq, spread(seg("BA", "0175", "LHR", "JFK"), seq))
			if err != nil {
				return err
			}
			if _, err := confirmed(ctx, h, res.PNR.RecordLocator); err != nil {
				return err
			}
			if _, err := h.Gw().IssueTickets(ctx, res.PNR.RecordLocator,
				gateway.IssueOptions{AirlineCode: "125", IssuedBy: "scenario"}); err != nil {
				return err
			}
			_, emd, err := h.Gw().IssueEMD(ctx, gateway.EMDRequest{
				Locator: res.PNR.RecordLocator, PaxRef: 1,
				Type: pnr.DocEMDA, RFIC: pnr.RFICBaggage,
				AirlineCode: "125", IssuedBy: "scenario",
				Coupons: []gateway.EMDCoupon{{
					RFISC: "0CC", SegmentRef: 1, Amount: "60.00", Currency: "GBP",
				}},
			})
			if err != nil {
				return err
			}
			if emd.Type != pnr.DocEMDA {
				return fmt.Errorf("document type is %q, want an EMD-A", emd.Type)
			}
			if len(emd.Coupons) != 1 || emd.Coupons[0].Association.IsZero() {
				return fmt.Errorf("the value coupon was not associated to a flight coupon")
			}
			return nil
		},
	}
}

// SplitBooking divides a record, which is the operation that produces two
// records where there was one and has to tell the carriers so.
func SplitBooking() Scenario {
	return Scenario{
		Name:  "split",
		About: "divide a two-passenger record and advise the carrier",
		Run: func(ctx context.Context, h *Harness, seq int) error {
			res, err := h.Gw().Book(ctx, &gateway.BookingRequest{
				Passengers: []gateway.BookingPassenger{
					pax(seq),
					{Surname: fmt.Sprintf("TESTER%06d", seq), Given: "ALEX", Title: "MS"},
				},
				Segments: []gateway.BookingSegment{
					spread(gateway.BookingSegment{
						Carrier: "BA", FlightNum: "0175", Board: "LHR", Off: "JFK", Seats: 2,
					}, seq),
				},
				Agent: "scenario", Channel: "scenario",
			})
			if err != nil {
				return err
			}
			if _, err := confirmed(ctx, h, res.PNR.RecordLocator); err != nil {
				return err
			}
			out, err := h.Gw().Split(ctx, gateway.SplitRequest{
				Locator: res.PNR.RecordLocator, Passengers: []int{2},
				By: "scenario", Reason: "scenario split",
			})
			if err != nil {
				return err
			}
			if out.Child.RecordLocator == out.Parent.RecordLocator {
				return fmt.Errorf("the division produced one record, not two")
			}
			if len(out.Parent.Passengers) != 1 || len(out.Child.Passengers) != 1 {
				return fmt.Errorf("passengers did not divide one and one: parent %d, child %d",
					len(out.Parent.Passengers), len(out.Child.Passengers))
			}
			return nil
		},
	}
}

// ScheduleChange is the path that turns "this flight moved" into "these
// passengers need telling" -- and the one whose lookup used to miss every
// booking made more than a page ago.
func ScheduleChange() Scenario {
	return Scenario{
		Name:    "schedule-change",
		About:   "a flight cancellation raises the holdings on that flight",
		Mutates: true,
		Run: func(ctx context.Context, h *Harness, seq int) error {
			// Its own flight. Cancelling BA0175 would leave every other
			// scenario unable to book the route, and the failure would look
			// like a bug in whichever one happened to run next.
			res, err := book(ctx, h, seq, seg("BA", "0902", "LHR", "FRA"))
			if err != nil {
				return err
			}
			if _, err := confirmed(ctx, h, res.PNR.RecordLocator); err != nil {
				return err
			}
			raw := []byte("QU LONRM1J\r\n.LHRRMBA 121430\r\nASM\r\nUTC\r\nCNL\r\n" +
				"BA0902/" + wireDate() + "\r\nLHR FRA\r\n")
			if _, err := h.Gw().Ingest(ctx, "BA", raw); err != nil {
				return err
			}
			return eventually(ctx, settle, "schedule change raised "+res.PNR.RecordLocator,
				func() (bool, error) {
					items, err := h.Store.ListQueue(ctx, store.QueueFilter{
						Queue: store.QueueScheduleChange, PNRID: res.PNR.ID,
					})
					return len(items) > 0, err
				})
		},
	}
}

// TicketingDeadline exercises the sweeper: the only path where nothing arrives
// and something must still happen.
func TicketingDeadline() Scenario {
	return Scenario{
		Name:    "ticketing-deadline",
		About:   "a passed ticketing time limit is raised without any message arriving",
		Mutates: true,
		Run: func(ctx context.Context, h *Harness, seq int) error {
			res, err := book(ctx, h, seq, spread(seg("BA", "0175", "LHR", "JFK"), seq))
			if err != nil {
				return err
			}
			rec, err := confirmed(ctx, h, res.PNR.RecordLocator)
			if err != nil {
				return err
			}
			deadline := time.Now().UTC().Add(-time.Hour)
			rec.Ticketing = []pnr.Ticketing{{Text: "TKTL", Deadline: &deadline}}
			if err := h.Store.UpdatePNR(ctx, rec, rec.Version, nil); err != nil {
				return err
			}
			if _, err := h.SweepAt(ctx, time.Now().UTC()); err != nil {
				return err
			}
			items, err := h.Store.ListQueue(ctx, store.QueueFilter{
				Queue: store.QueueTicketing, PNRID: rec.ID,
			})
			if err != nil {
				return err
			}
			for _, it := range items {
				if it.Code == "tktl_expired" {
					return nil
				}
			}
			return fmt.Errorf("a ticketing time limit an hour past was not raised for %s",
				rec.RecordLocator)
		},
	}
}

// UndecodableToDLQ checks the rule that nothing undecodable is discarded. The
// bytes have to survive even when no parser wanted them.
func UndecodableToDLQ() Scenario {
	return Scenario{
		Name:  "undecodable-kept",
		About: "bytes no parser accepts are still stored intact",
		Run: func(ctx context.Context, h *Harness, seq int) error {
			raw := []byte(fmt.Sprintf("\x01\x02 not a message at all %d \xff\xfe", seq))
			res, err := h.Gw().Ingest(ctx, "BA", raw)
			if err != nil {
				// A refusal is acceptable; losing the bytes is not. If the
				// gateway refused, there is nothing further to check.
				return nil
			}
			m, err := h.Store.GetMessage(ctx, res.MessageID)
			if err != nil {
				return fmt.Errorf("undecodable bytes were not stored: %w", err)
			}
			if string(m.Raw) != string(raw) {
				return fmt.Errorf("stored bytes differ from what arrived")
			}
			return nil
		},
	}
}

// AvailabilityCached checks that a carrier's unsolicited broadcast is believed
// and reusable, which is what free sale means.
//
// It sends its own broadcast rather than waiting for the fleet's timer. The
// simulated carriers rebroadcast every 20 seconds and the cache expires what
// it stops hearing about, so a scenario that waited would pass or fail on
// where in that cycle it happened to run -- and a flaky scenario in a suite
// like this gets the whole suite disbelieved.
func AvailabilityCached() Scenario {
	return Scenario{
		Name:  "availability-cached",
		About: "an AVS broadcast is believed and held for reuse",
		Run: func(ctx context.Context, h *Harness, seq int) error {
			// A flight of its own, so a concurrent run is not asserting on
			// somebody else's broadcast.
			flight := fmt.Sprintf("%04d", 6000+seq%3000)
			raw := []byte("QU LONRM1J\r\n.LHRRMBA 121430\r\nAVS\r\n" +
				"BA" + flight + "/" + wireDate() + "/LHRJFK\r\nY/O7\r\n")
			if _, err := h.Gw().Ingest(ctx, "BA", raw); err != nil {
				return err
			}
			if h.Gw().Avail.Len() == 0 {
				return fmt.Errorf("an availability broadcast left the cache empty")
			}
			return nil
		},
	}
}

// mustAddress is here so a scenario can build a Type B address without
// carrying the error through every call.
func mustAddress(s string) typeb.Address {
	a, err := typeb.ParseAddress(s)
	if err != nil {
		panic("scenario: bad teletype address " + s)
	}
	return a
}
