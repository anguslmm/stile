// Package server implements the inbound MCP HTTP server.
package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/anguslmm/stile/internal/auth"
	"github.com/anguslmm/stile/internal/config"
	"github.com/anguslmm/stile/internal/health"
	"github.com/anguslmm/stile/internal/jsonrpc"
	"github.com/anguslmm/stile/internal/metrics"
	"github.com/anguslmm/stile/internal/proxy"
	"github.com/anguslmm/stile/internal/router"
)

const supportedProtocolVersion = "2025-11-25"

// Server is the inbound MCP HTTP server.
type Server struct {
	httpServer    *http.Server
	proxy         *proxy.Handler
	router        *router.RouteTable
	tracer        trace.Tracer
	authenticator *auth.Authenticator
}

// AdminRegistrar registers admin routes on a mux.
type AdminRegistrar interface {
	Register(mux *http.ServeMux)
}

// Options configures optional Server behavior.
type Options struct {
	// Authenticator, if non-nil, wraps the MCP endpoint with auth middleware.
	Authenticator *auth.Authenticator
	// AdminAuth, if non-nil, wraps admin endpoints with admin auth middleware.
	AdminAuth func(http.Handler) http.Handler
	// AdminHandler, if non-nil, registers all /admin/ routes.
	AdminHandler AdminRegistrar
	// HealthChecker, if non-nil, enables /healthz and /readyz endpoints.
	HealthChecker *health.Checker
	// Tracer, if non-nil, enables distributed tracing on the request path.
	Tracer trace.Tracer
}

// New creates a Server from config, proxy handler, router, metrics, and options.
// m may be nil to disable the /metrics endpoint.
func New(cfg *config.Config, p *proxy.Handler, rt *router.RouteTable, m *metrics.Metrics, opts *Options) *Server {
	s := &Server{proxy: p, router: rt}
	if opts != nil && opts.Tracer != nil {
		s.tracer = opts.Tracer
	}

	mux := http.NewServeMux()

	var mcpHandler http.Handler = http.HandlerFunc(s.handleMCP)
	if opts != nil && opts.Authenticator != nil {
		s.authenticator = opts.Authenticator
		mcpHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()

			var authSpan trace.Span
			if s.tracer != nil {
				// Extract W3C Trace Context before creating the auth span
				// so it becomes a child of the agent's trace.
				ctx = otel.GetTextMapPropagator().Extract(ctx, propagation.HeaderCarrier(r.Header))
				ctx, authSpan = s.tracer.Start(ctx, "auth")
			}

			caller, err := s.authenticator.Authenticate(r)
			if err != nil {
				if authSpan != nil {
					authSpan.SetStatus(codes.Error, "unauthorized")
					authSpan.End()
				}
				resp := jsonrpc.NewErrorResponse(nil, -32000, "unauthorized")
				data, _ := json.Marshal(resp)
				w.Header().Set("Content-Type", "application/json")
				w.Write(data)
				return
			}
			if caller != nil {
				ctx = auth.ContextWithCaller(ctx, caller)
				if authSpan != nil {
					authSpan.SetAttributes(attribute.String("mcp.caller", caller.Name))
				}
			}
			if authSpan != nil {
				authSpan.End()
			}

			r = r.WithContext(ctx)
			s.handleMCP(w, r)
		})
	}
	mux.Handle("POST /mcp", mcpHandler)

	if opts != nil && opts.AdminHandler != nil {
		// Consolidated admin handler — registers all /admin/ routes.
		adminMux := http.NewServeMux()
		opts.AdminHandler.Register(adminMux)
		var adminRoot http.Handler = adminMux
		if opts.AdminAuth != nil {
			adminRoot = opts.AdminAuth(adminMux)
		}
		mux.Handle("/admin/", adminRoot)
	} else {
		// Fallback: individual admin routes (no caller management).
		var refreshHandler http.Handler = http.HandlerFunc(s.handleRefresh)
		if opts != nil && opts.AdminAuth != nil {
			refreshHandler = opts.AdminAuth(refreshHandler)
		}
		mux.Handle("POST /admin/refresh", refreshHandler)
	}

	if opts != nil && opts.HealthChecker != nil {
		mux.HandleFunc("GET /healthz", opts.HealthChecker.HandleLiveness)
		mux.HandleFunc("GET /readyz", opts.HealthChecker.HandleReadiness)
	}

	if m != nil {
		mux.Handle("GET /metrics", m.Handler())
	}

	s.httpServer = &http.Server{
		Addr:    cfg.Server().Address(),
		Handler: mux,
	}

	if tlsCfg := cfg.Server().TLS(); tlsCfg != nil {
		tc, err := buildServerTLSConfig(tlsCfg)
		if err != nil {
			slog.Error("build server TLS config failed", "error", err)
			os.Exit(1)
		}
		s.httpServer.TLSConfig = tc
	}

	return s
}

// ListenAndServe starts the HTTP server. If TLS is configured, it serves HTTPS.
func (s *Server) ListenAndServe() error {
	if s.httpServer.TLSConfig != nil {
		return s.httpServer.ListenAndServeTLS("", "")
	}
	return s.httpServer.ListenAndServe()
}

// TLSEnabled reports whether the server is configured for TLS.
func (s *Server) TLSEnabled() bool {
	return s.httpServer.TLSConfig != nil
}

// Shutdown gracefully shuts down the server.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}

// Handler returns the underlying http.Handler for use with httptest.
func (s *Server) Handler() http.Handler {
	return s.httpServer.Handler
}

const maxRequestBody = 10 << 20 // 10 MB
const maxBatchSize = 100

func (s *Server) handleMCP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if s.tracer != nil {
		// Extract W3C Trace Context if auth middleware hasn't already done so.
		if !trace.SpanFromContext(ctx).SpanContext().IsValid() {
			ctx = otel.GetTextMapPropagator().Extract(ctx, propagation.HeaderCarrier(r.Header))
		}
		var span trace.Span
		ctx, span = s.tracer.Start(ctx, "handleMCP")
		defer span.End()
		r = r.WithContext(ctx)
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBody+1))
	if err != nil {
		writeError(w, nil, jsonrpc.CodeParseError, "failed to read request body")
		return
	}
	if len(body) > maxRequestBody {
		writeError(w, nil, jsonrpc.CodeInvalidRequest, "request body too large")
		return
	}

	requests, isBatch, err := jsonrpc.ParseMessage(body)
	if err != nil {
		slog.WarnContext(ctx, "mcp parse error", "error", err, "body_prefix", truncate(string(body), 200))
		writeError(w, nil, jsonrpc.CodeParseError, err.Error())
		return
	}

	if len(requests) > maxBatchSize {
		writeError(w, nil, jsonrpc.CodeInvalidRequest, "batch too large")
		return
	}

	for _, req := range requests {
		slog.InfoContext(ctx, "mcp request", "method", req.Method, "id", req.ID)
	}

	if !isBatch && len(requests) == 1 {
		s.handleSingle(w, r, requests[0])
		return
	}

	// Batch: process each request and collect responses.
	var responses []*jsonrpc.Response
	for _, req := range requests {
		if req.IsNotification() {
			s.dispatchNotification(req)
			continue
		}
		resp := s.dispatch(r.Context(), req)
		if resp != nil {
			responses = append(responses, resp)
		}
	}

	if len(responses) == 0 {
		// All entries were notifications — no response body.
		w.WriteHeader(http.StatusAccepted)
		return
	}

	data, err := json.Marshal(responses)
	if err != nil {
		writeError(w, nil, jsonrpc.CodeInternalError, "failed to marshal batch response")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

func (s *Server) handleSingle(w http.ResponseWriter, r *http.Request, req *jsonrpc.Request) {
	if req.IsNotification() {
		s.dispatchNotification(req)
		w.WriteHeader(http.StatusAccepted)
		return
	}

	// tools/call is special: it may stream SSE, so it writes directly to w.
	if req.Method == "tools/call" {
		ctx := r.Context()
		if s.tracer != nil {
			var span trace.Span
			ctx, span = s.tracer.Start(ctx, "dispatch", trace.WithAttributes(
				attribute.String("mcp.method", "tools/call"),
			))
			defer span.End()
		}
		s.proxy.HandleToolsCall(ctx, w, req)
		return
	}

	resp := s.dispatch(r.Context(), req)
	if resp != nil {
		data, err := json.Marshal(resp)
		if err != nil {
			writeError(w, req.ID, jsonrpc.CodeInternalError, "failed to marshal response")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(data)
	}
}

func (s *Server) dispatch(ctx context.Context, req *jsonrpc.Request) *jsonrpc.Response {
	switch req.Method {
	case "initialize":
		return s.handleInitialize(ctx, req)
	case "ping":
		return s.handlePing(req)
	case "tools/list":
		return s.handleToolsList(ctx, req)
	case "tools/call":
		// Handled separately in handleSingle for SSE support.
		// In batch mode, we use transport.Send which resolves SSE to a final response.
		return jsonrpc.NewErrorResponse(req.ID, jsonrpc.CodeInternalError, "tools/call in batch not supported")
	default:
		return jsonrpc.NewErrorResponse(req.ID, jsonrpc.CodeMethodNotFound, "method not found")
	}
}

func (s *Server) dispatchNotification(req *jsonrpc.Request) {
	// notifications/initialized: silently accept.
	// All other notifications: silently ignore.
}

func (s *Server) handleInitialize(ctx context.Context, req *jsonrpc.Request) *jsonrpc.Response {
	var params struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if req.Params != nil {
		json.Unmarshal(req.Params, &params)
	}

	if params.ProtocolVersion != "" && params.ProtocolVersion != supportedProtocolVersion {
		return jsonrpc.NewErrorResponse(req.ID, jsonrpc.CodeInvalidParams,
			"unsupported protocol version: "+params.ProtocolVersion)
	}

	// Include the tool list in capabilities so clients don't need a separate tools/list call.
	tools := s.proxy.FilteredTools(ctx)

	result := map[string]any{
		"protocolVersion": supportedProtocolVersion,
		"capabilities": map[string]any{
			"tools": map[string]any{
				"listChanged": true,
			},
		},
		"serverInfo": map[string]any{"name": "stile", "version": "0.1.0"},
		"tools":      tools,
	}
	resp, err := jsonrpc.NewResponse(req.ID, result)
	if err != nil {
		return jsonrpc.NewErrorResponse(req.ID, jsonrpc.CodeInternalError, "failed to build initialize response")
	}
	return resp
}

func (s *Server) handlePing(req *jsonrpc.Request) *jsonrpc.Response {
	resp, err := jsonrpc.NewResponse(req.ID, map[string]any{})
	if err != nil {
		return jsonrpc.NewErrorResponse(req.ID, jsonrpc.CodeInternalError, "failed to build ping response")
	}
	return resp
}

func (s *Server) handleToolsList(ctx context.Context, req *jsonrpc.Request) *jsonrpc.Response {
	resp, err := s.proxy.HandleToolsList(ctx, req.ID)
	if err != nil {
		return jsonrpc.NewErrorResponse(req.ID, jsonrpc.CodeInternalError, err.Error())
	}
	return resp
}

func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	result := s.router.Refresh(r.Context())
	data, err := json.Marshal(result)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

func buildServerTLSConfig(cfg *config.ServerTLSConfig) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(cfg.CertFile(), cfg.KeyFile())
	if err != nil {
		return nil, fmt.Errorf("load server certificate: %w", err)
	}

	tc := &tls.Config{
		MinVersion:   parseTLSVersion(cfg.MinVersion()),
		Certificates: []tls.Certificate{cert},
	}

	if cfg.ClientCAFile() != "" {
		caCert, err := os.ReadFile(cfg.ClientCAFile())
		if err != nil {
			return nil, fmt.Errorf("read client CA file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caCert) {
			return nil, fmt.Errorf("failed to parse client CA certificate from %s", cfg.ClientCAFile())
		}
		tc.ClientCAs = pool
		tc.ClientAuth = tls.RequireAndVerifyClientCert
	}

	return tc, nil
}

func parseTLSVersion(v string) uint16 {
	switch v {
	case "1.0":
		return tls.VersionTLS10
	case "1.1":
		return tls.VersionTLS11
	case "1.3":
		return tls.VersionTLS13
	default:
		return tls.VersionTLS12
	}
}

func writeError(w http.ResponseWriter, id jsonrpc.ID, code int, message string) {
	resp := jsonrpc.NewErrorResponse(id, code, message)
	data, _ := json.Marshal(resp)
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
