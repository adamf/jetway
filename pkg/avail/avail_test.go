package avail

import (
	"sync"
	"testing"
	"time"
)

func at(y int, m time.Month, d, h int) time.Time {
	return time.Date(y, m, d, h, 0, 0, 0, time.UTC)
}

func newTestCache(now time.Time) *Cache {
	c := NewCache()
	cur := now
	c.Now = func() time.Time { return cur }
	return c
}

func key(class string) Key {
	return NewKey("ba", "0175", at(2026, time.September, 27, 0), "lhr", "jfk", class)
}

func TestKeyNormalisation(t *testing.T) {
	k := NewKey("ba", "0175", at(2026, time.September, 27, 14), "lhr", "jfk", "y")
	if k.Carrier != "BA" || k.Board != "LHR" || k.Off != "JFK" || k.Class != "Y" {
		t.Errorf("case not normalised: %+v", k)
	}
	// The date must reduce to the day, or two lookups for the same flight miss
	// each other because they were made at different times.
	if k.Date != "2026-09-27" {
		t.Errorf("Date = %q", k.Date)
	}
	if NewKey("BA", "0175", at(2026, time.September, 27, 23), "LHR", "JFK", "Y") != k {
		t.Error("two times on the same day must produce the same key")
	}
	// Availability is per segment, so the city pair is part of identity.
	if NewKey("BA", "0175", at(2026, time.September, 27, 0), "LHR", "BOS", "Y") == k {
		t.Error("a different off point must be a different key")
	}
}

func TestPutAndLookup(t *testing.T) {
	now := at(2026, time.September, 1, 12)
	c := newTestCache(now)
	if !c.Put(Entry{Key: key("Y"), Status: Open, Source: SourceAVS, AsOf: now}) {
		t.Fatal("Put was refused")
	}
	e, ok, fresh := c.Lookup(key("Y"))
	if !ok || !fresh {
		t.Fatalf("Lookup = %v, %v", ok, fresh)
	}
	if e.Status != Open {
		t.Errorf("status = %q", e.Status)
	}
	if _, ok, _ := c.Lookup(key("J")); ok {
		t.Error("a class we know nothing about must not resolve")
	}
	if got := c.Status(key("J")); got != Unknown {
		t.Errorf("unknown class status = %q, want unknown", got)
	}
}

// A stale belief is not evidence. Reporting it as a status would let the
// booking path free-sell on a claim from yesterday.
func TestStaleBeliefsAreNotEvidence(t *testing.T) {
	now := at(2026, time.September, 1, 12)
	c := newTestCache(now)
	c.StaleAfter = time.Hour
	c.Put(Entry{Key: key("Y"), Status: Open, Source: SourceAVS, AsOf: now.Add(-90 * time.Minute)})

	_, ok, fresh := c.Lookup(key("Y"))
	if !ok {
		t.Fatal("the entry should still be held")
	}
	if fresh {
		t.Error("a 90-minute-old belief is outside a one-hour window")
	}
	if got := c.Status(key("Y")); got != Unknown {
		t.Errorf("stale status = %q, want unknown", got)
	}
	d, why := c.Decide(key("Y"), 1)
	if d != Ask {
		t.Errorf("decision = %q, want ask; got reason %q", d, why)
	}
}

// Ignorance is neither a closed flight nor permission to sell.
func TestUnknownMeansAsk(t *testing.T) {
	c := newTestCache(at(2026, time.September, 1, 12))
	d, why := c.Decide(key("Y"), 1)
	if d != Ask {
		t.Errorf("decision = %q, want ask (%s)", d, why)
	}
}

func TestDecisions(t *testing.T) {
	now := at(2026, time.September, 1, 12)
	cases := []struct {
		status Status
		want   Decision
	}{
		{Open, FreeSale},
		{Closed, Refuse},
		{Waitlist, AskWaitlist},
		{Request, Ask},
	}
	for _, tc := range cases {
		c := newTestCache(now)
		c.Put(Entry{Key: key("Y"), Status: tc.status, Source: SourceAVS, AsOf: now})
		if d, why := c.Decide(key("Y"), 1); d != tc.want {
			t.Errorf("%s -> %q, want %q (%s)", tc.status, d, tc.want, why)
		}
	}
}

// Free sale is permission to sell what was offered, not more.
func TestSeatCountBoundsFreeSale(t *testing.T) {
	now := at(2026, time.September, 1, 12)
	c := newTestCache(now)
	c.Put(Entry{Key: key("Y"), Status: Open, Seats: 2, SeatsKnown: true, Source: SourceAVS, AsOf: now})

	if d, _ := c.Decide(key("Y"), 2); d != FreeSale {
		t.Errorf("two seats against an offer of two should free-sell, got %q", d)
	}
	if d, why := c.Decide(key("Y"), 3); d != Ask {
		t.Errorf("three seats against an offer of two must ask, got %q (%s)", d, why)
	}
	// Without a count, an open status permits the sale.
	c2 := newTestCache(now)
	c2.Put(Entry{Key: key("Y"), Status: Open, Source: SourceAVS, AsOf: now})
	if d, _ := c2.Decide(key("Y"), 9); d != FreeSale {
		t.Error("an open status with no count should not be bounded by a count")
	}
}

// Two bookings in quick succession must not both sell the last seat on the
// strength of one broadcast.
func TestSoldDecrementsAndCloses(t *testing.T) {
	now := at(2026, time.September, 1, 12)
	c := newTestCache(now)
	c.Put(Entry{Key: key("Y"), Status: Open, Seats: 2, SeatsKnown: true, Source: SourceAVS, AsOf: now})

	c.Sold(key("Y"), 1)
	e, _, _ := c.Lookup(key("Y"))
	if e.Seats != 1 || e.Status != Open {
		t.Errorf("after selling one of two: %+v", e)
	}
	c.Sold(key("Y"), 1)
	e, _, _ = c.Lookup(key("Y"))
	if e.Seats != 0 || e.Status != Closed {
		t.Errorf("selling the last seat must close the class: %+v", e)
	}
	if d, _ := c.Decide(key("Y"), 1); d != Refuse {
		t.Errorf("decision after sell-out = %q, want refuse", d)
	}
	// A status with no count is not decremented; there is nothing to decrement.
	c2 := newTestCache(now)
	c2.Put(Entry{Key: key("Y"), Status: Open, Source: SourceAVS, AsOf: now})
	c2.Sold(key("Y"), 5)
	if got := c2.Status(key("Y")); got != Open {
		t.Errorf("status = %q, want open", got)
	}
}

// A broadcast must not erase the answer a carrier just gave to a direct
// question.
func TestWeakerSourceDoesNotOverwriteFresherStronger(t *testing.T) {
	now := at(2026, time.September, 1, 12)
	c := newTestCache(now)
	c.Put(Entry{Key: key("Y"), Status: Closed, Source: SourceDirect, AsOf: now})

	if c.Put(Entry{Key: key("Y"), Status: Open, Source: SourceAVS, AsOf: now.Add(time.Second)}) {
		t.Error("a broadcast should not overwrite a fresh direct answer")
	}
	if got := c.Status(key("Y")); got != Closed {
		t.Errorf("status = %q, want closed", got)
	}
	// An equal or stronger source may.
	if !c.Put(Entry{Key: key("Y"), Status: Open, Source: SourceDirect, AsOf: now.Add(time.Minute)}) {
		t.Error("a newer direct answer should win")
	}
	if got := c.Status(key("Y")); got != Open {
		t.Errorf("status = %q, want open", got)
	}
}

// Once the stronger belief has gone stale it no longer blocks a broadcast.
func TestWeakerSourceWinsOnceStrongerIsStale(t *testing.T) {
	now := at(2026, time.September, 1, 12)
	c := newTestCache(now)
	c.StaleAfter = time.Hour
	c.Put(Entry{Key: key("Y"), Status: Closed, Source: SourceDirect, AsOf: now.Add(-2 * time.Hour)})
	if !c.Put(Entry{Key: key("Y"), Status: Open, Source: SourceAVS, AsOf: now}) {
		t.Fatal("a broadcast should win over a stale direct answer")
	}
	if got := c.Status(key("Y")); got != Open {
		t.Errorf("status = %q, want open", got)
	}
}

// Store-and-forward delivers out of order. An older assertion must never move
// state backwards.
func TestOlderAssertionIsIgnored(t *testing.T) {
	now := at(2026, time.September, 1, 12)
	c := newTestCache(now)
	c.Put(Entry{Key: key("Y"), Status: Closed, Source: SourceAVS, AsOf: now})
	if c.Put(Entry{Key: key("Y"), Status: Open, Source: SourceAVS, AsOf: now.Add(-time.Minute)}) {
		t.Error("an older broadcast should be ignored")
	}
	if got := c.Status(key("Y")); got != Closed {
		t.Errorf("status = %q, want closed", got)
	}
}

func TestPurge(t *testing.T) {
	now := at(2026, time.September, 27, 12)
	c := newTestCache(now)
	c.StaleAfter = time.Hour

	c.Put(Entry{Key: key("Y"), Status: Open, Source: SourceAVS, AsOf: now})
	c.Put(Entry{Key: key("J"), Status: Open, Source: SourceAVS, AsOf: now.Add(-2 * time.Hour)})
	past := NewKey("BA", "0175", at(2026, time.August, 1, 0), "LHR", "JFK", "M")
	c.Put(Entry{Key: past, Status: Open, Source: SourceAVS, AsOf: now})

	if n := c.Purge(); n != 2 {
		t.Errorf("purged %d, want 2 (one stale, one flown)", n)
	}
	if c.Len() != 1 {
		t.Errorf("remaining = %d, want 1", c.Len())
	}
	if got := c.Status(key("Y")); got != Open {
		t.Errorf("the fresh future entry should survive, got %q", got)
	}
}

func TestClassesForASegment(t *testing.T) {
	now := at(2026, time.September, 1, 12)
	c := newTestCache(now)
	c.Put(Entry{Key: key("Y"), Status: Open, Source: SourceAVS, AsOf: now})
	c.Put(Entry{Key: key("J"), Status: Closed, Source: SourceAVS, AsOf: now})
	// A different segment of the same flight must not leak in.
	other := NewKey("BA", "0175", at(2026, time.September, 27, 0), "JFK", "BOS", "Y")
	c.Put(Entry{Key: other, Status: Open, Source: SourceAVS, AsOf: now})

	got := c.Classes(key("Y"))
	if len(got) != 2 {
		t.Fatalf("classes = %d, want 2: %v", len(got), got)
	}
	if got["Y"].Status != Open || got["J"].Status != Closed {
		t.Errorf("classes wrong: %v", got)
	}
}

func TestSnapshotIsNewestFirst(t *testing.T) {
	now := at(2026, time.September, 1, 12)
	c := newTestCache(now)
	c.Put(Entry{Key: key("Y"), Status: Open, Source: SourceAVS, AsOf: now.Add(-time.Minute)})
	c.Put(Entry{Key: key("J"), Status: Open, Source: SourceAVS, AsOf: now})
	snap := c.Snapshot()
	if len(snap) != 2 || snap[0].Key.Class != "J" {
		t.Errorf("snapshot not newest first: %v", snap)
	}
}

func TestConcurrentUse(t *testing.T) {
	now := at(2026, time.September, 1, 12)
	c := newTestCache(now)
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			k := key(string(rune('A' + i%20)))
			for j := 0; j < 50; j++ {
				c.Put(Entry{Key: k, Status: Open, Source: SourceAVS, AsOf: now.Add(time.Duration(j) * time.Second)})
				c.Decide(k, 1)
				c.Status(k)
				_ = c.Snapshot()
			}
		}(i)
	}
	wg.Wait()
	if c.Len() != 20 {
		t.Errorf("entries = %d, want 20", c.Len())
	}
}
