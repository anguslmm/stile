package policy

import (
	"fmt"
	"testing"

	"github.com/anguslmm/stile/internal/config"
)

func localRateLimiterFromYAML(t *testing.T, yaml string) *LocalRateLimiter {
	t.Helper()
	cfg, err := config.LoadBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("LoadBytes: %v", err)
	}
	return NewLocalRateLimiter(cfg)
}

func TestUnderLimitPasses(t *testing.T) {
	rl := localRateLimiterFromYAML(t, `
upstreams:
  - name: svc
    transport: streamable-http
    url: http://fake
rate_limits:
  default_caller: 10/sec
  default_tool: 10/sec
`)

	for i := 0; i < 5; i++ {
		result := rl.Allow("alice", "tool-a", "svc", nil)
		if result != nil && result.Denial != nil {
			t.Fatalf("request %d should be allowed, denied by %s", i, result.Denial.Level)
		}
	}
}

func TestOverLimitRejects(t *testing.T) {
	rl := localRateLimiterFromYAML(t, `
upstreams:
  - name: svc
    transport: streamable-http
    url: http://fake
rate_limits:
  default_caller: 10/sec
  default_tool: 10/sec
`)

	rejected := 0
	for i := 0; i < 20; i++ {
		result := rl.Allow("alice", "tool-a", "svc", nil)
		if result != nil && result.Denial != nil {
			rejected++
		}
	}
	if rejected == 0 {
		t.Fatal("expected some requests to be rejected at 10/sec with 20 immediate requests")
	}
}

func TestPerCallerIsolation(t *testing.T) {
	rl := localRateLimiterFromYAML(t, `
upstreams:
  - name: svc
    transport: streamable-http
    url: http://fake
rate_limits:
  default_caller: 5/sec
  default_tool: 100/sec
`)

	// Exhaust caller A's limit.
	for i := 0; i < 20; i++ {
		rl.Allow("alice", "tool-a", "svc", nil)
	}

	// Caller B should still be allowed.
	result := rl.Allow("bob", "tool-a", "svc", nil)
	if result != nil && result.Denial != nil {
		t.Fatalf("bob should not be rate limited, denied by %s", result.Denial.Level)
	}
}

func TestPerToolIsolation(t *testing.T) {
	rl := localRateLimiterFromYAML(t, `
upstreams:
  - name: svc
    transport: streamable-http
    url: http://fake
rate_limits:
  default_caller: 100/sec
  default_tool: 5/sec
`)

	// Exhaust tool-a's limit for alice.
	for i := 0; i < 20; i++ {
		rl.Allow("alice", "tool-a", "svc", nil)
	}

	// Alice should still be allowed for tool-b.
	result := rl.Allow("alice", "tool-b", "svc", nil)
	if result != nil && result.Denial != nil {
		t.Fatalf("alice should be allowed for tool-b, denied by %s", result.Denial.Level)
	}
}

func TestPerUpstreamLimit(t *testing.T) {
	rl := localRateLimiterFromYAML(t, `
upstreams:
  - name: svc
    transport: streamable-http
    url: http://fake
    rate_limit: 10/sec
rate_limits:
  default_caller: 1000/sec
  default_tool: 1000/sec
`)

	rejected := 0
	// Two callers each sending rapid requests to the same upstream.
	for i := 0; i < 20; i++ {
		if r := rl.Allow("alice", "tool-a", "svc", nil); r != nil && r.Denial != nil {
			rejected++
		}
		if r := rl.Allow("bob", "tool-a", "svc", nil); r != nil && r.Denial != nil {
			rejected++
		}
	}
	if rejected == 0 {
		t.Fatal("expected some upstream-level rejections with 40 requests at 10/sec")
	}
}

func TestNoRateLimitsConfigured(t *testing.T) {
	rl := localRateLimiterFromYAML(t, `
upstreams:
  - name: svc
    transport: streamable-http
    url: http://fake
`)

	// With no rate limits, everything should pass (result is nil).
	for i := 0; i < 100; i++ {
		result := rl.Allow("alice", "tool-a", "svc", nil)
		if result != nil {
			t.Fatalf("request %d: expected nil result with no limits configured, got limit=%d remaining=%d", i, result.Limit, result.Remaining)
		}
	}
}

func TestEmptyCallerAllowed(t *testing.T) {
	rl := localRateLimiterFromYAML(t, `
upstreams:
  - name: svc
    transport: streamable-http
    url: http://fake
rate_limits:
  default_caller: 5/sec
`)

	// Empty caller should skip caller-level check.
	for i := 0; i < 20; i++ {
		result := rl.Allow("", "tool-a", "svc", nil)
		if result != nil && result.Denial != nil {
			t.Fatalf("request %d should be allowed with empty caller", i)
		}
	}
}

func TestRoleBasedCallerRates(t *testing.T) {
	rl := localRateLimiterFromYAML(t, `
upstreams:
  - name: svc
    transport: streamable-http
    url: http://fake
roles:
  premium:
    allowed_tools: ["*"]
    rate_limit: 100/sec
  basic:
    allowed_tools: ["*"]
    rate_limit: 5/sec
rate_limits:
  default_caller: 5/sec
  default_tool: 1000/sec
`)

	// Premium user should get high limit (roles passed via Allow).
	rejected := 0
	for i := 0; i < 50; i++ {
		if r := rl.Allow("premium-user", "tool-a", "svc", []string{"premium"}); r != nil && r.Denial != nil {
			rejected++
		}
	}
	if rejected > 0 {
		t.Errorf("premium user should allow 50 requests at 100/sec, got %d rejections", rejected)
	}
}

func TestDenialIndicatesLevel(t *testing.T) {
	rl := localRateLimiterFromYAML(t, `
upstreams:
  - name: svc
    transport: streamable-http
    url: http://fake
rate_limits:
  default_caller: 1/sec
  default_tool: 1000/sec
`)

	rl.Allow("alice", "tool-a", "svc", nil) // consume the 1 token

	result := rl.Allow("alice", "tool-a", "svc", nil)
	if result == nil || result.Denial == nil {
		t.Fatal("expected rejection")
	}
	if result.Denial.Level != "caller" {
		t.Errorf("expected denial level 'caller', got %q", result.Denial.Level)
	}
}

func TestToolLimiterMapCapped(t *testing.T) {
	rl := localRateLimiterFromYAML(t, `
upstreams:
  - name: svc
    transport: streamable-http
    url: http://fake
rate_limits:
  default_tool: 1000/sec
`)

	// Create more than maxToolLimitersPerCaller distinct tools.
	for i := 0; i < 1100; i++ {
		tool := fmt.Sprintf("tool-%d", i)
		rl.Allow("attacker", tool, "svc", nil)
	}

	// The map should be capped at maxToolLimitersPerCaller.
	rl.mu.Lock()
	count := len(rl.toolLimiters["attacker"])
	rl.mu.Unlock()

	if count > maxToolLimitersPerCaller {
		t.Errorf("tool limiter map grew to %d, expected cap at %d", count, maxToolLimitersPerCaller)
	}
}

func TestUpstreamDefaultRate(t *testing.T) {
	rl := localRateLimiterFromYAML(t, `
upstreams:
  - name: svc
    transport: streamable-http
    url: http://fake
rate_limits:
  default_upstream: 5/sec
`)

	rejected := 0
	for i := 0; i < 20; i++ {
		if r := rl.Allow("", "tool-a", "svc", nil); r != nil && r.Denial != nil {
			rejected++
		}
	}
	if rejected == 0 {
		t.Fatal("expected some rejections with default_upstream: 5/sec")
	}
}

func TestRateLimitResultFields(t *testing.T) {
	rl := localRateLimiterFromYAML(t, `
upstreams:
  - name: svc
    transport: streamable-http
    url: http://fake
rate_limits:
  default_caller: 5/sec
  default_tool: 5/sec
`)

	// First request: should be allowed and report state.
	result := rl.Allow("alice", "tool-a", "svc", nil)
	if result == nil {
		t.Fatal("expected non-nil result when limits are configured")
	}
	if result.Denial != nil {
		t.Fatal("first request should be allowed")
	}
	if result.Limit != 5 {
		t.Errorf("expected Limit=5, got %d", result.Limit)
	}
	if result.Remaining > 5 || result.Remaining < 0 {
		t.Errorf("Remaining out of range: %d", result.Remaining)
	}
}

func TestRateLimitRemainingDecreases(t *testing.T) {
	rl := localRateLimiterFromYAML(t, `
upstreams:
  - name: svc
    transport: streamable-http
    url: http://fake
rate_limits:
  default_caller: 10/sec
  default_tool: 10/sec
`)

	var prev int = -1
	decreased := false
	for i := 0; i < 5; i++ {
		result := rl.Allow("alice", "tool-a", "svc", nil)
		if result == nil || result.Denial != nil {
			t.Fatalf("request %d unexpectedly denied", i)
		}
		if prev >= 0 && result.Remaining < prev {
			decreased = true
		}
		prev = result.Remaining
	}
	if !decreased {
		t.Error("expected Remaining to decrease across requests")
	}
}

func TestDeniedResultHasRetryAfter(t *testing.T) {
	rl := localRateLimiterFromYAML(t, `
upstreams:
  - name: svc
    transport: streamable-http
    url: http://fake
rate_limits:
  default_caller: 1/sec
  default_tool: 1000/sec
`)

	rl.Allow("alice", "tool-a", "svc", nil) // consume the 1 token

	result := rl.Allow("alice", "tool-a", "svc", nil)
	if result == nil || result.Denial == nil {
		t.Fatal("expected denial")
	}
	if result.RetryAfter <= 0 {
		t.Errorf("expected positive RetryAfter, got %v", result.RetryAfter)
	}
	if result.Remaining != 0 {
		t.Errorf("expected Remaining=0 on denial, got %d", result.Remaining)
	}
}

// --- Benchmarks ---

func BenchmarkAllowNoLimits(b *testing.B) {
	rl := localRateLimiterFromYAML(&testing.T{}, `
upstreams:
  - name: svc
    transport: streamable-http
    url: http://fake
`)
	b.ResetTimer()
	for b.Loop() {
		rl.Allow("alice", "tool-a", "svc", nil)
	}
}

func benchAllow(b *testing.B, yaml string) {
	cfg, err := config.LoadBytes([]byte(yaml))
	if err != nil {
		b.Fatal(err)
	}
	rl := NewLocalRateLimiter(cfg)
	b.ResetTimer()
	for b.Loop() {
		rl.Allow(fmt.Sprintf("caller-%d", b.N%100), "tool-a", "svc", nil)
	}
}

func BenchmarkAllowCallerLimit(b *testing.B) {
	benchAllow(b, `
upstreams:
  - name: svc
    transport: streamable-http
    url: http://fake
rate_limits:
  default_caller: 100000/sec
  default_tool: 100000/sec
`)
}

func BenchmarkAllowAllLimits(b *testing.B) {
	benchAllow(b, `
upstreams:
  - name: svc
    transport: streamable-http
    url: http://fake
    rate_limit: 100000/sec
rate_limits:
  default_caller: 100000/sec
  default_tool: 100000/sec
  default_upstream: 100000/sec
`)
}

func BenchmarkAllowWithRoles(b *testing.B) {
	cfg, err := config.LoadBytes([]byte(`
upstreams:
  - name: svc
    transport: streamable-http
    url: http://fake
roles:
  premium:
    allowed_tools: ["*"]
    rate_limit: 100000/sec
    tool_rate_limit: 100000/sec
rate_limits:
  default_caller: 100000/sec
  default_tool: 100000/sec
`))
	if err != nil {
		b.Fatal(err)
	}
	rl := NewLocalRateLimiter(cfg)
	b.ResetTimer()
	for b.Loop() {
		rl.Allow("premium-user", "tool-a", "svc", []string{"premium"})
	}
}

func TestMostRestrictiveLimitReported(t *testing.T) {
	rl := localRateLimiterFromYAML(t, `
upstreams:
  - name: svc
    transport: streamable-http
    url: http://fake
rate_limits:
  default_caller: 100/sec
  default_tool: 3/sec
`)

	// Send 2 requests — tool limit (burst 3) will be more restrictive than caller (burst 100).
	rl.Allow("alice", "tool-a", "svc", nil)
	result := rl.Allow("alice", "tool-a", "svc", nil)
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	// The reported Limit should be 3 (the tool limit), not 100 (caller limit),
	// because tool limit has fewer remaining tokens.
	if result.Limit != 3 {
		t.Errorf("expected Limit=3 (most restrictive), got %d", result.Limit)
	}
}
