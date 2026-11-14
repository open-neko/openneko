# Worker

`openneko web` (in `apps/openneko/internal/webfront` and `internal/jobs`) now
runs the River background jobs, workflow execution, channel polling and
delivery, solution pack install/doctor/OAuth/actions, and the durable cron and
subscription loops in Go.

The Node worker (`apps/worker`) remains in the Docker stack for the admin
surfaces that `openneko web` still calls over `--worker-admin-url`:

- **Sign-in provider, Scalekit SSO setup, and Scalekit workspace connectors**
  (`/admin/auth/*`, `/admin/sso/setup/*`, `/admin/connect/*`).
- **Directory sync and provisioning** (`/admin/directory/status`,
  `/admin/directory/sync`, and `/admin/directory/users`).
- **GraphJin group grants** (`/admin/graphjin/group-grants*` and
  `/admin/graphjin/api-operations`).
- **Built-in action adapters** (`/admin/action-requests/create` and
  `/admin/actions/execute` for `channel_admin`, `code_create_issue`,
  `code_draft_patch`, `data_source_admin`, `group_admin`, `send_webhook`,
  `source_config_admin`, and `user_admin`; pack actions and Magento store
  changes run in Go).
- **External-event ingress and health** (`POST /admin/events/external`,
  `POST /admin/reconnect`, `GET /health`, `GET /health/scheduler`, and
  `GET /health/security`).

## Durable workflow scheduler

Cron is durable application state, not an in-process timer.
`workflow_schedule_state` stores the database-authoritative next occurrence
for every active cron workflow. `workflow_schedule_firing` is a transactional
firing ledger and outbox. A materialization transaction inserts the firing and
advances its cursor together; the unique `(workflow_id, scheduled_for)` key
makes concurrent or repeated sweeps idempotent.

`openneko web` runs the cron sweep every 15 seconds in Go
(`apps/openneko/internal/webfront/cronsweep.go`), and the Node worker runs the
same 30-second loop (`apps/worker/src/workflow-scheduler.ts`):

1. Synchronizes active definitions and materializes due firings.
2. Recovers expired dispatch/run leases.
3. Leases pending firings and delivers them to `workflow_run_fire` on River.
   `openneko web` runs the job.

River is the delivery transport, not the schedule clock or source of truth.

### Delivery and recovery

Delivery is at least once. Every cron queue payload carries a
`scheduleFiringId`; the consumer atomically claims that ledger row before it
creates a work thread or workflow run. Duplicate deliveries therefore do no
work.

| Failure point                                      | Recovery                                                                  |
| -------------------------------------------------- | ------------------------------------------------------------------------- |
| Before materialization commits                     | Cursor and firing both roll back.                                         |
| After materialization, before queue delivery       | The pending firing remains in the outbox.                                 |
| After queue send, before marking it enqueued       | The dispatch lease expires and the delivery is safely sent again.          |
| Duplicate queue delivery                           | Only the first ledger claim succeeds.                                     |
| Schedule disabled or edited before claim           | The unclaimed firing is cancelled; a queued duplicate cannot claim it.    |
| A stop of `openneko web` during an agent run      | The run lease returns to pending for retry.                               |
| Ledger completion update lost after a terminal run | Recovery observes the terminal run and completes the firing.              |

External action adapters still need their own idempotency keys: no scheduler
can make an arbitrary remote side effect exactly once. The firing ledger
guarantees one active OpenNeko consumer per occurrence and retains the
occurrence until it is consumed.

### Catch-up

New, edited, and re-enabled schedules start at the first occurrence strictly
after the definition update, so they never fabricate a pre-enable run.

The default `coalesce` policy emits only the latest missed occurrence after
downtime. `replay` emits a bounded prefix (24 by default) and leaves the cursor
overdue for later ticks until the backlog drains. Daily run budgets include
both existing workflow runs and materialized firings.

### Health and deployment

The loop persists progress in `workflow_scheduler_health` and exposes a
process-local progress signal:

- `GET /health` returns 503 while the scheduler is starting, failing, or has
  not succeeded for 120 seconds.
- `GET /health/scheduler` returns timestamps, the last error, consecutive
  failures, and materialized/dispatched/recovered counts.

The released-image VM deployment polls `/health/scheduler` inside the new
worker and fails unless that boot completes a successful scheduler database
transaction. This detects a live process whose schedule loop is not making
progress.

Diagnostic queries:

```sql
select * from workflow_schedule_state where workflow_id = $1;
select * from workflow_schedule_firing
where workflow_id = $1 order by scheduled_for desc limit 20;
select * from workflow_scheduler_health where id = 'cron';
```
