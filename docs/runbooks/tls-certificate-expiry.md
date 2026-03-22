# TLS Certificate Expiry

## Severity

P1 (if expired — agents cannot connect; P3 if approaching expiry with time to act)

## Symptoms

- Agents fail to connect to Stile with TLS handshake errors
- Browser or curl shows certificate expired: `SSL certificate problem: certificate has expired`
- Monitoring alerts on certificate expiry (if configured)
- For upstream TLS: Stile logs show TLS errors when connecting to upstream MCP servers

## Likely Causes

1. **Inbound certificate expired** — the TLS certificate on the load balancer, reverse proxy (Caddy, nginx), or service mesh sidecar fronting Stile has expired.
2. **Upstream certificate expired** — an upstream MCP server's certificate has expired, causing Stile's HTTP transport to fail TLS verification.
3. **Certificate auto-renewal failed** — Let's Encrypt or ACME renewal did not complete (DNS challenge failure, rate limits, permissions).
4. **Manual certificate not rotated** — certificate was provisioned manually and the rotation was missed.

## Diagnosis Steps

1. Check the inbound certificate expiry (from outside):
   ```bash
   echo | openssl s_client -connect <stile-domain>:443 -servername <stile-domain> 2>/dev/null | openssl x509 -noout -dates
   ```

2. Check upstream certificate expiry:
   ```bash
   echo | openssl s_client -connect <upstream-host>:443 -servername <upstream-host> 2>/dev/null | openssl x509 -noout -dates
   ```

3. If using Caddy, check its certificate status:
   ```bash
   caddy list-certificates
   ```

4. If using a cloud load balancer, check the certificate in the cloud console (ACM, GCP Certificate Manager, etc.).

5. Check Stile logs for TLS-related errors:
   ```bash
   journalctl -u stile | grep -iE 'tls|certificate|x509' | tail -20
   ```

6. Check if Stile itself is running fine (TLS is terminated before Stile):
   ```bash
   curl http://localhost:8080/healthz  # direct to Stile, bypassing TLS
   ```

## Remediation

**Inbound certificate (load balancer / reverse proxy):**

- **Caddy (auto-HTTPS):** Caddy auto-renews. Check that port 80/443 is accessible for ACME challenges and that Caddy has write access to its data directory. Restart Caddy if stuck:
  ```bash
  systemctl restart caddy
  ```

- **nginx:** Replace the certificate files and reload:
  ```bash
  cp new-cert.pem /etc/ssl/cert.pem
  cp new-key.pem /etc/ssl/key.pem
  nginx -s reload
  ```

- **Cloud load balancer (AWS ALB/NLB):** Update the certificate in ACM or upload a new one and associate it with the listener.

- **Kubernetes / service mesh:** Update the TLS secret:
  ```bash
  kubectl create secret tls stile-tls --cert=new-cert.pem --key=new-key.pem --dry-run=client -o yaml | kubectl apply -f -
  ```

**Upstream certificate expired:**
- This is on the upstream owner to fix. Stile cannot bypass TLS verification safely.
- As a temporary workaround if the upstream is internal and trusted, the upstream owner should provision a new certificate.

**Prevent recurrence:**
- Set up certificate expiry monitoring (e.g., Prometheus blackbox exporter `probe_ssl_earliest_cert_expiry`).
- Use auto-renewing certificate solutions (Caddy, cert-manager, ACM).
- Calendar reminders for manually provisioned certificates.

## Escalation

- If the certificate is managed by a cloud provider and auto-renewal failed, open a support ticket.
- If the upstream's certificate expired, contact the upstream service owner.
- If internal PKI is involved, escalate to the security/infrastructure team.
