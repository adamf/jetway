package gateway

import "testing"

// A publisher racing an unsubscriber must never send on the closed channel.
// The world simulator found this tearing down five hundred subscribers while
// its gateways were still publishing: Publish snapshotted the channels,
// dropped the lock, and sent into a close. Run with -race in CI; without the
// fix this panics outright either way.
func TestBusPublishUnsubscribeRace(t *testing.T) {
	b := NewBus(8)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 20000; i++ {
			b.Publish(EvTrace, i)
		}
	}()
	for i := 0; i < 2000; i++ {
		_, unsub := b.Subscribe()
		unsub()
	}
	<-done
}
