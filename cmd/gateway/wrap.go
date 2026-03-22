package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/anguslmm/stile/internal/config"
	"github.com/anguslmm/stile/internal/transport"
	"github.com/anguslmm/stile/internal/wrap"
)

func runWrap(args []string) {
	fs := flag.NewFlagSet("wrap", flag.ExitOnError)
	command := fs.String("command", "", "command to run (required)")
	port := fs.Int("port", 9090, "listen port (overridden by --address)")
	address := fs.String("address", "", "full listen address (overrides --port)")

	otelEndpoint := fs.String("otel-endpoint", "", "OTLP endpoint for tracing (e.g. localhost:4318); disabled when empty")

	var envVars multiFlag
	fs.Var(&envVars, "env", "extra env vars for the child (KEY=VALUE, repeatable)")

	fs.Parse(args)

	if *command == "" {
		fmt.Fprintln(os.Stderr, "error: --command is required")
		fmt.Fprintln(os.Stderr, "usage: stile wrap --command \"npx -y @modelcontextprotocol/server-github\" [--port 9090]")
		os.Exit(1)
	}

	listenAddr := fmt.Sprintf(":%d", *port)
	if *address != "" {
		listenAddr = *address
	}

	// Parse command string into args (simple shell-like split).
	cmdParts := strings.Fields(*command)

	// Parse env vars into a map.
	env := make(map[string]string)
	for _, e := range envVars {
		k, v, ok := strings.Cut(e, "=")
		if !ok {
			fmt.Fprintf(os.Stderr, "error: invalid --env value %q (expected KEY=VALUE)\n", e)
			os.Exit(1)
		}
		env[k] = v
	}

	cfg := config.NewStdioUpstreamConfig("wrapped", cmdParts, env)
	tr, err := transport.NewStdioTransport(cfg)
	if err != nil {
		slog.Error("create transport failed", "error", err)
		os.Exit(1)
	}

	// Initialize tracing if --otel-endpoint is set.
	var tracer trace.Tracer
	var tp *sdktrace.TracerProvider
	if *otelEndpoint != "" {
		tp, tracer, err = initWrapTracer(context.Background(), *otelEndpoint)
		if err != nil {
			slog.Error("init tracing failed", "error", err)
			os.Exit(1)
		}
		slog.Info("tracing enabled", "endpoint", *otelEndpoint)
	}

	var handlerOpts []wrap.Option
	if tracer != nil {
		handlerOpts = append(handlerOpts, wrap.WithTracer(tracer))
	}

	handler := wrap.NewHandler(tr, handlerOpts...)
	srv := &http.Server{
		Addr:    listenAddr,
		Handler: handler.ServeMux(),
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		slog.Info("shutting down wrap server...")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if err := srv.Shutdown(ctx); err != nil {
			slog.Error("wrap server shutdown error", "error", err)
		}
		if tp != nil {
			if err := tp.Shutdown(ctx); err != nil {
				slog.Error("tracer shutdown error", "error", err)
			}
		}
		if err := tr.Close(); err != nil {
			slog.Error("transport close error", "error", err)
		}
		slog.Info("wrap server stopped")
		os.Exit(0)
	}()

	slog.Info("stile wrap listening",
		"address", listenAddr,
		"command", *command,
		"tracing", *otelEndpoint != "",
	)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("wrap server error", "error", err)
		os.Exit(1)
	}
}

// initWrapTracer creates an OTLP tracer provider for the wrap subcommand.
func initWrapTracer(ctx context.Context, endpoint string) (*sdktrace.TracerProvider, trace.Tracer, error) {
	exporter, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpoint(endpoint),
		otlptracehttp.WithInsecure(),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("create OTLP exporter: %w", err)
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName("stile-wrap"),
			semconv.ServiceVersion("0.1.0"),
		),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("create resource: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.AlwaysSample())),
	)

	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})

	tracer := tp.Tracer("github.com/anguslmm/stile/wrap")
	return tp, tracer, nil
}

// multiFlag implements flag.Value for repeatable string flags.
type multiFlag []string

func (f *multiFlag) String() string { return strings.Join(*f, ", ") }
func (f *multiFlag) Set(v string) error {
	*f = append(*f, v)
	return nil
}
