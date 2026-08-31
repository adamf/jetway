package gateway

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/adamf/jetway/pkg/queue"
	"github.com/adamf/jetway/pkg/store"
	"github.com/adamf/jetway/pkg/edifact"
	"github.com/adamf/jetway/pkg/pnr"
)

// contrlNode builds a gateway with an EDIFACT peer and captures what it sends.
func contrlNode(t *testing.T, policy string) (*Gateway, *sentTo) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st := store.NewMem()
	gw := New(Identity{Designator: "1J", TTYAddress: "LONRM1J", Name: "jetway"},
		st, NewBus(100), log, []byte("secret"))
	gw.Queues = &queue.Manager{Store: st, Log: log}
	gw.AddPeer(&Peer{Name: "AA", Carrier: "AA", Format: store.FormatEDIFACT, CONTRL: policy})
	sent := newSentTo()
	gw.Sender = sent.sender()
	return gw, sent
}

// paoreq is a minimal well-formed interchange. ackRequest sets UNB 0031.
//
// The empty elements are counted: 0031 is the ninth data element of UNB, after
// S005 (recipient reference), 0026 (application reference) and 0029 (processing
// priority). One separator too many puts the flag in 0032 instead.
func paoreq(ref string, ackRequest bool) []byte {
	ack := ""
	if ackRequest {
		ack = "1"
	}
	return []byte("UNB+UNOA:3+AA:ZZ+1J:ZZ+260829:1200+" + ref + "++++" + ack + "'" +
		"UNH+1+PAOREQ:96:1:IA'" +
		"MSG+:11'" +
		"UNT+3+1'" +
		"UNZ+1+" + ref + "'")
}

func lastCONTRL(t *testing.T, sent *sentTo, peer string) *edifact.Report {
	t.Helper()
	sent.mu.Lock()
	defer sent.mu.Unlock()
	for i := len(sent.msgs[peer]) - 1; i >= 0; i-- {
		ic, err := edifact.Parse(sent.msgs[peer][i], edifact.ParseOptions{})
		if err != nil || len(ic.Messages) == 0 || !edifact.IsCONTRL(ic.Messages[0]) {
			continue
		}
		r, err := edifact.ParseCONTRL(ic.Messages[0])
		if err != nil {
			t.Fatalf("ParseCONTRL: %v", err)
		}
		return r
	}
	return nil
}

func TestCONTRLHonoursTheAcknowledgementRequest(t *testing.T) {
	gw, sent := contrlNode(t, "") // default policy
	ctx := context.Background()

	if _, err := gw.Ingest(ctx, "AA", paoreq("IC001", false)); err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if r := lastCONTRL(t, sent, "AA"); r != nil {
		t.Error("sent a CONTRL nobody asked for; UNB 0031 was not set")
	}

	if _, err := gw.Ingest(ctx, "AA", paoreq("IC002", true)); err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	r := lastCONTRL(t, sent, "AA")
	if r == nil {
		t.Fatal("UNB 0031 requested an acknowledgement and none was sent")
	}
	if r.ControlRef != "IC002" {
		t.Errorf("CONTRL reports on %q, want IC002", r.ControlRef)
	}
	if r.Action != edifact.ActionAcknowledged {
		t.Errorf("Action = %q, want 7 for a clean interchange", r.Action)
	}
}

func TestCONTRLPolicies(t *testing.T) {
	broken := []byte("UNB+UNOA:3+AA:ZZ+1J:ZZ+260829:1200+IC900'" +
		"UNH+1+PAOREQ:96:1:IA'MSG+:11'UNT+3+1'UNZ+1+MISMATCH'")

	t.Run("never", func(t *testing.T) {
		gw, sent := contrlNode(t, "never")
		if _, err := gw.Ingest(context.Background(), "AA", paoreq("N1", true)); err != nil {
			t.Fatal(err)
		}
		if lastCONTRL(t, sent, "AA") != nil {
			t.Error("policy never still sent a report")
		}
	})

	t.Run("always", func(t *testing.T) {
		gw, sent := contrlNode(t, "always")
		if _, err := gw.Ingest(context.Background(), "AA", paoreq("A1", false)); err != nil {
			t.Fatal(err)
		}
		if lastCONTRL(t, sent, "AA") == nil {
			t.Error("policy always sent nothing")
		}
	})

	t.Run("errors only", func(t *testing.T) {
		gw, sent := contrlNode(t, "errors")
		if _, err := gw.Ingest(context.Background(), "AA", paoreq("E1", true)); err != nil {
			t.Fatal(err)
		}
		if lastCONTRL(t, sent, "AA") != nil {
			t.Error("policy errors reported a clean interchange")
		}
		if _, err := gw.Ingest(context.Background(), "AA", broken); err != nil {
			t.Fatal(err)
		}
		r := lastCONTRL(t, sent, "AA")
		if r == nil {
			t.Fatal("policy errors did not report a broken interchange")
		}
		if r.Action != edifact.ActionRejected {
			t.Errorf("Action = %q, want 4", r.Action)
		}
		if r.Error != edifact.ReferencesDoNotMatch {
			t.Errorf("Error = %q, want 28 for a UNZ that disagrees with its UNB", r.Error)
		}
	})
}

// contrlFor builds a report a partner would send about an interchange we sent.
func contrlFor(t *testing.T, subjectRef string, action edifact.Action, syntaxErr edifact.SyntaxError) []byte {
	t.Helper()
	r := &edifact.Report{
		ControlRef: subjectRef,
		Sender:     edifact.Party{ID: "1J", Qualifier: "ZZ"},
		Recipient:  edifact.Party{ID: "AA", Qualifier: "ZZ"},
		Action:     action,
		Error:      syntaxErr,
	}
	ic, err := r.Build(edifact.CONTRLOptions{
		Sender: edifact.Party{ID: "AA", Qualifier: "ZZ"}, Recipient: edifact.Party{ID: "1J", Qualifier: "ZZ"},
		ControlRef: "CT" + subjectRef, Date: "260829", Time: "1210",
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := ic.Encode(edifact.EncodeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestInboundCONTRLMarksWhatItAcknowledges(t *testing.T) {
	gw, _ := contrlNode(t, "never")
	ctx := context.Background()
	peer := gw.Peer("AA")

	sentID, err := gw.SendKeyed(ctx, peer, []byte("UNB+...'"), "PAOREQ", "", "", "unb:OUT1")
	if err != nil {
		t.Fatalf("SendKeyed: %v", err)
	}

	if _, err := gw.Ingest(ctx, "AA", contrlFor(t, "OUT1", edifact.ActionAcknowledged, "")); err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	got, err := gw.Store.GetMessage(ctx, sentID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.StatusAcknowledged {
		t.Errorf("Status = %q, want acknowledged", got.Status)
	}
}

func TestInboundCONTRLRejectionIsRecordedAndQueued(t *testing.T) {
	gw, _ := contrlNode(t, "never")
	ctx := context.Background()
	peer := gw.Peer("AA")

	rec := samplePNRForCONTRL(t, gw)
	sentID, err := gw.SendKeyed(ctx, peer, []byte("UNB+...'"), "PAOREQ", rec.ID, "", "unb:OUT2")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := gw.Ingest(ctx, "AA",
		contrlFor(t, "OUT2", edifact.ActionRejected, edifact.InvalidValue)); err != nil {
		t.Fatal(err)
	}

	got, _ := gw.Store.GetMessage(ctx, sentID)
	if got.Status != store.StatusRefused {
		t.Errorf("Status = %q, want refused", got.Status)
	}
	if !strings.Contains(got.Error, "invalid value") {
		t.Errorf("the refusal should say why: %q", got.Error)
	}

	// A refusal is nobody's problem unless something surfaces it: the booking
	// looks sent and the partner is not acting on it.
	items, err := gw.Store.ListQueue(ctx, store.QueueFilter{Queue: store.QueueDivergence})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("expected a divergence queue item, got %d", len(items))
	}
	if items[0].Code != "contrl_rejected" {
		t.Errorf("queue code = %q", items[0].Code)
	}
}

func TestInboundCONTRLForSomethingWeNeverSent(t *testing.T) {
	gw, _ := contrlNode(t, "never")
	ctx := context.Background()

	res, err := gw.Ingest(ctx, "AA", contrlFor(t, "UNKNOWN", edifact.ActionAcknowledged, ""))
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	msg, err := gw.Store.GetMessage(ctx, res.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	// Not an error, but not silence either: our view of the link and theirs
	// disagree about what crossed it.
	if !strings.Contains(msg.Error, "no record of sending") {
		t.Errorf("expected the divergence to be recorded, got %q", msg.Error)
	}
}

func samplePNRForCONTRL(t *testing.T, gw *Gateway) *pnr.PNR {
	t.Helper()
	now := time.Now().UTC()
	rec := &pnr.PNR{
		RecordLocator: "CTR001", Status: pnr.StatusOpen,
		CreatedAt: now, UpdatedAt: now,
		Segments: []pnr.Segment{{Ref: 1, Carrier: "AA", FlightNum: "0050", Status: "HN"}},
	}
	if err := gw.Store.CreatePNR(context.Background(), rec, nil); err != nil {
		t.Fatal(err)
	}
	return rec
}
