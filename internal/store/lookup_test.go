package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/adamf/jetway/pkg/pnr"
)

// The bug these lookups replaced: three call sites searched with ListPNRs,
// which is "ORDER BY updated_at DESC LIMIT n". A booking that had not been
// touched recently fell off the end of the list and the caller reported, in
// good faith and in writing to a partner, that no record held the document.
//
// So the test buries the record under more traffic than any such limit, and
// asserts it is still found. A lookup that searches a recent prefix fails here.
const buriedUnder = 250

// seedBackground fills the store with records the lookups must not stop at.
func seedBackground(t *testing.T, ctx context.Context, s Store) {
	t.Helper()
	for i := 0; i < buriedUnder; i++ {
		p := samplePNR(fmt.Sprintf("BG%04d", i))
		// Touched after the record under test, so it sorts ahead of it.
		p.UpdatedAt = time.Now().UTC().Add(time.Duration(i) * time.Second)
		if err := s.CreatePNR(ctx, p, nil); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}
}

func TestFindPNRByDocumentSearchesEveryRecord(t *testing.T) {
	eachBackend(t, func(t *testing.T, s Store) {
		ctx := context.Background()

		old := samplePNR("OLDTKT")
		old.Tickets = []pnr.Ticket{{
			Number:   pnr.TicketNumber{AirlineCode: "125", Serial: "1234567890"},
			PaxRef:   1,
			IssuedAt: time.Now().UTC().AddDate(0, -3, 0),
			Coupons:  []pnr.Coupon{{Number: 1, SegmentRef: 1, Status: pnr.CouponOpen}},
		}}
		old.UpdatedAt = time.Now().UTC().AddDate(0, -3, 0)
		if err := s.CreatePNR(ctx, old, nil); err != nil {
			t.Fatalf("CreatePNR: %v", err)
		}
		seedBackground(t, ctx, s)

		got, err := s.FindPNRByDocument(ctx, "1251234567890")
		if err != nil {
			t.Fatalf("FindPNRByDocument: %v", err)
		}
		if got == nil {
			t.Fatalf("a document issued three months ago and buried under %d newer "+
				"records was not found; this is the false 'no record holds this "+
				"document' the lookup exists to prevent", buriedUnder)
		}
		if got.RecordLocator != "OLDTKT" {
			t.Errorf("found %q, want OLDTKT", got.RecordLocator)
		}

		// A document nobody issued must be absent, not merely unreached, so a
		// caller can tell "not ours" from "we did not look hard enough".
		missing, err := s.FindPNRByDocument(ctx, "1259999999999")
		if err != nil {
			t.Fatalf("FindPNRByDocument (absent): %v", err)
		}
		if missing != nil {
			t.Errorf("found %q for a document that was never issued", missing.RecordLocator)
		}
	})
}

func TestFindPNRByExternalLocatorSearchesEveryRecord(t *testing.T) {
	eachBackend(t, func(t *testing.T, s Store) {
		ctx := context.Background()

		old := samplePNR("OLDLOC")
		old.Locators = []pnr.ExternalLocator{{Owner: "BA", Value: "XY7QP2"}}
		old.UpdatedAt = time.Now().UTC().AddDate(0, -3, 0)
		if err := s.CreatePNR(ctx, old, nil); err != nil {
			t.Fatalf("CreatePNR: %v", err)
		}
		seedBackground(t, ctx, s)

		for _, owner := range []string{"BA", ""} {
			got, err := s.FindPNRByExternalLocator(ctx, owner, "XY7QP2")
			if err != nil {
				t.Fatalf("FindPNRByExternalLocator(%q): %v", owner, err)
			}
			if got == nil || got.RecordLocator != "OLDLOC" {
				t.Fatalf("owner %q: got %v, want OLDLOC", owner, got)
			}
		}

		// The owner narrows rather than decorates: the same value from a
		// different system is a different booking.
		got, err := s.FindPNRByExternalLocator(ctx, "AA", "XY7QP2")
		if err != nil {
			t.Fatalf("FindPNRByExternalLocator: %v", err)
		}
		if got != nil {
			t.Errorf("BA's locator was returned for AA: %q", got.RecordLocator)
		}
	})
}

func TestFindPNRsByFlightSearchesEveryRecord(t *testing.T) {
	eachBackend(t, func(t *testing.T, s Store) {
		ctx := context.Background()

		// Written the way a carrier would: same flight, two spellings.
		for i, num := range []string{"0175", "175"} {
			p := samplePNR(fmt.Sprintf("OLDF%02d", i))
			p.Segments[0].FlightNum = num
			p.UpdatedAt = time.Now().UTC().AddDate(0, -3, 0)
			if err := s.CreatePNR(ctx, p, nil); err != nil {
				t.Fatalf("CreatePNR: %v", err)
			}
		}
		// A cancelled holding on the same flight. Containment will match it,
		// which is why the backends decide with SegmentOnFlight rather than
		// with the index.
		dead := samplePNR("DEADFL")
		dead.Segments[0].Status = "XX"
		dead.UpdatedAt = time.Now().UTC().AddDate(0, -3, 0)
		if err := s.CreatePNR(ctx, dead, nil); err != nil {
			t.Fatalf("CreatePNR: %v", err)
		}
		seedBackground(t, ctx, s)

		// Every background record is on BA0175 too, so this proves the whole
		// store was searched: the count has to include the buried ones.
		got, err := s.FindPNRsByFlight(ctx, "BA175", "15JUN", 0)
		if err != nil {
			t.Fatalf("FindPNRsByFlight: %v", err)
		}
		found := map[string]bool{}
		for _, p := range got {
			found[p.RecordLocator] = true
		}
		for _, want := range []string{"OLDF00", "OLDF01"} {
			if !found[want] {
				t.Errorf("%s not found; a lookup that stops at a recent prefix "+
					"would miss it", want)
			}
		}
		if found["DEADFL"] {
			t.Error("a cancelled segment was reported as a holding on the flight")
		}
		if len(got) < buriedUnder {
			t.Errorf("found %d bookings on the flight, want at least %d",
				len(got), buriedUnder)
		}

		// The limit is a limit, not a search boundary.
		few, err := s.FindPNRsByFlight(ctx, "BA0175", "15JUN", 5)
		if err != nil {
			t.Fatalf("FindPNRsByFlight (limited): %v", err)
		}
		if len(few) != 5 {
			t.Errorf("limit 5 returned %d", len(few))
		}

		// A different date is a different flight.
		other, err := s.FindPNRsByFlight(ctx, "BA175", "16JUN", 0)
		if err != nil {
			t.Fatalf("FindPNRsByFlight (other date): %v", err)
		}
		if len(other) != 0 {
			t.Errorf("found %d bookings on a date nothing was booked for", len(other))
		}
	})
}

// Both due-lookups order by the thing that makes a record due, so a limit drops
// the least urgent work. The old sweep did the opposite -- it ordered by most
// recently touched, which put the stale records it was looking for last.
func TestDueLookupsOrderByUrgency(t *testing.T) {
	eachBackend(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		now := time.Now().UTC()

		for i := 1; i <= 40; i++ {
			p := samplePNR(fmt.Sprintf("DUE%03d", i))
			// The higher the index, the more overdue.
			p.UpdatedAt = now.Add(-time.Duration(i) * time.Hour)
			d := now.Add(-time.Duration(i) * time.Hour)
			p.Ticketing = []pnr.Ticketing{{Text: "TKTL", Deadline: &d}}
			if err := s.CreatePNR(ctx, p, nil); err != nil {
				t.Fatalf("CreatePNR: %v", err)
			}
		}

		stale, err := s.FindPNRsStale(ctx, now.Add(-time.Hour), 5)
		if err != nil {
			t.Fatalf("FindPNRsStale: %v", err)
		}
		if len(stale) != 5 {
			t.Fatalf("got %d stale records, want 5", len(stale))
		}
		if stale[0].RecordLocator != "DUE040" {
			t.Errorf("most overdue = %q, want DUE040; a limit must drop the "+
				"least urgent work, not the most", stale[0].RecordLocator)
		}
		for i := 1; i < len(stale); i++ {
			if stale[i].UpdatedAt.Before(stale[i-1].UpdatedAt) {
				t.Fatalf("stale records are not most-overdue-first at %d", i)
			}
		}

		due, err := s.FindPNRsDueBy(ctx, now, 5)
		if err != nil {
			t.Fatalf("FindPNRsDueBy: %v", err)
		}
		if len(due) != 5 || due[0].RecordLocator != "DUE040" {
			t.Errorf("got %d due records starting %v, want 5 starting DUE040",
				len(due), due)
		}

		// A record with no deadline owes nothing and must not appear.
		plain := samplePNR("NODL01")
		plain.UpdatedAt = now.Add(-100 * time.Hour)
		if err := s.CreatePNR(ctx, plain, nil); err != nil {
			t.Fatalf("CreatePNR: %v", err)
		}
		all, err := s.FindPNRsDueBy(ctx, now, 0)
		if err != nil {
			t.Fatalf("FindPNRsDueBy: %v", err)
		}
		for _, p := range all {
			if p.RecordLocator == "NODL01" {
				t.Error("a record owing no deadline was reported as due")
			}
		}
	})
}
