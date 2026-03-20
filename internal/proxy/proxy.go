// Package proxy implements the core proxy handler that dispatches
// MCP requests to the correct upstream transport.
package proxy

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/anguslmm/stile/internal/jsonrpc"
	"github.com/anguslmm/stile/internal/router"
	"github.com/anguslmm/stile/internal/transport"
)

// Handler dispatches MCP tool calls to the correct upstream via the router.
type Handler struct {
	router *router.RouteTable
}

// NewHandler creates a Handler backed by the given RouteTable.
func NewHandler(rt *router.RouteTable) *Handler {
	return &Handler{router: rt}
}

// HandleToolsList returns the merged tool list from all upstreams.
func (h *Handler) HandleToolsList(id jsonrpc.ID) (*jsonrpc.Response, error) {
	tools := h.router.ListTools()

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
	var params struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil || params.Name == "" {
		writeJSONResponse(w, jsonrpc.NewErrorResponse(req.ID, jsonrpc.CodeInvalidParams, "missing or invalid params.name"))
		return
	}

	route, err := h.router.Resolve(params.Name)
	if err != nil {
		writeJSONResponse(w, jsonrpc.NewErrorResponse(req.ID, jsonrpc.CodeInvalidParams, "unknown tool"))
		return
	}

	result, err := route.Upstream.Transport.RoundTrip(ctx, req)
	if err != nil {
		writeJSONResponse(w, jsonrpc.NewErrorResponse(req.ID, jsonrpc.CodeInternalError, err.Error()))
		return
	}

	result.WriteResponse(ctx, w)
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
