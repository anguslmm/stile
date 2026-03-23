package health

import (
	"context"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestLocalStoreGetSet(t *testing.T) {
	store := NewLocalStore()
	ctx := context.Background()

	// Get on empty store returns ErrNotFound.
	_, err := store.Get(ctx, "upstream-a")
	if err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}

	// Set and Get.
	now := time.Now()
	err = store.Set(ctx, "upstream-a", Status{Healthy: true, CheckedAt: now}, time.Minute)
	if err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	st, err := store.Get(ctx, "upstream-a")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if !st.Healthy {
		t.Error("expected healthy=true")
	}

	// Different key still not found.
	_, err = store.Get(ctx, "upstream-b")
	if err != ErrNotFound {
		t.Fatalf("expected ErrNotFound for unknown key, got %v", err)
	}
}

func TestRedisStoreGetSet(t *testing.T) {
	sharedMR.FlushAll()
	client := redis.NewClient(&redis.Options{Addr: sharedMR.Addr()})
	defer client.Close()

	store := NewRedisStore(client, "stile:")
	ctx := context.Background()

	// Get on empty store returns ErrNotFound.
	_, err := store.Get(ctx, "upstream-a")
	if err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}

	// Set and Get.
	now := time.Now().Truncate(time.Millisecond)
	err = store.Set(ctx, "upstream-a", Status{Healthy: true, CheckedAt: now}, 10*time.Second)
	if err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	st, err := store.Get(ctx, "upstream-a")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if !st.Healthy {
		t.Error("expected healthy=true")
	}

	// Verify key format.
	keys, err := client.Keys(ctx, "stile:health:*").Result()
	if err != nil {
		t.Fatalf("Keys failed: %v", err)
	}
	if len(keys) != 1 || keys[0] != "stile:health:upstream-a" {
		t.Errorf("unexpected keys: %v", keys)
	}

	// Verify TTL was set.
	ttl := sharedMR.TTL("stile:health:upstream-a")
	if ttl <= 0 {
		t.Error("expected positive TTL on key")
	}
}

func TestRedisStoreExpiry(t *testing.T) {
	sharedMR.FlushAll()
	client := redis.NewClient(&redis.Options{Addr: sharedMR.Addr()})
	defer client.Close()

	store := NewRedisStore(client, "stile:")
	ctx := context.Background()

	err := store.Set(ctx, "upstream-a", Status{Healthy: true, CheckedAt: time.Now()}, 2*time.Second)
	if err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	// Key exists before expiry.
	_, err = store.Get(ctx, "upstream-a")
	if err != nil {
		t.Fatalf("Get before expiry failed: %v", err)
	}

	// Fast-forward past TTL.
	sharedMR.FastForward(3 * time.Second)

	// Key should be expired.
	_, err = store.Get(ctx, "upstream-a")
	if err != ErrNotFound {
		t.Fatalf("expected ErrNotFound after expiry, got %v", err)
	}
}

func TestRedisStoreOverwrite(t *testing.T) {
	sharedMR.FlushAll()
	client := redis.NewClient(&redis.Options{Addr: sharedMR.Addr()})
	defer client.Close()

	store := NewRedisStore(client, "test:")
	ctx := context.Background()

	// Set healthy, then unhealthy.
	store.Set(ctx, "up", Status{Healthy: true, CheckedAt: time.Now()}, time.Minute)
	store.Set(ctx, "up", Status{Healthy: false, CheckedAt: time.Now()}, time.Minute)

	st, err := store.Get(ctx, "up")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if st.Healthy {
		t.Error("expected healthy=false after overwrite")
	}
}
