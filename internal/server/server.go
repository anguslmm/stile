// Package server implements the inbound MCP HTTP server.
package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/anguslmm/stile/internal/config"
	"github.com/anguslmm/stile/internal/jsonrpc"
	"github.com/anguslmm/stile/internal/proxy"
	"github.com/anguslmm/stile/internal/router"
)

const supportedProtocolVersion = "2025-11-25"

// Server is the inbound MCP HTTP server.
type Server struct {
	httpServer *http.Server
	proxy      *proxy.Handler
	router     *router.RouteTable
}

// New creates a Server from config, proxy handler, and router.
func New(cfg *config.Config, p *proxy.Handler, rt *router.RouteTable) *Server {
	s := &Server{proxy: p, router: rt}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /mcp", s.handleMCP)
	mux.HandleFunc("POST /admin/refresh", s.handleRefresh)

	s.httpServer = &http.Server{
		Addr:    cfg.Server().Address(),
		Handler: mux,
	}

	return s
}

// ListenAndServe starts the HTTP server.
func (s *Server) ListenAndServe() error {
	return s.httpServer.ListenAndServe()
}

// Shutdown gracefully shuts down the server.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}

// Handler returns the underlying http.Handler for use with httptest.
func (s *Server) Handler() http.Handler {
	return s.httpServer.Handler
}

func (s *Server) handleMCP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, nil, jsonrpc.CodeParseError, "failed to read request body")
		return
	}

	requests, isBatch, err := jsonrpc.ParseMessage(body)
	if err != nil {
		writeError(w, nil, jsonrpc.CodeParseError, err.Error())
		return
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
		s.proxy.HandleToolsCall(r.Context(), w, req)
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

func (s *Server) dispatch(_ context.Context, req *jsonrpc.Request) *jsonrpc.Response {
	switch req.Method {
	case "initialize":
		return s.handleInitialize(req)
	case "ping":
		return s.handlePing(req)
	case "tools/list":
		return s.handleToolsList(req)
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

func (s *Server) handleInitialize(req *jsonrpc.Request) *jsonrpc.Response {
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

	result := map[string]any{
		"protocolVersion": supportedProtocolVersion,
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"serverInfo":      map[string]any{"name": "stile", "version": "0.1.0"},
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

func (s *Server) handleToolsList(req *jsonrpc.Request) *jsonrpc.Response {
	resp, err := s.proxy.HandleToolsList(req.ID)
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

func writeError(w http.ResponseWriter, id jsonrpc.ID, code int, message string) {
	resp := jsonrpc.NewErrorResponse(id, code, message)
	data, _ := json.Marshal(resp)
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}
