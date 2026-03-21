package jsonrpc

import (
	"encoding/json"
	"testing"
)

func TestParseSingleRequest(t *testing.T) {
	data := []byte(`{"jsonrpc":"2.0","method":"tools/list","id":1}`)
	reqs, isBatch, err := ParseMessage(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if isBatch {
		t.Fatal("expected isBatch=false")
	}
	if len(reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(reqs))
	}
	r := reqs[0]
	if r.Method != "tools/list" {
		t.Errorf("method = %q, want %q", r.Method, "tools/list")
	}
	id, ok := r.ID.(IntID)
	if !ok || int64(id) != 1 {
		t.Errorf("id = %v, want integer 1", r.ID)
	}
	if r.IsNotification() {
		t.Error("expected request, not notification")
	}
}

func TestParseRequestWithParams(t *testing.T) {
	data := []byte(`{"jsonrpc":"2.0","method":"tools/call","params":{"name":"db_query"},"id":"abc"}`)
	reqs, isBatch, err := ParseMessage(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if isBatch {
		t.Fatal("expected isBatch=false")
	}
	if len(reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(reqs))
	}
	r := reqs[0]
	id, ok := r.ID.(StringID)
	if !ok || string(id) != "abc" {
		t.Errorf("id = %v, want string \"abc\"", r.ID)
	}
	if r.Params == nil {
		t.Fatal("expected params, got nil")
	}
	var params map[string]string
	if err := json.Unmarshal(r.Params, &params); err != nil {
		t.Fatalf("unmarshal params: %v", err)
	}
	if params["name"] != "db_query" {
		t.Errorf("params.name = %q, want %q", params["name"], "db_query")
	}
}

func TestParseNotification(t *testing.T) {
	data := []byte(`{"jsonrpc":"2.0","method":"notifications/cancelled"}`)
	reqs, isBatch, err := ParseMessage(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if isBatch {
		t.Fatal("expected isBatch=false")
	}
	if len(reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(reqs))
	}
	r := reqs[0]
	if r.ID != nil {
		t.Errorf("expected nil ID, got %v", r.ID)
	}
	if !r.IsNotification() {
		t.Error("expected notification")
	}
}

func TestParseBatch(t *testing.T) {
	data := []byte(`[{"jsonrpc":"2.0","method":"ping","id":1},{"jsonrpc":"2.0","method":"ping","id":2}]`)
	reqs, isBatch, err := ParseMessage(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isBatch {
		t.Fatal("expected isBatch=true")
	}
	if len(reqs) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(reqs))
	}
	if id, ok := reqs[0].ID.(IntID); !ok || int64(id) != 1 {
		t.Errorf("first id = %v, want 1", reqs[0].ID)
	}
	if id, ok := reqs[1].ID.(IntID); !ok || int64(id) != 2 {
		t.Errorf("second id = %v, want 2", reqs[1].ID)
	}
}

func TestRoundTripMarshalUnmarshal(t *testing.T) {
	original := &Request{
		JSONRPC: Version,
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"test"}`),
		ID:      IntID(42),
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	reqs, _, err := ParseMessage(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	r := reqs[0]
	if r.Method != original.Method {
		t.Errorf("method = %q, want %q", r.Method, original.Method)
	}
	if r.ID != original.ID {
		t.Errorf("id mismatch: got %v, want %v", r.ID, original.ID)
	}
	if string(r.Params) != string(original.Params) {
		t.Errorf("params = %s, want %s", r.Params, original.Params)
	}
}

func TestResponseWithResult(t *testing.T) {
	result := map[string]string{"status": "ok"}
	resp, err := NewResponse(IntID(1), result)
	if err != nil {
		t.Fatalf("NewResponse: %v", err)
	}
	if resp.JSONRPC != Version {
		t.Errorf("jsonrpc = %q, want %q", resp.JSONRPC, Version)
	}
	if resp.Error != nil {
		t.Error("expected nil error")
	}
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := raw["result"]; !ok {
		t.Error("expected result field in JSON")
	}
}

func TestErrorResponse(t *testing.T) {
	resp := NewErrorResponse(IntID(1), CodeMethodNotFound, "method not found")
	if resp.Error == nil {
		t.Fatal("expected error")
	}
	if resp.Error.Code != CodeMethodNotFound {
		t.Errorf("code = %d, want %d", resp.Error.Code, CodeMethodNotFound)
	}
	if resp.Error.Message != "method not found" {
		t.Errorf("message = %q, want %q", resp.Error.Message, "method not found")
	}
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := raw["result"]; ok {
		t.Error("expected no result field in JSON")
	}
}

func TestIDEdgeCases(t *testing.T) {
	tests := []struct {
		name string
		id   ID
		json string
	}{
		{"string", StringID("abc"), `"abc"`},
		{"integer", IntID(42), `42`},
		{"null", NullID{}, `null`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.id)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(data) != tt.json {
				t.Errorf("marshal = %s, want %s", data, tt.json)
			}

			got, err := parseID(data)
			if err != nil {
				t.Fatalf("parseID: %v", err)
			}
			if got != tt.id {
				t.Errorf("round-tripped ID = %v, want %v", got, tt.id)
			}
		})
	}

	// Verify all three are distinguishable from each other via type switch.
	var ids [3]ID = [3]ID{StringID("abc"), IntID(42), NullID{}}
	for i := 0; i < len(ids); i++ {
		for j := i + 1; j < len(ids); j++ {
			if ids[i] == ids[j] {
				t.Errorf("expected ids[%d] != ids[%d]", i, j)
			}
		}
	}
}

func TestIDValue(t *testing.T) {
	tests := []struct {
		name string
		id   ID
		want any
	}{
		{"string", StringID("abc"), "abc"},
		{"integer", IntID(42), int64(42)},
		{"null", NullID{}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.id.Value()
			if got != tt.want {
				t.Errorf("Value() = %v (%T), want %v (%T)", got, got, tt.want, tt.want)
			}
		})
	}
}

func TestValidationEmptyMethod(t *testing.T) {
	data := []byte(`{"jsonrpc":"2.0","method":"","id":1}`)
	_, _, err := ParseMessage(data)
	if err == nil {
		t.Fatal("expected error for empty method")
	}
}

func TestValidationWrongVersion(t *testing.T) {
	data := []byte(`{"jsonrpc":"1.0","method":"ping","id":1}`)
	_, _, err := ParseMessage(data)
	if err == nil {
		t.Fatal("expected error for wrong jsonrpc version")
	}
}

func TestErrorImplementsErrorInterface(t *testing.T) {
	var err error = &Error{Code: CodeInternalError, Message: "something broke"}
	s := err.Error()
	if s == "" {
		t.Error("expected non-empty error string")
	}
}
