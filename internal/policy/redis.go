package policy

import (
	"context"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/anguslmm/stile/internal/config"
)

// slidingWindowScript is a Lua script for atomic sliding window rate limiting.
// It uses a sorted set where scores are timestamps in microseconds.
// Returns 1 if allowed, 0 if denied.
var slidingWindowScript = redis.NewScript(`
local key = KEYS[1]
local limit = tonumber(ARGV[1])
local window_us = tonumber(ARGV[2])
local now = tonumber(ARGV[3])

redis.call('ZREMRANGEBYSCORE', key, 0, now - window_us)
local count = redis.call('ZCARD', key)

if count < limit then
    redis.call('ZADD', key, now, now .. ':' .. math.random())
    redis.call('EXPIRE', key, math.ceil(window_us / 1000000) + 1)
    return 1
end
return 0
`)

// RedisRateLimiter enforces sliding window rate limits backed by Redis,
// enabling global rate limiting across multiple Stile instances.
type RedisRateLimiter struct {
	client    *redis.Client
	keyPrefix string

	defaultCaller   *config.RateLimit
	defaultTool     *config.RateLimit
	defaultUpstream *config.RateLimit

	roleCaller map[string]*config.RateLimit
	roleTool   map[string]*config.RateLimit

	upstreamLimits map[string]*config.RateLimit
}

// Compile-time interface check.
var _ RateLimiter = (*RedisRateLimiter)(nil)

// NewRedisRateLimiter creates a RedisRateLimiter from config.
// It pings Redis at startup and returns an error if unreachable (fail-closed).
func NewRedisRateLimiter(cfg *config.Config) (*RedisRateLimiter, error) {
	redisCfg := cfg.RedisConfig()

	client := redis.NewClient(&redis.Options{
		Addr:     redisCfg.Address(),
		Password: redisCfg.Password(),
		DB:       redisCfg.DB(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		client.Close()
		return nil, err
	}

	defaults := cfg.RateLimitDefaults()

	rl := &RedisRateLimiter{
		client:    client,
		keyPrefix: redisCfg.KeyPrefix(),

		defaultCaller:   defaults.DefaultCaller(),
		defaultTool:     defaults.DefaultTool(),
		defaultUpstream: defaults.DefaultUpstream(),

		roleCaller:     make(map[string]*config.RateLimit),
		roleTool:       make(map[string]*config.RateLimit),
		upstreamLimits: make(map[string]*config.RateLimit),
	}

	for _, r := range cfg.Roles() {
		if r.RateLimit() != nil {
			rl.roleCaller[r.Name()] = r.RateLimit()
		}
		if r.ToolRateLimit() != nil {
			rl.roleTool[r.Name()] = r.ToolRateLimit()
		}
	}

	for _, u := range cfg.Upstreams() {
		if u.RateLimit() != nil {
			rl.upstreamLimits[u.Name()] = u.RateLimit()
		}
	}

	return rl, nil
}

// Allow checks all three rate limit levels via Redis. Returns nil if allowed,
// or a Denial. If Redis is unreachable at runtime, returns a denial (fail-closed).
func (rl *RedisRateLimiter) Allow(caller, tool, upstream string, roles []string) *Denial {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	now := time.Now().UnixMicro()

	// Check per-caller limit.
	if caller != "" {
		limit := rl.effectiveCallerLimit(roles)
		if limit != nil {
			key := rl.keyPrefix + "rl:caller:" + caller
			if !rl.checkLimit(ctx, key, limit, now) {
				return &Denial{Level: "caller"}
			}
		}
	}

	// Check per-caller-per-tool limit.
	if caller != "" && tool != "" {
		limit := rl.effectiveToolLimit(roles)
		if limit != nil {
			key := rl.keyPrefix + "rl:tool:" + caller + ":" + tool
			if !rl.checkLimit(ctx, key, limit, now) {
				return &Denial{Level: "tool"}
			}
		}
	}

	// Check per-upstream limit.
	if upstream != "" {
		limit := rl.upstreamLimit(upstream)
		if limit != nil {
			key := rl.keyPrefix + "rl:upstream:" + upstream
			if !rl.checkLimit(ctx, key, limit, now) {
				return &Denial{Level: "upstream"}
			}
		}
	}

	return nil
}

// Close closes the underlying Redis client.
func (rl *RedisRateLimiter) Close() error {
	return rl.client.Close()
}

// checkLimit runs the sliding window script. Returns true if allowed.
// On Redis errors, returns false (fail-closed).
func (rl *RedisRateLimiter) checkLimit(ctx context.Context, key string, limit *config.RateLimit, nowMicro int64) bool {
	windowMicro := int64(limit.Window()) * 1_000_000

	result, err := slidingWindowScript.Run(ctx, rl.client, []string{key},
		limit.Count(), windowMicro, nowMicro,
	).Int64()

	if err != nil {
		slog.Error("redis rate limit check failed, denying request (fail-closed)",
			"key", key, "error", err)
		return false
	}
	return result == 1
}

// effectiveCallerLimit returns the most permissive caller rate from the given
// roles, falling back to the default. Returns nil if no limit is configured.
func (rl *RedisRateLimiter) effectiveCallerLimit(roles []string) *config.RateLimit {
	var best *config.RateLimit
	for _, role := range roles {
		if r, ok := rl.roleCaller[role]; ok {
			if best == nil || r.Rate() > best.Rate() {
				best = r
			}
		}
	}
	if best != nil {
		return best
	}
	return rl.defaultCaller
}

// effectiveToolLimit returns the most permissive tool rate from the given
// roles, falling back to the default. Returns nil if no limit is configured.
func (rl *RedisRateLimiter) effectiveToolLimit(roles []string) *config.RateLimit {
	var best *config.RateLimit
	for _, role := range roles {
		if r, ok := rl.roleTool[role]; ok {
			if best == nil || r.Rate() > best.Rate() {
				best = r
			}
		}
	}
	if best != nil {
		return best
	}
	return rl.defaultTool
}

// upstreamLimit returns the rate limit for the named upstream, falling back
// to the default upstream limit. Returns nil if no limit is configured.
func (rl *RedisRateLimiter) upstreamLimit(upstream string) *config.RateLimit {
	if l, ok := rl.upstreamLimits[upstream]; ok {
		return l
	}
	return rl.defaultUpstream
}
