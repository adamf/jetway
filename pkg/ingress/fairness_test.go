package ingress

import (
	"context"
	"sync"
	"testing"
	"time"
)

// Two peers on one ingress each hold their own share; the shared bucket
// caps the sum. A peer that floods is paced to its share and the other
// peer's messages still get through at its own rate.
func TestSharedBucketCapsTheIngressNotThePeer(t *testing.T) {
	shared := newBucket(40, 4)
	flooder := newBucket(20, 2)
	quiet := newBucket(20, 2)
	var wg sync.WaitGroup
	took := make([]time.Duration, 2)
	for i, b := range []*bucket{flooder, quiet} {
		wg.Add(1)
		go func(i int, b *bucket) {
			defer wg.Done()
			start := time.Now()
			for n := 0; n < 6; n++ {
				b.wait(context.Background())
				shared.wait(context.Background())
			}
			took[i] = time.Since(start)
		}(i, b)
	}
	wg.Wait()
	// Six each at 20/s with a burst of two: about 200ms apiece, and the
	// shared 40/s cap has room for both, so neither is slowed by the other.
	for i, d := range took {
		if d < 120*time.Millisecond || d > 700*time.Millisecond {
			t.Fatalf("peer %d: six at 20/s should take about 200ms, took %v", i, d)
		}
	}
	// With the shared cap below the sum of the shares, the total is what
	// the cap says: twelve more through a 10/s bucket take about a second.
	shared = newBucket(10, 1)
	start := time.Now()
	for n := 0; n < 12; n++ {
		shared.wait(context.Background())
	}
	if d := time.Since(start); d < 800*time.Millisecond || d > 2*time.Second {
		t.Fatalf("twelve at a shared 10/s should take about 1.1s, took %v", d)
	}
}
