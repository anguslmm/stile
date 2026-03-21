// Package policy enforces rate limits, ACLs, and input validation.
package policy

import (
	"sync"

	"golang.org/x/time/rate"

	"github.com/anguslmm/stile/internal/config"
)

// Denial describes which rate limit was hit.
type Denial struct {
	Level string // "caller", "tool", or "upstream"
}

// RateLimiter checks whether a request is allowed under the configured rate limits.
// Implementations must be safe for concurrent use.
type RateLimiter interface {
	Allow(caller, tool, upstream string, roles []string) *Denial
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

// Allow checks all three rate limit levels. Returns nil if allowed, or a
// Denial indicating which limit was hit. Roles are used to determine the
// effective rate for the caller (most permissive role wins).
func (rl *LocalRateLimiter) Allow(caller, tool, upstream string, roles []string) *Denial {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	// Register caller rates from roles (idempotent after first call per caller).
	if caller != "" && len(roles) > 0 {
		rl.registerCallerLocked(caller, roles)
	}

	// Check per-caller limit.
	if caller != "" {
		lim := rl.getOrCreateCallerLimiterLocked(caller)
		if lim != nil && !lim.Allow() {
			return &Denial{Level: "caller"}
		}
	}

	// Check per-caller-per-tool limit.
	if caller != "" && tool != "" {
		lim := rl.getOrCreateToolLimiterLocked(caller, tool)
		if lim != nil && !lim.Allow() {
			return &Denial{Level: "tool"}
		}
	}

	// Check per-upstream limit.
	if upstream != "" {
		if lim, ok := rl.upstreamLimiters[upstream]; ok {
			if !lim.Allow() {
				return &Denial{Level: "upstream"}
			}
		}
	}

	return nil
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
