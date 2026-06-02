package obs

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
)

// SetupTracing installs an OTLP/gRPC tracer provider as the global default
// and returns its Shutdown function. Callers must defer the returned
// shutdown so the span batch is flushed before exit; otherwise the last
// few spans of a run will be lost.
//
// When otlpEndpoint is empty (the dev default), this is a noop and returns
// a Shutdown that does nothing. That keeps cmd/* main functions identical
// across dev and prod — they always call SetupTracing, the endpoint config
// decides whether tracing is actually live.
//
// The W3C TraceContext propagator is installed unconditionally on the
// success path so cross-service spans stitch together correctly when a
// downstream service is traced and upstream is not.
func SetupTracing(ctx context.Context, service, otlpEndpoint string) (func(context.Context) error, error) {
	if otlpEndpoint == "" {
		return func(context.Context) error { return nil }, nil
	}
	exp, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(otlpEndpoint),
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		return nil, fmt.Errorf("otlp exporter: %w", err)
	}
	// resource.New only returns a partial resource when individual detectors
	// disagree; the merge fault is non-fatal here because the ServiceName
	// attribute we explicitly set is what dashboards key off.
	res, _ := resource.New(ctx, resource.WithAttributes(semconv.ServiceName(service)))
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	return tp.Shutdown, nil
}
