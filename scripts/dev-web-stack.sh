#!/bin/sh
set -eu

refuse_non_development_mode() {
  echo "Host web + OpenShell registration is development-only." >&2
  echo "Production and demo modes must run the web service inside the stack." >&2
  exit 1
}

case "${NODE_ENV:-development}" in
  development) ;;
  *) refuse_non_development_mode ;;
esac

case "${OPENNEKO_STACK_MODE:-}" in
  demo|DEMO) refuse_non_development_mode ;;
esac

case "${NEXT_PUBLIC_DEMO:-false}" in
  1|true|TRUE|yes|YES|on|ON) refuse_non_development_mode ;;
esac

case "${DEMO:-false}" in
  1|true|TRUE|yes|YES|on|ON) refuse_non_development_mode ;;
esac

web_port="${OPENNEKO_PORT:-3200}"
state_root="${OPENNEKO_HOST_WEB_STATE_DIR:-$PWD/.openneko/host-web}"

if [ -n "${OPENNEKO_DEV_STACK:-}" ]; then
  project_name="$OPENNEKO_DEV_STACK"
else
  running_projects="$(
    docker ps \
      --filter label=com.docker.compose.service=worker \
      --format '{{.Label "com.docker.compose.project"}}' |
      sed '/^$/d' |
      sort -u
  )"
  set -f
  set -- $running_projects
  set +f
  case "$#" in
    1) project_name="$1" ;;
    0)
      echo "No running OpenNeko Compose stack was found." >&2
      echo "Start the development backing stack before running the host web server." >&2
      exit 1
      ;;
    *)
      echo "More than one OpenNeko Compose stack is running:" >&2
      printf '  %s\n' "$@" >&2
      echo "Choose one with OPENNEKO_DEV_STACK=<project> pnpm dev:web:stack." >&2
      exit 1
      ;;
  esac
fi

container_prefix="$project_name"

container_ip() {
  docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$1"
}

published_port() {
  docker port "$1" "$2/tcp" | sed -n '1s/.*://p'
}

worker_container="${container_prefix}-worker-1"
embedding_container="${container_prefix}-embedding-1"
records_graphjin_container="${container_prefix}-records-graphjin-1"
metadata_db_container="${container_prefix}-neko-db-1"
records_db_container="${container_prefix}-records-db-1"
metadata_graphjin_container="${container_prefix}-neko-graphjin-1"

for required_container in \
  "$worker_container" \
  "$embedding_container" \
  "$records_graphjin_container" \
  "$metadata_db_container" \
  "$records_db_container" \
  "$metadata_graphjin_container"
do
  if ! docker inspect "$required_container" >/dev/null 2>&1 || \
     [ "$(docker inspect -f '{{.State.Running}}' "$required_container")" != "true" ]; then
    echo "OpenNeko service is unavailable: $required_container" >&2
    echo "Start the ${project_name} stack before running the host web server." >&2
    exit 1
  fi
done

if [ ! -f "$state_root/config/openneko/secret-key" ] || \
   [ ! -f "$state_root/config/openshell/gateways/openneko/metadata.json" ]; then
  echo "Host web state is missing at $state_root." >&2
  echo "Copy the stack config, OpenShell registration, and agent workspace there before first use." >&2
  exit 1
fi

mkdir -p "$state_root/tmp"

export OPENNEKO_AGENT_HOME="$state_root/home"
export OPENNEKO_HOST_WEB_DEV=1
export NODE_ENV=development
export TMPDIR="$state_root/tmp"
export XDG_CONFIG_HOME="$state_root/config"
export XDG_CACHE_HOME="$state_root/home/.cache"
export XDG_DATA_HOME="$state_root/home/.local/share"
export XDG_STATE_HOME="$state_root/home/.local/state"

export NEKO_PG_HOST=127.0.0.1
export NEKO_PG_PORT="$(published_port "$metadata_db_container" 5432)"

export RECORDS_PG_HOST=127.0.0.1
export RECORDS_PG_PORT="$(published_port "$records_db_container" 5432)"
export OPENNEKO_PG_ENV_OVERRIDE=1

export NEKO_EMBEDDING_URL="http://$(container_ip "$embedding_container"):5003"
export WORKER_ADMIN_URL="http://$(container_ip "$worker_container"):4100"
export OPENNEKO_RECORDS_GRAPHJIN_URL="http://$(container_ip "$records_graphjin_container"):8090"
export OPENNEKO_GRAPHJIN_URL="http://127.0.0.1:$(published_port "$metadata_graphjin_container" 8089)"
export OPENNEKO_GRAPHJIN_CONFIG="$state_root/config/graphjin/agentic.yml"

export OPENSHELL_STATE_DIR="${OPENSHELL_STATE_DIR:-$PWD/.openneko/openshell}"
export OPENSHELL_GATEWAY="${OPENSHELL_GATEWAY:-openneko}"
export OPENNEKO_AGENT_IMAGE="${OPENNEKO_AGENT_IMAGE:-openneko-agent:dev}"
export OPENNEKO_SANDBOX_SHARED_NETWORK=0
export OPENNEKO_BROKER_PORT="${OPENNEKO_WEB_BROKER_PORT:-4198}"

echo "OpenNeko web: http://localhost:${web_port} (hot reload)"
echo "Backing services: Docker Compose project ${project_name}"
exec pnpm --filter @neko/web exec next dev --port "$web_port"
