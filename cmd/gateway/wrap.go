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

	"github.com/anguslmm/stile/internal/config"
	"github.com/anguslmm/stile/internal/transport"
	"github.com/anguslmm/stile/internal/wrap"
)

func runWrap(args []string) {
	fs := flag.NewFlagSet("wrap", flag.ExitOnError)
	command := fs.String("command", "", "command to run (required)")
	port := fs.Int("port", 9090, "listen port (overridden by --address)")
	address := fs.String("address", "", "full listen address (overrides --port)")

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

	handler := wrap.NewHandler(tr)
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
		if err := tr.Close(); err != nil {
			slog.Error("transport close error", "error", err)
		}
		slog.Info("wrap server stopped")
		os.Exit(0)
	}()

	slog.Info("stile wrap listening",
		"address", listenAddr,
		"command", *command,
	)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("wrap server error", "error", err)
		os.Exit(1)
	}
}

// multiFlag implements flag.Value for repeatable string flags.
type multiFlag []string

func (f *multiFlag) String() string { return strings.Join(*f, ", ") }
func (f *multiFlag) Set(v string) error {
	*f = append(*f, v)
	return nil
}
