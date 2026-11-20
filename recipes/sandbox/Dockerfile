# Build: docker build -t openneko-sandbox recipes/sandbox

FROM node:24.22.1-bookworm-slim AS node

FROM golang:1.26.10-bookworm AS build
WORKDIR /src
COPY go.mod go.sum *.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/openneko-sandbox .
COPY --from=node /usr/local/bin/node /out/node
RUN strip /out/node

FROM node AS skills
COPY skills/package.json skills/package-lock.json /opt/openneko/skills/
# Only the files that the skills load stay: pdf-lib through require, and
# pdfjs-dist through legacy/build with its cmaps, fonts, wasm and iccs.
RUN cd /opt/openneko/skills && npm ci --omit=dev --omit=optional --ignore-scripts --no-audit --no-fund \
    && cd node_modules \
    && rm -rf pdfjs-dist/build pdfjs-dist/web pdfjs-dist/legacy/web pdfjs-dist/types pdfjs-dist/image_decoders \
      pdfjs-dist/legacy/image_decoders pdf-lib/dist pdf-lib/es pdf-lib/ts3.4 pdf-lib/src \
    && find . \( -name '*.map' -o -name '*.d.ts' -o -name '*.d.mts' -o -name '*.d.cts' -o -name '*.md' \) -type f -delete

FROM debian:bookworm-slim AS python
RUN apt-get update && apt-get install -y --no-install-recommends python3-pip \
    && pip3 install --no-cache-dir --break-system-packages --target /out \
      pypdf python-docx python-pptx openpyxl PyYAML \
    && rm -rf /out/bin

FROM debian:bookworm-slim
# The script skills' toolchain. Keep it in step with KNOWN_SKILL_DEPS in
# packages/llm/src/work/skill-deps.ts and with skills/package.json. The Python
# document libraries serve terminal commands and the skill tests.
# The pdf skill's fallback fonts. Only the faces that it uses stay in the image.
# The pdf skill's ocr runs --psm 3, which needs no osd data.
RUN printf 'Acquire::Retries "5";\nAcquire::http::Timeout "30";\n' > /etc/apt/apt.conf.d/80-retries \
    && printf '%s\n' 'path-exclude=/usr/share/fonts/truetype/dejavu/DejaVuSerif*' \
      'path-exclude=/usr/share/fonts/truetype/dejavu/DejaVuSansMono-Bold.ttf' \
      'path-exclude=/usr/share/fonts/truetype/droid/*' \
      'path-exclude=/usr/share/tesseract-ocr/*/tessdata/osd.traineddata' \
      'path-exclude=/usr/share/doc/*' 'path-include=/usr/share/doc/*/copyright' \
      'path-exclude=/usr/share/man/*' 'path-exclude=/usr/share/info/*' \
      'path-exclude=/usr/share/locale/*' 'path-exclude=/usr/share/lintian/*' > /etc/dpkg/dpkg.cfg.d/90-slim \
    && apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates jq ripgrep file python3 poppler-utils \
      tesseract-ocr fonts-dejavu-core fonts-droid-fallback nftables \
    && dpkg --compare-versions "$(dpkg-query -W -f='${Version}' ca-certificates)" ge 20250419~deb12u2 \
    && rm -rf /var/lib/apt/lists/* /var/cache/debconf/*-old /var/log/* /tmp/*
COPY --from=python /out /usr/local/lib/python3.11/dist-packages
COPY --from=build /out/node /usr/local/bin/node
COPY --from=skills /opt/openneko/skills/node_modules /opt/openneko/skills/node_modules
ENV NODE_PATH=/opt/openneko/skills/node_modules
COPY --from=build /out/openneko-sandbox /usr/local/bin/openneko-sandbox
# Root only to give each run its own user and to set the runs' firewall at
# start; compose drops every other capability.
USER 0:0
ENTRYPOINT ["/usr/local/bin/openneko-sandbox"]
