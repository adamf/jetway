package gateway

import (
	"testing"
	"time"

	"github.com/adamf/jetway/pkg/avail"
)

// Capacity gets raised by harnesses and demos while the inventory is already
// answering availability probes on another goroutine. A naked field write
// there was a data race the scenario suite caught under -race; the write has
// to take the same lock the readers hold.
func TestInventoryCapacityCanBeRaisedWhileAnswering(t *testing.T) {
	inv := NewInventory()
	inv.Carrier = "BA"
	keys := []avail.Key{{Carrier: "BA", FlightNum: "0117", Date: "2026-12-15",
		Board: "LHR", Off: "JFK", Class: "Y"}}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 500; i++ {
			inv.Availability(keys, time.Now())
		}
	}()
	for i := 0; i < 500; i++ {
		inv.SetCapacity(50 + i)
	}
	<-done
}
