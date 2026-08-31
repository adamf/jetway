package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// otlpJSON exports spans as OTLP over HTTP with a JSON body.
//
// The protocol permits JSON as well as protobuf, and every OTel collector
// accepts it. Taking that option keeps the dependency tree at twenty-two
// modules instead of ninety-six: the protobuf exporter pulls in grpc,
// protobuf and grpc-gateway, which is the same stack internal/metrics declined
// for the same reason. Carriers audit this tree.
//
// The encoding below is small because OTLP's JSON mapping is small: identifiers
// are hex, 64-bit numbers are strings, and attribute values are tagged unions.
type otlpJSON struct {
	endpoint string
	headers  map[string]string
	client   *http.Client
	resource []keyValue
}

type keyValue struct {
	Key   string   `json:"key"`
	Value anyValue `json:"value"`
}

// anyValue is OTLP's tagged union for attribute values.
type anyValue struct {
	String *string  `json:"stringValue,omitempty"`
	Bool   *bool    `json:"boolValue,omitempty"`
	Int    *string  `json:"intValue,omitempty"`
	Double *float64 `json:"doubleValue,omitempty"`
}

func stringValue(s string) anyValue { return anyValue{String: &s} }

func attrValue(v attribute.Value) anyValue {
	switch v.Type() {
	case attribute.BOOL:
		b := v.AsBool()
		return anyValue{Bool: &b}
	case attribute.INT64:
		// 64-bit numbers travel as strings, because JSON numbers are doubles
		// and a trace identifier or a byte count must not be rounded.
		s := strconv.FormatInt(v.AsInt64(), 10)
		return anyValue{Int: &s}
	case attribute.FLOAT64:
		f := v.AsFloat64()
		return anyValue{Double: &f}
	default:
		return stringValue(v.Emit())
	}
}

func attrs(kv []attribute.KeyValue) []keyValue {
	if len(kv) == 0 {
		return nil
	}
	out := make([]keyValue, 0, len(kv))
	for _, a := range kv {
		out = append(out, keyValue{Key: string(a.Key), Value: attrValue(a.Value)})
	}
	return out
}

type otlpSpan struct {
	TraceID           string      `json:"traceId"`
	SpanID            string      `json:"spanId"`
	ParentSpanID      string      `json:"parentSpanId,omitempty"`
	Name              string      `json:"name"`
	Kind              int         `json:"kind"`
	StartTimeUnixNano string      `json:"startTimeUnixNano"`
	EndTimeUnixNano   string      `json:"endTimeUnixNano"`
	Attributes        []keyValue  `json:"attributes,omitempty"`
	Events            []otlpEvent `json:"events,omitempty"`
	Status            otlpStatus  `json:"status"`
}

type otlpEvent struct {
	TimeUnixNano string     `json:"timeUnixNano"`
	Name         string     `json:"name"`
	Attributes   []keyValue `json:"attributes,omitempty"`
}

type otlpStatus struct {
	Message string `json:"message,omitempty"`
	Code    int    `json:"code"`
}

type otlpResource struct {
	Attributes []keyValue `json:"attributes,omitempty"`
}

type otlpScope struct {
	Name string `json:"name"`
}

type otlpScopeSpans struct {
	Scope otlpScope  `json:"scope"`
	Spans []otlpSpan `json:"spans"`
}

type otlpResourceSpans struct {
	Resource   otlpResource     `json:"resource"`
	ScopeSpans []otlpScopeSpans `json:"scopeSpans"`
}

type otlpPayload struct {
	ResourceSpans []otlpResourceSpans `json:"resourceSpans"`
}

func nano(t time.Time) string { return strconv.FormatInt(t.UnixNano(), 10) }

func statusOf(s sdktrace.ReadOnlySpan) otlpStatus {
	switch s.Status().Code {
	case codes.Ok:
		return otlpStatus{Code: 1}
	case codes.Error:
		return otlpStatus{Code: 2, Message: s.Status().Description}
	}
	return otlpStatus{Code: 0}
}

// ExportSpans implements sdktrace.SpanExporter.
func (e *otlpJSON) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	if len(spans) == 0 {
		return nil
	}
	out := make([]otlpSpan, 0, len(spans))
	for _, s := range spans {
		sc := s.SpanContext()
		os := otlpSpan{
			TraceID:           sc.TraceID().String(),
			SpanID:            sc.SpanID().String(),
			Name:              s.Name(),
			Kind:              int(s.SpanKind()),
			StartTimeUnixNano: nano(s.StartTime()),
			EndTimeUnixNano:   nano(s.EndTime()),
			Attributes:        attrs(s.Attributes()),
			Status:            statusOf(s),
		}
		if p := s.Parent(); p.HasSpanID() {
			os.ParentSpanID = p.SpanID().String()
		}
		for _, ev := range s.Events() {
			os.Events = append(os.Events, otlpEvent{
				TimeUnixNano: nano(ev.Time), Name: ev.Name, Attributes: attrs(ev.Attributes),
			})
		}
		out = append(out, os)
	}
	p := otlpPayload{ResourceSpans: []otlpResourceSpans{{
		Resource:   otlpResource{Attributes: e.resource},
		ScopeSpans: []otlpScopeSpans{{Scope: otlpScope{Name: ScopeName}, Spans: out}},
	}}}

	body, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("telemetry: encode spans: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range e.headers {
		req.Header.Set(k, v)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return fmt.Errorf("telemetry: export spans: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("telemetry: collector answered %s", resp.Status)
	}
	return nil
}

// Shutdown implements sdktrace.SpanExporter.
func (e *otlpJSON) Shutdown(context.Context) error { return nil }

var _ sdktrace.SpanExporter = (*otlpJSON)(nil)
var _ = trace.SpanKindServer
