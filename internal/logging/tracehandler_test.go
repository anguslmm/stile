package logging

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestTraceHandlerIncludesTraceFields(t *testing.T) {
	var buf bytes.Buffer
	inner := slog.NewJSONHandler(&buf, nil)
	handler := NewTraceHandler(inner)
	logger := slog.New(handler)

	// Create a real span to get a valid trace context.
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	tracer := tp.Tracer("test")
	ctx, span := tracer.Start(context.Background(), "test-span")
	defer span.End()

	logger.InfoContext(ctx, "hello from traced context")

	output := buf.String()
	if !strings.Contains(output, "trace_id") {
		t.Errorf("expected trace_id in log output, got: %s", output)
	}
	if !strings.Contains(output, "span_id") {
		t.Errorf("expected span_id in log output, got: %s", output)
	}
	if !strings.Contains(output, span.SpanContext().TraceID().String()) {
		t.Errorf("expected actual trace ID %s in output, got: %s", span.SpanContext().TraceID(), output)
	}
}

func TestTraceHandlerNoTraceFieldsWithoutSpan(t *testing.T) {
	var buf bytes.Buffer
	inner := slog.NewJSONHandler(&buf, nil)
	handler := NewTraceHandler(inner)
	logger := slog.New(handler)

	logger.Info("hello without trace")

	output := buf.String()
	if strings.Contains(output, "trace_id") {
		t.Errorf("unexpected trace_id in log output: %s", output)
	}
	if strings.Contains(output, "span_id") {
		t.Errorf("unexpected span_id in log output: %s", output)
	}
}

func TestTraceHandlerWithAttrs(t *testing.T) {
	var buf bytes.Buffer
	inner := slog.NewJSONHandler(&buf, nil)
	handler := NewTraceHandler(inner)
	handler = handler.WithAttrs([]slog.Attr{slog.String("service", "stile")}).(*TraceHandler)
	logger := slog.New(handler)

	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	tracer := tp.Tracer("test")
	ctx, span := tracer.Start(context.Background(), "test-span")
	defer span.End()

	logger.InfoContext(ctx, "with attrs")

	output := buf.String()
	if !strings.Contains(output, "trace_id") {
		t.Errorf("expected trace_id: %s", output)
	}
	if !strings.Contains(output, `"service":"stile"`) {
		t.Errorf("expected service attr: %s", output)
	}
}
