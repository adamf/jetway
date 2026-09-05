package ops

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/adamf/jetway/pkg/acars"
	"github.com/adamf/jetway/pkg/aftn"
	"github.com/adamf/jetway/pkg/atfm"
	"github.com/adamf/jetway/pkg/pnl"
	"github.com/adamf/jetway/pkg/ssim"
	"github.com/adamf/jetway/pkg/typeb"
)

func schedule(t *testing.T) []Leg {
	t.Helper()
	day := time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC)
	f := &ssim.File{Carrier: "BA", TimeMode: ssim.UTC, Season: "W26", From: day, To: day, Created: day, Released: day, Status: "C",
		Legs: []ssim.FlightLeg{
			{Carrier: "BA", Number: "117", Variation: 1, Sequence: 1, ServiceType: "J", From: day, To: day, Days: "1234567", Board: "LHR", STD: "0800", Off: "JFK", STA: "1100", Equipment: "77W"},
			{Carrier: "BA", Number: "117", Variation: 1, Sequence: 2, ServiceType: "J", From: day, To: day, Days: "1234567", Board: "JFK", STD: "1300", Off: "BOS", STA: "1400", Equipment: "77W"},
		}}
	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "ba.ssim")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	legs, err := LoadSchedule(path)
	if err != nil {
		t.Fatal(err)
	}
	return legs
}

// The schedule loads from the SSIM file, the desk finds the right leg of a
// number that flies twice, and an OOOI departure report becomes the MVT
// with the delay against schedule and the ETA; an arrival report the AA.
func TestScheduleAndMovements(t *testing.T) {
	legs := schedule(t)
	if len(legs) != 2 || legs[0].STD != 8*60 || legs[1].Board != "JFK" {
		t.Fatalf("legs %+v", legs)
	}
	d := New(nil, "BA", legs, Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	d.Now = func() time.Time { return time.Date(2026, 11, 26, 8, 40, 0, 0, time.UTC) }
	m, ok := d.MovementFor(&acars.Message{Kind: acars.KindDEP, Flight: "BA117", Registration: "G-STBA", Out: "0825", Off: "0840"})
	if !ok || m.Flight != "BA117" || m.Station != "LHR" || m.AD.First != "0825" || m.AD.Second != "0840" || m.EA == nil || m.EA.Airport != "JFK" || m.EA.Time != "1125" {
		t.Fatalf("departure MVT %+v (ok %v)", m, ok)
	}
	if len(m.Delays) != 1 || m.Delays[0].Code != "93" || m.Delays[0].Duration != "0025" {
		t.Errorf("delay %+v", m.Delays)
	}
	if text, err := m.Build(); err != nil || !bytes.Contains([]byte(text), []byte("MVT")) {
		t.Errorf("build %q %v", text, err)
	}
	// The second leg of the day, by the hour of the report.
	m, ok = d.MovementFor(&acars.Message{Kind: acars.KindDEP, Flight: "BA117", Out: "1300", Off: "1312"})
	if !ok || m.Station != "JFK" || m.EA.Airport != "BOS" || len(m.Delays) != 0 {
		t.Errorf("second leg MVT %+v", m)
	}
	m, ok = d.MovementFor(&acars.Message{Kind: acars.KindARR, Flight: "BA117", On: "1055", In: "1102"})
	if !ok || m.AA == nil || m.AA.First != "1055" || m.Station != "JFK" {
		t.Errorf("arrival MVT %+v", m)
	}
	if _, ok := d.MovementFor(&acars.Message{Kind: acars.KindDEP, Flight: "BA117", Out: "0825"}); ok {
		t.Error("an OUT without an OFF is half a movement")
	}
	if _, ok := d.MovementFor(&acars.Message{Kind: acars.KindDEP, Flight: "BA999", Off: "0900"}); ok {
		t.Error("a flight not in the schedule has no movement")
	}
}

// The desk is a Ground: a name list opens the flight in departure control
// with the schedule's aircraft, and a slot is filed against the callsign.
func TestDeskOpensFlightsAndFilesSlots(t *testing.T) {
	d := New(nil, "BA", schedule(t), Config{AccountingCode: "125"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx := context.Background()
	list, err := pnl.Parse("PNL\nBA0117/26NOV LHR PART1\n-JFK002Y\n1SMITH/JOHNMR\n1JONES/ANNMS\nENDPNL\n")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.NameList(ctx, list, typeb.Address{}); err != nil {
		t.Fatal(err)
	}
	fl, ok := d.Station.Find("BA0117", "26NOV")
	if !ok || len(fl.Passengers) != 2 || fl.Equipment != "77W" {
		t.Fatalf("flight %+v ok %v", fl, ok)
	}
	sam, _ := atfm.Parse("-TITLE SAM\n-ARCID BAW117\n-IFPLID AA00000001\n-ADEP EGLL\n-ADES KJFK\n-EOBD 261126\n-EOBT 0800\n-CTOT 0855\n-REGUL KJFKA26M\n-TAXITIME 0015\n-REGCAUSE WA 84")
	if err := d.ATFM(ctx, sam, &aftn.Message{}); err != nil {
		t.Fatal(err)
	}
	if s := d.Slots()["BAW117/261126"]; s != "CTOT 0855 KJFKA26M WA 84" {
		t.Errorf("slot line %q", s)
	}
}
