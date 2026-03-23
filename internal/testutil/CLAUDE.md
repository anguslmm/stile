# testutil

Test helpers for preventing ephemeral port exhaustion in test suites that create many short-lived HTTP servers.

## Key Exported Functions

- **`NewServer(handler)`** — Drop-in replacement for `httptest.NewServer`. Returns a started server with SO_LINGER=0 on all accepted connections.
- **`NewUnstartedServer(handler)`** — Drop-in replacement for `httptest.NewUnstartedServer`. Caller must call `srv.Start()` or `srv.StartTLS()` after additional configuration.
- **`PatchDefaultTransport()`** — Replaces `http.DefaultTransport` with a clone that disables keep-alives and sets SO_LINGER=0 on outgoing connections. Call once in `TestMain` for packages using `http.DefaultClient`, `http.Get`, or `http.Post`.
- **`PatchTransport(t *http.Transport)`** — Same as above but applied to a caller-supplied transport (for packages not using `http.DefaultTransport`).

## Design Notes

- The root problem: macOS has a limited ephemeral port range. Tests creating many short-lived TCP connections accumulate TIME_WAIT state, exhausting ports and causing flaky failures.
- SO_LINGER=0 causes `Close` to send TCP RST instead of FIN, bypassing TIME_WAIT entirely. This is applied to both sides: server-side via `lingerListener` (unexported), client-side via `lingerControl` (unexported dial hook).
- Disabling keep-alives on client transports ensures connections are not pooled and reused, so each connection is closed promptly after each request.
- `lingerListener` is unexported; it is only exposed indirectly through `NewServer` and `NewUnstartedServer`.
