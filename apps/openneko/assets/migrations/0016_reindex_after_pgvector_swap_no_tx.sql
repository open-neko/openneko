-- Reindex everything after the postgres:16-alpine -> pgvector/pgvector:pg16
-- image swap. Different libc (musl vs glibc) means a different ICU/locale
-- collation sort order; existing btree indexes on text columns become
-- invalid silently — equality lookups can miss rows that exist.
--
-- We hit this concretely on pgboss.queue: the row for `__pgboss__send-it`
-- was visible to LIKE scans but invisible to `WHERE name = ?`, which made
-- pg-boss's FK check fail with "Key (name) is not present in table queue"
-- on every cron tick.
--
-- REINDEX is fully online for the small tables we have (pg-boss queue
-- catalog, etc.) and idempotent — re-running after a fresh install does
-- nothing harmful.

REINDEX DATABASE neko;
