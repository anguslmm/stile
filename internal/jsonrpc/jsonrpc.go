// Package jsonrpc implements JSON-RPC 2.0 wire types for MCP.
package jsonrpc

import (
	"encoding/json"
	"fmt"
)

// JSON-RPC 2.0 version string.
const Version = "2.0"

// Standard JSON-RPC 2.0 error codes.
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
)

// ID represents a JSON-RPC request ID: a string, an integer, or null.
// A nil ID (the interface zero value) means the id field is absent (notification).
// The interface is sealed by the unexported marker method.
type ID interface {
	jsonrpcID() // sealed — only types in this package can implement ID
}

// StringID is a string-valued JSON-RPC ID.
type StringID string

func (StringID) jsonrpcID() {}

// IntID is an integer-valued JSON-RPC ID.
type IntID int64

func (IntID) jsonrpcID() {}

// NullID is a JSON-RPC ID that is explicitly null.
type NullID struct{}

func (NullID) jsonrpcID() {}

// Compile-time interface satisfaction checks.
var (
	_ ID = StringID("")
	_ ID = IntID(0)
	_ ID = NullID{}
)

// MarshalJSON implements json.Marshaler so NullID serializes as JSON null.
func (NullID) MarshalJSON() ([]byte, error) {
	return []byte("null"), nil
}

// parseID parses a raw JSON id value into the appropriate ID variant.
func parseID(data []byte) (ID, error) {
	if string(data) == "null" {
		return NullID{}, nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		return StringID(s), nil
	}
	var n int64
	if err := json.Unmarshal(data, &n); err == nil {
		return IntID(n), nil
	}
	return nil, fmt.Errorf("jsonrpc: id must be a string, integer, or null, got %s", string(data))
}

// Request represents a JSON-RPC 2.0 request or notification.
// A nil ID means this is a notification (no id field in the JSON).
// A non-nil ID (even if NullID) means this is a request expecting a response.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
	ID      ID              `json:"id,omitempty"`
}

// IsNotification reports whether this is a notification (no id field).
func (r *Request) IsNotification() bool {
	return r.ID == nil
}

// Response represents a JSON-RPC 2.0 response.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
	ID      ID              `json:"id"`
}

// MarshalJSON implements json.Marshaler for Response.
// It handles the sealed ID interface by converting it to a JSON-compatible value.
func (r *Response) MarshalJSON() ([]byte, error) {
	type responseAlias struct {
		JSONRPC string          `json:"jsonrpc"`
		Result  json.RawMessage `json:"result,omitempty"`
		Error   *Error          `json:"error,omitempty"`
		ID      any             `json:"id"`
	}
	return json.Marshal(responseAlias{
		JSONRPC: r.JSONRPC,
		Result:  r.Result,
		Error:   r.Error,
		ID:      marshalID(r.ID),
	})
}

// UnmarshalJSON implements json.Unmarshaler for Response.
// It handles the sealed ID interface by parsing the raw id field.
func (r *Response) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	if v, ok := raw["jsonrpc"]; ok {
		if err := json.Unmarshal(v, &r.JSONRPC); err != nil {
			return err
		}
	}
	if v, ok := raw["result"]; ok {
		r.Result = v
	}
	if v, ok := raw["error"]; ok {
		r.Error = new(Error)
		if err := json.Unmarshal(v, r.Error); err != nil {
			return err
		}
	}
	if v, ok := raw["id"]; ok {
		id, err := parseID(v)
		if err != nil {
			return err
		}
		r.ID = id
	}
	return nil
}

// marshalID converts an ID to a value suitable for json.Marshal.
func marshalID(id ID) any {
	switch v := id.(type) {
	case StringID:
		return string(v)
	case IntID:
		return int64(v)
	case NullID:
		return nil
	default:
		return nil
	}
}

// Error represents a JSON-RPC 2.0 error object.
type Error struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// Error implements the error interface.
func (e *Error) Error() string {
	return fmt.Sprintf("jsonrpc: code %d: %s", e.Code, e.Message)
}

// NewResponse creates a success response, marshaling result to JSON.
func NewResponse(id ID, result any) (*Response, error) {
	data, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("jsonrpc: marshal result: %w", err)
	}
	return &Response{
		JSONRPC: Version,
		Result:  data,
		ID:      id,
	}, nil
}

// NewErrorResponse creates an error response.
func NewErrorResponse(id ID, code int, message string) *Response {
	return &Response{
		JSONRPC: Version,
		Error:   &Error{Code: code, Message: message},
		ID:      id,
	}
}

// ParseMessage parses a JSON-RPC 2.0 message, which may be a single request
// or a batch (JSON array). It returns the parsed requests, whether the input
// was a batch, and any error.
//
// Validation is performed at parse time: every request must have jsonrpc "2.0"
// and a non-empty method. If validation fails, an error is returned.
func ParseMessage(data []byte) ([]*Request, bool, error) {
	// Determine if this is a batch by finding the first non-whitespace byte.
	isBatch := false
	for _, b := range data {
		switch b {
		case ' ', '\t', '\n', '\r':
			continue
		case '[':
			isBatch = true
		}
		break
	}

	if isBatch {
		return parseBatch(data)
	}
	return parseSingle(data)
}

func parseSingle(data []byte) ([]*Request, bool, error) {
	req, err := parseOneRequest(data)
	if err != nil {
		return nil, false, err
	}
	return []*Request{req}, false, nil
}

func parseBatch(data []byte) ([]*Request, bool, error) {
	var rawMessages []json.RawMessage
	if err := json.Unmarshal(data, &rawMessages); err != nil {
		return nil, true, &Error{Code: CodeParseError, Message: "invalid JSON"}
	}
	if len(rawMessages) == 0 {
		return nil, true, &Error{Code: CodeInvalidRequest, Message: "empty batch"}
	}

	requests := make([]*Request, 0, len(rawMessages))
	for _, raw := range rawMessages {
		req, err := parseOneRequest(raw)
		if err != nil {
			return nil, true, err
		}
		requests = append(requests, req)
	}
	return requests, true, nil
}

// parseOneRequest unmarshals and validates a single JSON-RPC 2.0 request.
func parseOneRequest(data []byte) (*Request, error) {
	// Use a raw map to detect the presence of the "id" field,
	// since encoding/json with omitempty will treat null id as absent.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, &Error{Code: CodeParseError, Message: "invalid JSON"}
	}

	// Check jsonrpc version.
	vRaw, ok := raw["jsonrpc"]
	if !ok {
		return nil, &Error{Code: CodeInvalidRequest, Message: "missing jsonrpc field"}
	}
	var version string
	if err := json.Unmarshal(vRaw, &version); err != nil || version != Version {
		return nil, &Error{Code: CodeInvalidRequest, Message: "jsonrpc must be \"2.0\""}
	}

	// Check method.
	mRaw, ok := raw["method"]
	if !ok {
		return nil, &Error{Code: CodeInvalidRequest, Message: "missing method field"}
	}
	var method string
	if err := json.Unmarshal(mRaw, &method); err != nil || method == "" {
		return nil, &Error{Code: CodeInvalidRequest, Message: "method must be a non-empty string"}
	}

	req := &Request{
		JSONRPC: version,
		Method:  method,
	}

	// Params (optional).
	if pRaw, ok := raw["params"]; ok {
		req.Params = pRaw
	}

	// ID: distinguish absent from null.
	if idRaw, ok := raw["id"]; ok {
		id, err := parseID(idRaw)
		if err != nil {
			return nil, &Error{Code: CodeInvalidRequest, Message: "invalid id field"}
		}
		req.ID = id
	}

	return req, nil
}
