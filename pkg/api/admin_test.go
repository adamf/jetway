package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/adamf/jetway/pkg/store"
)

func TestRetireNeedsAStoreThatRetires(t *testing.T) {
	s := &Server{Store: store.NewMem(), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	mux := s.Handler()
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest("POST", "/api/admin/retire", strings.NewReader(`{"before":"2025-11-27T00:00:00Z"}`)))
	if rr.Code != http.StatusNotImplemented {
		t.Errorf("memory store retire: %d %s", rr.Code, rr.Body.String())
	}
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest("POST", "/api/admin/retire", strings.NewReader(`{}`)))
	if rr.Code != http.StatusBadRequest {
		t.Errorf("retire without a cutoff: %d", rr.Code)
	}
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest("GET", "/readyz", nil))
	if rr.Code != http.StatusOK {
		t.Errorf("readyz with a memory store: %d %s", rr.Code, rr.Body.String())
	}
}
