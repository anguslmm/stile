package health

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// ErrNotFound is returned by StatusStore.Get when no status exists for an upstream.
var ErrNotFound = errors.New("health: status not found")

// Status is the health status of a single upstream as stored in a StatusStore.
type Status struct {
	Healthy   bool      `json:"healthy"`
	CheckedAt time.Time `json:"checked_at"`
}

// StatusStore reads and writes upstream health status.
type StatusStore interface {
	Get(ctx context.Context, upstream string) (Status, error)
	Set(ctx context.Context, upstream string, status Status, ttl time.Duration) error
}

// LocalStore is an in-memory StatusStore for single-instance deployments.
type LocalStore struct {
	mu     sync.RWMutex
	status map[string]Status
}

// Compile-time interface check.
var _ StatusStore = (*LocalStore)(nil)

// NewLocalStore creates a LocalStore.
func NewLocalStore() *LocalStore {
	return &LocalStore{status: make(map[string]Status)}
}

// Get returns the stored status for an upstream, or ErrNotFound.
func (s *LocalStore) Get(_ context.Context, upstream string) (Status, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	st, ok := s.status[upstream]
	if !ok {
		return Status{}, ErrNotFound
	}
	return st, nil
}

// Set stores the health status for an upstream. The ttl is ignored for in-memory storage.
func (s *LocalStore) Set(_ context.Context, upstream string, status Status, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status[upstream] = status
	return nil
}

// RedisStore is a Redis-backed StatusStore for centralized health checking.
type RedisStore struct {
	client    *redis.Client
	keyPrefix string
}

// Compile-time interface check.
var _ StatusStore = (*RedisStore)(nil)

// NewRedisStore creates a RedisStore. The keyPrefix is prepended to health keys
// (e.g. "stile:" → keys like "stile:health:upstream-name").
func NewRedisStore(client *redis.Client, keyPrefix string) *RedisStore {
	return &RedisStore{client: client, keyPrefix: keyPrefix}
}

// Get returns the stored health status for an upstream from Redis.
// Returns ErrNotFound if the key does not exist or has expired.
func (s *RedisStore) Get(ctx context.Context, upstream string) (Status, error) {
	key := s.keyPrefix + "health:" + upstream
	data, err := s.client.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return Status{}, ErrNotFound
	}
	if err != nil {
		return Status{}, err
	}
	var st Status
	if err := json.Unmarshal(data, &st); err != nil {
		return Status{}, err
	}
	return st, nil
}

// Set stores the health status for an upstream in Redis with the given TTL.
func (s *RedisStore) Set(ctx context.Context, upstream string, status Status, ttl time.Duration) error {
	key := s.keyPrefix + "health:" + upstream
	data, err := json.Marshal(status)
	if err != nil {
		return err
	}
	return s.client.Set(ctx, key, data, ttl).Err()
}

// Close closes the underlying Redis client.
func (s *RedisStore) Close() error {
	return s.client.Close()
}
