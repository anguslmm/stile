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
		denial := rl.Allow("alice", "tool-a", "svc", nil)
		if denial != nil {
			t.Fatalf("request %d should be allowed, denied by %s", i, denial.Level)
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
		denial := rl.Allow("alice", "tool-a", "svc", nil)
		if denial != nil {
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
	denial := rl.Allow("bob", "tool-a", "svc", nil)
	if denial != nil {
		t.Fatalf("bob should not be rate limited, denied by %s", denial.Level)
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
	denial := rl.Allow("alice", "tool-b", "svc", nil)
	if denial != nil {
		t.Fatalf("alice should be allowed for tool-b, denied by %s", denial.Level)
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
		if denial := rl.Allow("alice", "tool-a", "svc", nil); denial != nil {
			rejected++
		}
		if denial := rl.Allow("bob", "tool-a", "svc", nil); denial != nil {
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

	// With no rate limits, everything should pass.
	for i := 0; i < 100; i++ {
		denial := rl.Allow("alice", "tool-a", "svc", nil)
		if denial != nil {
			t.Fatalf("request %d should be allowed with no limits configured, denied by %s", i, denial.Level)
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
		denial := rl.Allow("", "tool-a", "svc", nil)
		if denial != nil {
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
		if denial := rl.Allow("premium-user", "tool-a", "svc", []string{"premium"}); denial != nil {
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

	denial := rl.Allow("alice", "tool-a", "svc", nil)
	if denial == nil {
		t.Fatal("expected rejection")
	}
	if denial.Level != "caller" {
		t.Errorf("expected denial level 'caller', got %q", denial.Level)
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
		if denial := rl.Allow("", "tool-a", "svc", nil); denial != nil {
			rejected++
		}
	}
	if rejected == 0 {
		t.Fatal("expected some rejections with default_upstream: 5/sec")
	}
}
