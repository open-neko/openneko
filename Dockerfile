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
RUN corepack enable && corepack prepare pnpm@9.14.1 --activate
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates curl tini \
    && rm -rf /var/lib/apt/lists/*

# ─── 2. cli: install runtime CLIs the agent shells out to ──────────────
# graphjin: required by both backends (metric agent uses `graphjin cli`).
# hermes:   required by the default Hermes backend.
# claude:   required by the claude-agent backend (Anthropic SDK spawns it).
FROM base AS cli
ARG GRAPHJIN_VERSION=3.18.18
ARG HERMES_AGENT_REF=a91a57fa5a13d516c38b07a141a9ce8a3daabeb0
# TARGETARCH is auto-supplied by buildx (amd64 or arm64) and lets the
# graphjin download pick the right tarball when building multi-arch.
ARG TARGETARCH
# unzip + postgresql-client are needed by db/load-adventureworks-baked.sh
# (demo seeder, runs inside the worker container with no apt-get available
# at runtime since the container runs as the `neko` user, not root).
RUN apt-get update && apt-get install -y --no-install-recommends \
      git unzip postgresql-client \
    && rm -rf /var/lib/apt/lists/*
RUN curl -fsSL -o /tmp/graphjin.tgz \
      "https://github.com/dosco/graphjin/releases/download/v${GRAPHJIN_VERSION}/graphjin_${GRAPHJIN_VERSION}_linux_${TARGETARCH}.tar.gz" \
    && tar -xzf /tmp/graphjin.tgz -C /usr/local/bin graphjin \
    && rm /tmp/graphjin.tgz \
    && graphjin version
RUN curl -LsSf https://astral.sh/uv/install.sh \
      | env UV_INSTALL_DIR=/usr/local/bin sh -s -- --no-modify-path \
    && UV_TOOL_DIR=/opt/uv-tools \
       UV_TOOL_BIN_DIR=/usr/local/bin \
       UV_PYTHON_INSTALL_DIR=/opt/uv-python \
       UV_CACHE_DIR=/tmp/uv-cache \
       uv tool install --python 3.11 \
         "hermes-agent[acp] @ git+https://github.com/NousResearch/hermes-agent.git@${HERMES_AGENT_REF}" \
    && rm -rf /tmp/uv-cache /root/.cache/uv \
    && hermes --version
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
COPY packages/db/package.json packages/db/package.json
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
FROM cli AS web
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
# Vendored embedding model (see embedding-prewarm stage above). Ships the
# ~22MB model files inside the image so save:/auto-context never blocks
# on a network download at runtime.
COPY --from=embedding-prewarm --chown=neko:neko /app/.transformers-cache /app/.transformers-cache
USER neko
EXPOSE 8080
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/entrypoint.sh"]
CMD ["node", "apps/web/server.js"]

# ─── 5b. worker runtime ────────────────────────────────────────────────
# The worker uses tsx (not a build step) so it ships full source +
# node_modules. It serves /health + admin endpoints on port 4100 for
# liveness probes (Cloud Run service startup probe, k8s liveness, etc).
FROM cli AS worker
WORKDIR /app
ENV NODE_ENV=production \
    PORT=4100 \
    HOSTNAME=0.0.0.0 \
    HOME=/tmp/openneko-home \
    XDG_CONFIG_HOME=/tmp/openneko-config
RUN useradd --system --create-home --uid 1001 neko
# /cache is mounted by the demo-mode adventureworks-init step; pre-creating
# it here lets the named volume initialize with neko ownership instead of
# root (Docker copies image-side ownership into a fresh named volume on
# first mount). Without this the loader can't write the downloaded zip.
# /app must also be writable by neko so `openneko install` (run in-container
# via docker exec) can write pnpm temp files + update package.json /
# pnpm-lock.yaml during plugin installs.
RUN mkdir -p /config/openneko /config/graphjin /tmp/openneko-home /tmp/openneko-tmp /cache /var/lib/openneko/plugins \
    && chown -R neko:neko /app /config /tmp/openneko-home /tmp/openneko-tmp /cache /var/lib/openneko
# Seed the plugin install dir with an empty package.json so `npm install`
# inside that dir has a workspace to operate on. This dir is isolated from
# /app's pnpm-managed node_modules — plugin packages land here cleanly
# regardless of how the worker's own deps were installed.
RUN printf '{\n  "name": "openneko-plugins",\n  "version": "0.0.0",\n  "private": true\n}\n' > /var/lib/openneko/plugins/package.json \
    && chown neko:neko /var/lib/openneko/plugins/package.json
COPY --from=deps --chown=neko:neko /app/node_modules ./node_modules
COPY --from=deps --chown=neko:neko /app/apps/worker/node_modules ./apps/worker/node_modules
COPY --from=deps --chown=neko:neko /app/packages/db/node_modules ./packages/db/node_modules
COPY --from=deps --chown=neko:neko /app/packages/llm/node_modules ./packages/llm/node_modules
COPY --from=deps --chown=neko:neko /app/packages/plugin-install/node_modules ./packages/plugin-install/node_modules
COPY --from=deps --chown=neko:neko /app/packages/plugin-types/node_modules ./packages/plugin-types/node_modules
COPY --chown=neko:neko package.json pnpm-workspace.yaml pnpm-lock.yaml ./
COPY --chown=neko:neko apps/worker ./apps/worker
COPY --chown=neko:neko packages ./packages
# Whole db/ (not just migrations): seeds + load-adventureworks-baked.sh
# are needed for `openneko start --mode demo`'s adventureworks-init step.
COPY --chown=neko:neko db ./db
COPY --chown=neko:neko entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh
RUN ln -s /app/apps/worker/node_modules/tsx /app/node_modules/tsx
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
CMD ["node", "--import", "tsx/esm", "apps/worker/src/index.ts"]

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
ARG GRAPHJIN_VERSION=3.18.18
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
