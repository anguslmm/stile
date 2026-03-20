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

// Compile-time check that SQLiteStore satisfies the Store interface.
var _ Store = (*SQLiteStore)(nil)
