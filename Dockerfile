# syntax=docker/dockerfile:1.7
#
# One Dockerfile for the OpenNeko runtime images, with shared build and runtime
# layers. Release targets are enumerated in .github/workflows/release-binaries.yml.
#
# Runtime config strategy: the app reads ~/.config/openneko/config.json
# (DB connection) and ~/.config/openneko/secret-key (at-rest encryption
# key) on every boot. To support read-only container filesystems (e.g.
# Cloud Run), HOME + XDG_CONFIG_HOME point at writable /tmp paths and
# entrypoint.sh materializes those files from env vars before exec'ing
# the app. See entrypoint.sh for the env var contract.

# ─── 1. base: node + system tooling ────────────────────────────────────
# The official Node image also carries npm, Corepack, Yarn, C/C++ headers, and
# documentation. Copy only the executable into the shared runtime lineage;
# the worker adds a pruned npm payload below because it offers runtime package
# installation. Web does not pay that tax.
FROM node:24.22.1-bookworm-slim AS node-distribution

FROM node-distribution AS npm-payload
RUN rm -rf /usr/local/lib/node_modules/npm/docs /usr/local/lib/node_modules/npm/man \
    && find /usr/local/lib/node_modules/npm -type f -name '*.map' -delete

FROM debian:bookworm-slim AS node-runtime
COPY --from=node-distribution /usr/local/bin/node /usr/local/bin/node
COPY --from=node-distribution /usr/local/share/doc/node /usr/local/share/doc/node
RUN ln -s node /usr/local/bin/nodejs
# Retry transient apt mirror hiccups instead of hard-failing the image build.
RUN printf 'Acquire::Retries "5";\nAcquire::http::Timeout "30";\n' > /etc/apt/apt.conf.d/80-retries
# ca-certificates >= 20250419~deb12u2 (DLA-4845-1) drops the emSign ECC Root
# CA - G3 root after the eMudhra intermediate key compromise (CVE-2026-111943).
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates curl tini \
    && rm -rf /var/lib/apt/lists/* \
    && dpkg --compare-versions "$(dpkg-query -W -f='${Version}' ca-certificates)" ge 20250419~deb12u2

# npm is part of the worker/agent feature contract, but its generated manuals,
# source maps, Corepack, Yarn, and development headers are not.
FROM node-runtime AS npm-runtime
COPY --from=npm-payload /usr/local/lib/node_modules/npm /usr/local/lib/node_modules/npm
RUN ln -s ../lib/node_modules/npm/bin/npm-cli.js /usr/local/bin/npm \
    && ln -s ../lib/node_modules/npm/bin/npx-cli.js /usr/local/bin/npx

# Build stages additionally need Corepack/pnpm. Keeping it in this build-only
# lineage prevents package-manager shims and downloads from shipping.
FROM npm-runtime AS base
ENV PNPM_HOME=/pnpm
ENV PATH=$PNPM_HOME:$PATH
COPY --from=node-distribution /usr/local/lib/node_modules/corepack /usr/local/lib/node_modules/corepack
RUN ln -s ../lib/node_modules/corepack/dist/corepack.js /usr/local/bin/corepack \
    && corepack enable \
    && corepack prepare pnpm@9.14.1 --activate

# The metadata database has an enforced storage ABI: PostgreSQL 16 on Debian
# Bookworm/glibc. Text indexes persist in a named volume, so switching the
# libc/collation provider (for example, to Alpine/musl) can make an existing
# btree silently disagree with equality lookups. Keep the base immutable. If
# its collation provider/version changes, bump the compiled
# StorageContractVersion so existing volumes are rebuilt exactly once.
# neko-db is otherwise the stock pgvector image and entrypoint; backups are
# logical dumps taken by `openneko backup`.
FROM pgvector/pgvector:0.8.6-pg16-bookworm@sha256:ccc6e83d6e35e931dc7c5def2022729d5a6c370318d099181995567ff1fb4d6b AS neko-db
LABEL org.openneko.storage-contract="1" \
      org.openneko.storage-owner="999:999"
RUN test "$(getconf GNU_LIBC_VERSION)" = "glibc 2.36" \
    && test "$(pg_config --version)" = "PostgreSQL 16.15 (Debian 16.15-1.pgdg12+2)" \
    && test "$(id -u postgres):$(id -g postgres)" = "999:999"

# ─── 2. control-plane runtime ─────────────────────────────────────────
# Web holds no GraphJin CLI and no document toolchain. The worker uses the
# narrower shared GraphJin/npm runtime defined below.
FROM node-runtime AS runtime-base
# Git and ssh serve trusted config VCS and git-backed installs.
RUN apt-get update && apt-get install -y --no-install-recommends \
      git openssh-client \
    && rm -rf /var/lib/apt/lists/*

# Pinned GraphJin binary for the worker's config checks and the backend eval. The customer GraphJin runs inside openneko serve, linked from
# serv/v3 v3.21.18 (upstream commit 6f14910d5e64, the replace lines in
# apps/openneko/go.mod); bump both together.
FROM debian:bookworm-slim AS graphjin-bin
ARG GRAPHJIN_VERSION=3.21.18
ARG TARGETARCH
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl \
    && case "${TARGETARCH}" in amd64|arm64) ;; *) exit 1 ;; esac \
    && curl -fsSL --retry 10 --retry-delay 5 --retry-all-errors \
      -o /tmp/graphjin.tgz \
      "https://github.com/dosco/graphjin/releases/download/v${GRAPHJIN_VERSION}/graphjin_${GRAPHJIN_VERSION}_linux_${TARGETARCH}.tar.gz" \
    && tar -xzf /tmp/graphjin.tgz -C /usr/local/bin graphjin \
    && rm /tmp/graphjin.tgz \
    && rm -rf /var/lib/apt/lists/* \
    && graphjin version

# The worker requires npm plus the pinned GraphJin CLI for trusted host-side
# configuration.
FROM npm-runtime AS graphjin-node-runtime
COPY --from=graphjin-bin /usr/local/bin/graphjin /usr/local/bin/graphjin

# ─── 3. deps: workspace install (cached on lockfile) ───────────────────
FROM base AS deps
WORKDIR /app
COPY pnpm-workspace.yaml pnpm-lock.yaml package.json ./
COPY apps/web/package.json apps/web/package.json
COPY apps/worker/package.json apps/worker/package.json
COPY packages/avatars/package.json packages/avatars/package.json
COPY packages/channels/package.json packages/channels/package.json
COPY packages/db/package.json packages/db/package.json
COPY packages/evals/package.json packages/evals/package.json
COPY packages/interaction/package.json packages/interaction/package.json
COPY packages/llm/package.json packages/llm/package.json
COPY packages/packs/package.json packages/packs/package.json
COPY packages/secrets/package.json packages/secrets/package.json
COPY packages/secret-crypt/package.json packages/secret-crypt/package.json
COPY packages/telemetry/package.json packages/telemetry/package.json
# onnxruntime-node defaults Linux x64 installs to its 300+ MiB CUDA provider.
# Control-plane images use the bundled CPU runtime and ship no NVIDIA stack.
RUN --mount=type=cache,id=pnpm,target=/pnpm/store \
    ONNXRUNTIME_NODE_INSTALL=skip \
      pnpm install --frozen-lockfile --side-effects-cache=false

# ─── 4. source + web build ─────────────────────────────────────────────
# Keep source assembly separate from the expensive Next.js build. Worker,
# agent, and one-shot targets only need the installed workspace plus sources;
# forcing them through `next build` added several minutes to every image build.
FROM deps AS source
WORKDIR /app
COPY . .

FROM source AS build
ENV NEXT_TELEMETRY_DISABLED=1
RUN pnpm --filter @neko/web build

# Next's standalone tree and its manually copied native dependency use the same
# server-only pruning contract as the worker deployment.
FROM build AS web-deploy
ARG TARGETARCH
RUN sh scripts/prune-node-runtime.sh /app/apps/web/.next/standalone "$TARGETARCH" \
    && sh scripts/prune-node-runtime.sh /app "$TARGETARCH"

# ─── 4b. openneko go binary ────────────────────────────────────────────
# Built from apps/openneko (Go 1.24 module) and baked into the worker
# image so the agent's Bash tool can run `openneko install/secrets/...`
# from inside the worker container. The same binary operators install via
# Homebrew / GitHub Releases on their host.
FROM golang:1.26.10-bookworm AS go-build
WORKDIR /src
COPY apps/openneko/go.mod apps/openneko/go.sum apps/openneko/
COPY apps/ax-harness/go.mod apps/ax-harness/go.sum apps/ax-harness/
RUN cd apps/openneko && go mod download
COPY apps/ax-harness apps/ax-harness
COPY apps/openneko apps/openneko
COPY db/migrations db/migrations
# Ensure the embedded migration copies match the canonical source. CI also
# runs this check separately; build-time guard prevents drift sneaking in
# via an image-only rebuild.
RUN cd apps/openneko && ./scripts/sync-migrations.sh --check
RUN cd apps/openneko && \
    CGO_ENABLED=0 GOOS=linux \
    go build -trimpath -ldflags "-s -w -X github.com/open-neko/neko/apps/openneko/internal/version.Version=container -X github.com/open-neko/neko/apps/openneko/internal/version.Commit=container -X github.com/open-neko/neko/apps/openneko/internal/version.CommitTimestamp=container" \
      -o /out/openneko ./cmd/openneko

# DuckDB CLI for sheet tables in openneko serve, pinned by checksum.
FROM debian:bookworm-slim AS duckdb-bin
ARG TARGETARCH
ARG DUCKDB_VERSION=v1.5.5
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl \
    && rm -rf /var/lib/apt/lists/* \
    && case "$TARGETARCH" in \
         amd64) sum=c61f21485e6e41d3a0c28ce9904ea18346309cf427b4cf9479bc3564348dc885 ;; \
         arm64) sum=50c719e603a4e599d435e5321542458edc1cfc5f7eed65979fe9bc5ae4e3ba23 ;; \
         *) echo "unsupported arch $TARGETARCH" && exit 1 ;; \
       esac \
    && curl -fsSL --retry 5 --retry-delay 5 --retry-all-errors -o /tmp/duckdb.gz \
         "https://github.com/duckdb/duckdb/releases/download/${DUCKDB_VERSION}/duckdb_cli-linux-${TARGETARCH}.gz" \
    && echo "$sum  /tmp/duckdb.gz" | sha256sum -c - \
    && gunzip -c /tmp/duckdb.gz > /usr/local/bin/duckdb \
    && chmod 0755 /usr/local/bin/duckdb \
    && /usr/local/bin/duckdb --version

# Small Node one-shots. Bundling them prevents demo/config initialization from
# reusing the full worker image and its production dependency closure.
FROM source AS init-tools-deploy
RUN mkdir -p /out/init-tools \
    && cd apps/worker \
    && pnpm exec esbuild ../../packages/db/src/seed-adventureworks.mjs \
      --bundle --minify --platform=node --format=esm \
      --banner:js="import { createRequire } from 'node:module'; const require = createRequire(import.meta.url);" \
      --outfile=/out/init-tools/seed-adventureworks.js

# Alpine one-shots likewise need only the Node executable, not npm/Yarn.
FROM node:24.22.1-alpine AS node-alpine-distribution

FROM alpine:3.22 AS node-alpine-runtime
RUN apk add --no-cache libstdc++
COPY --from=node-alpine-distribution /usr/local/bin/node /usr/local/bin/node
COPY --from=node-alpine-distribution /usr/local/share/doc/node /usr/local/share/doc/node

FROM node-alpine-runtime AS init-tools
WORKDIR /app
COPY --from=init-tools-deploy /out/init-tools /app
CMD ["node", "--version"]

FROM source AS adventureworks-loader-deploy
RUN pnpm --dir apps/worker exec esbuild scripts/load-adventureworks.ts \
      --bundle --minify --platform=node --format=esm \
      --banner:js="import { createRequire } from 'node:module'; const require = createRequire(import.meta.url);" \
      --outfile=/out/load-adventureworks.js

FROM node-alpine-runtime AS adventureworks-loader
RUN apk add --no-cache ca-certificates curl postgresql-client unzip
WORKDIR /app
COPY --from=adventureworks-loader-deploy /out/load-adventureworks.js /app/load-adventureworks.js
ENTRYPOINT ["node", "/app/load-adventureworks.js"]

# Stateless upstream used only by the backend eval to prove that GraphJin
# forwards an explicitly exposed API `call` mutation. It keeps no mutable
# state, so repeated/cancelled episodes are deterministic and require no reset.
FROM node-alpine-runtime AS eval-api-fixture
WORKDIR /app
COPY evals/environment/adventureworks/api/server.mjs /app/server.mjs
EXPOSE 8090
ENTRYPOINT ["node", "/app/server.mjs"]

# ─── 4z. agent runner bundle ───────────────────────────────────────────
# The runner entry and multiplexed tool bridge, without the worker's
# dependency closure. Web and worker run it from /opt/openneko-agent.
FROM source AS agent-deploy
RUN cd apps/worker && pnpm exec esbuild \
      src/agent-sandbox/entry.ts src/agent-sandbox/mcp-bridge.ts \
      --bundle --platform=node --format=esm \
      --banner:js="import { createRequire } from 'node:module'; const require = createRequire(import.meta.url);" \
      --outdir=/out/agent-app \
    && printf '{"type":"module"}\n' > /out/agent-app/package.json \
    && mkdir -p /out/agent-app/assets \
    && cp -R ../../packages/llm/assets/builtin-skills /out/agent-app/assets/

# ─── 5a. web runtime ───────────────────────────────────────────────────
# Web runs each agent run's runner from /opt/openneko-agent; turns run in serve.
FROM runtime-base AS web
ARG OPENNEKO_VERSION=unknown
ARG OPENNEKO_COMMIT=unknown
ENV OPENNEKO_VERSION=$OPENNEKO_VERSION OPENNEKO_COMMIT=$OPENNEKO_COMMIT
LABEL org.opencontainers.image.revision=$OPENNEKO_COMMIT org.opencontainers.image.version=$OPENNEKO_VERSION
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
RUN install -d -o 1001 -m 0770 /var/lib/openneko/agent-runs /run/openneko
RUN mkdir -p /config/openneko /config/graphjin /tmp/openneko-home /tmp/openneko-tmp \
    && chown -R neko:neko /config /tmp/openneko-home /tmp/openneko-tmp
# Standalone output is self-contained (server.js + traced node_modules).
# Static + public are served by server.js but not auto-copied — we copy
# them in alongside, matching the layout server.js expects.
COPY --from=web-deploy --chown=neko:neko /app/apps/web/.next/standalone ./
COPY --from=web-deploy --chown=neko:neko /app/apps/web/.next/static ./apps/web/.next/static
COPY --from=web-deploy --chown=neko:neko /app/apps/web/public ./apps/web/public
# Next.js standalone tracing misses static asset dirs — copy explicitly.
COPY --from=web-deploy --chown=neko:neko /app/packages/llm/assets ./packages/llm/assets
COPY --chown=neko:neko entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh
# Vendor the openneko Go binary so the entrypoint can run `openneko migrate`
# at boot (replaces the legacy neko-db-init container). Same binary as the
# worker image and the host install.
COPY --from=go-build --chown=neko:neko /out/openneko /usr/local/bin/openneko
RUN chmod +x /usr/local/bin/openneko
COPY --from=agent-deploy /out/agent-app /opt/openneko-agent
RUN chmod -R a+rX /opt/openneko-agent
USER neko
EXPOSE 8080
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/entrypoint.sh"]
CMD ["node", "apps/web/server.js"]

# ─── 5b. worker runtime ────────────────────────────────────────────────
# Trimmed prod closure of @neko/worker: drops devDeps + other apps' sources +
# web/Next, keeps src + tsx + @neko/llm (with assets). Same
# mechanism as agent-deploy; rooted at /app, so the entry is /app/src/index.ts.
FROM source AS worker-deploy
ARG TARGETARCH
RUN --mount=type=cache,id=pnpm,target=/pnpm/store \
    ONNXRUNTIME_NODE_INSTALL=skip \
      pnpm --filter @neko/worker deploy --prod /out/worker-app \
    && sh scripts/prune-node-runtime.sh /out/worker-app "$TARGETARCH"

# The worker runs from source via tsx (not a build step). It serves /health +
# admin endpoints on port 4100 for liveness probes. Records config validation
# and approved schema diff/sync require the shared pinned GraphJin CLI; this
# parent supplies it without inheriting the document toolchain.
FROM graphjin-node-runtime AS worker
ARG OPENNEKO_VERSION=unknown
ARG OPENNEKO_COMMIT=unknown
ENV OPENNEKO_VERSION=$OPENNEKO_VERSION OPENNEKO_COMMIT=$OPENNEKO_COMMIT
LABEL org.opencontainers.image.revision=$OPENNEKO_COMMIT org.opencontainers.image.version=$OPENNEKO_VERSION
WORKDIR /app
# pg_dump must be at least the server's major version; Debian's client is 15.
COPY --from=neko-db /usr/local/share/keyrings/postgres.gpg.asc /usr/local/share/keyrings/postgres.gpg.asc
COPY --from=neko-db /etc/apt/sources.list.d/pgdg.list /etc/apt/sources.list.d/pgdg.list
RUN apt-get update && apt-get install -y --no-install-recommends \
      git openssh-client postgresql-client-16 unzip \
    && rm -rf /var/lib/apt/lists/*
ENV NODE_ENV=production \
    PORT=4100 \
    HOSTNAME=0.0.0.0 \
    HOME=/tmp/openneko-home \
    XDG_CONFIG_HOME=/tmp/openneko-config
RUN useradd --system --create-home --uid 1001 neko
RUN install -d -o 1001 -m 0770 /var/lib/openneko/agent-runs /run/openneko
# /cache is mounted by the demo-mode adventureworks-init step; pre-creating
# it here lets the named volume initialize with neko ownership instead of root
# (Docker copies image-side ownership into a fresh named volume on first mount).
RUN mkdir -p /config/openneko /config/graphjin /tmp/openneko-home /tmp/openneko-tmp /cache \
    && chown -R neko:neko /app /config /tmp/openneko-home /tmp/openneko-tmp /cache
# Prod closure (src + node_modules) rooted at /app, replacing the full pnpm
# workspace install + per-package source copies.
COPY --from=worker-deploy --chown=neko:neko /out/worker-app ./
# Whole db/ (not just migrations): seeds + load-adventureworks-baked.sh
# are needed for `openneko start --mode demo`'s adventureworks-init step.
COPY --chown=neko:neko db ./db
# Embedded first-party solution packs are loaded and validated by the worker.
COPY --chown=neko:neko packs ./packs
COPY --chown=neko:neko apps/worker/scripts/magento-v2-live-acceptance.ts ./scripts/magento-v2-live-acceptance.ts
COPY --chown=neko:neko entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh
# Vendor the openneko Go binary so the agent's Bash tool inside the worker
# container can run `openneko install/secrets/pack …` without an
# extra install step. Same binary operators install on their host.
COPY --from=go-build --chown=neko:neko /out/openneko /usr/local/bin/openneko
RUN chmod +x /usr/local/bin/openneko
COPY --from=agent-deploy /out/agent-app /opt/openneko-agent
RUN chmod -R a+rX /opt/openneko-agent
USER neko
RUN cd /opt/openneko-agent && node entry.js --preflight \
    && node --input-type=module -e "await import('./mcp-bridge.js')"
EXPOSE 4100
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/entrypoint.sh"]
CMD ["node", "--import", "tsx/esm", "src/index.ts"]

# ─── 5c. neko-cli runtime ──────────────────────────────────────────────
# Minimal image containing just the openneko Go binary. It runs the
# `storage-reconcile` and `neko-migrate` start steps, which `openneko start`
# runs before any database consumer starts.
#
# The binary is static (CGO_ENABLED=0), so the distroless static image is the
# complete runtime. Its Debian CA bundle keeps managed-Postgres TLS working.
FROM gcr.io/distroless/static-debian12:latest AS neko-cli
COPY --from=go-build /out/openneko /openneko
ENTRYPOINT ["/openneko"]
CMD ["--help"]

# ─── 5d. neko-serve runtime ────────────────────────────────────────────
# `openneko serve`: each user's private files, their keyword index in neko-db,
# SQL over sheet tables, agent turns, the customer GraphJin and the
# subscriptions GraphJin. DuckDB needs
# libstdc++, so the base is the distroless cc image. The folders are created
# here so a fresh named volume is owned by the non-root user.
FROM debian:bookworm-slim AS serve-data
RUN mkdir -p /data/files /data/file-index /var/lib/openneko/agent-runs /run/openneko /credentials /config/graphjin \
    && chown -R 1001:1001 /data /var/lib/openneko /run/openneko /credentials /config \
    && chmod 0770 /var/lib/openneko/agent-runs /run/openneko \
    && chmod 0700 /credentials /config/graphjin

FROM gcr.io/distroless/cc-debian12:latest AS neko-serve
COPY --from=go-build /out/openneko /openneko
COPY --from=duckdb-bin /usr/local/bin/duckdb /usr/local/bin/duckdb
COPY --from=serve-data --chown=1001:1001 /data /data
COPY --from=serve-data --chown=1001:1001 /var/lib/openneko /var/lib/openneko
COPY --from=serve-data --chown=1001:1001 /run/openneko /run/openneko
COPY --from=serve-data --chown=1001:1001 /credentials /credentials
COPY --from=serve-data --chown=1001:1001 /config /config
COPY db/graphjin/customer.sources.example.yml /seed/customer.sources.yml
COPY db/graphjin/dev.sources.example.yml /seed/dev.sources.yml
ENV OPENNEKO_GRAPHJIN_SEED=/seed/customer.sources.yml
USER 1001:1001
ENTRYPOINT ["/openneko"]
CMD ["serve"]

# ─── 6. graphjin-runtime ───────────────────────────────────────────────
# The GraphJin binary alone, for the backend eval stack.
FROM alpine:3.22 AS graphjin-runtime
RUN apk add --no-cache ca-certificates curl tini
COPY --from=graphjin-bin /usr/local/bin/graphjin /usr/local/bin/graphjin

