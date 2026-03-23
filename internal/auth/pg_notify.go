package auth

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

const pgChannel = "stile_auth_invalidate"

// PGNotifyListener listens for Postgres NOTIFY messages on the
// stile_auth_invalidate channel and evicts cache entries accordingly.
// It also provides a NotifyFunc for the CachedStore to broadcast
// invalidations to other instances.
type PGNotifyListener struct {
	dsn   string
	db    *sql.DB // shared pool, used for sending NOTIFY
	cache *CachedStore

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewPGNotifyListener creates and starts a listener. It uses a dedicated pgx
// connection for LISTEN and the shared sql.DB pool for sending NOTIFY.
func NewPGNotifyListener(dsn string, db *sql.DB, cache *CachedStore) (*PGNotifyListener, error) {
	ctx, cancel := context.WithCancel(context.Background())

	l := &PGNotifyListener{
		dsn:    dsn,
		db:     db,
		cache:  cache,
		cancel: cancel,
	}

	// Verify dedicated connection works before returning.
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("pg_notify: connect: %w", err)
	}
	conn.Close(ctx)

	l.wg.Add(1)
	go l.listen(ctx)

	return l, nil
}

// NotifyFunc returns a CacheNotifyFunc that broadcasts via Postgres NOTIFY.
func (l *PGNotifyListener) NotifyFunc() CacheNotifyFunc {
	return func(kind, callerName string) {
		payload := kind + ":" + callerName
		_, err := l.db.Exec("SELECT pg_notify($1, $2)", pgChannel, payload)
		if err != nil {
			slog.Warn("pg_notify: send failed", "payload", payload, "error", err)
		} else {
			slog.Debug("pg_notify: sent", "payload", payload)
		}
	}
}

// Close stops the listener and waits for cleanup.
func (l *PGNotifyListener) Close() {
	l.cancel()
	l.wg.Wait()
}

func (l *PGNotifyListener) listen(ctx context.Context) {
	defer l.wg.Done()

	for {
		if ctx.Err() != nil {
			return
		}
		l.listenLoop(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
			slog.Debug("pg_notify: reconnecting")
		}
	}
}

func (l *PGNotifyListener) listenLoop(ctx context.Context) {
	conn, err := pgx.Connect(ctx, l.dsn)
	if err != nil {
		slog.Warn("pg_notify: connect failed", "error", err)
		return
	}
	defer conn.Close(ctx)

	if _, err := conn.Exec(ctx, "LISTEN "+pgChannel); err != nil {
		slog.Warn("pg_notify: LISTEN failed", "error", err)
		return
	}
	slog.Debug("pg_notify: listening", "channel", pgChannel)

	for {
		notification, err := conn.WaitForNotification(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Warn("pg_notify: wait failed", "error", err)
			return
		}
		l.handleNotification(notification.Payload)
	}
}

func (l *PGNotifyListener) handleNotification(payload string) {
	if payload == "flush:" {
		l.cache.flushLocal()
		slog.Debug("pg_notify: flushed cache")
		return
	}
	parts := strings.SplitN(payload, ":", 2)
	if len(parts) != 2 {
		slog.Warn("pg_notify: invalid payload", "payload", payload)
		return
	}
	kind, callerName := parts[0], parts[1]
	switch kind {
	case "keys":
		l.cache.EvictKeys(callerName)
		slog.Debug("pg_notify: evicted keys", "caller", callerName)
	case "roles":
		l.cache.EvictRoles(callerName)
		slog.Debug("pg_notify: evicted roles", "caller", callerName)
	default:
		slog.Warn("pg_notify: unknown kind", "kind", kind, "payload", payload)
	}
}
