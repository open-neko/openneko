package webfront

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// The queue names and send options of packages/db/src/jobs.ts. The worker
// creates the pgboss schema when it starts.

// insertJob is insertJob() of pg-boss 10.4.2 (src/plans.js).
const insertJob = `
    INSERT INTO %[1]s.job (
      id, name, data, priority, start_after, singleton_key, singleton_on, dead_letter,
      expire_in, keep_until, retry_limit, retry_delay, retry_backoff, policy
    )
    SELECT
      id, j.name, data, priority, start_after, singleton_key, singleton_on,
      COALESCE(j.dead_letter, q.dead_letter) as dead_letter,
      CASE
        WHEN expire_in IS NOT NULL THEN expire_in::numeric * interval '1s'
        WHEN q.expire_seconds IS NOT NULL THEN q.expire_seconds * interval '1s'
        WHEN expire_in_default IS NOT NULL THEN expire_in_default::numeric * interval '1s'
        ELSE interval '15 minutes'
        END as expire_in,
      CASE
        WHEN right(keep_until, 1) = 'Z' THEN CAST(keep_until as timestamp with time zone)
        ELSE start_after + CAST(COALESCE(keep_until, (q.retention_minutes * 60)::text, keep_until_default, '14 days') as interval)
        END as keep_until,
      COALESCE(j.retry_limit, q.retry_limit, retry_limit_default, 2) as retry_limit,
      CASE
        WHEN COALESCE(j.retry_backoff, q.retry_backoff, retry_backoff_default, false)
        THEN GREATEST(COALESCE(j.retry_delay, q.retry_delay, retry_delay_default), 1)
        ELSE COALESCE(j.retry_delay, q.retry_delay, retry_delay_default, 0)
        END as retry_delay,
      COALESCE(j.retry_backoff, q.retry_backoff, retry_backoff_default, false) as retry_backoff,
      q.policy
    FROM
      ( SELECT
          COALESCE($1::uuid, gen_random_uuid()) as id,
          $2 as name,
          $3::jsonb as data,
          COALESCE($4::int, 0) as priority,
          CASE
            WHEN right($5, 1) = 'Z' THEN CAST($5 as timestamp with time zone)
            ELSE now() + CAST(COALESCE($5,'0') as interval)
            END as start_after,
          $6 as singleton_key,
          CASE
            WHEN $7::integer IS NOT NULL THEN 'epoch'::timestamp + '1 second'::interval * ($7 * floor((date_part('epoch', now()) + $8) / $7))
            ELSE NULL
            END as singleton_on,
          $9 as dead_letter,
          $10 as expire_in,
          $11 as expire_in_default,
          $12 as keep_until,
          $13 as keep_until_default,
          $14::int as retry_limit,
          $15::int as retry_limit_default,
          $16::int as retry_delay,
          $17::int as retry_delay_default,
          $18::bool as retry_backoff,
          $19::bool as retry_backoff_default
      ) j JOIN %[1]s.queue q ON j.name = q.name
    ON CONFLICT DO NOTHING
    RETURNING id::text`

func (a *API) ensureQueue(ctx context.Context, name string) error {
	if _, ok := a.queues.Load(name); ok {
		return nil
	}
	if _, err := a.DB.Exec(ctx, `select `+a.bossSchema()+`.create_queue($1, '{"policy":"standard"}'::json)`, name); err != nil {
		return err
	}
	a.queues.Store(name, true)
	return nil
}

// enqueue sends a job as enqueue() in packages/db/src/jobs.ts does: retry
// limit 2, retry delay 30 s, and the 7-day retention of the shared boss.
func (a *API) enqueue(ctx context.Context, tx pgx.Tx, queue string, data any) (string, error) {
	return a.enqueueWith(ctx, tx, queue, data, 2)
}

func (a *API) enqueueWith(ctx context.Context, tx pgx.Tx, queue string, data any, retryLimit int) (string, error) {
	return a.send(ctx, tx, queue, data, retryLimit, nil, nil)
}

// enqueueSingleton is enqueue() with singletonKey and singletonSeconds: a
// second job with the key in the same window is not queued.
func (a *API) enqueueSingleton(ctx context.Context, tx pgx.Tx, queue string, data any, key string, seconds int) (string, error) {
	return a.send(ctx, tx, queue, data, 2, &key, &seconds)
}

func (a *API) send(ctx context.Context, tx pgx.Tx, queue string, data any, retryLimit int, singletonKey *string, singletonSeconds *int) (string, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return "", err
	}
	if queue == queueWorkflowRunFire {
		if raw, err = withQueuedAt(raw); err != nil {
			return "", err
		}
	}
	var id string
	err = tx.QueryRow(ctx, fmt.Sprintf(insertJob, a.bossSchema()), nil, queue, string(raw), 0, nil, singletonKey, singletonSeconds, 0.0, nil, nil, nil, nil,
		"7 days", retryLimit, nil, 30, nil, nil, nil).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return id, err
}

func (a *API) bossSchema() string {
	if a.BossSchema != "" {
		return pgx.Identifier{a.BossSchema}.Sanitize()
	}
	return "pgboss"
}

// withQueuedAt adds the queuedAt that enqueue() sets on the queues whose
// worker reports the queue wait.
func withQueuedAt(raw []byte) ([]byte, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, err
	}
	obj["queuedAt"] = json.RawMessage(strconv.FormatInt(time.Now().UnixMilli(), 10))
	return json.Marshal(obj)
}
