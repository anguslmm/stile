# Task 31 — Audit: reconcile task docs with codebase

Status: **done**

## Prerequisites

All other tasks (1–30) must be complete before starting this one.

## Goal

Walk through every task document (task-01.md through task-30.md) one by one and verify that the codebase matches what the task describes. Fix whichever side is wrong:

- **Code doesn't match task**: The implementation is missing or incomplete — fix the code.
- **Task doesn't match code**: The design evolved during implementation or a deliberate change was made — update the task doc to reflect what was actually built and why.

## Process

For each task:

1. Read the task doc.
2. Verify each deliverable, file, interface, behavior, and test described in the doc against the current codebase.
3. If there's a mismatch, determine which is correct (the code or the doc) and fix the other.
4. Mark the task as reviewed in the checklist below.

## Checklist

- [x] Task 1 — Project scaffold + JSON-RPC 2.0 codec ✓ match
- [x] Task 2 — Config + Transport interface + HTTP transport client ✓ match
- [x] Task 3 — Inbound server + proxy handler ✓ match
- [x] Task 4 — Stdio transport ✓ match
- [x] Task 5 — Router + route table + tool discovery/caching ✓ match
- [x] Task 6 — Auth middleware ✓ **doc updated** — signatures evolved (KeyLookupResult, key label, AdminAuthOption)
- [x] Task 6.1 — Role-based access control ✓ match
- [x] Task 6.2 — Decouple roles from API keys ✓ match
- [x] Task 6.3 — CLI caller management ✓ match
- [x] Task 7 — Rate limiting ✓ match
- [x] Task 8 — Observability ✓ match
- [x] Task 9 — Health checks + graceful shutdown + hardening ✓ match
- [x] Task 9.1 — Cleanup — main.go and wiring layer ✓ **doc updated** — reload/SetRateLimiter items superseded by Task 18.1
- [x] Task 10 — Admin API for caller management ✓ **doc updated** — roles moved from per-key to per-caller (Task 6.2)
- [x] Task 10.1 — Admin API — role management endpoints ✓ match
- [x] Task 11 — Integration tests + release packaging ✓ match
- [x] Task 12 — Critical security fixes ✓ match
- [x] Task 13 — HTTP transport hardening ✓ **doc updated** — ResponseHeaderTimeout used instead of Client.Timeout (SSE compat)
- [x] Task 14 — SQLite + rate limiter hardening ✓ match
- [x] Task 15 — Code health + minor fixes ✓ match
- [x] Task 16 — OpenTelemetry observability ✓ match
- [x] Task 17 — Configurable database backend with Postgres support ✓ match
- [x] Task 18 — Redis-backed rate limiting ✓ match
- [x] Task 18.1 — Remove config hot-reload mechanism ✓ match
- [x] Task 19 — Horizontal scaling documentation and stdio guidance ✓ match
- [x] Task 20 — Upstream resilience ✓ match
- [x] Task 21 — Trace context propagation ✓ match
- [x] Task 22 — Rate limit response headers ✓ match
- [x] Task 23 — Load testing and performance benchmarks ✓ match
- [x] Task 24 — Operational runbooks ✓ match
- [x] Task 25 — Centralized health checks ✓ match
- [x] Task 26 — TLS and mTLS support ✓ match
- [x] Task 27 — `stile wrap` — stdio-to-HTTP adapter subcommand ✓ match
- [x] Task 27.1 — Add OpenTelemetry tracing to `stile wrap` ✓ match
- [x] Task 28 — Admin CLI remote mode (`--remote`) ✓ match
- [x] Task 29 — In-memory cache for hot-path auth lookups ✓ match
- [x] Task 29.1 — Fix flaky test suite under parallel execution ✓ match
- [x] Task 30 — Admin dashboard (embedded web UI) ✓ match
