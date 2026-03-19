// Package transport defines the Transport interface and implementations
// for communicating with MCP upstreams.
package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"

	"github.com/anguslmm/stile/internal/jsonrpc"
)

// ToolSchema represents an MCP tool definition returned by tools/list.
type ToolSchema struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
}

// TransportResult is a sealed union representing the result of a round-trip
// to an upstream. It is either a *JSONResult or a *StreamResult.
type TransportResult interface {
	transportResult() // sealed — only types in this package implement TransportResult
	ContentType() string

	// Resolve returns the final JSON-RPC response. For streaming results,
	// it reads through the event stream and closes it.
	Resolve() (*jsonrpc.Response, error)

	// WriteResponse writes the result to an HTTP response writer.
	// For streaming results, it pipes events until EOF or client disconnect.
	WriteResponse(ctx context.Context, w http.ResponseWriter)
}

// JSONResult is a TransportResult for non-streaming (application/json) responses.
type JSONResult struct {
	response    *jsonrpc.Response
	contentType string
}

func (*JSONResult) transportResult()            {}
func (r *JSONResult) ContentType() string        { return r.contentType }
func (r *JSONResult) Response() *jsonrpc.Response { return r.response }

func (r *JSONResult) Resolve() (*jsonrpc.Response, error) {
	return r.response, nil
}

func (r *JSONResult) WriteResponse(_ context.Context, w http.ResponseWriter) {
	data, err := json.Marshal(r.response)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

// NewJSONResult creates a JSONResult wrapping the given response.
func NewJSONResult(resp *jsonrpc.Response) *JSONResult {
	return &JSONResult{response: resp, contentType: "application/json"}
}

// StreamResult is a TransportResult for streaming (text/event-stream) responses.
// The caller is responsible for reading from and closing the stream.
type StreamResult struct {
	stream      io.ReadCloser
	contentType string
}

func (*StreamResult) transportResult()        {}
func (r *StreamResult) ContentType() string   { return r.contentType }
func (r *StreamResult) Stream() io.ReadCloser { return r.stream }

func (r *StreamResult) Resolve() (*jsonrpc.Response, error) {
	defer r.stream.Close()
	return readFinalResponse(r.stream)
}

func (r *StreamResult) WriteResponse(ctx context.Context, w http.ResponseWriter) {
	defer r.stream.Close()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, canFlush := w.(http.Flusher)

	buf := make([]byte, 4096)
	for {
		n, readErr := r.stream.Read(buf)
		if n > 0 {
			if _, writeErr := w.Write(buf[:n]); writeErr != nil {
				return
			}
			if canFlush {
				flusher.Flush()
			}
		}
		if readErr != nil {
			if readErr != io.EOF {
				log.Printf("transport: stream read error: %v", readErr)
			}
			return
		}
		if ctx.Err() != nil {
			return
		}
	}
}

// NewStreamResult creates a StreamResult wrapping the given reader.
func NewStreamResult(stream io.ReadCloser) *StreamResult {
	return &StreamResult{stream: stream, contentType: "text/event-stream"}
}

// Transport is the interface for communicating with an MCP upstream.
type Transport interface {
	// RoundTrip sends a JSON-RPC request to the upstream and returns the result.
	// For streaming responses, the caller must close StreamResult.Stream().
	RoundTrip(ctx context.Context, req *jsonrpc.Request) (TransportResult, error)

	// Close shuts down the transport and releases resources.
	Close() error

	// Healthy reports whether the upstream is reachable.
	Healthy() bool
}

// Send is a convenience that sends a request and returns the final Response.
// If the upstream responds with SSE, it reads events until the final
// JSON-RPC response and returns it. For non-streaming responses, it
// returns the Response directly.
func Send(ctx context.Context, t Transport, req *jsonrpc.Request) (*jsonrpc.Response, error) {
	result, err := t.RoundTrip(ctx, req)
	if err != nil {
		return nil, err
	}
	return result.Resolve()
}

func readFinalResponse(stream io.Reader) (*jsonrpc.Response, error) {
	reader := NewSSEReader(stream)
	var lastResponse *jsonrpc.Response

	for {
		event, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("transport: read SSE event: %w", err)
		}

		if event.Event != "message" && event.Event != "" {
			continue
		}

		var resp jsonrpc.Response
		if err := json.Unmarshal([]byte(event.Data), &resp); err != nil {
			continue
		}
		lastResponse = &resp
	}

	if lastResponse == nil {
		return nil, fmt.Errorf("transport: no JSON-RPC response found in SSE stream")
	}
	return lastResponse, nil
}
