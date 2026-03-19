// Package transport defines the Transport interface and implementations
// for communicating with MCP upstreams.
package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

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
}

// JSONResult is a TransportResult for non-streaming (application/json) responses.
type JSONResult struct {
	response    *jsonrpc.Response
	contentType string
}

func (*JSONResult) transportResult()               {}
func (r *JSONResult) ContentType() string           { return r.contentType }
func (r *JSONResult) Response() *jsonrpc.Response    { return r.response }

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

func (*StreamResult) transportResult()            {}
func (r *StreamResult) ContentType() string       { return r.contentType }
func (r *StreamResult) Stream() io.ReadCloser     { return r.stream }

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

	switch r := result.(type) {
	case *JSONResult:
		return r.Response(), nil
	case *StreamResult:
		defer r.Stream().Close()
		return readFinalResponse(r.Stream())
	default:
		return nil, fmt.Errorf("transport: unexpected result type %T", result)
	}
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
