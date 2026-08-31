package gateway

import (
	"context"
	"testing"

	"github.com/adamf/jetway/pkg/store"
)

// A name that cannot survive the wire is refused at the counter, not
// accepted and stranded. Both wire charsets -- teletype ITA2 and EDIFACT
// level A -- reject characters like underscores and accented letters, and a
// booking that encodes for nobody sits at HN forever with only a warning in
// a log to say why. Real distribution systems restrict names to the
// interline repertoire at entry for exactly this reason.
func TestBookingRejectsNamesOutsideTheWireCharset(t *testing.T) {
	ctx := context.Background()
	gds, _ := wire(t, "BA", store.FormatTypeB)

	for _, bad := range []string{"CONSERVE0_0", "MÜLLER", "O~BRIEN"} {
		req := booking("Y", 1)
		req.Passengers[0].Surname = bad
		if _, err := gds.gw.Book(ctx, req); err == nil {
			t.Errorf("surname %q was accepted; it cannot be encoded for any link", bad)
		}
	}
	// The names real passengers actually have must keep working.
	for _, good := range []string{"O'BRIEN", "MARY-JANE", "DA SILVA", "SMITH JR."} {
		req := booking("Y", 1)
		req.Passengers[0].Surname = good
		if _, err := gds.gw.Book(ctx, req); err != nil {
			t.Errorf("surname %q was refused: %v", good, err)
		}
	}
}
