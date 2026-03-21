# Task 20: TLS and mTLS Support

**Status:** todo
**Depends on:** 15

---

## Goal

Add TLS termination for inbound connections and TLS/mTLS for outbound connections to upstream MCP servers. Without this, API keys and auth tokens travel in plaintext, which is a non-starter for production deployments.

---

## 1. Inbound TLS termination

Add TLS config to the server section:

```yaml
server:
  address: ":8443"
  tls:
    cert_file: /path/to/cert.pem
    key_file: /path/to/key.pem
    min_version: "1.2"           # optional, default TLS 1.2
    client_ca_file: /path/to/ca.pem  # optional, enables mTLS for inbound
```

**Implementation:**

- Add `TLSConfig` type to `internal/config/` with getters for each field
- In `main.go`, if TLS is configured, use `httpServer.ListenAndServeTLS()` instead of `ListenAndServe()`
- Build a `tls.Config` with `MinVersion`, and optionally `ClientCAs` + `ClientAuth: tls.RequireAndVerifyClientCert` when `client_ca_file` is set
- If TLS is not configured, continue to serve plaintext (for development and for deployments where TLS is terminated at the load balancer)

---

## 2. Outbound TLS for HTTP upstreams

Upstream URLs already support `https://`, but there's no way to configure custom CA certificates or client certificates (mTLS) for upstream connections.

Add per-upstream TLS config:

```yaml
upstreams:
  - name: secure-tools
    transport: streamable-http
    url: https://tools.internal:8443/mcp
    tls:
      ca_file: /path/to/internal-ca.pem      # custom CA for upstream
      cert_file: /path/to/client-cert.pem     # client cert for mTLS
      key_file: /path/to/client-key.pem       # client key for mTLS
      insecure_skip_verify: false             # never true in prod, useful for dev
```

**Implementation:**

- Add `TLSConfig` to `UpstreamConfig`
- In `NewHTTPTransport`, build a custom `tls.Config` and set it on the `http.Transport`
- If no TLS config is provided, use the default system CA pool (current behavior)

---

## 3. TLS reload on SIGHUP

When config is reloaded, update TLS certificates without restarting. Use `tls.Config.GetCertificate` with a callback that reads the current cert from a reloadable field, rather than loading the cert once at startup.

---

## Verification

- Existing tests pass (no TLS configured = plaintext, unchanged behavior)
- Add test: server accepts HTTPS connections with valid cert
- Add test: server rejects connections when client cert is required but not provided
- Add test: HTTP transport connects to upstream with custom CA
- Add test: HTTP transport sends client cert for mTLS upstream
- Add test: invalid TLS config (missing key file, etc.) fails at startup with clear error
- Test: `insecure_skip_verify: true` works for dev scenarios
