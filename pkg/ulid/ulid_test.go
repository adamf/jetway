package ulid

import (
	"sort"
	"testing"
	"time"
)

func TestFormatAndValidity(t *testing.T) {
	id := New()
	if len(id) != Len {
		t.Fatalf("length = %d, want %d", len(id), Len)
	}
	if !Valid(id) {
		t.Errorf("%q should be valid", id)
	}
	if Valid("short") || Valid("IIIIIIIIIIIIIIIIIIIIIIIIII") {
		t.Error("invalid ids accepted")
	}
}

func TestTimeRoundTrip(t *testing.T) {
	now := time.Now().Truncate(time.Millisecond)
	got, err := Time(NewAt(now))
	if err != nil {
		t.Fatalf("Time: %v", err)
	}
	if !got.Equal(now.UTC()) {
		t.Errorf("got %s, want %s", got, now.UTC())
	}
}

// Ids minted inside one millisecond must still sort in mint order, or an audit
// trail can reorder the messages it is meant to prove the sequence of.
func TestMonotonicWithinMillisecond(t *testing.T) {
	at := time.Now()
	ids := make([]string, 500)
	for i := range ids {
		ids[i] = NewAt(at)
	}
	if !sort.StringsAreSorted(ids) {
		for i := 1; i < len(ids); i++ {
			if ids[i] <= ids[i-1] {
				t.Fatalf("not monotonic at %d: %q then %q", i, ids[i-1], ids[i])
			}
		}
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = true
	}
}

func TestSortOrderMatchesTimeOrder(t *testing.T) {
	base := time.Now()
	a := NewAt(base)
	b := NewAt(base.Add(time.Millisecond))
	c := NewAt(base.Add(2 * time.Millisecond))
	if !(a < b && b < c) {
		t.Errorf("string order does not match time order: %q %q %q", a, b, c)
	}
}
