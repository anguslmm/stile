# Stile — Implementation Progress

This document tracks the status of each implementation task. Agents should read this before starting work and update it when they complete a task.

## Task Status

| # | Task | Doc | Status |
|---|------|-----|--------|
| 1 | Project scaffold + JSON-RPC 2.0 codec | [task-01.md](tasks/task-01.md) | done |
| 2 | Config + Transport interface + HTTP transport client | [task-02.md](tasks/task-02.md) | done |
| 3 | Inbound server + proxy handler | [task-03.md](tasks/task-03.md) | done |
| 4 | Stdio transport | [task-04.md](tasks/task-04.md) | done |
| 5 | Router + route table + tool discovery/caching | [task-05.md](tasks/task-05.md) | done |
| 6 | Auth middleware | [task-06.md](tasks/task-06.md) | done |
| 6.1 | Role-based access control | [task-06.1.md](tasks/task-06.1.md) | done |
| 6.2 | Decouple roles from API keys | [task-06.2.md](tasks/task-06.2.md) | done |
| 6.3 | CLI caller management | [task-06.3.md](tasks/task-06.3.md) | done |
| 7 | Rate limiting | [task-07.md](tasks/task-07.md) | done |
| 8 | Observability | [task-08.md](tasks/task-08.md) | done |
| 9 | Health checks + graceful shutdown + hardening | [task-09.md](tasks/task-09.md) | done |
| 9.1 | Cleanup — main.go and wiring layer | [task-09.1.md](tasks/task-09.1.md) | done |
| 10 | Admin API for caller management | [task-10.md](tasks/task-10.md) | done |
| 10.1 | Admin API — role management endpoints | [task-10.1.md](tasks/task-10.1.md) | done |
| 11 | Integration tests + release packaging | [task-11.md](tasks/task-11.md) | done |

## Notes

- Tasks should be completed in order — each depends on the ones before it.
- Also update the task specific document to say "done" once it is complete.
- After Task 3, you can connect an MCP agent to the gateway and see it proxy to an HTTP upstream.
- If a task changes something that affects a later task doc, update that doc.
- The design doc is at [stile-design-doc.md](stile-design-doc.md).
