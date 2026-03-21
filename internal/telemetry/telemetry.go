// Package telemetry initializes OpenTelemetry tracing and provides
// a tracer for the Stile gateway.
package telemetry

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/anguslmm/stile/internal/config"
)

const tracerName = "github.com/anguslmm/stile"

// Provider wraps the tracer provider and exposes a Tracer.
// Call Shutdown during graceful shutdown to flush pending spans.
type Provider struct {
	tp     *sdktrace.TracerProvider
	tracer trace.Tracer
}

// Init creates and registers a global tracer provider based on config.
// If tracing is disabled, it registers a no-op provider and returns a
// Provider whose Shutdown is a no-op.
func Init(ctx context.Context, cfg config.TelemetryConfig) (*Provider, error) {
	tc := cfg.Traces()
	if !tc.Enabled() {
		noopTracer := noop.NewTracerProvider().Tracer(tracerName)
		return &Provider{tracer: noopTracer}, nil
	}

	exporter, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpoint(tc.Endpoint()),
		otlptracehttp.WithInsecure(),
	)
	if err != nil {
		return nil, fmt.Errorf("telemetry: create OTLP exporter: %w", err)
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName("stile"),
			semconv.ServiceVersion("0.1.0"),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("telemetry: create resource: %w", err)
	}

	sampler := sdktrace.ParentBased(sdktrace.TraceIDRatioBased(tc.SampleRate()))

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sampler),
	)

	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})

	return &Provider{
		tp:     tp,
		tracer: tp.Tracer(tracerName),
	}, nil
}

// Tracer returns the tracer for creating spans.
func (p *Provider) Tracer() trace.Tracer {
	return p.tracer
}

// Shutdown flushes pending spans and shuts down the tracer provider.
func (p *Provider) Shutdown(ctx context.Context) error {
	if p.tp == nil {
		return nil
	}
	return p.tp.Shutdown(ctx)
}
