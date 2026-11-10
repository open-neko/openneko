#!/usr/bin/env bash
# Fast static guard for the release recovery state machine. GitHub owns the
# workflow runtime, so this check ensures the source keeps the transitions that
# a failed-then-recovered stable release needs before Deploy-to-VM runs.
set -euo pipefail

release_workflow=".github/workflows/release-binaries.yml"
smoke_workflow=".github/workflows/post-release-smoke.yml"
budgets="scripts/image-size-budgets.json"

jq -e '
  .agent == 800
  and .uniqueStack == 2500
  and (.agent | type) == "number"
  and (.uniqueStack | type) == "number"
' "$budgets" >/dev/null

grep -Fq "Manual recovery runs may contain CI hardening newer than the tag" "$release_workflow"
grep -Fq "github.event_name == 'workflow_dispatch' && github.ref" "$release_workflow"
grep -Fq "Promote recovered stable release to \"Latest\" after smoke" "$smoke_workflow"
grep -Fq "success() && steps.ctx.outputs.stable == 'true' && steps.ctx.outputs.tag != ''" "$smoke_workflow"
grep -Fq -- "--prerelease=false --latest" "$smoke_workflow"
grep -Fq "failure() && steps.ctx.outputs.stable == 'true' && steps.ctx.outputs.tag != ''" "$smoke_workflow"
grep -Fq -- "--prerelease --latest=false" "$smoke_workflow"
grep -Fq 'for image in neko-db records-db neko-backup; do' "$smoke_workflow"
grep -Fq 'org.openneko.storage-owner' "$smoke_workflow"
grep -Fq 'test "$image_owner" = "999:999"' "$smoke_workflow"

# Release builds must use the immutable versioned GraphJin asset URL. The API
# asset-ID endpoint is rate limited for unauthenticated Docker build steps and
# can return 403 even while the release asset itself remains healthy.
if grep -Fq "api.github.com/repos/dosco/graphjin/releases/assets" Dockerfile; then
  echo "GraphJin build still depends on the rate-limited GitHub API asset endpoint" >&2
  exit 1
fi
grep -Fq 'releases/download/v${GRAPHJIN_VERSION}/graphjin_${GRAPHJIN_VERSION}_linux_${TARGETARCH}.tar.gz' Dockerfile

echo "release recovery contract is intact"
