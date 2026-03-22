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
// Returns {allowed, count}: allowed=1/0, count=current entries in window.
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
    return {1, count + 1}
end
return {0, count}
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

// redisCheckResult holds the result of a single Redis rate limit check.
type redisCheckResult struct {
	allowed bool
	limit   int // configured max count
	count   int // current count in window
	window  int // window in seconds
}

// Allow checks all three rate limit levels via Redis. Returns nil if no limits
// are configured. Otherwise returns a RateLimitResult with the most restrictive
// limit's state. If Redis is unreachable, returns a denial (fail-closed).
func (rl *RedisRateLimiter) Allow(caller, tool, upstream string, roles []string) *RateLimitResult {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	now := time.Now().UnixMicro()

	type snapshot struct {
		limit     int
		remaining int
		window    int
	}

	var snapshots []snapshot
	var denial *Denial

	// Check per-caller limit.
	if caller != "" {
		limit := rl.effectiveCallerLimit(roles)
		if limit != nil {
			key := rl.keyPrefix + "rl:caller:" + caller
			cr := rl.checkLimitWithInfo(ctx, key, limit, now)
			remaining := cr.limit - cr.count
			if remaining < 0 {
				remaining = 0
			}
			if !cr.allowed {
				denial = &Denial{Level: "caller"}
			}
			snapshots = append(snapshots, snapshot{limit: cr.limit, remaining: remaining, window: cr.window})
		}
	}

	// Check per-caller-per-tool limit.
	if denial == nil && caller != "" && tool != "" {
		limit := rl.effectiveToolLimit(roles)
		if limit != nil {
			key := rl.keyPrefix + "rl:tool:" + caller + ":" + tool
			cr := rl.checkLimitWithInfo(ctx, key, limit, now)
			remaining := cr.limit - cr.count
			if remaining < 0 {
				remaining = 0
			}
			if !cr.allowed {
				denial = &Denial{Level: "tool"}
			}
			snapshots = append(snapshots, snapshot{limit: cr.limit, remaining: remaining, window: cr.window})
		}
	}

	// Check per-upstream limit.
	if denial == nil && upstream != "" {
		limit := rl.upstreamLimit(upstream)
		if limit != nil {
			key := rl.keyPrefix + "rl:upstream:" + upstream
			cr := rl.checkLimitWithInfo(ctx, key, limit, now)
			remaining := cr.limit - cr.count
			if remaining < 0 {
				remaining = 0
			}
			if !cr.allowed {
				denial = &Denial{Level: "upstream"}
			}
			snapshots = append(snapshots, snapshot{limit: cr.limit, remaining: remaining, window: cr.window})
		}
	}

	if len(snapshots) == 0 {
		return nil
	}

	// Pick the most restrictive (lowest remaining).
	best := snapshots[0]
	for _, s := range snapshots[1:] {
		if s.remaining < best.remaining {
			best = s
		}
	}

	nowTime := time.Now()
	result := &RateLimitResult{
		Denial:    denial,
		Limit:     best.limit,
		Remaining: best.remaining,
		ResetAt:   nowTime.Add(time.Duration(best.window) * time.Second),
	}

	if denial != nil && best.limit > 0 {
		result.RetryAfter = time.Duration(best.window) * time.Second / time.Duration(best.limit)
		if result.RetryAfter < time.Second {
			result.RetryAfter = time.Second
		}
	}

	return result
}

// Close closes the underlying Redis client.
func (rl *RedisRateLimiter) Close() error {
	return rl.client.Close()
}

// checkLimitWithInfo runs the sliding window script and returns structured info.
// On Redis errors, returns a denied result (fail-closed).
func (rl *RedisRateLimiter) checkLimitWithInfo(ctx context.Context, key string, limit *config.RateLimit, nowMicro int64) redisCheckResult {
	windowMicro := int64(limit.Window()) * 1_000_000

	vals, err := slidingWindowScript.Run(ctx, rl.client, []string{key},
		limit.Count(), windowMicro, nowMicro,
	).Int64Slice()

	if err != nil {
		slog.Error("redis rate limit check failed, denying request (fail-closed)",
			"key", key, "error", err)
		return redisCheckResult{
			allowed: false,
			limit:   limit.Count(),
			count:   limit.Count(),
			window:  limit.Window(),
		}
	}

	return redisCheckResult{
		allowed: vals[0] == 1,
		limit:   limit.Count(),
		count:   int(vals[1]),
		window:  limit.Window(),
	}
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
