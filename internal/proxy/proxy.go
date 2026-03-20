// Package proxy implements the core proxy handler that dispatches
// MCP requests to the correct upstream transport.
package proxy

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

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
	rateLimiter *policy.RateLimiter
	metrics     *metrics.Metrics
	auditStore  audit.Store
}

// NewHandler creates a Handler backed by the given RouteTable.
// rateLimiter, m, and auditStore may be nil to disable their respective features.
func NewHandler(rt *router.RouteTable, rateLimiter *policy.RateLimiter, m *metrics.Metrics, auditStore audit.Store) *Handler {
	return &Handler{router: rt, rateLimiter: rateLimiter, metrics: m, auditStore: auditStore}
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
		h.recordRequest(ctx, callerName, "tools/call", params.Name, "", "error", req.Params, start)
		writeJSONResponse(w, jsonrpc.NewErrorResponse(req.ID, -32000, "access denied"))
		return
	}

	route, err := h.router.Resolve(params.Name)
	if err != nil {
		h.recordRequest(ctx, callerName, "tools/call", params.Name, "", "error", req.Params, start)
		writeJSONResponse(w, jsonrpc.NewErrorResponse(req.ID, jsonrpc.CodeInvalidParams, "unknown tool"))
		return
	}

	upstreamName := route.Upstream.Name

	// Rate limit check.
	if h.rateLimiter != nil {
		if caller != nil {
			h.rateLimiter.RegisterCaller(caller.Name, caller.Roles)
		}
		if ok, denial := h.rateLimiter.Allow(callerName, params.Name, upstreamName); !ok {
			slog.Debug("rate limit rejected",
				"caller", callerName,
				"tool", params.Name,
				"upstream", upstreamName,
				"level", denial.Level,
			)
			if h.metrics != nil {
				h.metrics.RateLimitRejections.WithLabelValues(callerName, params.Name).Inc()
			}
			h.recordRequest(ctx, callerName, "tools/call", params.Name, upstreamName, "error", req.Params, start)
			data, _ := json.Marshal(map[string]string{"limit": denial.Level})
			writeJSONResponse(w, jsonrpc.NewErrorResponseWithData(req.ID, -32000, "rate limit exceeded", data))
			return
		}
	}

	result, err := route.Upstream.Transport.RoundTrip(ctx, req)

	status := "ok"
	if err != nil {
		status = "error"
	}
	h.recordRequest(ctx, callerName, "tools/call", params.Name, upstreamName, status, req.Params, start)

	if err != nil {
		writeJSONResponse(w, jsonrpc.NewErrorResponse(req.ID, jsonrpc.CodeInternalError, err.Error()))
		return
	}

	result.WriteResponse(ctx, w)
}

func (h *Handler) recordRequest(ctx context.Context, callerName, method, tool, upstream, status string, params json.RawMessage, start time.Time) {
	latency := time.Since(start)

	slog.Info("request handled",
		"caller", callerName,
		"method", method,
		"tool", tool,
		"upstream", upstream,
		"latency_ms", latency.Milliseconds(),
		"status", status,
	)

	if h.metrics != nil {
		h.metrics.RequestsTotal.WithLabelValues(callerName, tool, upstream, status).Inc()
		h.metrics.RequestDuration.WithLabelValues(callerName, tool, upstream).Observe(latency.Seconds())
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
		if err := h.auditStore.Log(ctx, entry); err != nil {
			slog.Error("audit log write failed", "error", err)
		}
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
