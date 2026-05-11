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
ARG GRAPHJIN_VERSION=3.18.10
ARG HERMES_AGENT_REF=64145a1996554e4e81b694e9737421f34f44e212
RUN apt-get update && apt-get install -y --no-install-recommends \
      git \
    && rm -rf /var/lib/apt/lists/*
RUN curl -fsSL -o /tmp/graphjin.tgz \
      "https://github.com/dosco/graphjin/releases/download/v${GRAPHJIN_VERSION}/graphjin_${GRAPHJIN_VERSION}_linux_amd64.tar.gz" \
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

# ─── 3. deps: workspace install (cached on lockfile) ───────────────────
FROM base AS deps
WORKDIR /app
COPY pnpm-workspace.yaml pnpm-lock.yaml package.json ./
COPY apps/web/package.json apps/web/package.json
COPY apps/worker/package.json apps/worker/package.json
COPY packages/db/package.json packages/db/package.json
COPY packages/llm/package.json packages/llm/package.json
RUN --mount=type=cache,id=pnpm,target=/pnpm/store \
    pnpm install --frozen-lockfile

# ─── 4. build: next build (standalone output) ──────────────────────────
FROM deps AS build
WORKDIR /app
COPY . .
ENV NEXT_TELEMETRY_DISABLED=1
RUN pnpm --filter @neko/web build

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
COPY --chown=neko:neko entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh
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
RUN mkdir -p /config/openneko /config/graphjin /tmp/openneko-home /tmp/openneko-tmp \
    && chown -R neko:neko /config /tmp/openneko-home /tmp/openneko-tmp
COPY --from=deps --chown=neko:neko /app/node_modules ./node_modules
COPY --from=deps --chown=neko:neko /app/apps/worker/node_modules ./apps/worker/node_modules
COPY --from=deps --chown=neko:neko /app/packages/db/node_modules ./packages/db/node_modules
COPY --from=deps --chown=neko:neko /app/packages/llm/node_modules ./packages/llm/node_modules
COPY --chown=neko:neko package.json pnpm-workspace.yaml pnpm-lock.yaml ./
COPY --chown=neko:neko apps/worker ./apps/worker
COPY --chown=neko:neko packages ./packages
COPY --chown=neko:neko db/migrations ./db/migrations
COPY --chown=neko:neko entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh
RUN ln -s /app/apps/worker/node_modules/tsx /app/node_modules/tsx
USER neko
EXPOSE 4100
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/entrypoint.sh"]
CMD ["node", "--import", "tsx/esm", "apps/worker/src/index.ts"]
