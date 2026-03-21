# Observability

Stile exports three pillars of observability: **OTLP traces**, **Prometheus metrics**, and **trace-correlated structured logs**.

## What Stile exports

| Signal | Format | Endpoint / Output |
|--------|--------|-------------------|
| Traces | OTLP over HTTP | Exported to configured `telemetry.traces.endpoint` |
| Metrics | Prometheus | `GET /metrics` scrape endpoint |
| Logs | Structured JSON (slog) | stderr, with `trace_id` / `span_id` fields |

## Configuration reference

```yaml
telemetry:
  traces:
    enabled: false              # opt-in (default: false)
    endpoint: "localhost:4318"  # OTLP HTTP endpoint (default when enabled)
    sample_rate: 1.0            # 0.0 to 1.0 (default: 1.0 when enabled)
```

When `traces.enabled` is `false` or omitted, Stile uses a no-op tracer with zero overhead. Metrics and logs work regardless of the tracing setting.

## Trace span structure

Every inbound `POST /mcp` request creates a span tree:

```
[handleMCP]
  +- [auth]                         caller authentication
  +- [dispatch]                     method dispatch (tools/call)
  |    +- [route + rate limit]      tool routing + rate limit check
  |    +- [upstream.RoundTrip]      HTTP call or stdio write to upstream
  +- [StreamResult.WriteResponse]   SSE stream copy (if streaming)
```

Span attributes:
- `mcp.method` — JSON-RPC method (e.g. `tools/call`)
- `mcp.tool` — tool name being called
- `mcp.upstream` — upstream that handled the request
- `mcp.caller` — authenticated caller name (or `anonymous`)
- `mcp.status` — `ok` or `error`

The `StreamResult.WriteResponse` span is the key diagnostic for SSE streams — if a stream dies mid-flight, the span captures the error and exact duration.

## Deployment patterns

### Direct export

Point `endpoint` at any OTLP-compatible receiver:

```yaml
telemetry:
  traces:
    enabled: true
    endpoint: "tempo.internal:4318"     # Grafana Tempo
    # endpoint: "localhost:4318"        # Jaeger OTLP
    # endpoint: "dd-agent:4318"         # Datadog Agent
    sample_rate: 1.0
```

### OTel Collector sidecar

For production, run an [OpenTelemetry Collector](https://opentelemetry.io/docs/collector/) as a sidecar. The Collector handles routing, batching, tail sampling, and backend auth. Point Stile at it:

```yaml
telemetry:
  traces:
    enabled: true
    endpoint: "localhost:4318"   # Collector sidecar
    sample_rate: 1.0             # Send everything to Collector; it does the sampling
```

Recommended Collector config with tail sampling (keep 100% of error traces, sample baseline traffic):

```yaml
# otel-collector-config.yaml
receivers:
  otlp:
    protocols:
      http:
        endpoint: "0.0.0.0:4318"

processors:
  tail_sampling:
    decision_wait: 10s
    policies:
      - name: errors
        type: status_code
        status_code:
          status_codes: [ERROR]
      - name: baseline
        type: probabilistic
        probabilistic:
          sampling_percentage: 10

exporters:
  otlp:
    endpoint: "tempo.internal:4317"

service:
  pipelines:
    traces:
      receivers: [otlp]
      processors: [tail_sampling]
      exporters: [otlp]
```

### Prometheus metrics

The `/metrics` endpoint works unchanged — no configuration needed beyond having at least one upstream. The metrics are produced via the OpenTelemetry Prometheus exporter bridge, so they appear in standard Prometheus format with the same metric names.

## Head vs. tail sampling

The `sample_rate` in Stile's config controls **head sampling** — a coarse volume reduction applied at trace creation time. Setting it to `0.1` means only 10% of requests create a full trace.

**Tail sampling** (e.g. "keep all error traces regardless of head sampling") is configured in the OTel Collector, not in Stile. For production deployments:

1. Set `sample_rate: 1.0` in Stile (send everything to the Collector)
2. Configure tail sampling policies in the Collector (see example above)

This gives you 100% visibility into errors while controlling storage costs for normal traffic.
