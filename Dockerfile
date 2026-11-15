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

# ─── 1. base: system tooling; node only in the build lineage ───────────
FROM node:24.22.1-bookworm-slim AS node-distribution

FROM debian:bookworm-slim AS os-runtime
# Retry transient apt mirror hiccups instead of hard-failing the image build.
RUN printf 'Acquire::Retries "5";\nAcquire::http::Timeout "30";\n' > /etc/apt/apt.conf.d/80-retries
# ca-certificates >= 20250419~deb12u2 (DLA-4845-1) drops the emSign ECC Root
# CA - G3 root after the eMudhra intermediate key compromise (CVE-2026-111943).
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates curl tini \
    && rm -rf /var/lib/apt/lists/* \
    && dpkg --compare-versions "$(dpkg-query -W -f='${Version}' ca-certificates)" ge 20250419~deb12u2

FROM os-runtime AS node-runtime
COPY --from=node-distribution /usr/local/bin/node /usr/local/bin/node
RUN ln -s node /usr/local/bin/nodejs

# Build stages additionally need Corepack/pnpm. Keeping it in this build-only
# lineage prevents package-manager shims and downloads from shipping.
FROM node-runtime AS base
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
FROM os-runtime AS runtime-base
# pg_dump must be at least the server's major version; Debian's client is 15.
COPY --from=neko-db /usr/local/share/keyrings/postgres.gpg.asc /usr/local/share/keyrings/postgres.gpg.asc
COPY --from=neko-db /etc/apt/sources.list.d/pgdg.list /etc/apt/sources.list.d/pgdg.list
# Git and ssh serve git-URL skill installs and the move off an old config repository.
RUN apt-get update && apt-get install -y --no-install-recommends \
      git openssh-client postgresql-client-16 \
    && rm -rf /var/lib/apt/lists/*

# Pinned GraphJin binary for the backend eval. The customer GraphJin runs inside openneko serve, linked from
# serv/v3 v3.21.18 (upstream commit 6f14910d5e64, the replace lines in
# apps/openneko/go.mod); bump both together.
FROM debian:bookworm-slim AS graphjin-bin
ARG GRAPHJIN_VERSION=3.21.18
ARG TARGETARCH
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl \
    && dpkg --compare-versions "$(dpkg-query -W -f='${Version}' ca-certificates)" ge 20250419~deb12u2 \
    && case "${TARGETARCH}" in amd64|arm64) ;; *) exit 1 ;; esac \
    && curl -fsSL --retry 10 --retry-delay 5 --retry-all-errors \
      -o /tmp/graphjin.tgz \
      "https://github.com/dosco/graphjin/releases/download/v${GRAPHJIN_VERSION}/graphjin_${GRAPHJIN_VERSION}_linux_${TARGETARCH}.tar.gz" \
    && tar -xzf /tmp/graphjin.tgz -C /usr/local/bin graphjin \
    && rm /tmp/graphjin.tgz \
    && rm -rf /var/lib/apt/lists/* \
    && graphjin version

# ─── 3. deps: workspace install (cached on lockfile) ───────────────────
FROM base AS deps
WORKDIR /app
COPY pnpm-workspace.yaml pnpm-lock.yaml package.json ./
COPY apps/web/package.json apps/web/package.json
COPY packages/avatars/package.json packages/avatars/package.json
COPY packages/contract/package.json packages/contract/package.json
COPY packages/db/package.json packages/db/package.json
COPY packages/evals/package.json packages/evals/package.json
COPY packages/llm/package.json packages/llm/package.json
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
RUN pnpm --filter @neko/web build:spa

# ─── 4b. openneko go binary ────────────────────────────────────────────
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
    && dpkg --compare-versions "$(dpkg-query -W -f='${Version}' ca-certificates)" ge 20250419~deb12u2 \
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

# Alpine runtime for the backend eval's stateless API fixture.
FROM node:24.22.1-alpine AS node-alpine-distribution

FROM alpine:3.22 AS node-alpine-runtime
RUN apk add --no-cache --upgrade libstdc++ ca-certificates-bundle
COPY --from=node-alpine-distribution /usr/local/bin/node /usr/local/bin/node
COPY --from=node-alpine-distribution /usr/local/share/doc/node /usr/local/share/doc/node

# Stateless upstream used only by the backend eval to prove that GraphJin
# forwards an explicitly exposed API `call` mutation. It keeps no mutable
# state, so repeated/cancelled episodes are deterministic and require no reset.
FROM node-alpine-runtime AS eval-api-fixture
WORKDIR /app
COPY evals/environment/adventureworks/api/server.mjs /app/server.mjs
EXPOSE 8090
ENTRYPOINT ["node", "/app/server.mjs"]

# ─── 5a. web runtime ───────────────────────────────────────────────────
FROM runtime-base AS web
ARG OPENNEKO_VERSION=unknown
ARG OPENNEKO_COMMIT=unknown
ENV OPENNEKO_VERSION=$OPENNEKO_VERSION OPENNEKO_COMMIT=$OPENNEKO_COMMIT
LABEL org.opencontainers.image.revision=$OPENNEKO_COMMIT org.opencontainers.image.version=$OPENNEKO_VERSION
WORKDIR /app
# Writable HOME under /tmp so the entrypoint can materialize config on
# read-only container filesystems. PORT=8080 matches the common PaaS
# convention (Cloud Run, Heroku, Fly, Railway).
ENV OPENNEKO_IN_CONTAINER=1 \
    PORT=8080 \
    OPENNEKO_WEB_DIR=/app/apps/web/dist \
    HOME=/tmp/openneko-home \
    XDG_CONFIG_HOME=/tmp/openneko-config
RUN useradd --system --create-home --uid 1001 neko
RUN install -d -o 1001 -m 0770 /var/lib/openneko/agent-runs /run/openneko
RUN mkdir -p /config/openneko /config/graphjin /tmp/openneko-home /tmp/openneko-tmp \
    && chown -R neko:neko /config /tmp/openneko-home /tmp/openneko-tmp
COPY --from=build --chown=neko:neko /app/apps/web/dist ./apps/web/dist
COPY --chown=neko:neko db/seeds ./db/seeds
COPY packs /usr/local/share/openneko/packs
COPY --chown=neko:neko entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh
COPY --from=go-build --chown=neko:neko /out/openneko /usr/local/bin/openneko
RUN chmod +x /usr/local/bin/openneko
USER neko
EXPOSE 8080
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/entrypoint.sh"]
CMD ["openneko", "web"]

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
ENV OPENNEKO_GRAPHJIN_SEED=/seed/customer.sources.yml OPENNEKO_IN_CONTAINER=1
USER 1001:1001
ENTRYPOINT ["/openneko"]
CMD ["serve"]

# ─── 6. graphjin-runtime ───────────────────────────────────────────────
# The GraphJin binary alone, for the backend eval stack.
FROM alpine:3.22 AS graphjin-runtime
RUN apk add --no-cache --upgrade ca-certificates ca-certificates-bundle curl tini
COPY --from=graphjin-bin /usr/local/bin/graphjin /usr/local/bin/graphjin

