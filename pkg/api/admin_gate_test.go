package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/adamf/jetway/pkg/store"
)

// With an admin token set, anything that changes the system or reads its
// records needs the bearer; status and health stay open. Without one the
// console is as open as it always was.
func TestAdminTokenGuardsRecordsAndChanges(t *testing.T) {
	s := &Server{Store: store.NewMem(), Log: slog.New(slog.NewTextHandler(io.Discard, nil)), AdminToken: "s3cret"}
	h := s.Handler()
	try := func(method, path, bearer string) int {
		req := httptest.NewRequest(method, path, nil)
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr.Code
	}
	// Health stays open (status and flights too, but they need a gateway
	// this bare server does not carry; guarded() is what decides).
	if code := try(http.MethodGet, "/healthz", ""); code != http.StatusOK {
		t.Errorf("GET /healthz should stay open, got %d", code)
	}
	for _, p := range []string{"/api/status", "/api/flights", "/api/availability", "/api/journeys?x=1"} {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		if p == "/api/journeys?x=1" {
			if !guarded(req) {
				t.Errorf("GET %s should be guarded: it lists bookings", p)
			}
			continue
		}
		if guarded(req) {
			t.Errorf("GET %s should stay open", p)
		}
	}
	guardedReads := []string{"/api/pnrs", "/api/pnr/ABC123", "/api/messages", "/api/message/x", "/api/admin/export", "/api/queues"}
	for _, p := range guardedReads {
		if code := try(http.MethodGet, p, ""); code != http.StatusUnauthorized {
			t.Errorf("GET %s without the token: %d, want 401", p, code)
		}
		if code := try(http.MethodGet, p, "wrong"); code != http.StatusUnauthorized {
			t.Errorf("GET %s with a wrong token: %d, want 401", p, code)
		}
		if code := try(http.MethodGet, p, "s3cret"); code == http.StatusUnauthorized {
			t.Errorf("GET %s with the token was refused", p)
		}
	}
	for _, p := range []string{"/api/book", "/api/pnr/ABC123/cancel", "/api/admin/retire", "/ndc", "/api/dcs/flight/BA1/06SEP/close"} {
		if code := try(http.MethodPost, p, ""); code != http.StatusUnauthorized {
			t.Errorf("POST %s without the token: %d, want 401", p, code)
		}
	}

	openServer := &Server{Store: store.NewMem(), Log: s.Log}
	oh := openServer.Handler()
	req := httptest.NewRequest(http.MethodGet, "/api/pnrs", nil)
	rr := httptest.NewRecorder()
	oh.ServeHTTP(rr, req)
	if rr.Code == http.StatusUnauthorized {
		t.Error("with no admin token configured the console must stay open")
	}
}

// A caller's limit is a page size: it cannot size an allocation.
func TestLimitParamIsClamped(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/queue/all?limit=100000000", nil)
	if n := intParam(req, "limit", 100); n != 10000 {
		t.Fatalf("limit=1e8 became %d, want the 10000 cap", n)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/queue/all?limit=-5", nil)
	if n := intParam(req, "limit", 100); n != 100 {
		t.Fatalf("a negative limit became %d, want the default", n)
	}
}
