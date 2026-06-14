# syntax=docker/dockerfile:1.7
#
# Single Dockerfile, two targets (web + worker), shared base layers.
# Build with `--target=web` or `--target=worker`.
#
# Runtime config strategy: the app reads ~/.config/openneko/config.json
# (DB connection) and ~/.config/openneko/secret-key (at-rest encryption
# key) on every boot. To support read-only container filesystems (e.g.
# Cloud Run), HOME + XDG_CONFIG_HOME point at writable /tmp paths and
# entrypoint.sh materializes those files from env vars before exec'ing
# the app. See entrypoint.sh for the env var contract.

# ─── 1. base: node + system tooling ────────────────────────────────────
FROM node:22-bookworm-slim AS base
ENV PNPM_HOME=/pnpm
ENV PATH=$PNPM_HOME:$PATH
# Retry transient apt mirror hiccups instead of hard-failing the image build.
RUN printf 'Acquire::Retries "5";\nAcquire::http::Timeout "30";\n' > /etc/apt/apt.conf.d/80-retries
RUN corepack enable && corepack prepare pnpm@9.14.1 --activate
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates curl tini \
    && rm -rf /var/lib/apt/lists/*

# ─── 2. runtime-base: lean base for web/worker ─────────────────────────
# web/worker share this; the heavy agent toolchain (graphjin/hermes/claude/
# libreoffice) lives one layer up in `cli` so only the agent image carries it.
FROM base AS runtime-base
ARG OPENSHELL_VERSION=0.0.54
# TARGETARCH is auto-supplied by buildx (amd64 or arm64).
ARG TARGETARCH
# unzip + postgresql-client are needed by db/load-adventureworks-baked.sh
# (demo seeder, runs inside the worker container with no apt-get available
# at runtime since the container runs as the `neko` user, not root).
# git: git-URL skill installs shallow-clone. openssh-client: `openshell sandbox
# exec` relays over ssh (unix:/run/openshell/ssh.sock), so the worker — which
# shells out to it for the agent sandbox — needs an ssh client.
RUN apt-get update && apt-get install -y --no-install-recommends \
      git unzip postgresql-client openssh-client \
    && rm -rf /var/lib/apt/lists/*
# openshell: CLI driver the worker uses to spawn + relay to agent sandboxes.
# Static musl binary, no runtime deps.
RUN OS_ARCH="$(case "${TARGETARCH}" in amd64) echo x86_64 ;; arm64) echo aarch64 ;; *) echo "${TARGETARCH}" ;; esac)" \
    && curl -fsSL -o /tmp/openshell.tgz \
      "https://github.com/NVIDIA/OpenShell/releases/download/v${OPENSHELL_VERSION}/openshell-${OS_ARCH}-unknown-linux-musl.tar.gz" \
    && tar -xzf /tmp/openshell.tgz -C /usr/local/bin openshell \
    && rm /tmp/openshell.tgz \
    && openshell --version

# ─── 2b. cli: agent toolchain (only the `agent` image builds from this) ──
# graphjin: metric agent uses `graphjin cli`. hermes: default Hermes backend.
# claude: claude-agent backend (Anthropic SDK spawns it).
FROM runtime-base AS cli
ARG GRAPHJIN_VERSION=3.18.37
ARG HERMES_AGENT_REF=a91a57fa5a13d516c38b07a141a9ce8a3daabeb0
ARG TARGETARCH
RUN curl -fsSL --retry 5 --retry-delay 5 --retry-all-errors -o /tmp/graphjin.tgz \
      "https://github.com/dosco/graphjin/releases/download/v${GRAPHJIN_VERSION}/graphjin_${GRAPHJIN_VERSION}_linux_${TARGETARCH}.tar.gz" \
    && tar -xzf /tmp/graphjin.tgz -C /usr/local/bin graphjin \
    && rm /tmp/graphjin.tgz \
    && graphjin version
RUN curl -LsSf --retry 5 --retry-delay 5 --retry-all-errors https://astral.sh/uv/install.sh \
      | env UV_INSTALL_DIR=/usr/local/bin sh -s -- --no-modify-path \
    && UV_TOOL_DIR=/usr/local/uv/tools \
       UV_TOOL_BIN_DIR=/usr/local/bin \
       UV_PYTHON_INSTALL_DIR=/usr/local/uv/python \
       UV_CACHE_DIR=/tmp/uv-cache \
       uv tool install --python 3.11 \
         --with mcp --with websockets \
         "hermes-agent[acp] @ git+https://github.com/NousResearch/hermes-agent.git@${HERMES_AGENT_REF}" \
    && rm -rf /tmp/uv-cache /root/.cache/uv \
    && hermes --version \
    && /usr/local/uv/tools/hermes-agent/bin/python -c "import mcp, websockets" \
    && echo "hermes MCP SDK present"
RUN npm install -g @anthropic-ai/claude-code

# Bundled skills (xlsx / pptx / docx / pdf / skill-creator) shell out to
# Python + LibreOffice + Poppler / qpdf. Mirror packages/llm/src/work/skill-deps.ts
# so a fresh image has what they need at runtime.
RUN apt-get update && apt-get install -y --no-install-recommends \
      python3 python3-pip libreoffice poppler-utils qpdf \
    && rm -rf /var/lib/apt/lists/* \
    && pip3 install --no-cache-dir --break-system-packages \
       openpyxl python-pptx Pillow python-docx pypdf pdfplumber reportlab PyYAML

# ─── 3. deps: workspace install (cached on lockfile) ───────────────────
FROM base AS deps
WORKDIR /app
COPY pnpm-workspace.yaml pnpm-lock.yaml package.json ./
COPY apps/web/package.json apps/web/package.json
COPY apps/worker/package.json apps/worker/package.json
COPY packages/channels/package.json packages/channels/package.json
COPY packages/db/package.json packages/db/package.json
COPY packages/interaction/package.json packages/interaction/package.json
COPY packages/llm/package.json packages/llm/package.json
COPY packages/plugin-install/package.json packages/plugin-install/package.json
COPY packages/plugin-types/package.json packages/plugin-types/package.json
RUN --mount=type=cache,id=pnpm,target=/pnpm/store \
    pnpm install --frozen-lockfile

# ─── 4. build: next build (standalone output) ──────────────────────────
FROM deps AS build
WORKDIR /app
COPY . .
ENV NEXT_TELEMETRY_DISABLED=1
RUN pnpm --filter @neko/web build

# ─── 4b. openneko go binary ────────────────────────────────────────────
# Built from apps/openneko (Go 1.24 module) and baked into the worker
# image so the agent's Bash tool can run `openneko install/secrets/...`
# from inside the worker container. The same binary operators install via
# Homebrew / GitHub Releases on their host.
FROM golang:1.25-bookworm AS go-build
WORKDIR /src
COPY apps/openneko/go.mod apps/openneko/go.sum apps/openneko/
RUN cd apps/openneko && go mod download
COPY apps/openneko apps/openneko
COPY db/migrations db/migrations
# Ensure the embedded migration copies match the canonical source. CI also
# runs this check separately; build-time guard prevents drift sneaking in
# via an image-only rebuild.
RUN cd apps/openneko && ./scripts/sync-migrations.sh --check
RUN cd apps/openneko && \
    CGO_ENABLED=0 GOOS=linux \
    go build -trimpath -ldflags "-s -w -X github.com/open-neko/neko/apps/openneko/internal/version.Version=container" \
      -o /out/openneko ./cmd/openneko

# ─── 4b. embedding-model prewarm ───────────────────────────────────────
# Download Xenova/all-MiniLM-L6-v2 (q8 quantized, ~22MB) into a stable
# cache that both web and worker stages copy into their final images.
# Without this, the first save: command in the running container blocks
# on a HuggingFace download (and would fail in air-gapped deployments).
FROM deps AS embedding-prewarm
WORKDIR /app
# The script imports @huggingface/transformers, which pnpm installs under
# /app/packages/llm/node_modules/ (isolated workspace deps, not hoisted
# to /app/node_modules). Running from the package directory lets Node's
# resolver find it. Same path packages/llm's `models:warm` script uses
# in dev, so behavior matches.
COPY packages/llm/scripts/prewarm-embedding.mjs /app/packages/llm/scripts/prewarm-embedding.mjs
ENV NODE_ENV=production
RUN mkdir -p /app/.transformers-cache && \
    cd /app/packages/llm && node scripts/prewarm-embedding.mjs

# ─── 5a. web runtime ───────────────────────────────────────────────────
FROM base AS web
WORKDIR /app
# Writable HOME under /tmp so the entrypoint can materialize config on
# read-only container filesystems. PORT=8080 matches the common PaaS
# convention (Cloud Run, Heroku, Fly, Railway).
ENV NODE_ENV=production \
    PORT=8080 \
    HOSTNAME=0.0.0.0 \
    HOME=/tmp/openneko-home \
    XDG_CONFIG_HOME=/tmp/openneko-config \
    NEXT_TELEMETRY_DISABLED=1
RUN useradd --system --create-home --uid 1001 neko
RUN mkdir -p /config/openneko /config/graphjin /tmp/openneko-home /tmp/openneko-tmp \
    && chown -R neko:neko /config /tmp/openneko-home /tmp/openneko-tmp
# Standalone output is self-contained (server.js + traced node_modules).
# Static + public are served by server.js but not auto-copied — we copy
# them in alongside, matching the layout server.js expects.
COPY --from=build --chown=neko:neko /app/apps/web/.next/standalone ./
COPY --from=build --chown=neko:neko /app/apps/web/.next/static ./apps/web/.next/static
COPY --from=build --chown=neko:neko /app/apps/web/public ./apps/web/public
# Next.js standalone tracing misses static asset dirs — copy explicitly.
COPY --from=build --chown=neko:neko /app/packages/llm/assets ./packages/llm/assets
# Next.js standalone tracing also misses the onnxruntime-node native .so
# libraries (they're loaded by @huggingface/transformers at runtime via
# dlopen, not via require()). Without these copies, /settings and every
# other route that touches the embedding model 500s with
# "libonnxruntime.so.1: cannot open shared object file".
COPY --from=build --chown=neko:neko /app/node_modules/.pnpm/onnxruntime-node@1.24.3/node_modules/onnxruntime-node ./node_modules/.pnpm/onnxruntime-node@1.24.3/node_modules/onnxruntime-node
COPY --from=build --chown=neko:neko /app/node_modules/.pnpm/onnxruntime-common@1.24.3/node_modules/onnxruntime-common ./node_modules/.pnpm/onnxruntime-common@1.24.3/node_modules/onnxruntime-common
COPY --chown=neko:neko entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh
# Vendor the openneko Go binary so the entrypoint can run `openneko migrate`
# at boot (replaces the legacy neko-db-init container). Same binary as the
# worker image and the host install.
COPY --from=go-build --chown=neko:neko /out/openneko /usr/local/bin/openneko
RUN chmod +x /usr/local/bin/openneko
# Vendored embedding model (see embedding-prewarm stage above). Ships the
# ~22MB model files inside the image so save:/auto-context never blocks
# on a network download at runtime.
COPY --from=embedding-prewarm --chown=neko:neko /app/.transformers-cache /app/.transformers-cache
USER neko
EXPOSE 8080
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/entrypoint.sh"]
CMD ["node", "apps/web/server.js"]

# ─── 5b. worker runtime ────────────────────────────────────────────────
# Trimmed prod closure of @neko/worker: drops devDeps + other apps' sources +
# web/Next, keeps src + tsx + @neko/llm (with assets) + onnxruntime. Same
# mechanism as agent-deploy; rooted at /app, so the entry is /app/src/index.ts.
FROM build AS worker-deploy
RUN pnpm --filter @neko/worker deploy --prod /out/worker-app

# The worker runs from source via tsx (not a build step). It serves /health +
# admin endpoints on port 4100 for liveness probes.
FROM runtime-base AS worker
WORKDIR /app
ENV NODE_ENV=production \
    PORT=4100 \
    HOSTNAME=0.0.0.0 \
    HOME=/tmp/openneko-home \
    XDG_CONFIG_HOME=/tmp/openneko-config
RUN useradd --system --create-home --uid 1001 neko
# /cache is mounted by the demo-mode adventureworks-init step; pre-creating
# it here lets the named volume initialize with neko ownership instead of root
# (Docker copies image-side ownership into a fresh named volume on first mount).
# /app must be writable by neko so `openneko install` (run in-container via
# docker exec) can write the plugin manifest during installs.
RUN mkdir -p /config/openneko /config/graphjin /tmp/openneko-home /tmp/openneko-tmp /cache /var/lib/openneko/plugins \
    && chown -R neko:neko /app /config /tmp/openneko-home /tmp/openneko-tmp /cache /var/lib/openneko
# Seed the plugin install dir with an empty package.json so `npm install`
# inside that dir has a workspace to operate on. Isolated from /app's
# node_modules (OPENNEKO_PLUGIN_INSTALL_DIR points here), so plugin packages
# land cleanly regardless of the worker's own (pruned) deps.
RUN printf '{\n  "name": "openneko-plugins",\n  "version": "0.0.0",\n  "private": true\n}\n' > /var/lib/openneko/plugins/package.json \
    && chown neko:neko /var/lib/openneko/plugins/package.json
# Prod closure (src + node_modules) rooted at /app, replacing the full pnpm
# workspace install + per-package source copies.
COPY --from=worker-deploy --chown=neko:neko /out/worker-app ./
# Whole db/ (not just migrations): seeds + load-adventureworks-baked.sh
# are needed for `openneko start --mode demo`'s adventureworks-init step.
COPY --chown=neko:neko db ./db
COPY --chown=neko:neko entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh
# Vendor the openneko Go binary so the agent's Bash tool inside the worker
# container can run `openneko install/secrets/marketplace …` without an
# extra install step. Same binary operators install on their host.
COPY --from=go-build --chown=neko:neko /out/openneko /usr/local/bin/openneko
RUN chmod +x /usr/local/bin/openneko
# Vendored embedding model (see embedding-prewarm stage above). Ships the
# ~22MB model files inside the image so worker auto-memory and metric-agent
# context retrieval never block on a network fetch.
COPY --from=embedding-prewarm --chown=neko:neko /app/.transformers-cache /app/.transformers-cache
USER neko
EXPOSE 4100
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/entrypoint.sh"]
CMD ["node", "--import", "tsx/esm", "src/index.ts"]

# ─── 5d. agent sandbox runtime (OpenShell) ─────────────────────────────
# The agent loop running as a child inside an OpenShell sandbox (Phase 3,
# OPENNEKO_AGENT_RUNTIME=openshell), reaching the control plane only through the
# broker. It is deliberately NOT `FROM worker`: that would pin the worker's full
# ~1.3GB node_modules (incl. the web/Next.js deps the agent never runs) in an
# immutable base layer the trim couldn't shrink. Instead we `pnpm deploy` the
# worker's trimmed PROD closure and lay it onto the lean `cli` base — which
# already provides node + hermes (/usr/local/uv) + claude + graphjin +
# libreoffice. Net /app: ~774MB vs ~2.1GB. hermes is under /usr/local (Landlock
# allows /usr, blocks /opt).

# Trimmed prod closure of @neko/worker: drops web/Next.js + devDeps + other
# apps' sources; keeps tsx, @neko/llm (with its assets), and the claude SDK.
FROM build AS agent-deploy
RUN pnpm --filter @neko/worker deploy --prod /out/agent-app
# Each MCP bridge is its own process (12 per agent run); under tsx one
# bridge costs ~300MB RSS (~3.5GiB per sandbox — real memory pressure on
# small hosts). The same bridge as a plain-JS bundle runs at ~80MB.
# entry.ts prefers the bundle when present. transformers/onnx stay
# external: the bridge never embeds, so they must not load eagerly.
RUN cd apps/worker && pnpm exec esbuild src/agent-sandbox/mcp-bridge.ts \
      --bundle --platform=node --format=esm \
      --external:onnxruntime-node --external:@huggingface/transformers \
      --banner:js="import { createRequire } from 'node:module'; const require = createRequire(import.meta.url);" \
      --outfile=/out/agent-app/dist/agent-sandbox/mcp-bridge.js

FROM cli AS agent
USER root
# Supervisor egress-netns tools + a non-root `sandbox` user (high UID, OpenShell
# convention). node/hermes/claude/graphjin/libreoffice already come from `cli`.
RUN apt-get update && apt-get install -y --no-install-recommends iproute2 nftables \
    && rm -rf /var/lib/apt/lists/*
RUN groupadd -g 1000660000 sandbox \
    && useradd -u 1000660000 -g sandbox -d /sandbox -M sandbox \
    && install -d -o sandbox -g sandbox /sandbox
# The deploy roots the worker package at /app (entry.ts -> /app/src/...,
# @neko/llm -> /app/node_modules), owned by the sandbox user so it can run it.
COPY --from=agent-deploy --chown=1000660000:1000660000 /out/agent-app /app
WORKDIR /sandbox
# Supervisor-replaced; launcher runs:
#   cd /app && node --import tsx/esm /app/src/agent-sandbox/entry.ts
CMD ["node", "--version"]

# ─── 5c. neko-cli runtime ──────────────────────────────────────────────
# Minimal image containing just the openneko Go binary. Used as the
# `neko-migrate` one-shot container in compose: starts, runs
# `openneko migrate`, exits. web / worker / neko-graphjin all depend on
# its successful completion via service_completed_successfully, so by
# the time they boot the schema is in place.
#
# Static Go binary (CGO_ENABLED=0), so debian-slim is enough — no glibc
# version pinning needed. tini gives clean Ctrl-C / SIGTERM behavior;
# ca-certs keeps TLS-to-managed-Postgres working if anyone points this
# at a remote DB.
FROM debian:bookworm-slim AS neko-cli
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates tini \
    && rm -rf /var/lib/apt/lists/*
COPY --from=go-build /out/openneko /usr/local/bin/openneko
RUN chmod +x /usr/local/bin/openneko
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/openneko"]
CMD ["--help"]

# ─── 6. neko-graphjin runtime ──────────────────────────────────────────
# OpenNeko's own GraphJin instance — exposes the metadata Postgres
# (workflow_definition, workflow_run, workflow_output, observation,
# subscription, action_*) so the worker can subscribe to output-match
# firings and dogfood query features. Distinct from the customer-data
# graphjin in compose.adventureworks.yml.
#
# The entrypoint re-templates db/graphjin/neko.yml from the openneko
# config.json on every start, so password rotation via /setup just
# requires `docker compose restart neko-graphjin`. Built on the slim
# node base so we have node + curl available for the templating script
# and a real healthcheck.
FROM node:22-bookworm-slim AS neko-graphjin
ARG GRAPHJIN_VERSION=3.18.37
ARG TARGETARCH
ENV NODE_ENV=production
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates curl tini \
    && curl -fsSL -o /tmp/graphjin.tgz \
      "https://github.com/dosco/graphjin/releases/download/v${GRAPHJIN_VERSION}/graphjin_${GRAPHJIN_VERSION}_linux_${TARGETARCH}.tar.gz" \
    && tar -xzf /tmp/graphjin.tgz -C /usr/local/bin graphjin \
    && rm /tmp/graphjin.tgz \
    && rm -rf /var/lib/apt/lists/* \
    && graphjin version
COPY scripts/neko-graphjin-entrypoint.sh /usr/local/bin/neko-graphjin-entrypoint.sh
RUN chmod +x /usr/local/bin/neko-graphjin-entrypoint.sh
COPY db/graphjin/neko.yml /seed/neko.yml
EXPOSE 8089
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/neko-graphjin-entrypoint.sh"]
CMD ["serve", "--path", "/config"]
