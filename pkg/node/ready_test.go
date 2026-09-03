package node

import (
	"errors"
	"testing"
	"time"
)

type fakeBacklog struct {
	age time.Duration
	ok  bool
	err error
}

func (f fakeBacklog) Oldest() (time.Duration, bool, error) { return f.age, f.ok, f.err }

// A spool whose oldest entry has waited longer than the store should ever
// take makes the node not ready, so a load balancer sends partners' new
// sessions elsewhere; an empty or young spool does not.
func TestReadinessFollowsTheSpoolBacklog(t *testing.T) {
	if err := spoolReady(nil, time.Second); err != nil {
		t.Fatalf("no spool is ready: %v", err)
	}
	if err := spoolReady(fakeBacklog{}, time.Second); err != nil {
		t.Fatalf("an empty spool is ready: %v", err)
	}
	if err := spoolReady(fakeBacklog{age: 200 * time.Millisecond, ok: true}, time.Second); err != nil {
		t.Fatalf("a young backlog is ready: %v", err)
	}
	if err := spoolReady(fakeBacklog{age: 2 * time.Minute, ok: true}, 30*time.Second); err == nil {
		t.Fatal("a two-minute backlog should not be ready")
	}
	if err := spoolReady(fakeBacklog{err: errors.New("disk")}, time.Second); err == nil {
		t.Fatal("a spool that cannot be read is not ready")
	}
}
