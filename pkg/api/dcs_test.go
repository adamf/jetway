package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/adamf/jetway/pkg/ops"
)

// GET /api/ops answers 404 on a plain gateway and, on a node with a desk,
// serves the schedule's legs and the slots the desk has filed.
func TestOpsEndpoint(t *testing.T) {
	s := &Server{}
	rec := httptest.NewRecorder()
	s.opsDesk(rec, httptest.NewRequest("GET", "/api/ops", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("without a desk: %d", rec.Code)
	}
	desk := ops.New(nil, "BA", []ops.Leg{{Carrier: "BA", Number: "117", Board: "LHR", Off: "JFK", STD: 480, STA: 660, Equipment: "77W"}}, ops.Config{Via: "1X", MovementsTo: []string{"LONRM1G"}}, nil)
	s.Ops = desk
	rec = httptest.NewRecorder()
	s.opsDesk(rec, httptest.NewRequest("GET", "/api/ops", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"legs"`) || !strings.Contains(rec.Body.String(), "LHR") || !strings.Contains(rec.Body.String(), `"via":"1X"`) {
		t.Fatalf("with a desk: %d %s", rec.Code, rec.Body.String())
	}
}
