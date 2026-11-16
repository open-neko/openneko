#!/usr/bin/env bash
# Usage: scripts/smoke-script-step.sh [compose project] [web URL]
# Checks that the first organization lists every bundled skill, then runs a
# cron workflow whose script step uses the bundled xlsx skill in the sandbox.
# The step runs before the agent turn, so the check needs no model provider.
set -euo pipefail

project="${1:-openneko-demo}"
url="${2:-http://127.0.0.1:3000}"
db=$(docker ps -q --filter "label=com.docker.compose.project=$project" \
  --filter label=com.docker.compose.service=neko-db | head -1)
[ -n "$db" ] || { echo "no neko-db container in $project" >&2; exit 1; }
web=$(docker ps -q --filter "label=com.docker.compose.project=$project" \
  --filter label=com.docker.compose.service=web | head -1)
[ -n "$web" ] || { echo "no web container in $project" >&2; exit 1; }
pq() { docker exec -i "$db" psql -v ON_ERROR_STOP=1 -U neko -d neko -Atq "$@"; }

bundled=$(docker exec "$web" sh -c 'cd /usr/local/share/openneko/skills && ls -d builtin/*/ sandbox/*/' | cut -d/ -f2 | sort -u)
listed=$(curl -fsS "$url/api/v1/work/skills" | python3 -c 'import json, sys; print("\n".join(sorted(s["name"] for s in json.load(sys.stdin)["skills"])))')
missing=$(comm -23 <(echo "$bundled") <(echo "$listed"))
[ -n "$bundled" ] && [ -z "$missing" ] || { echo "the organization lacks the bundled skills: ${missing:-none bundled}" >&2; exit 1; }
echo "bundled skills listed: $(echo $bundled)"

workflow=a2200000-0000-4000-8000-0000000000fe
org=$(pq -c "select id from organization order by created_at limit 1")
[ -n "$org" ] || { echo "no organization" >&2; exit 1; }

step='printf %s '\''{"sheets":[{"name":"Probe","rows":[["a","b","sum"],[2,3,"=A2+B2"]]}]}'\'' > spec.json && node "$SKILL_DIR/scripts/xlsx.cjs" create spec.json probe.xlsx && node "$SKILL_DIR/scripts/xlsx.cjs" read probe.xlsx --format csv | grep -qx 2,3,5'
steps=$(STEP="$step" python3 -c 'import json, os; print(json.dumps([{"id": "probe", "description": "Spreadsheet probe", "script": {"skill": "xlsx", "command": ["sh", "-c", os.environ["STEP"]]}}]))')

cleanup() { pq -c "delete from workflow_definition where id = '$workflow'" >/dev/null || true; }
trap cleanup EXIT
pq -v org="$org" -v steps="$steps" -v id="$workflow" <<'SQL'
delete from workflow_definition where id = :'id';
insert into workflow_definition (id, org_id, name, description, goal, cron, cron_timezone, cron_enabled, steps)
values (:'id', :'org', 'Script step smoke', 'post-release smoke', 'Report the probe output.', '* * * * *', 'UTC', true, :'steps'::jsonb);
SQL

run=""
for _ in $(seq 1 30); do
  run=$(pq -c "select work_run_id from workflow_run where workflow_id = '$workflow' and status in ('completed', 'failed') limit 1")
  [ -n "$run" ] && break
  sleep 5
done
[ -n "$run" ] || { echo "the script step workflow did not run within 150s" >&2; exit 1; }

events=$(pq -c "select payload->>'message' from work_run_event where run_id = '$run' and payload->>'message' like 'Script step %' order by id")
echo "$events"
grep -qE '^Script step probe ok in ' <<<"$events"
