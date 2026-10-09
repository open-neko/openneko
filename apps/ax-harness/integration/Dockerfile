FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates iproute2 nftables curl && rm -rf /var/lib/apt/lists/* && groupadd -g 1000 sandbox && useradd -m -u 1000 -g sandbox sandbox && mkdir /sandbox && chown sandbox:sandbox /sandbox
COPY fixture.crt /usr/local/share/ca-certificates/fixture.crt
RUN update-ca-certificates
COPY probe-bin /usr/local/bin/harness-probe
WORKDIR /sandbox
CMD ["sleep", "infinity"]
