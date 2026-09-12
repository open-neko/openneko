ARG BASE_IMAGE=ghcr.io/open-neko/agent:branch-186383e
FROM ${BASE_IMAGE}
COPY scripts/bench/generic-warm/measure.mjs scripts/bench/generic-warm/preload.py /bench/
COPY scripts/fixtures/hermes-bench-mcp.mjs /bench/fixture.mjs
USER 1000660000:1000660000
ENTRYPOINT ["node", "/bench/measure.mjs"]
