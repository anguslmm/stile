# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project

Stile is a reverse proxy gateway for the Model Context Protocol (MCP), written in Go. It sits between AI agents and MCP tool servers, providing authentication, routing, rate limiting, and observability as a single binary.

## Build & Test Commands

```bash
go build ./...                          # Build everything
go test ./...                           # Run all tests
go test ./internal/jsonrpc/ -v          # Run tests for a single package
go test ./internal/jsonrpc/ -run TestParseSingleRequest -v  # Run a single test
go vet ./...                            # Static analysis
```

## Architecture

The request flow: Agent → HTTP server → auth middleware → router (tool→upstream lookup) → policy (rate limits, ACLs) → proxy → upstream (via Transport interface) → response back.

Key abstraction: the `Transport` interface (`Send`, `ListTools`, `Close`, `Healthy`) hides whether an upstream is a remote HTTP server or a local stdio process. The router and proxy layers are transport-agnostic.

### Package layout (`internal/`)

- **jsonrpc** — JSON-RPC 2.0 codec (Request, Response, Error, ID types, batch parsing). Hand-written, no framework.
- **config** — YAML config loading. Types use unexported fields with getters; `Load()` returns valid config or error.
- **transport** — `Transport` interface + HTTP (Streamable HTTP/SSE) and stdio implementations.
- **router** — Route table mapping tool names to upstreams, tool discovery/caching via `tools/list`.
- **auth** — Inbound API key auth (SHA-256 hashed lookup), outbound credential injection per upstream.
- **policy** — Token bucket rate limiting (per-caller, per-tool, per-upstream), ACL checks, optional JSON Schema input validation.
- **proxy** — Core proxy handler dispatching requests through the pipeline.
- **health** — Upstream health checks, `/healthz` and `/readyz` endpoints.

## Code Conventions

- **No public `Validate()` methods.** Validate at construction time — constructors/parsers return valid objects or errors. If you have a `*Request`, it's well-formed.
- **Config types are immutable.** Unexported fields, exported getters. Slice getters return copies.
- **Correctness over brevity.** Go boilerplate is fine.
- **Minimal dependencies.** stdlib where possible. External deps: `gopkg.in/yaml.v3`, `golang.org/x/time/rate`, `prometheus/client_golang`, `santhosh-tekuri/jsonschema`, `gobwas/glob`.
- **Compile-time interface checks.** Every concrete type that implements an interface must have a `var _ Interface = (*Type)(nil)` assertion (or value literal for value-receiver types). Add these when creating new implementations.

## Git

- Do not add `Co-Authored-By` lines to commits.

## Documentation

- **`docs/request-flow.md`** — End-to-end walkthrough of the request path through Stile. If you change the request pipeline (auth, routing, proxying, response handling), update this doc to match.
- **`docs/stile-design-doc.md`** — Original design doc.
- **Per-package `CLAUDE.md`** — Each package in `internal/` has its own `CLAUDE.md` summarizing its types, functions, and design constraints. When you change a package's exported interface (add/remove/rename types or functions, change key behavior), update its `CLAUDE.md` to match.

## Development Workflow

Tasks are defined in `docs/tasks/task-01.md` through `task-10.md` and must be completed in order. Track status in `docs/progress.md`. The design doc is at `docs/stile-design-doc.md`.
