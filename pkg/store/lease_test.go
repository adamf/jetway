package store

import (
	"context"
	"os"
	"testing"
	"time"
)

func testLease(t *testing.T, s Leaser, tick func(time.Duration)) {
	ctx := context.Background()
	ok, err := s.Acquire(ctx, "BA", "host-1", 2*time.Second)
	if err != nil || !ok {
		t.Fatalf("first acquire: %v %v", ok, err)
	}
	if ok, _ := s.Acquire(ctx, "BA", "host-2", 2*time.Second); ok {
		t.Fatal("a held lease was taken by a second holder")
	}
	if ok, _ := s.Renew(ctx, "BA", "host-2", 2*time.Second); ok {
		t.Fatal("a stranger renewed the lease")
	}
	if ok, _ := s.Renew(ctx, "BA", "host-1", 2*time.Second); !ok {
		t.Fatal("the holder could not renew")
	}
	if ok, _ := s.Acquire(ctx, "BA", "host-1", 2*time.Second); !ok {
		t.Fatal("the holder re-acquiring its own lease should succeed")
	}
	tick(3 * time.Second)
	if ok, _ := s.Renew(ctx, "BA", "host-1", 2*time.Second); ok {
		t.Fatal("a lapsed lease renewed")
	}
	if ok, _ := s.Acquire(ctx, "BA", "host-2", 2*time.Second); !ok {
		t.Fatal("a lapsed lease was not taken by the standby")
	}
	if err := s.Release(ctx, "BA", "host-2"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.Acquire(ctx, "BA", "host-1", 2*time.Second); !ok {
		t.Fatal("a released lease was not free")
	}
}

// Exactly one holder, a term that lapses, a release that frees at once.
func TestMemLease(t *testing.T) {
	m := NewMem()
	now := time.Now()
	m.Now = func() time.Time { return now }
	testLease(t, m, func(d time.Duration) { now = now.Add(d) })
}

func TestPostgresLease(t *testing.T) {
	dsn := os.Getenv("JETWAY_TEST_DSN")
	if dsn == "" {
		t.Skip("JETWAY_TEST_DSN not set")
	}
	ctx := context.Background()
	pg, err := OpenPostgres(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	if err := MigrateSchema(ctx, pg); err != nil {
		t.Fatal(err)
	}
	pg.pool.Exec(ctx, `DELETE FROM system_lease`) //nolint:errcheck
	testLease(t, pg, func(d time.Duration) { time.Sleep(d) })
}
