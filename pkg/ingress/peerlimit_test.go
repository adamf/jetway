package ingress

import (
	"context"
	"testing"
	"time"
)

// A peer with its own rate_limit is paced to it; the others keep the
// ingress's. A big partner and a small one on one ingress are not the same
// share.
func TestPeerLimitOverridesTheIngressRate(t *testing.T) {
	tcp := &TCP{rateLimit: 1000, burst: 1}
	tcp.SetPeerLimit("SLOW", 20, 1)
	fast := tcp.limitFor("FAST")
	slow := tcp.limitFor("SLOW")
	start := time.Now()
	for i := 0; i < 5; i++ {
		fast.wait(context.Background())
	}
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Fatalf("the ingress rate of 1000/s should pass five frames at once: %v", d)
	}
	start = time.Now()
	for i := 0; i < 5; i++ {
		slow.wait(context.Background())
	}
	if d := time.Since(start); d < 150*time.Millisecond || d > 600*time.Millisecond {
		t.Fatalf("the peer's own 20/s should take about 200ms for five: %v", d)
	}
	if none := (&TCP{}).limitFor("ANY"); none != nil {
		t.Fatal("no limit configured means no bucket")
	}
}
