package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/adamf/jetway/pkg/gateway"
	"github.com/adamf/jetway/pkg/pnr"
	"github.com/adamf/jetway/pkg/store"
)

func testServer(t *testing.T) (http.Handler, *store.Mem) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st := store.NewMem()
	gw := gateway.New(gateway.Identity{Designator: "1J", TTYAddress: "LONRM1J", Name: "test"},
		st, gateway.NewBus(16), log, []byte("api-test"))
	srv := &Server{Gateway: gw, Store: st, Bus: gw.Bus, Log: log}
	return srv.Handler(), st
}

// Twenty open consoles polling insights must cost one computation per TTL,
// not twenty: inside the window the snapshot answers, even when the store
// has already moved on.
func TestInsightsSnapshotServesRepeatRequests(t *testing.T) {
	h, st := testServer(t)
	get := func() string {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/insights", nil))
		if rec.Code != 200 {
			t.Fatalf("insights: %d %s", rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}
	first := get()
	// The store changes; the snapshot inside the TTL must not.
	rec2 := &pnr.PNR{RecordLocator: "SNAP01", Status: pnr.StatusOpen,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
		Passengers: []pnr.Passenger{{Ref: 1, Surname: "CACHE", Given: "HIT"}}}
	if err := st.CreatePNR(context.Background(), rec2, nil); err != nil {
		t.Fatal(err)
	}
	if second := get(); second != first {
		t.Error("a second request inside the TTL recomputed the aggregate")
	}
}
