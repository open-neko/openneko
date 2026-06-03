# OpenShell sandbox base for the AGENT runtime (Phase 2c) — the agent turn
# (runChatTurn + the claude/hermes subprocess) runs here, reaching the control
# plane only through the broker.
#
# This file is the OS base layer (the OpenShell-required shape: glibc +
# iproute2/nftables + a `sandbox` user @ /sandbox, same as plugin-base). The
# RUNNABLE agent image layers the worker runtime on top — node + the compiled
# @neko/llm bundle + the claude/hermes/graphjin binaries + agent-sandbox/entry.ts
# — which is produced by composing the main Dockerfile's `cli`/`build` stages.
# See docs/OPENSHELL_MIGRATION_PLAN.md (Phase 2c) for the full image + launcher.
#   docker build -f docker/agent-base.Dockerfile -t ghcr.io/open-neko/agent-base:node20 .
FROM node:20-bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates iproute2 nftables \
    && rm -rf /var/lib/apt/lists/*

RUN groupadd -g 1000660000 sandbox \
    && useradd -u 1000660000 -g sandbox -d /sandbox -M sandbox \
    && install -d -o sandbox -g sandbox /sandbox

WORKDIR /sandbox
CMD ["node", "--version"]
