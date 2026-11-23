#!/bin/sh
# Container entrypoint. Seeds the config files Neko expects, then exec's
# the real CMD. Existing files win by default so first-run setup changes
# (DB password rotation, encryption key) survive container restarts.
#
# Required env to bootstrap from a fresh container:
#   NEKO_PG_HOST          Postgres host (or socket path starting with /)
#   NEKO_PG_PASSWORD      Postgres user password
#   NEKO_SECRET_KEY       base64 32-byte at-rest encryption key
#
# Optional env (sane defaults baked in):
#   NEKO_PG_PORT          default 5432
#   NEKO_PG_USER          default neko
#   NEKO_PG_DATABASE      default neko
#   NEKO_PG_SSLMODE       default unset (no TLS); set to "require" for
#                         managed Postgres with self-signed CAs
#                         (e.g. Cloud SQL public IP, RDS, etc.)
#
# When no env vars are set, the app falls back to its local dev
# defaults (host=localhost, password=secret, database=neko). The /setup
# wizard handles password rotation in that mode.
set -eu

CONFIG_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/openneko"
mkdir -p \
  "$CONFIG_DIR" \
  "${XDG_CONFIG_HOME:-$HOME/.config}/graphjin" \
  "${XDG_CACHE_HOME:-$HOME/.cache}" \
  "${XDG_DATA_HOME:-$HOME/.local/share}" \
  "${XDG_STATE_HOME:-$HOME/.local/state}" \
  "${TMPDIR:-/tmp}"

openneko container-init --config-dir "$CONFIG_DIR"

# Migrations: `openneko start` runs the `neko-migrate` start step before it
# starts web. Bare-Docker users run `openneko migrate` first.

exec "$@"
