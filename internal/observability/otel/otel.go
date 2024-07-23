// Package otel wires the OTel tracer and exposes the WithServerSpan
// HTTP middleware.
package otel

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// ExporterNone returns the SDK no-op tracer; span recording is a no-op
// but the WithServerSpan middleware still pays the propagator-extract
// + statusRecorder cost (see BenchmarkServerSpan_NoopTracer).
const ExporterNone = "none"

// ExporterOTLP wires an OTLP gRPC exporter against Config.Endpoint.
const ExporterOTLP = "otlp"

// Config drives Bootstrap. The cmd shell binds these via --otel-* flags.
type Config struct {
	Exporter string
	// Endpoint is required when Exporter is ExporterOTLP; ignored otherwise.
	Endpoint            string
	InstrumentationName string
}

// Shutdown drains pending spans and closes the exporter. Callers
// defer it at process exit. The no-op tracer's Shutdown returns nil
// immediately.
type Shutdown func(ctx context.Context) error

// Bootstrap returns a Tracer + Shutdown for the configured exporter.
// On the OTLP path Bootstrap sets the process-global TracerProvider
// and TextMapPropagator; the no-op path leaves the globals untouched.
// Unknown Exporter values return an error so a typo'd config surfaces
// at boot rather than silently defaulting.
func Bootstrap(ctx context.Context, cfg Config) (trace.Tracer, Shutdown, error) {
	switch strings.ToLower(strings.TrimSpace(cfg.Exporter)) {
	case "", ExporterNone:
		tracer := trace.NewNoopTracerProvider().Tracer(cfg.InstrumentationName)
		return tracer, func(context.Context) error { return nil }, nil
	case ExporterOTLP:
		if strings.TrimSpace(cfg.Endpoint) == "" {
			return nil, nil, errors.New("otel: endpoint required when exporter=otlp")
		}
		return bootstrapOTLP(ctx, cfg)
	default:
		return nil, nil, fmt.Errorf("otel: unknown exporter %q (want %s|%s)", cfg.Exporter, ExporterNone, ExporterOTLP)
	}
}

func bootstrapOTLP(ctx context.Context, cfg Config) (trace.Tracer, Shutdown, error) {
	exp, err := otlptrace.New(ctx, otlptracegrpc.NewClient(
		otlptracegrpc.WithEndpoint(cfg.Endpoint),
		otlptracegrpc.WithInsecure(),
	))
	if err != nil {
		return nil, nil, fmt.Errorf("otel: otlp exporter: %w", err)
	}
	res, err := resource.New(ctx)
	if err != nil {
		_ = exp.Shutdown(ctx)
		return nil, nil, fmt.Errorf("otel: resource: %w", err)
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
	return tp.Tracer(cfg.InstrumentationName), tp.Shutdown, nil
}

// WithServerSpan returns an HTTP middleware that opens one
// SpanKindServer span per inbound request named
// `registry.http.<METHOD>.<route>`. W3C traceparent extracted from
// r.Header so an upstream parent trace becomes the parent of this
// span. The route parameter MUST be the matched-route template —
// passing r.URL.Path on a parameterised path explodes http.route
// cardinality. Span attributes: http.method, http.route,
// http.status_code. Status 5xx marks the span Error; 4xx is left OK
// (operator-side mistake, not a registry failure).
func WithServerSpan(tracer trace.Tracer, route string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if tracer == nil {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
			ctx, span := tracer.Start(ctx, spanName(r.Method, route),
				trace.WithSpanKind(trace.SpanKindServer),
				trace.WithAttributes(
					attribute.String("http.method", r.Method),
					attribute.String("http.route", route),
				),
			)
			defer span.End()

			sw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(sw, r.WithContext(ctx))

			span.SetAttributes(attribute.Int("http.status_code", sw.status))
			if sw.status >= 500 {
				span.SetStatus(codes.Error, http.StatusText(sw.status))
			}
		})
	}
}

func spanName(method, route string) string {
	if route == "" {
		return "registry.http." + method
	}
	return "registry.http." + method + "." + route
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}
