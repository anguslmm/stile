# Task 31 — Audit: reconcile task docs with codebase

Status: **todo**

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

- [ ] Task 1 — Project scaffold + JSON-RPC 2.0 codec
- [ ] Task 2 — Config + Transport interface + HTTP transport client
- [ ] Task 3 — Inbound server + proxy handler
- [ ] Task 4 — Stdio transport
- [ ] Task 5 — Router + route table + tool discovery/caching
- [ ] Task 6 — Auth middleware
- [ ] Task 6.1 — Role-based access control
- [ ] Task 6.2 — Decouple roles from API keys
- [ ] Task 6.3 — CLI caller management
- [ ] Task 7 — Rate limiting
- [ ] Task 8 — Observability
- [ ] Task 9 — Health checks + graceful shutdown + hardening
- [ ] Task 9.1 — Cleanup — main.go and wiring layer
- [ ] Task 10 — Admin API for caller management
- [ ] Task 10.1 — Admin API — role management endpoints
- [ ] Task 11 — Integration tests + release packaging
- [ ] Task 12 — Critical security fixes
- [ ] Task 13 — HTTP transport hardening
- [ ] Task 14 — SQLite + rate limiter hardening
- [ ] Task 15 — Code health + minor fixes
- [ ] Task 16 — OpenTelemetry observability
- [ ] Task 17 — Configurable database backend with Postgres support
- [ ] Task 18 — Redis-backed rate limiting
- [ ] Task 18.1 — Remove config hot-reload mechanism
- [ ] Task 19 — Horizontal scaling documentation and stdio guidance
- [ ] Task 20 — Upstream resilience
- [ ] Task 21 — Trace context propagation
- [ ] Task 22 — Rate limit response headers
- [ ] Task 23 — Load testing and performance benchmarks
- [ ] Task 24 — Operational runbooks
- [ ] Task 25 — Centralized health checks
- [ ] Task 26 — TLS and mTLS support
- [ ] Task 27 — `stile wrap` — stdio-to-HTTP adapter subcommand
- [ ] Task 27.1 — Add OpenTelemetry tracing to `stile wrap`
- [ ] Task 28 — Admin CLI remote mode (`--remote`)
- [ ] Task 29 — In-memory cache for hot-path auth lookups
- [ ] Task 29.1 — Fix flaky test suite under parallel execution
- [ ] Task 30 — Admin dashboard (embedded web UI)
