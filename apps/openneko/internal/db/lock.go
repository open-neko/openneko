package db

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// LockConn is the session that holds an advisory lock. The lock lives as long
// as the session, so it must be one connection, not a pool.
type LockConn interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// LockReportEvery is how often a waiting process says who holds the lock.
var LockReportEvery = 15 * time.Second

const lockPoll = 250 * time.Millisecond

// Lock takes the session advisory lock key for this database. While another
// session holds it, Lock reports the holder at once and then every
// LockReportEvery, until it gets the lock or ctx ends.
func Lock(ctx context.Context, conn LockConn, key int64, what string, logf func(string, ...any)) (func(), error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	var reported time.Time
	for {
		var got bool
		if err := conn.QueryRow(ctx, `select pg_try_advisory_lock($1)`, key).Scan(&got); err != nil {
			return nil, fmt.Errorf("take the %s lock: %w", what, err)
		}
		if got {
			return func() {
				_, _ = conn.Exec(context.WithoutCancel(ctx), `select pg_advisory_unlock($1)`, key)
			}, nil
		}
		if reported.IsZero() || time.Since(reported) >= LockReportEvery {
			logf("waiting for the %s lock: %s", what, lockHolder(ctx, conn, key))
			reported = time.Now()
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("take the %s lock: %w", what, ctx.Err())
		case <-time.After(lockPoll):
		}
	}
}

func lockHolder(ctx context.Context, conn LockConn, key int64) string {
	var pid int32
	var app string
	var seconds float64
	err := conn.QueryRow(ctx, `
select a.pid, coalesce(a.application_name, ''), extract(epoch from now() - a.backend_start)::float8
from pg_locks l
join pg_stat_activity a on a.pid = l.pid
where l.locktype = 'advisory' and l.granted
  and l.database = (select oid from pg_database where datname = current_database())
  and l.classid::bigint = $1 and l.objid::bigint = $2 and l.objsubid = 1
limit 1`, int64(uint64(key)>>32), int64(uint32(key))).Scan(&pid, &app, &seconds)
	if err != nil {
		return "another session holds it"
	}
	if app == "" {
		app = "no application name"
	}
	return fmt.Sprintf("process %d (%s) holds it, connected %s ago", pid, app, (time.Duration(seconds) * time.Second).Round(time.Second))
}
