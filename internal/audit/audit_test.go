package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"
)

func TestSQLiteStoreLog(t *testing.T) {
	store, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	entry := Entry{
		Timestamp: time.Date(2026, 3, 20, 10, 0, 0, 0, time.UTC),
		Caller:    "alice",
		Method:    "tools/call",
		Tool:      "search",
		Upstream:  "upstream-a",
		Params:    json.RawMessage(`{"name":"search","arguments":{"q":"hello"}}`),
		Status:    "ok",
		LatencyMS: 42,
	}

	if err := store.Log(context.Background(), entry); err != nil {
		t.Fatalf("Log: %v", err)
	}

	// Verify the row was written.
	var (
		caller    string
		method    string
		tool      sql.NullString
		upstream  sql.NullString
		params    sql.NullString
		status    string
		latencyMS int64
	)
	err = store.db.QueryRow("SELECT caller, method, tool, upstream, params, status, latency_ms FROM audit_log WHERE id = 1").
		Scan(&caller, &method, &tool, &upstream, &params, &status, &latencyMS)
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	if caller != "alice" {
		t.Errorf("caller = %q, want alice", caller)
	}
	if method != "tools/call" {
		t.Errorf("method = %q, want tools/call", method)
	}
	if !tool.Valid || tool.String != "search" {
		t.Errorf("tool = %v, want search", tool)
	}
	if !upstream.Valid || upstream.String != "upstream-a" {
		t.Errorf("upstream = %v, want upstream-a", upstream)
	}
	if !params.Valid || params.String != `{"name":"search","arguments":{"q":"hello"}}` {
		t.Errorf("params = %v, want JSON body", params)
	}
	if status != "ok" {
		t.Errorf("status = %q, want ok", status)
	}
	if latencyMS != 42 {
		t.Errorf("latency_ms = %d, want 42", latencyMS)
	}
}

func TestSQLiteStoreNilParams(t *testing.T) {
	store, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	entry := Entry{
		Timestamp: time.Now(),
		Caller:    "bob",
		Method:    "tools/list",
		Status:    "ok",
		LatencyMS: 5,
	}

	if err := store.Log(context.Background(), entry); err != nil {
		t.Fatalf("Log with nil params: %v", err)
	}
}

func TestSQLiteStoreQuery(t *testing.T) {
	store, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	now := time.Now().UTC()

	// Insert test entries.
	entries := []Entry{
		{Timestamp: now.Add(-3 * time.Hour), Caller: "alice", Method: "tools/call", Tool: "search", Upstream: "up-a", Status: "ok", LatencyMS: 10, TraceID: "abc123def456"},
		{Timestamp: now.Add(-2 * time.Hour), Caller: "bob", Method: "tools/call", Tool: "fetch", Upstream: "up-b", Status: "error", LatencyMS: 200},
		{Timestamp: now.Add(-1 * time.Hour), Caller: "alice", Method: "tools/call", Tool: "search", Upstream: "up-a", Status: "ok", LatencyMS: 15, TraceID: "xyz789"},
		{Timestamp: now, Caller: "alice", Method: "tools/list", Tool: "", Upstream: "", Status: "ok", LatencyMS: 5},
	}
	for _, e := range entries {
		if err := store.Log(ctx, e); err != nil {
			t.Fatalf("Log: %v", err)
		}
	}

	t.Run("all entries", func(t *testing.T) {
		results, err := store.Query(ctx, QueryFilter{})
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
		if len(results) != 4 {
			t.Fatalf("expected 4 entries, got %d", len(results))
		}
		// Should be newest first.
		if results[0].ID < results[1].ID {
			t.Error("expected newest first ordering")
		}
	})

	t.Run("filter by caller", func(t *testing.T) {
		results, err := store.Query(ctx, QueryFilter{Caller: "alice"})
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
		if len(results) != 3 {
			t.Fatalf("expected 3 entries for alice, got %d", len(results))
		}
	})

	t.Run("filter by status", func(t *testing.T) {
		results, err := store.Query(ctx, QueryFilter{Status: "error"})
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
		if len(results) != 1 {
			t.Fatalf("expected 1 error entry, got %d", len(results))
		}
		if results[0].Caller != "bob" {
			t.Errorf("expected bob, got %q", results[0].Caller)
		}
	})

	t.Run("filter by tool", func(t *testing.T) {
		results, err := store.Query(ctx, QueryFilter{Tool: "search"})
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
		if len(results) != 2 {
			t.Fatalf("expected 2 search entries, got %d", len(results))
		}
	})

	t.Run("limit and offset", func(t *testing.T) {
		results, err := store.Query(ctx, QueryFilter{Limit: 2})
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
		if len(results) != 2 {
			t.Fatalf("expected 2 entries with limit, got %d", len(results))
		}

		results2, err := store.Query(ctx, QueryFilter{Limit: 2, Offset: 2})
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
		if len(results2) != 2 {
			t.Fatalf("expected 2 entries with offset, got %d", len(results2))
		}
		if results[0].ID == results2[0].ID {
			t.Error("offset should return different entries")
		}
	})

	t.Run("time range", func(t *testing.T) {
		results, err := store.Query(ctx, QueryFilter{
			Start: now.Add(-90 * time.Minute),
			End:   now.Add(-30 * time.Minute),
		})
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
		if len(results) != 1 {
			t.Fatalf("expected 1 entry in time range, got %d", len(results))
		}
	})

	t.Run("entries have IDs", func(t *testing.T) {
		results, err := store.Query(ctx, QueryFilter{Limit: 1})
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
		if results[0].ID == 0 {
			t.Error("expected non-zero ID")
		}
	})

	t.Run("trace IDs round-trip", func(t *testing.T) {
		results, err := store.Query(ctx, QueryFilter{Caller: "alice", Tool: "search"})
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
		// Newest first — first result has TraceID "xyz789", second has "abc123def456".
		if results[0].TraceID != "xyz789" {
			t.Errorf("expected trace_id xyz789, got %q", results[0].TraceID)
		}
		if results[1].TraceID != "abc123def456" {
			t.Errorf("expected trace_id abc123def456, got %q", results[1].TraceID)
		}
		// Entry without trace ID should have empty string.
		untraced, _ := store.Query(ctx, QueryFilter{Caller: "bob"})
		if untraced[0].TraceID != "" {
			t.Errorf("expected empty trace_id for unsampled entry, got %q", untraced[0].TraceID)
		}
	})
}

// Compile-time check that SQLiteStore satisfies the Store interface.
var _ Store = (*SQLiteStore)(nil)
