package policy

import (
	"testing"

	"github.com/anguslmm/stile/internal/config"
)

func rateLimiterFromYAML(t *testing.T, yaml string) *RateLimiter {
	t.Helper()
	cfg, err := config.LoadBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("LoadBytes: %v", err)
	}
	return NewRateLimiter(cfg)
}

func TestUnderLimitPasses(t *testing.T) {
	rl := rateLimiterFromYAML(t, `
upstreams:
  - name: svc
    transport: streamable-http
    url: http://fake
rate_limits:
  default_caller: 10/sec
  default_tool: 10/sec
`)

	for i := 0; i < 5; i++ {
		ok, denial := rl.Allow("alice", "tool-a", "svc")
		if !ok {
			t.Fatalf("request %d should be allowed, denied by %s", i, denial.Level)
		}
	}
}

func TestOverLimitRejects(t *testing.T) {
	rl := rateLimiterFromYAML(t, `
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
		ok, _ := rl.Allow("alice", "tool-a", "svc")
		if !ok {
			rejected++
		}
	}
	if rejected == 0 {
		t.Fatal("expected some requests to be rejected at 10/sec with 20 immediate requests")
	}
}

func TestPerCallerIsolation(t *testing.T) {
	rl := rateLimiterFromYAML(t, `
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
		rl.Allow("alice", "tool-a", "svc")
	}

	// Caller B should still be allowed.
	ok, denial := rl.Allow("bob", "tool-a", "svc")
	if !ok {
		t.Fatalf("bob should not be rate limited, denied by %s", denial.Level)
	}
}

func TestPerToolIsolation(t *testing.T) {
	rl := rateLimiterFromYAML(t, `
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
		rl.Allow("alice", "tool-a", "svc")
	}

	// Alice should still be allowed for tool-b.
	ok, denial := rl.Allow("alice", "tool-b", "svc")
	if !ok {
		t.Fatalf("alice should be allowed for tool-b, denied by %s", denial.Level)
	}
}

func TestPerUpstreamLimit(t *testing.T) {
	rl := rateLimiterFromYAML(t, `
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
		if ok, _ := rl.Allow("alice", "tool-a", "svc"); !ok {
			rejected++
		}
		if ok, _ := rl.Allow("bob", "tool-a", "svc"); !ok {
			rejected++
		}
	}
	if rejected == 0 {
		t.Fatal("expected some upstream-level rejections with 40 requests at 10/sec")
	}
}

func TestNoRateLimitsConfigured(t *testing.T) {
	rl := rateLimiterFromYAML(t, `
upstreams:
  - name: svc
    transport: streamable-http
    url: http://fake
`)

	// With no rate limits, everything should pass.
	for i := 0; i < 100; i++ {
		ok, denial := rl.Allow("alice", "tool-a", "svc")
		if !ok {
			t.Fatalf("request %d should be allowed with no limits configured, denied by %s", i, denial.Level)
		}
	}
}

func TestEmptyCallerAllowed(t *testing.T) {
	rl := rateLimiterFromYAML(t, `
upstreams:
  - name: svc
    transport: streamable-http
    url: http://fake
rate_limits:
  default_caller: 5/sec
`)

	// Empty caller should skip caller-level check.
	for i := 0; i < 20; i++ {
		ok, _ := rl.Allow("", "tool-a", "svc")
		if !ok {
			t.Fatalf("request %d should be allowed with empty caller", i)
		}
	}
}

func TestRoleBasedCallerRates(t *testing.T) {
	rl := rateLimiterFromYAML(t, `
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

	// Register premium caller.
	rl.RegisterCaller("premium-user", []string{"premium"})

	// Premium user should get high limit.
	rejected := 0
	for i := 0; i < 50; i++ {
		if ok, _ := rl.Allow("premium-user", "tool-a", "svc"); !ok {
			rejected++
		}
	}
	if rejected > 0 {
		t.Errorf("premium user should allow 50 requests at 100/sec, got %d rejections", rejected)
	}
}

func TestDenialIndicatesLevel(t *testing.T) {
	rl := rateLimiterFromYAML(t, `
upstreams:
  - name: svc
    transport: streamable-http
    url: http://fake
rate_limits:
  default_caller: 1/sec
  default_tool: 1000/sec
`)

	rl.Allow("alice", "tool-a", "svc") // consume the 1 token

	ok, denial := rl.Allow("alice", "tool-a", "svc")
	if ok {
		t.Fatal("expected rejection")
	}
	if denial.Level != "caller" {
		t.Errorf("expected denial level 'caller', got %q", denial.Level)
	}
}

func TestUpstreamDefaultRate(t *testing.T) {
	rl := rateLimiterFromYAML(t, `
upstreams:
  - name: svc
    transport: streamable-http
    url: http://fake
rate_limits:
  default_upstream: 5/sec
`)

	rejected := 0
	for i := 0; i < 20; i++ {
		if ok, _ := rl.Allow("", "tool-a", "svc"); !ok {
			rejected++
		}
	}
	if rejected == 0 {
		t.Fatal("expected some rejections with default_upstream: 5/sec")
	}
}
