package ingress

import (
	"context"
	"sync/atomic"
	"time"
)

// inflight counts handlers still running so a drain can wait for them. It
// is not a sync.WaitGroup on purpose: a WaitGroup forbids Add while a Wait
// is in progress with the counter at zero, and an ingress cannot promise
// that -- a frame can arrive on any link at the moment the drain begins.
// The race detector caught exactly that on the multi-machine test.
type inflight struct{ n atomic.Int64 }

func (f *inflight) Add(d int64) { f.n.Add(d) }
func (f *inflight) Done()       { f.n.Add(-1) }

// Wait returns when nothing is in flight or the context ends.
func (f *inflight) Wait(ctx context.Context) error {
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for f.n.Load() > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
	return nil
}
