// Package proxy implements the core proxy handler that dispatches
// MCP requests to the correct upstream transport.
package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"

	"github.com/anguslmm/stile/internal/config"
	"github.com/anguslmm/stile/internal/jsonrpc"
	"github.com/anguslmm/stile/internal/transport"
)

// upstream holds a transport and its discovered tools.
type upstream struct {
	name      string
	transport transport.Transport
	tools     []transport.ToolSchema
}

// Handler dispatches MCP tool calls to the correct upstream.
type Handler struct {
	mu        sync.RWMutex
	toolMap   map[string]*upstream // tool name → upstream
	upstreams []*upstream
}

// TransportFactory creates a Transport for a given upstream config.
// This is an exported type to allow testing with mock transports.
type TransportFactory func(cfg config.UpstreamConfig) (transport.Transport, error)

// NewHandler creates a Handler, initializing transports and discovering tools.
// Individual upstream failures are non-fatal (logged and skipped).
func NewHandler(cfg *config.Config) (*Handler, error) {
	return NewHandlerWithFactory(cfg, defaultTransportFactory)
}

// NewHandlerWithFactory creates a Handler using the provided transport factory.
func NewHandlerWithFactory(cfg *config.Config, factory TransportFactory) (*Handler, error) {
	h := &Handler{
		toolMap: make(map[string]*upstream),
	}

	for _, ucfg := range cfg.Upstreams() {
		t, err := factory(ucfg)
		if err != nil {
			log.Printf("proxy: skip upstream %q: create transport: %v", ucfg.Name(), err)
			continue
		}

		u := &upstream{
			name:      ucfg.Name(),
			transport: t,
		}

		tools, err := discoverTools(t)
		if err != nil {
			log.Printf("proxy: skip upstream %q: tool discovery: %v", ucfg.Name(), err)
			t.Close()
			continue
		}

		u.tools = tools
		h.upstreams = append(h.upstreams, u)

		for _, tool := range tools {
			h.toolMap[tool.Name] = u
		}
	}

	return h, nil
}

func defaultTransportFactory(cfg config.UpstreamConfig) (transport.Transport, error) {
	switch cfg.Transport() {
	case "streamable-http":
		return transport.NewHTTPTransport(cfg)
	case "stdio":
		return transport.NewStdioTransport(cfg)
	default:
		return nil, fmt.Errorf("unsupported transport type %q", cfg.Transport())
	}
}

func discoverTools(t transport.Transport) ([]transport.ToolSchema, error) {
	req := &jsonrpc.Request{
		JSONRPC: jsonrpc.Version,
		Method:  "tools/list",
		ID:      jsonrpc.IntID(1),
	}

	resp, err := transport.Send(context.Background(), t, req)
	if err != nil {
		return nil, fmt.Errorf("tools/list request: %w", err)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("tools/list error: %s", resp.Error.Message)
	}

	var result struct {
		Tools []transport.ToolSchema `json:"tools"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("parse tools/list result: %w", err)
	}

	return result.Tools, nil
}

// HandleToolsList returns the merged tool list from all upstreams.
func (h *Handler) HandleToolsList(id jsonrpc.ID) (*jsonrpc.Response, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	var allTools []transport.ToolSchema
	for _, u := range h.upstreams {
		allTools = append(allTools, u.tools...)
	}

	result := struct {
		Tools []transport.ToolSchema `json:"tools"`
	}{
		Tools: allTools,
	}

	return jsonrpc.NewResponse(id, result)
}

// HandleToolsCall dispatches a tools/call request to the correct upstream.
// It writes the response directly to the http.ResponseWriter to support SSE passthrough.
func (h *Handler) HandleToolsCall(ctx context.Context, w http.ResponseWriter, req *jsonrpc.Request) {
	h.mu.RLock()

	var params struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil || params.Name == "" {
		h.mu.RUnlock()
		writeJSONResponse(w, jsonrpc.NewErrorResponse(req.ID, jsonrpc.CodeInvalidParams, "missing or invalid params.name"))
		return
	}

	u, ok := h.toolMap[params.Name]
	h.mu.RUnlock()

	if !ok {
		writeJSONResponse(w, jsonrpc.NewErrorResponse(req.ID, jsonrpc.CodeInvalidParams, "unknown tool"))
		return
	}

	result, err := u.transport.RoundTrip(ctx, req)
	if err != nil {
		writeJSONResponse(w, jsonrpc.NewErrorResponse(req.ID, jsonrpc.CodeInternalError, err.Error()))
		return
	}

	result.WriteResponse(ctx, w)
}

// Close shuts down all upstream transports.
func (h *Handler) Close() error {
	for _, u := range h.upstreams {
		u.transport.Close()
	}
	return nil
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
