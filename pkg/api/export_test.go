package api

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/adamf/jetway/pkg/pnr"
	"github.com/adamf/jetway/pkg/store"
)

// The archive: every record the node holds, one JSON line each, oldest
// first, readable back into the same model.
func TestExportStreamsEveryRecordAsNDJSON(t *testing.T) {
	st := store.NewMem()
	ctx := context.Background()
	for _, loc := range []string{"AAA111", "BBB222", "CCC333"} {
		p := &pnr.PNR{RecordLocator: loc, Status: pnr.StatusOpen,
			Passengers: []pnr.Passenger{{Ref: 1, Surname: "EXPORT", Given: loc, Title: "MR"}}}
		if err := st.CreatePNR(ctx, p, []store.Event{{Type: "created"}}); err != nil {
			t.Fatal(err)
		}
	}
	s := &Server{Store: st, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest("GET", "/api/admin/export", nil))
	if rr.Code != 200 || !strings.HasPrefix(rr.Header().Get("Content-Type"), "application/x-ndjson") {
		t.Fatalf("status %d type %q", rr.Code, rr.Header().Get("Content-Type"))
	}
	var got []string
	sc := bufio.NewScanner(rr.Body)
	for sc.Scan() {
		var p pnr.PNR
		if err := json.Unmarshal(sc.Bytes(), &p); err != nil {
			t.Fatalf("line is not a record: %v", err)
		}
		got = append(got, p.RecordLocator)
	}
	if len(got) != 3 {
		t.Fatalf("three records exported, got %v", got)
	}
}
