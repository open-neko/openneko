#!/bin/sh
set -eu

# Apply schema migrations against the metadata DB. Org and data-source rows
# are NOT seeded here — they're created by the app on first boot
# (`getOrgId()` auto-bootstraps the org) and via the /setup wizard.
#
# Credentials are hardcoded to match what the postgres container ships
# with (see compose.yml). The /setup wizard later changes the password
# and persists the new value to ~/.config/neko/config.json on the host running
# the app. The container's own role can keep using the bootstrap password
# inside Docker — only the app needs to know the changed value.

export PGPASSWORD="secret"

PGHOST="neko-db"
PGPORT="5432"
PGUSER="neko"
PGDATABASE="neko"
MIGRATION_FILE="/workspace/db/migrations/0001_init.sql"

echo "[neko-db-init] waiting for postgres at ${PGHOST}:${PGPORT}"
until pg_isready -h "${PGHOST}" -p "${PGPORT}" -U "${PGUSER}" -d "${PGDATABASE}" >/dev/null 2>&1; do
  sleep 1
done

schema_exists="$(
  psql \
    -h "${PGHOST}" \
    -p "${PGPORT}" \
    -U "${PGUSER}" \
    -d "${PGDATABASE}" \
    -tAc "select to_regclass('public.organization') is not null"
)"

if [ "${schema_exists}" != "t" ]; then
  echo "[neko-db-init] metadata schema missing; running consolidated migration"
  if [ ! -f "${MIGRATION_FILE}" ]; then
    echo "[neko-db-init] missing required migration ${MIGRATION_FILE}" >&2
    exit 1
  fi

  echo "[neko-db-init] applying ${MIGRATION_FILE}"
  psql \
    -v ON_ERROR_STOP=1 \
    -h "${PGHOST}" \
    -p "${PGPORT}" \
    -U "${PGUSER}" \
    -d "${PGDATABASE}" \
    -f "${MIGRATION_FILE}"
else
  echo "[neko-db-init] metadata schema already present; skipping baseline"
fi

# Apply incremental migrations after the baseline. Each file must be idempotent
# (CREATE/ADD ... IF NOT EXISTS or guarded with DO blocks) since we re-run on
# every container start. Order is lexicographic — keep the NNNN_ prefix.
for migration in /workspace/db/migrations/*.sql; do
  case "${migration}" in
    "${MIGRATION_FILE}") continue ;;
  esac
  echo "[neko-db-init] applying $(basename "${migration}")"
  psql \
    -v ON_ERROR_STOP=1 \
    -h "${PGHOST}" \
    -p "${PGPORT}" \
    -U "${PGUSER}" \
    -d "${PGDATABASE}" \
    -f "${migration}"
done

echo "[neko-db-init] done — visit /setup in the web app to change the DB password and finish configuration"
