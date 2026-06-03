# Shared OpenShell sandbox base for all @open-neko plugins. Each plugin's
# self-contained dist/run.js is uploaded to /sandbox/run.js at start, not baked.
#   docker build -f docker/plugin-base.Dockerfile -t ghcr.io/open-neko/plugin-base:node20 .
FROM node:20-bookworm-slim

# iproute2 + nftables: required by the supervisor's egress setup (without them
# the sandbox hangs at "waiting for supervisor relay").
RUN apt-get update \
    && apt-get install -y --no-install-recommends iproute2 nftables \
    && rm -rf /var/lib/apt/lists/*

# Home MUST be /sandbox so `sandbox upload` (writes to ~) succeeds.
RUN groupadd -g 1000660000 sandbox \
    && useradd -u 1000660000 -g sandbox -d /sandbox -M sandbox \
    && install -d -o sandbox -g sandbox /sandbox

WORKDIR /sandbox
CMD ["node", "--version"]
