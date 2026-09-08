FROM node:24-bookworm-slim@sha256:ba849c60be29959425b8734d57b8b4b7d56f98edd9504c9af091d5281095a71e
RUN apt-get update && apt-get install -y --no-install-recommends iproute2 nftables ca-certificates && rm -rf /var/lib/apt/lists/* && useradd -m -d /sandbox sandbox
COPY --chmod=755 connector /app/connector
USER sandbox
WORKDIR /sandbox
