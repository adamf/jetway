package inventory

import (
	"context"
	"testing"
)

// SoldByClass reads a pool's classes from an index kept in step with every
// sale, seed and release, so it costs the pool's classes and not the book:
// a filled carrier's four hundred flights were each a scan of the whole.
func TestSoldByClassIsIndexed(t *testing.T) {
	inv := New("WN", b737())
	decide(t, inv, seg("2554", "Y", "HN", 3))
	decide(t, inv, seg("2554", "K", "HN", 2))
	inv.Seed(seg("2554", "J", "HK", 1))
	// A 737 is one cabin: every class sells from the same pool.
	econ := CompartmentFor("Y", nil)
	got := inv.SoldByClass("WN", "2554", "26NOV", "BNA", econ)
	if got["Y"] != 3 || got["K"] != 2 || got["J"] != 1 || len(got) != 3 {
		t.Fatalf("classes on 2554: %v", got)
	}
	if got := inv.SoldByClass("WN", "2555", "26NOV", "BNA", econ); len(got) != 0 {
		t.Fatalf("a flight nothing sold on: %v", got)
	}
	inv.Release(context.Background(), seg("2554", "K", "XX", 2), "KK")
	if got := inv.SoldByClass("WN", "2554", "26NOV", "BNA", econ); got["Y"] != 3 || got["J"] != 1 || len(got) != 2 {
		t.Fatalf("after releasing K: %v", got)
	}
	inv.Reset()
	if got := inv.SoldByClass("WN", "2554", "26NOV", "BNA", econ); len(got) != 0 {
		t.Fatalf("after reset: %v", got)
	}
}
