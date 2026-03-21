package policy

import (
	"fmt"

	"github.com/anguslmm/stile/internal/config"
)

// NewRateLimiterFromConfig creates the appropriate RateLimiter based on config.
func NewRateLimiterFromConfig(cfg *config.Config) (RateLimiter, error) {
	switch cfg.RateLimitBackend() {
	case "redis":
		return NewRedisRateLimiter(cfg)
	default:
		return NewLocalRateLimiter(cfg), nil
	}
}

// CloseRateLimiter closes the rate limiter if it implements io.Closer.
// This is a no-op for LocalRateLimiter.
func CloseRateLimiter(rl RateLimiter) {
	if c, ok := rl.(*RedisRateLimiter); ok {
		if err := c.Close(); err != nil {
			fmt.Printf("error closing redis rate limiter: %v\n", err)
		}
	}
}
