// Package wrap implements a stdio-to-HTTP adapter that exposes a stdio
// MCP server as a Streamable HTTP endpoint.
package wrap

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/anguslmm/stile/internal/jsonrpc"
	"github.com/anguslmm/stile/internal/transport"
)

const maxRequestBody = 10 << 20 // 10 MB

// Handler serves JSON-RPC requests by forwarding them to a StdioTransport.
type Handler struct {
	transport *transport.StdioTransport
	tracer    trace.Tracer
}

// NewHandler creates a wrap Handler backed by the given StdioTransport.
func NewHandler(t *transport.StdioTransport, opts ...Option) *Handler {
	h := &Handler{transport: t}
	for _, o := range opts {
		o(h)
	}
	return h
}

// Option configures a Handler.
type Option func(*Handler)

// WithTracer sets the OTel tracer for the handler.
func WithTracer(t trace.Tracer) Option {
	return func(h *Handler) { h.tracer = t }
}

// ServeMux returns an http.ServeMux with /mcp and /healthz routes registered.
func (h *Handler) ServeMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /mcp", h.handleMCP)
	mux.HandleFunc("GET /healthz", h.handleHealthz)
	return mux
}

func (h *Handler) handleMCP(w http.ResponseWriter, r *http.Request) {
	// Extract incoming trace context from HTTP headers.
	ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagatorCarrier(r.Header))

	var span trace.Span
	if h.tracer != nil {
		ctx, span = h.tracer.Start(ctx, "wrap.handleMCP")
		defer span.End()
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
		writeError(w, nil, jsonrpc.CodeParseError, err.Error())
		return
	}

	if !isBatch && len(requests) == 1 {
		h.handleSingle(ctx, w, requests[0])
		return
	}

	// Batch: process each request sequentially through stdio.
	var responses []*jsonrpc.Response
	for _, req := range requests {
		if req.IsNotification() {
			// Forward notifications to the child but don't collect a response.
			h.forwardNotification(ctx, req)
			continue
		}
		resp := h.forward(ctx, req)
		responses = append(responses, resp)
	}

	if len(responses) == 0 {
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

func (h *Handler) handleSingle(ctx context.Context, w http.ResponseWriter, req *jsonrpc.Request) {
	if req.IsNotification() {
		h.forwardNotification(ctx, req)
		w.WriteHeader(http.StatusAccepted)
		return
	}

	resp := h.forward(ctx, req)
	data, err := json.Marshal(resp)
	if err != nil {
		writeError(w, nil, jsonrpc.CodeInternalError, "failed to marshal response")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

func (h *Handler) forward(ctx context.Context, req *jsonrpc.Request) *jsonrpc.Response {
	slog.Debug("wrap request", "method", req.Method, "id", req.ID)

	if h.tracer != nil {
		var span trace.Span
		ctx, span = h.tracer.Start(ctx, "wrap.forward", trace.WithAttributes(
			attribute.String("mcp.method", req.Method),
		))
		defer func() {
			span.End()
		}()

		resp, err := transport.Send(ctx, h.transport, req)
		if err != nil {
			span.SetStatus(codes.Error, err.Error())
			span.RecordError(err)
			slog.Error("wrap forward error", "method", req.Method, "error", err)
			return jsonrpc.NewErrorResponse(req.ID, jsonrpc.CodeInternalError, err.Error())
		}
		return resp
	}

	resp, err := transport.Send(ctx, h.transport, req)
	if err != nil {
		slog.Error("wrap forward error", "method", req.Method, "error", err)
		return jsonrpc.NewErrorResponse(req.ID, jsonrpc.CodeInternalError, err.Error())
	}
	return resp
}

func (h *Handler) forwardNotification(ctx context.Context, req *jsonrpc.Request) {
	slog.Debug("wrap notification", "method", req.Method)
	// Best-effort: send to child, ignore response/errors.
	transport.Send(ctx, h.transport, req)
}

func (h *Handler) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	if h.transport.Healthy() {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusServiceUnavailable)
	w.Write([]byte(`{"status":"unhealthy"}`))
}

// propagatorCarrier adapts http.Header for use with OTel text map propagators.
type propagatorCarrier http.Header

func (c propagatorCarrier) Get(key string) string          { return http.Header(c).Get(key) }
func (c propagatorCarrier) Set(key, value string)          { http.Header(c).Set(key, value) }
func (c propagatorCarrier) Keys() []string {
	keys := make([]string, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}
	return keys
}

func writeError(w http.ResponseWriter, id jsonrpc.ID, code int, message string) {
	resp := jsonrpc.NewErrorResponse(id, code, message)
	data, _ := json.Marshal(resp)
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}
