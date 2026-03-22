// Package policy enforces rate limits, ACLs, and input validation.
package policy

import (
	"math"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/anguslmm/stile/internal/config"
)

// Denial describes which rate limit was hit.
type Denial struct {
	Level string // "caller", "tool", or "upstream"
}

// RateLimitResult holds the outcome of a rate limit check along with state
// for populating response headers. A nil result means no limits are configured.
type RateLimitResult struct {
	Denial     *Denial       // nil if allowed
	Limit      int           // total requests allowed (burst / window count)
	Remaining  int           // requests remaining in current window/bucket
	ResetAt    time.Time     // when the limit fully resets
	RetryAfter time.Duration // suggested wait before retry (only meaningful on denial)
}

// RateLimiter checks whether a request is allowed under the configured rate limits.
// Implementations must be safe for concurrent use.
type RateLimiter interface {
	Allow(caller, tool, upstream string, roles []string) *RateLimitResult
}

// LocalRateLimiter enforces token bucket rate limits at three granularities:
// per-caller, per-caller-per-tool, and per-upstream. All state is in-memory.
type LocalRateLimiter struct {
	mu               sync.Mutex
	callerLimiters   map[string]*rate.Limiter
	toolLimiters     map[string]map[string]*rate.Limiter
	upstreamLimiters map[string]*rate.Limiter

	defaultCallerRate  rate.Limit
	defaultCallerBurst int
	defaultToolRate    rate.Limit
	defaultToolBurst   int

	// Per-role overrides for caller and tool rates.
	roleCallerRate  map[string]rate.Limit
	roleCallerBurst map[string]int
	roleToolRate    map[string]rate.Limit
	roleToolBurst   map[string]int

	// Per-caller overrides (set via registerCaller).
	callerRate      map[string]rate.Limit
	callerBurst     map[string]int
	callerToolRate  map[string]rate.Limit
	callerToolBurst map[string]int
}

// Compile-time interface check.
var _ RateLimiter = (*LocalRateLimiter)(nil)

// NewLocalRateLimiter creates a LocalRateLimiter from config. Per-upstream limiters are
// created eagerly; per-caller and per-tool limiters are created lazily.
func NewLocalRateLimiter(cfg *config.Config) *LocalRateLimiter {
	defaults := cfg.RateLimitDefaults()

	rl := &LocalRateLimiter{
		callerLimiters:   make(map[string]*rate.Limiter),
		toolLimiters:     make(map[string]map[string]*rate.Limiter),
		upstreamLimiters: make(map[string]*rate.Limiter),
		roleCallerRate:   make(map[string]rate.Limit),
		roleCallerBurst:  make(map[string]int),
		roleToolRate:     make(map[string]rate.Limit),
		roleToolBurst:    make(map[string]int),
		callerRate:       make(map[string]rate.Limit),
		callerBurst:      make(map[string]int),
		callerToolRate:   make(map[string]rate.Limit),
		callerToolBurst:  make(map[string]int),
	}

	// Set global defaults. rate.Inf means no limit.
	if d := defaults.DefaultCaller(); d != nil {
		rl.defaultCallerRate = rate.Limit(d.Rate())
		rl.defaultCallerBurst = d.Burst()
	} else {
		rl.defaultCallerRate = rate.Inf
		rl.defaultCallerBurst = 0
	}
	if d := defaults.DefaultTool(); d != nil {
		rl.defaultToolRate = rate.Limit(d.Rate())
		rl.defaultToolBurst = d.Burst()
	} else {
		rl.defaultToolRate = rate.Inf
		rl.defaultToolBurst = 0
	}

	defaultUpstreamRate := rate.Inf
	defaultUpstreamBurst := 0
	if d := defaults.DefaultUpstream(); d != nil {
		defaultUpstreamRate = rate.Limit(d.Rate())
		defaultUpstreamBurst = d.Burst()
	}

	// Create per-upstream limiters eagerly.
	for _, u := range cfg.Upstreams() {
		if u.RateLimit() != nil {
			rl.upstreamLimiters[u.Name()] = rate.NewLimiter(
				rate.Limit(u.RateLimit().Rate()),
				u.RateLimit().Burst(),
			)
		} else if defaultUpstreamRate != rate.Inf {
			rl.upstreamLimiters[u.Name()] = rate.NewLimiter(defaultUpstreamRate, defaultUpstreamBurst)
		}
	}

	// Store per-role rate overrides.
	for _, r := range cfg.Roles() {
		if r.RateLimit() != nil {
			rl.roleCallerRate[r.Name()] = rate.Limit(r.RateLimit().Rate())
			rl.roleCallerBurst[r.Name()] = r.RateLimit().Burst()
		}
		if r.ToolRateLimit() != nil {
			rl.roleToolRate[r.Name()] = rate.Limit(r.ToolRateLimit().Rate())
			rl.roleToolBurst[r.Name()] = r.ToolRateLimit().Burst()
		}
	}

	return rl
}

// limitSnapshot captures the state of a single rate.Limiter after a check.
type limitSnapshot struct {
	limit     int
	remaining int
	ratePerS  float64
}

// Allow checks all three rate limit levels. Returns nil if no limits are
// configured. Otherwise returns a RateLimitResult with the most restrictive
// limit's state (lowest Remaining). On denial, returns the first denying limit.
func (rl *LocalRateLimiter) Allow(caller, tool, upstream string, roles []string) *RateLimitResult {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	// Register caller rates from roles (idempotent after first call per caller).
	if caller != "" && len(roles) > 0 {
		rl.registerCallerLocked(caller, roles)
	}

	var snapshots []limitSnapshot
	var denial *Denial

	// Check per-caller limit.
	if caller != "" {
		lim := rl.getOrCreateCallerLimiterLocked(caller)
		if lim != nil {
			if !lim.Allow() {
				denial = &Denial{Level: "caller"}
				snapshots = append(snapshots, snapshotFromLimiter(lim))
			} else {
				snapshots = append(snapshots, snapshotFromLimiter(lim))
			}
		}
	}

	// Check per-caller-per-tool limit.
	if denial == nil && caller != "" && tool != "" {
		lim := rl.getOrCreateToolLimiterLocked(caller, tool)
		if lim != nil {
			if !lim.Allow() {
				denial = &Denial{Level: "tool"}
				snapshots = append(snapshots, snapshotFromLimiter(lim))
			} else {
				snapshots = append(snapshots, snapshotFromLimiter(lim))
			}
		}
	}

	// Check per-upstream limit.
	if denial == nil && upstream != "" {
		if lim, ok := rl.upstreamLimiters[upstream]; ok {
			if !lim.Allow() {
				denial = &Denial{Level: "upstream"}
				snapshots = append(snapshots, snapshotFromLimiter(lim))
			} else {
				snapshots = append(snapshots, snapshotFromLimiter(lim))
			}
		}
	}

	if len(snapshots) == 0 {
		return nil
	}

	// Pick the most restrictive snapshot (lowest remaining).
	best := snapshots[0]
	for _, s := range snapshots[1:] {
		if s.remaining < best.remaining {
			best = s
		}
	}

	now := time.Now()
	deficit := float64(best.limit - best.remaining)
	resetAt := now
	if deficit > 0 && best.ratePerS > 0 {
		resetAt = now.Add(time.Duration(deficit / best.ratePerS * float64(time.Second)))
	}

	result := &RateLimitResult{
		Denial:    denial,
		Limit:     best.limit,
		Remaining: best.remaining,
		ResetAt:   resetAt,
	}

	if denial != nil && best.ratePerS > 0 {
		result.RetryAfter = time.Duration(math.Ceil(1.0/best.ratePerS)) * time.Second
	}

	return result
}

// snapshotFromLimiter captures current state from a rate.Limiter.
func snapshotFromLimiter(lim *rate.Limiter) limitSnapshot {
	tokens := lim.Tokens()
	remaining := int(tokens)
	if remaining < 0 {
		remaining = 0
	}
	return limitSnapshot{
		limit:     lim.Burst(),
		remaining: remaining,
		ratePerS:  float64(lim.Limit()),
	}
}

// registerCallerLocked sets per-caller rates from roles. Only the first call
// per caller takes effect.
func (rl *LocalRateLimiter) registerCallerLocked(caller string, roles []string) {
	if _, exists := rl.callerRate[caller]; exists {
		return
	}

	// Find the most permissive caller rate from roles.
	callerRate := rl.defaultCallerRate
	callerBurst := rl.defaultCallerBurst
	for _, role := range roles {
		if r, ok := rl.roleCallerRate[role]; ok {
			if callerRate == rate.Inf {
				callerRate = r
				callerBurst = rl.roleCallerBurst[role]
			} else if r > callerRate {
				callerRate = r
				callerBurst = rl.roleCallerBurst[role]
			}
		}
	}

	// Find the most permissive tool rate from roles.
	toolRate := rl.defaultToolRate
	toolBurst := rl.defaultToolBurst
	for _, role := range roles {
		if r, ok := rl.roleToolRate[role]; ok {
			if toolRate == rate.Inf {
				toolRate = r
				toolBurst = rl.roleToolBurst[role]
			} else if r > toolRate {
				toolRate = r
				toolBurst = rl.roleToolBurst[role]
			}
		}
	}

	rl.callerRate[caller] = callerRate
	rl.callerBurst[caller] = callerBurst
	rl.callerToolRate[caller] = toolRate
	rl.callerToolBurst[caller] = toolBurst
}

func (rl *LocalRateLimiter) getOrCreateCallerLimiterLocked(caller string) *rate.Limiter {
	if lim, ok := rl.callerLimiters[caller]; ok {
		return lim
	}

	r := rl.defaultCallerRate
	b := rl.defaultCallerBurst
	if cr, ok := rl.callerRate[caller]; ok {
		r = cr
		b = rl.callerBurst[caller]
	}

	if r == rate.Inf {
		return nil
	}

	lim := rate.NewLimiter(r, b)
	rl.callerLimiters[caller] = lim
	return lim
}

const maxToolLimitersPerCaller = 1000

func (rl *LocalRateLimiter) getOrCreateToolLimiterLocked(caller, tool string) *rate.Limiter {
	callerTools, ok := rl.toolLimiters[caller]
	if !ok {
		callerTools = make(map[string]*rate.Limiter)
		rl.toolLimiters[caller] = callerTools
	}

	if lim, ok := callerTools[tool]; ok {
		return lim
	}

	r := rl.defaultToolRate
	b := rl.defaultToolBurst
	if cr, ok := rl.callerToolRate[caller]; ok {
		r = cr
		b = rl.callerToolBurst[caller]
	}

	if r == rate.Inf {
		return nil
	}

	if len(callerTools) >= maxToolLimitersPerCaller {
		// Return a one-shot limiter instead of caching to prevent unbounded growth.
		return rate.NewLimiter(r, b)
	}

	lim := rate.NewLimiter(r, b)
	callerTools[tool] = lim
	return lim
}
