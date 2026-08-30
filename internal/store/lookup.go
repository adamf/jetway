package store

import (
	"context"
	"strings"
	"time"

	"github.com/adamf/jetway/pkg/pnr"
)

// Lookups that find a record by something other than its own locator.
//
// These exist because the alternative was a bug rather than merely a slow
// query. Three places on the hot path used to walk ListPNRs, which is
// "ORDER BY updated_at DESC LIMIT n" -- so they searched only recently touched
// records and reported *not found* for anything older. A ticket control
// message about a booking made last month was refused with "no record holds
// this document", which is not slow, it is false, and it gets more likely as
// the store grows.
//
// So the contract here is total: an implementation must search every record or
// return an error. It must never quietly answer from a prefix.
type Lookup interface {
	// FindPNRByDocument returns the record holding a document, by its compact
	// thirteen-digit number. Not found is (nil, nil): a document this node
	// never issued is an ordinary answer, not a failure.
	FindPNRByDocument(ctx context.Context, compactNumber string) (*pnr.PNR, error)

	// FindPNRByExternalLocator returns the record carrying another system's
	// locator. Owner may be empty to match any.
	FindPNRByExternalLocator(ctx context.Context, owner, value string) (*pnr.PNR, error)

	// FindPNRsByFlight returns every live record holding a segment on a
	// flight. wireDate may be empty to match every date, which is what a
	// schedule message covering a period needs.
	//
	// flightKey is the carrier and flight number with leading zeros removed,
	// because carriers write the same flight both ways and a schedule change
	// that misses half its holdings is worse than useless.
	FindPNRsByFlight(ctx context.Context, flightKey, wireDate string, limit int) ([]*pnr.PNR, error)

	// FindPNRsStale returns live records untouched since before the given
	// time, most overdue first.
	//
	// The ordering is the point. The sweeper used to read the most recently
	// updated records and look for stale ones among them, which is inverted:
	// the freshest records are precisely not the stale ones. Ordering by the
	// thing that makes a record due means a limit drops the least urgent work
	// rather than all of it.
	FindPNRsStale(ctx context.Context, before time.Time, limit int) ([]*pnr.PNR, error)

	// FindPNRsDueBy returns live records owing a ticketing time limit before
	// the given time, soonest deadline first.
	FindPNRsDueBy(ctx context.Context, deadline time.Time, limit int) ([]*pnr.PNR, error)
}

// NormaliseFlightKey renders a carrier and flight number in the one spelling
// the lookups compare against.
//
// Carriers write the same flight both zero-padded and bare, sometimes in the
// same conversation, so a key is only useful if both spellings collapse onto
// it.
func NormaliseFlightKey(key string) string {
	for i, r := range key {
		if r >= '0' && r <= '9' {
			n := strings.TrimLeft(key[i:], "0")
			if n == "" {
				n = "0"
			}
			return key[:i] + n
		}
	}
	return key
}

// SegmentOnFlight reports whether a segment is a live holding on a flight.
//
// Both backends decide with this function rather than each with its own
// predicate. The Postgres lookup narrows with an index and then asks this, so
// an index that over-matches -- and containment does, because it will match a
// cancelled segment as happily as a live one -- cannot change the answer.
func SegmentOnFlight(seg *pnr.Segment, flightKey, wireDate string) bool {
	if seg.Type != pnr.SegmentAir || seg.Status == "XX" {
		return false
	}
	if NormaliseFlightKey(seg.Carrier+seg.FlightNum) != NormaliseFlightKey(flightKey) {
		return false
	}
	return wireDate == "" || strings.EqualFold(seg.WireDate, wireDate)
}

// pnrOnFlight reports whether any segment of a record is a live holding.
func pnrOnFlight(p *pnr.PNR, flightKey, wireDate string) bool {
	if p.Status == pnr.StatusCancelled {
		return false
	}
	for i := range p.Segments {
		if SegmentOnFlight(&p.Segments[i], flightKey, wireDate) {
			return true
		}
	}
	return false
}
