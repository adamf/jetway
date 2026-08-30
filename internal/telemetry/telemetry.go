// Package telemetry gives the gateway OpenTelemetry tracing.
//
// # Why the exporter is hand-rolled
//
// The spans, the context and the propagation are real OpenTelemetry: the API
// and SDK are imported and used as intended, so anything that understands OTel
// understands this. What is written here is only the exporter, because OTLP
// permits a JSON body over HTTP and every collector accepts it. Taking that
// option keeps the module tree at twenty-two rather than ninety-six; the
// protobuf exporter brings grpc, protobuf and grpc-gateway, which is the same
// stack internal/metrics declined for the same reason. Carriers audit this
// tree.
//
// # What goes in a span, and what must not
//
// Spans leave the building. They go to a collector that is very often operated
// by somebody else, and they are kept for longer than anybody intends. So no
// passenger data goes in one: no names, no contacts, no documents, no frequent
// flyer numbers. Locators and carrier codes do, because they are the
// identifiers an operator needs to follow a booking through the system and are
// meaningless without the record they point at.
//
// The attributes here are deliberately useful to two audiences at once. An
// operator wants to know which link is slow and what is failing; the commercial
// side wants to know how often a carrier refuses, how much of a booking sells
// without asking, and what ancillary revenue was issued against what. Both
// questions are answered by the same spans.
package telemetry

import (
	"context"
	"net/http"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// ScopeName names the instrumentation scope in exported spans.
const ScopeName = "github.com/adamf/jetway"

// Config configures tracing.
type Config struct {
	// Endpoint is an OTLP HTTP traces endpoint, e.g.
	// http://collector:4318/v1/traces. Empty disables tracing entirely, which
	// is the default: a gateway with nowhere to send spans should not pay to
	// make them.
	Endpoint string
	// Headers are sent with every export, for collectors behind an API key.
	Headers map[string]string
	// ServiceName identifies this node. Defaults to "jetway".
	ServiceName string
	// Environment is an optional deployment label.
	Environment string
	// SampleRatio is the head sampling ratio, 0 to 1. Zero means one, because
	// a gateway that has been told where to send spans wants them.
	SampleRatio float64
	// Timeout bounds one export. Zero uses ten seconds.
	Timeout time.Duration
}

// Enabled reports whether tracing will do anything.
func (c Config) Enabled() bool { return strings.TrimSpace(c.Endpoint) != "" }

// Setup installs a tracer provider and returns a shutdown function.
//
// With no endpoint it installs a no-op provider and returns immediately, so
// every call site can create spans unconditionally and pay nothing.
func Setup(ctx context.Context, cfg Config) (shutdown func(context.Context) error, err error) {
	// The propagator is installed either way. A partner that sends us a
	// traceparent should have it honoured even if we are not exporting, so
	// their trace is not silently broken by our configuration.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))

	if !cfg.Enabled() {
		otel.SetTracerProvider(noop.NewTracerProvider())
		return func(context.Context) error { return nil }, nil
	}

	service := cfg.ServiceName
	if service == "" {
		service = "jetway"
	}
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	res := []keyValue{
		{Key: "service.name", Value: stringValue(service)},
		{Key: "telemetry.sdk.language", Value: stringValue("go")},
		{Key: "telemetry.sdk.name", Value: stringValue("opentelemetry")},
	}
	if cfg.Environment != "" {
		res = append(res, keyValue{Key: "deployment.environment", Value: stringValue(cfg.Environment)})
	}

	exp := &otlpJSON{
		endpoint: cfg.Endpoint,
		headers:  cfg.Headers,
		client:   &http.Client{Timeout: timeout},
		resource: res,
	}
	ratio := cfg.SampleRatio
	if ratio <= 0 {
		ratio = 1
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(ratio))),
	)
	otel.SetTracerProvider(tp)
	return tp.Shutdown, nil
}

// Tracer returns the gateway's tracer.
func Tracer() trace.Tracer { return otel.Tracer(ScopeName) }

// Start begins a span. It is a thin wrapper so call sites need not import the
// OTel packages directly.
func Start(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	return Tracer().Start(ctx, name, trace.WithAttributes(attrs...))
}

// IDs returns the trace and span identifiers on a context, or empty strings.
//
// They are recorded against the message in the log, which is what turns "what
// happened to this message" into a link rather than a search.
func IDs(ctx context.Context) (traceID, spanID string) {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return "", ""
	}
	return sc.TraceID().String(), sc.SpanID().String()
}

// FromHTTP returns a context carrying any trace the caller propagated.
//
// Teletype and EDIFACT links carry no place to put a traceparent, so a message
// arriving on one starts a new trace. HTTP does carry one, so an agent or an
// NDC client that is already tracing keeps a single trace across the boundary.
func FromHTTP(ctx context.Context, h http.Header) context.Context {
	return otel.GetTextMapPropagator().Extract(ctx, propagation.HeaderCarrier(h))
}

// ToHTTP writes the current trace into outgoing headers.
func ToHTTP(ctx context.Context, h http.Header) {
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(h))
}

// Fail marks a span as failed and records the error.
func Fail(span trace.Span, err error) {
	if err == nil {
		return
	}
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
}
