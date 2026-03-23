package policy

import (
	"testing"

	"github.com/alicebob/miniredis/v2"

	"github.com/anguslmm/stile/internal/config"
)

func redisRateLimiterFromYAML(t *testing.T, mr *miniredis.Miniredis, yaml string) *RedisRateLimiter {
	t.Helper()
	// Inject the miniredis address into the YAML.
	fullYAML := yaml + "\n  backend: redis\n  redis:\n    address: " + mr.Addr() + "\n"
	cfg, err := config.LoadBytes([]byte(fullYAML))
	if err != nil {
		t.Fatalf("LoadBytes: %v", err)
	}
	rl, err := NewRedisRateLimiter(cfg)
	if err != nil {
		t.Fatalf("NewRedisRateLimiter: %v", err)
	}
	t.Cleanup(func() { rl.Close() })
	return rl
}

func TestRedisUnderLimitPasses(t *testing.T) {
	sharedMR.FlushAll()

	rl := redisRateLimiterFromYAML(t, sharedMR, `
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

func TestRedisOverLimitRejects(t *testing.T) {
	sharedMR.FlushAll()

	rl := redisRateLimiterFromYAML(t, sharedMR, `
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

func TestRedisPerCallerIsolation(t *testing.T) {
	sharedMR.FlushAll()

	rl := redisRateLimiterFromYAML(t, sharedMR, `
upstreams:
  - name: svc
    transport: streamable-http
    url: http://fake
rate_limits:
  default_caller: 5/sec
  default_tool: 100/sec
`)

	// Exhaust caller A's limit.
	for i := 0; i < 10; i++ {
		rl.Allow("alice", "tool-a", "svc", nil)
	}

	// Caller B should still be allowed.
	result := rl.Allow("bob", "tool-a", "svc", nil)
	if result != nil && result.Denial != nil {
		t.Fatalf("bob should not be rate limited, denied by %s", result.Denial.Level)
	}
}

func TestRedisPerToolIsolation(t *testing.T) {
	sharedMR.FlushAll()

	rl := redisRateLimiterFromYAML(t, sharedMR, `
upstreams:
  - name: svc
    transport: streamable-http
    url: http://fake
rate_limits:
  default_caller: 100/sec
  default_tool: 5/sec
`)

	// Exhaust tool-a's limit for alice.
	for i := 0; i < 10; i++ {
		rl.Allow("alice", "tool-a", "svc", nil)
	}

	// Alice should still be allowed for tool-b.
	result := rl.Allow("alice", "tool-b", "svc", nil)
	if result != nil && result.Denial != nil {
		t.Fatalf("alice should be allowed for tool-b, denied by %s", result.Denial.Level)
	}
}

func TestRedisPerUpstreamLimit(t *testing.T) {
	sharedMR.FlushAll()

	rl := redisRateLimiterFromYAML(t, sharedMR, `
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

func TestRedisNoRateLimitsConfigured(t *testing.T) {
	sharedMR.FlushAll()

	rl := redisRateLimiterFromYAML(t, sharedMR, `
upstreams:
  - name: svc
    transport: streamable-http
    url: http://fake
rate_limits:
`)

	for i := 0; i < 100; i++ {
		result := rl.Allow("alice", "tool-a", "svc", nil)
		if result != nil {
			t.Fatalf("request %d: expected nil result with no limits configured", i)
		}
	}
}

func TestRedisFailClosedOnDisconnect(t *testing.T) {
	// This test stops miniredis mid-test, so it needs its own instance.
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("start miniredis: %v", err)
	}

	rl := redisRateLimiterFromYAML(t, mr, `
upstreams:
  - name: svc
    transport: streamable-http
    url: http://fake
rate_limits:
  default_caller: 100/sec
  default_tool: 100/sec
`)

	// Should work initially.
	result := rl.Allow("alice", "tool-a", "svc", nil)
	if result != nil && result.Denial != nil {
		t.Fatal("expected to be allowed while Redis is up")
	}

	// Stop Redis.
	mr.Close()

	// Should deny (fail-closed).
	result = rl.Allow("alice", "tool-a", "svc", nil)
	if result == nil || result.Denial == nil {
		t.Fatal("expected denial when Redis is down (fail-closed)")
	}
}

func TestRedisFailClosedAtStartup(t *testing.T) {
	yaml := `
upstreams:
  - name: svc
    transport: streamable-http
    url: http://fake
rate_limits:
  backend: redis
  redis:
    address: localhost:1
  default_caller: 10/sec
`
	cfg, err := config.LoadBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("LoadBytes: %v", err)
	}
	_, err = NewRedisRateLimiter(cfg)
	if err == nil {
		t.Fatal("expected error when Redis is unreachable at startup")
	}
}

func TestRedisGlobalLimitSharedAcrossInstances(t *testing.T) {
	sharedMR.FlushAll()

	yaml := `
upstreams:
  - name: svc
    transport: streamable-http
    url: http://fake
rate_limits:
  default_caller: 10/sec
  default_tool: 10/sec
  backend: redis
  redis:
    address: ` + sharedMR.Addr() + `
`
	cfg, err := config.LoadBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("LoadBytes: %v", err)
	}

	// Simulate two instances sharing the same Redis.
	rl1, err := NewRedisRateLimiter(cfg)
	if err != nil {
		t.Fatalf("NewRedisRateLimiter instance 1: %v", err)
	}
	defer rl1.Close()

	rl2, err := NewRedisRateLimiter(cfg)
	if err != nil {
		t.Fatalf("NewRedisRateLimiter instance 2: %v", err)
	}
	defer rl2.Close()

	// Each instance sends 5 requests (within individual limit of 10).
	for i := 0; i < 5; i++ {
		rl1.Allow("alice", "tool-a", "svc", nil)
	}
	for i := 0; i < 5; i++ {
		rl2.Allow("alice", "tool-a", "svc", nil)
	}

	// The 11th request (from either instance) should be denied.
	rejected := 0
	for i := 0; i < 5; i++ {
		if r := rl1.Allow("alice", "tool-a", "svc", nil); r != nil && r.Denial != nil {
			rejected++
		}
	}
	if rejected == 0 {
		t.Fatal("expected global rate limit to be enforced across instances")
	}
}

func TestRedisRoleBasedCallerRates(t *testing.T) {
	sharedMR.FlushAll()

	rl := redisRateLimiterFromYAML(t, sharedMR, `
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

	// Premium user should get high limit.
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

func TestRedisConfigParsing(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr bool
	}{
		{
			name: "local backend (default)",
			yaml: `
upstreams:
  - name: svc
    transport: streamable-http
    url: http://fake
`,
			wantErr: false,
		},
		{
			name: "explicit local backend",
			yaml: `
upstreams:
  - name: svc
    transport: streamable-http
    url: http://fake
rate_limits:
  backend: local
`,
			wantErr: false,
		},
		{
			name: "redis backend with address",
			yaml: `
upstreams:
  - name: svc
    transport: streamable-http
    url: http://fake
rate_limits:
  backend: redis
  redis:
    address: localhost:6379
`,
			wantErr: false,
		},
		{
			name: "redis backend without address",
			yaml: `
upstreams:
  - name: svc
    transport: streamable-http
    url: http://fake
rate_limits:
  backend: redis
`,
			wantErr: true,
		},
		{
			name: "invalid backend",
			yaml: `
upstreams:
  - name: svc
    transport: streamable-http
    url: http://fake
rate_limits:
  backend: memcached
`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := config.LoadBytes([]byte(tt.yaml))
			if (err != nil) != tt.wantErr {
				t.Errorf("LoadBytes() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
