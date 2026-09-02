package ingress

import (
	"context"
	"testing"
	"time"
)

// A bucket lets the burst through at once and then paces to the rate; a
// nil bucket is no limit.
func TestBucketPacesToTheRate(t *testing.T) {
	var none *bucket
	none.wait(context.Background()) // no limit, returns at once
	b := newBucket(50, 5)
	start := time.Now()
	for i := 0; i < 5; i++ {
		b.wait(context.Background())
	}
	if d := time.Since(start); d > 50*time.Millisecond {
		t.Fatalf("the burst should pass without waiting: %v", d)
	}
	start = time.Now()
	for i := 0; i < 10; i++ {
		b.wait(context.Background())
	}
	if d := time.Since(start); d < 150*time.Millisecond || d > 600*time.Millisecond {
		t.Fatalf("ten more at 50/s should take about 200ms: %v", d)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start = time.Now()
	b.wait(ctx) // a cancelled context does not wait
	if time.Since(start) > 50*time.Millisecond {
		t.Fatal("a cancelled wait should return at once")
	}
}
