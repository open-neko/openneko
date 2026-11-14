# Environment for host web and worker processes against `pnpm dev:up`.
# Source from the repository root: `. scripts/dev-env.sh`.
#
# OPENNEKO_DEV_CONFIG_HOME: config home for OpenNeko state
#   (default: your XDG config home). Set it per worktree to keep stacks apart.
# OPENNEKO_DEV_STATE: GraphJin config directories shared with Docker
#   (default: .openneko/dev in this checkout).

if [ -n "${OPENNEKO_DEV_CONFIG_HOME:-}" ]; then
  export XDG_CONFIG_HOME="$OPENNEKO_DEV_CONFIG_HOME"
fi
export OPENNEKO_DEV_STATE="${OPENNEKO_DEV_STATE:-$PWD/.openneko/dev}"

# Every shell must reach the stack dev-up.sh started. Without one project
# name, a later compose call builds a second stack and the network clashes.
if [ -z "${COMPOSE_PROJECT_NAME:-}" ] && [ -s "$OPENNEKO_DEV_STATE/compose-project" ]; then
  COMPOSE_PROJECT_NAME="$(cat "$OPENNEKO_DEV_STATE/compose-project")"
fi
# Compose accepts only lowercase letters, digits, "-" and "_".
COMPOSE_PROJECT_NAME="$(printf '%s' "${COMPOSE_PROJECT_NAME:-$(basename "$PWD")}" | tr '[:upper:]' '[:lower:]' | tr -c 'a-z0-9_-' '-')"
export COMPOSE_PROJECT_NAME
export OPENNEKO_CONFIG_VOLUME="${OPENNEKO_CONFIG_VOLUME:-${XDG_CONFIG_HOME:-$HOME/.config}/openneko}"
# `pnpm dev:up` installs the pinned GraphJin CLI here.
export PATH="$OPENNEKO_DEV_STATE/bin:$PATH"

# Docker hostnames in config.json do not resolve on the host; the password
# still comes from config.json.
export OPENNEKO_PG_ENV_OVERRIDE=1
export NEKO_PG_HOST=127.0.0.1
export NEKO_PG_PORT="${OPENNEKO_DB_PORT:-5432}"

export OPENNEKO_GRAPHJIN_URL="http://127.0.0.1:${OPENNEKO_GRAPHJIN_PORT:-8089}"
export OPENNEKO_GRAPHJIN_CONFIG="$OPENNEKO_DEV_STATE/graphjin/agentic.yml"

# scripts/dev-agent.sh runs `openneko serve` on the host. Agent runners run
# from source and stage runs where serve reads them.
export OPENNEKO_SERVE_URL="${OPENNEKO_SERVE_URL:-http://127.0.0.1:${OPENNEKO_SERVE_PORT:-8090}}"
export OPENNEKO_SERVE_SOCKET="${OPENNEKO_SERVE_SOCKET:-$OPENNEKO_DEV_STATE/run/serve.sock}"
export OPENNEKO_AGENT_RUNS_DIR="${OPENNEKO_AGENT_RUNS_DIR:-$OPENNEKO_DEV_STATE/agent-runs}"

export OPENNEKO_PUBLIC_URL="${OPENNEKO_PUBLIC_URL:-http://localhost:${OPENNEKO_PORT:-3000}}"
export WORKER_ADMIN_URL="${WORKER_ADMIN_URL:-http://127.0.0.1:4100}"
