# Task 8: Observability

**Status:** done
**Depends on:** Task 7 (rate limiting — rejection metrics), Task 5 (router — cache refresh metrics)
**Needed by:** Task 9 (health checks complement observability)

---

## Goal

Add structured logging, Prometheus metrics, and an optional audit log. After this task, operators can see what the gateway is doing, measure performance, and maintain an audit trail.

---

## 1. Structured Logging

### Using Go's `log/slog`

Add structured logging throughout the request path. This is a retrofit across existing code — not a new package, but updates to existing packages.

### What to log

**Per-request (INFO level):**
- Caller name (from auth context, or "anonymous" if auth disabled)
- Method (`tools/list`, `tools/call`, etc.)
- Tool name (for `tools/call`)
- Upstream name (for proxied requests)
- Latency (duration)
- Status: `"ok"` or `"error"`
- Error message (if applicable)

**Lifecycle events (INFO level):**
- Server start (listen address)
- Server shutdown
- Upstream connected / disconnected
- Tool cache refresh (per-upstream: tool count, duration, success/failure)
- Config loaded (upstream count, caller count)

**Debug level:**
- Full JSON-RPC request/response bodies (useful for development, too verbose for production)
- Rate limit decisions (allowed/rejected, remaining tokens)
- Auth decisions (caller resolved, key rejected)

### Logger setup

Configure the logger in `cmd/gateway/main.go`:

```yaml
logging:
  level: info    # debug, info, warn, error
  format: json   # json or text
```

Use `slog.SetDefault()` so all packages can use `slog.Info()`, `slog.Debug()`, etc. without passing a logger around.

### Integration approach

Add logging calls to the existing request handling path in the server and proxy handler. Use middleware or a wrapper to capture timing:

```go
start := time.Now()
// ... handle request ...
slog.Info("request handled",
    "caller", caller.Name,
    "method", req.Method,
    "tool", toolName,
    "upstream", upstreamName,
    "latency_ms", time.Since(start).Milliseconds(),
    "status", status,
)
```

---

## 2. Prometheus Metrics

### Package: `internal/metrics`

Register and expose Prometheus metrics.

### Metrics to implement

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `stile_requests_total` | Counter | caller, tool, upstream, status | Total requests processed |
| `stile_request_duration_seconds` | Histogram | caller, tool, upstream | Request latency |
| `stile_upstream_health` | Gauge | upstream | 1 = healthy, 0 = unhealthy |
| `stile_rate_limit_rejections_total` | Counter | caller, tool | Rate limit rejections |
| `stile_tool_cache_refresh_total` | Counter | upstream, status | Tool cache refresh attempts |

Note: the design doc used `gateway_` prefix; use `stile_` instead to match the project name.

### Metrics endpoint

Serve Prometheus metrics on `GET /metrics` using `prometheus/client_golang`'s `promhttp.Handler()`.

### Integration

Create a `Metrics` struct with methods to record events. Pass it to the proxy handler, rate limiter, and router so they can increment counters:

```go
type Metrics struct {
    RequestsTotal        *prometheus.CounterVec
    RequestDuration      *prometheus.HistogramVec
    UpstreamHealth       *prometheus.GaugeVec
    RateLimitRejections  *prometheus.CounterVec
    ToolCacheRefresh     *prometheus.CounterVec
}

func New() *Metrics  // registers with prometheus.DefaultRegisterer
```

The proxy handler calls `metrics.RequestsTotal.WithLabelValues(caller, tool, upstream, status).Inc()` etc.

---

## 3. Audit Log

### Optional append-only log of all tool calls

For compliance and debugging. Stored in a SQLite database (reusing the existing `modernc.org/sqlite` dependency) behind a `Store` interface so the backend can be swapped later (e.g. Postgres).

### Package: `internal/audit`

### Config

```yaml
audit:
  enabled: false
  database: /var/lib/stile/audit.db   # SQLite database path
```

### Store interface

```go
type Entry struct {
    Timestamp  time.Time
    Caller     string
    Method     string
    Tool       string
    Upstream   string
    Params     json.RawMessage
    Status     string
    LatencyMS  int64
}

type Store interface {
    Log(ctx context.Context, entry Entry) error
    Close() error
}
```

The interface is write-only for now. Query/search methods can be added later.

### SQLite implementation

```go
type SQLiteStore struct {
    db *sql.DB
}

func NewSQLiteStore(dbPath string) (*SQLiteStore, error)
func (s *SQLiteStore) Log(ctx context.Context, entry Entry) error
func (s *SQLiteStore) Close() error
```

Schema:

```sql
CREATE TABLE IF NOT EXISTS audit_log (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    timestamp  DATETIME NOT NULL,
    caller     TEXT NOT NULL,
    method     TEXT NOT NULL,
    tool       TEXT,
    upstream   TEXT,
    params     TEXT,
    status     TEXT NOT NULL,
    latency_ms INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_audit_log_timestamp ON audit_log(timestamp);
CREATE INDEX IF NOT EXISTS idx_audit_log_caller ON audit_log(caller);
```

The store is called by the proxy handler after each request completes. If audit is disabled, the store is nil and the proxy handler skips the call. Writes are synchronous for v0.1.

---

## 4. Config Additions

Extend the config for logging and audit:

```yaml
logging:
  level: info
  format: json

audit:
  enabled: false
  database: /var/lib/stile/audit.db
```

Add the corresponding config types with the same unexported-fields-and-getters pattern.

---

## 5. Testable Deliverables

### Logging tests

1. **Request log output:** process a request through the proxy, capture slog output → log entry contains caller, method, tool, upstream, latency, status
2. **Error logging:** upstream returns error → log entry has status "error" and error message
3. **Debug level suppressed at info:** set level to info, make a request → no debug-level entries in output

### Metrics tests (`internal/metrics/`)

4. **Request counter increments:** process a request → `stile_requests_total` incremented with correct labels
5. **Duration histogram recorded:** process a request → `stile_request_duration_seconds` has an observation
6. **Rate limit rejection counted:** trigger a rate limit → `stile_rate_limit_rejections_total` incremented
7. **Metrics endpoint serves:** GET /metrics → valid Prometheus exposition format

### Audit log tests

8. **Audit entry written:** create SQLiteStore with in-memory DB, call Log → row exists with correct fields
9. **Audit disabled:** audit disabled in config → store is nil, proxy skips audit
10. **Store interface:** SQLiteStore satisfies Store interface (compile-time check)

### Build check

```bash
go build ./...
go test ./...
go vet ./...
```

---

## 6. Dependencies

This task adds: `prometheus/client_golang` for metrics.

---

## 7. Out of Scope

- Distributed tracing (OpenTelemetry)
- Log aggregation or shipping
- Metrics alerting rules
- Dashboard templates
