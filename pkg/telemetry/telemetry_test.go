package telemetry

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestDisabledByDefault(t *testing.T) {
	shutdown, err := Setup(context.Background(), Config{})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	defer shutdown(context.Background())

	if (Config{}).Enabled() {
		t.Error("tracing with no endpoint must be disabled")
	}
	// A no-op provider still gives a usable span, so call sites never have to
	// check whether tracing is on.
	ctx, span := Start(context.Background(), "test")
	span.End()
	if id, _ := IDs(ctx); id != "" {
		t.Errorf("a disabled provider should mint no identifiers, got %q", id)
	}
}

func TestIDsAreRecordable(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	ctx, span := tp.Tracer(ScopeName).Start(context.Background(), "jetway.ingest")
	traceID, spanID := IDs(ctx)
	span.End()

	if len(traceID) != 32 || len(spanID) != 16 {
		t.Errorf("identifiers = %q / %q, want 32 and 16 hex characters", traceID, spanID)
	}
	if traceID == strings.Repeat("0", 32) {
		t.Error("trace id is all zeroes")
	}
}

// Spans leave the building, usually to a collector somebody else runs, and
// outlive the intention to keep them. Nothing about a passenger goes in one.
func TestNoPassengerDataInAttributeVocabulary(t *testing.T) {
	forbidden := []string{"name", "surname", "given", "passenger.name", "contact",
		"email", "phone", "document.holder", "frequent"}
	// The vocabulary is the promise: if no attribute is named for passenger
	// data, no call site can absent-mindedly attach it under a blessed key.
	keys := []string{
		string(AttrPeer), string(AttrCarrier), string(AttrFormat), string(AttrTransport),
		string(AttrMessageID), string(AttrMessageKind), string(AttrMessageSize),
		string(AttrLocator), string(AttrRecordID), string(AttrSegmentCount),
		string(AttrPaxCount), string(AttrSeats), string(AttrFreeSale), string(AttrOutcome),
		string(AttrDocumentType), string(AttrDocumentNumber), string(AttrRFIC),
		string(AttrRFISC), string(AttrAmount), string(AttrCurrency),
		string(AttrQueue), string(AttrQueueCode), string(AttrDivergence),
	}
	for _, k := range keys {
		for _, bad := range forbidden {
			// jetway.record.passengers is a count, not a name; guard the
			// substring check against it.
			if k == string(AttrPaxCount) {
				continue
			}
			if strings.Contains(k, bad) {
				t.Errorf("attribute %q looks like it carries passenger data", k)
			}
		}
	}
}

func TestOTLPJSONEncoding(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	_, span := tp.Tracer(ScopeName).Start(context.Background(), "jetway.book")
	span.SetAttributes(AttrCarrier.String("BA"), AttrSeats.Int(3), AttrFreeSale.Bool(true))
	span.End()

	spans := exp.GetSpans().Snapshots()
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	if len(spans) != 1 {
		t.Fatalf("expected one span, got %d", len(spans))
	}

	e := &otlpJSON{resource: []keyValue{{Key: "service.name", Value: stringValue("jetway")}}}
	var buf strings.Builder
	// Encode through the same path the exporter uses.
	out := make([]otlpSpan, 0, 1)
	for _, s := range spans {
		out = append(out, otlpSpan{
			TraceID: s.SpanContext().TraceID().String(),
			SpanID:  s.SpanContext().SpanID().String(),
			Name:    s.Name(), Kind: int(s.SpanKind()),
			StartTimeUnixNano: nano(s.StartTime()), EndTimeUnixNano: nano(s.EndTime()),
			Attributes: attrs(s.Attributes()), Status: statusOf(s),
		})
	}
	p := otlpPayload{ResourceSpans: []otlpResourceSpans{{
		Resource:   otlpResource{Attributes: e.resource},
		ScopeSpans: []otlpScopeSpans{{Scope: otlpScope{Name: ScopeName}, Spans: out}},
	}}}
	enc := json.NewEncoder(&buf)
	if err := enc.Encode(p); err != nil {
		t.Fatalf("encode: %v", err)
	}
	body := buf.String()

	// The shapes a collector requires: hex identifiers and 64-bit numbers as
	// strings, because a JSON number is a double and would round them.
	for _, want := range []string{
		`"resourceSpans"`, `"scopeSpans"`, `"traceId"`, `"spanId"`,
		`"startTimeUnixNano":"`, `"stringValue"`, `"intValue":"`, `"boolValue"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("OTLP JSON missing %s:\n%s", want, body)
		}
	}
	var round map[string]any
	if err := json.Unmarshal([]byte(body), &round); err != nil {
		t.Fatalf("collector would not parse this: %v", err)
	}
}
