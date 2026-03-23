// Package proxy implements the core proxy handler that dispatches
// MCP requests to the correct upstream transport.
package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/anguslmm/stile/internal/audit"
	"github.com/anguslmm/stile/internal/auth"
	"github.com/anguslmm/stile/internal/jsonrpc"
	"github.com/anguslmm/stile/internal/metrics"
	"github.com/anguslmm/stile/internal/policy"
	"github.com/anguslmm/stile/internal/router"
	"github.com/anguslmm/stile/internal/transport"
)

// Handler dispatches MCP tool calls to the correct upstream via the router.
type Handler struct {
	router      *router.RouteTable
	rateLimiter policy.RateLimiter
	metrics     *metrics.Metrics
	auditStore  audit.Store
	tracer      trace.Tracer
}

// NewHandler creates a Handler backed by the given RouteTable.
// rateLimiter, m, auditStore, and tracer may be nil to disable their features.
func NewHandler(rt *router.RouteTable, rateLimiter policy.RateLimiter, m *metrics.Metrics, auditStore audit.Store, opts ...HandlerOption) *Handler {
	h := &Handler{router: rt, rateLimiter: rateLimiter, metrics: m, auditStore: auditStore}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// HandlerOption configures a Handler.
type HandlerOption func(*Handler)

// WithTracer sets the tracer for the proxy handler.
func WithTracer(t trace.Tracer) HandlerOption {
	return func(h *Handler) { h.tracer = t }
}

// HandleToolsList returns the merged tool list from all upstreams,
// filtered by the caller's allowed tools if a caller is present.
func (h *Handler) HandleToolsList(ctx context.Context, id jsonrpc.ID) (*jsonrpc.Response, error) {
	tools := h.router.ListTools()

	caller := auth.CallerFromContext(ctx)
	if caller != nil {
		filtered := make([]transport.ToolSchema, 0, len(tools))
		for _, t := range tools {
			if caller.CanAccessTool(t.Name) {
				filtered = append(filtered, t)
			}
		}
		tools = filtered
	}

	result := struct {
		Tools []transport.ToolSchema `json:"tools"`
	}{
		Tools: tools,
	}

	return jsonrpc.NewResponse(id, result)
}

// FilteredTools returns the tool list filtered by the caller in context.
func (h *Handler) FilteredTools(ctx context.Context) []transport.ToolSchema {
	tools := h.router.ListTools()

	caller := auth.CallerFromContext(ctx)
	if caller != nil {
		filtered := make([]transport.ToolSchema, 0, len(tools))
		for _, t := range tools {
			if caller.CanAccessTool(t.Name) {
				filtered = append(filtered, t)
			}
		}
		return filtered
	}
	return tools
}

// HandleToolsCall dispatches a tools/call request to the correct upstream.
// It writes the response directly to the http.ResponseWriter to support SSE passthrough.
func (h *Handler) HandleToolsCall(ctx context.Context, w http.ResponseWriter, req *jsonrpc.Request) {
	start := time.Now()

	var params struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil || params.Name == "" {
		writeJSONResponse(w, jsonrpc.NewErrorResponse(req.ID, jsonrpc.CodeInvalidParams, "missing or invalid params.name"))
		return
	}

	caller := auth.CallerFromContext(ctx)
	callerName := "anonymous"
	if caller != nil {
		callerName = caller.Name
	}

	if caller != nil && !caller.CanAccessTool(params.Name) {
		h.setSpanError(ctx, "access denied")
		h.recordRequest(ctx, callerName, "tools/call", params.Name, "", "error", req.Params, start)
		writeJSONResponse(w, jsonrpc.NewErrorResponse(req.ID, -32000, "access denied"))
		return
	}

	// Route + rate limit span.
	var routeCtx context.Context
	var routeSpan trace.Span
	if h.tracer != nil {
		routeCtx, routeSpan = h.tracer.Start(ctx, "route + rate limit")
	} else {
		routeCtx = ctx
	}

	route, err := h.router.Resolve(params.Name)
	if err != nil {
		if routeSpan != nil {
			routeSpan.SetStatus(codes.Error, "unknown tool")
			routeSpan.End()
		}
		h.setSpanError(ctx, "unknown tool")
		h.recordRequest(ctx, callerName, "tools/call", params.Name, "", "error", req.Params, start)
		writeJSONResponse(w, jsonrpc.NewErrorResponse(req.ID, jsonrpc.CodeInvalidParams, "unknown tool"))
		return
	}

	upstreamName := route.Upstream.Name

	if routeSpan != nil {
		routeSpan.SetAttributes(
			attribute.String("mcp.tool", params.Name),
			attribute.String("mcp.upstream", upstreamName),
			attribute.String("mcp.caller", callerName),
		)
	}

	// Rate limit check.
	var rlResult *policy.RateLimitResult
	if h.rateLimiter != nil {
		var roles []string
		if caller != nil {
			roles = caller.Roles
		}
		rlResult = h.rateLimiter.Allow(callerName, params.Name, upstreamName, roles)
		if rlResult != nil && rlResult.Denial != nil {
			slog.DebugContext(ctx, "rate limit rejected",
				"caller", callerName,
				"tool", params.Name,
				"upstream", upstreamName,
				"level", rlResult.Denial.Level,
			)
			if routeSpan != nil {
				routeSpan.SetStatus(codes.Error, "rate limited: "+rlResult.Denial.Level)
				routeSpan.End()
			}
			if h.metrics != nil {
				h.metrics.RecordRateLimitRejection(callerName, params.Name)
			}
			h.setSpanError(ctx, "rate limited")
			h.recordRequest(ctx, callerName, "tools/call", params.Name, upstreamName, "error", req.Params, start)
			setRateLimitHeaders(w, rlResult)
			data, _ := json.Marshal(map[string]string{"limit": rlResult.Denial.Level})
			writeJSONResponse(w, jsonrpc.NewErrorResponseWithData(req.ID, -32000, "rate limit exceeded", data))
			return
		}
	}

	if routeSpan != nil {
		routeSpan.End()
	}

	// Upstream round-trip span.
	roundTripCtx := routeCtx
	var rtSpan trace.Span
	if h.tracer != nil {
		roundTripCtx, rtSpan = h.tracer.Start(routeCtx, "upstream.RoundTrip", trace.WithAttributes(
			attribute.String("mcp.upstream", upstreamName),
		))
	}

	result, err := route.Upstream.Transport.RoundTrip(roundTripCtx, req)

	if rtSpan != nil {
		if err != nil {
			rtSpan.SetStatus(codes.Error, err.Error())
			rtSpan.RecordError(err)
		}
		rtSpan.End()
	}

	status := "ok"
	if err != nil {
		status = "error"
	}

	// Set span attributes on the parent dispatch span.
	parentSpan := trace.SpanFromContext(ctx)
	if parentSpan.SpanContext().IsValid() {
		parentSpan.SetAttributes(
			attribute.String("mcp.method", "tools/call"),
			attribute.String("mcp.tool", params.Name),
			attribute.String("mcp.upstream", upstreamName),
			attribute.String("mcp.caller", callerName),
			attribute.String("mcp.status", status),
		)
		if status == "error" {
			parentSpan.SetStatus(codes.Error, "upstream error")
		}
	}

	h.recordRequest(ctx, callerName, "tools/call", params.Name, upstreamName, status, req.Params, start)

	if err != nil {
		setRateLimitHeaders(w, rlResult)
		writeJSONResponse(w, jsonrpc.NewErrorResponse(req.ID, jsonrpc.CodeInternalError, err.Error()))
		return
	}

	setRateLimitHeaders(w, rlResult)
	result.WriteResponse(ctx, w, h.tracer)
}

// setSpanError marks the current span (if any) as errored.
func (h *Handler) setSpanError(ctx context.Context, msg string) {
	span := trace.SpanFromContext(ctx)
	if span.SpanContext().IsValid() {
		span.SetStatus(codes.Error, msg)
	}
}

func (h *Handler) recordRequest(ctx context.Context, callerName, method, tool, upstream, status string, params json.RawMessage, start time.Time) {
	latency := time.Since(start)

	slog.InfoContext(ctx, "request handled",
		"caller", callerName,
		"method", method,
		"tool", tool,
		"upstream", upstream,
		"latency_ms", latency.Milliseconds(),
		"status", status,
	)

	if h.metrics != nil {
		h.metrics.RecordRequest(callerName, tool, upstream, status)
		h.metrics.RecordDuration(callerName, tool, upstream, latency.Seconds())
	}

	if h.auditStore != nil {
		entry := audit.Entry{
			Timestamp: start,
			Caller:    callerName,
			Method:    method,
			Tool:      tool,
			Upstream:  upstream,
			Params:    params,
			Status:    status,
			LatencyMS: latency.Milliseconds(),
		}
		if sc := trace.SpanFromContext(ctx).SpanContext(); sc.IsSampled() {
			entry.TraceID = sc.TraceID().String()
		}
		entry.KeyLabel = auth.KeyLabelFromContext(ctx)
		if err := h.auditStore.Log(ctx, entry); err != nil {
			slog.Error("audit log write failed", "error", err)
		}
	}
}

// setRateLimitHeaders sets standard rate limit headers on the response.
// Does nothing if result is nil (no rate limits configured).
func setRateLimitHeaders(w http.ResponseWriter, result *policy.RateLimitResult) {
	if result == nil {
		return
	}
	w.Header().Set("X-RateLimit-Limit", fmt.Sprintf("%d", result.Limit))
	w.Header().Set("X-RateLimit-Remaining", fmt.Sprintf("%d", result.Remaining))
	w.Header().Set("X-RateLimit-Reset", fmt.Sprintf("%d", result.ResetAt.Unix()))
	if result.Denial != nil {
		retryAfter := int(math.Ceil(result.RetryAfter.Seconds()))
		if retryAfter < 1 {
			retryAfter = 1
		}
		w.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfter))
	}
}

func writeJSONResponse(w http.ResponseWriter, resp *jsonrpc.Response) {
	data, err := json.Marshal(resp)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}
