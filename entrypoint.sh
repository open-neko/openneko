#!/bin/sh
# Container entrypoint. Reads env vars and materializes the host config
# files Neko expects, then exec's the real CMD.
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
# When no env vars are set, the app falls back to its compose-stack
# defaults (host=localhost, password=secret, database=neko). The /setup
# wizard handles password rotation in that mode.
set -eu

CONFIG_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/neko"
mkdir -p "$CONFIG_DIR" "$HOME/.hermes"

if [ -n "${NEKO_PG_HOST:-}" ]; then
  node -e "
    const cfg = {
      pg: {
        host: process.env.NEKO_PG_HOST,
        port: Number(process.env.NEKO_PG_PORT || 5432),
        user: process.env.NEKO_PG_USER || 'neko',
        password: process.env.NEKO_PG_PASSWORD || '',
        database: process.env.NEKO_PG_DATABASE || 'neko',
      },
    };
    if (process.env.NEKO_PG_SSLMODE) cfg.pg.sslmode = process.env.NEKO_PG_SSLMODE;
    require('fs').writeFileSync('$CONFIG_DIR/config.json', JSON.stringify(cfg, null, 2));
    require('fs').chmodSync('$CONFIG_DIR/config.json', 0o600);
  "
fi

if [ -n "${NEKO_SECRET_KEY:-}" ]; then
  printf '%s' "$NEKO_SECRET_KEY" > "$CONFIG_DIR/secret-key"
  chmod 600 "$CONFIG_DIR/secret-key"
fi

exec "$@"
