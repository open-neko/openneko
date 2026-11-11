# Build: docker build -t openneko-sandbox recipes/sandbox

FROM golang:1.26.10-bookworm AS build
WORKDIR /src
COPY go.mod *.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/openneko-sandbox .

FROM node:24.22.1-bookworm-slim
# The script skills' toolchain. Keep it in step with KNOWN_SKILL_DEPS in
# packages/llm/src/work/skill-deps.ts and with skills/package.json. The Python
# document libraries serve terminal commands and the skill tests.
# The pdf skill's fallback fonts. Only the faces that it uses stay in the image.
RUN printf 'Acquire::Retries "5";\nAcquire::http::Timeout "30";\n' > /etc/apt/apt.conf.d/80-retries \
    && printf '%s\n' 'path-exclude=/usr/share/fonts/truetype/dejavu/DejaVuSerif*' \
      'path-exclude=/usr/share/fonts/truetype/dejavu/DejaVuSansMono-Bold.ttf' \
      'path-exclude=/usr/share/fonts/truetype/droid/*' > /etc/dpkg/dpkg.cfg.d/90-fonts \
    && apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates git jq ripgrep file python3 python3-pip poppler-utils \
      tesseract-ocr fonts-dejavu-core fonts-droid-fallback \
    && rm -rf /var/lib/apt/lists/* \
    && dpkg --compare-versions "$(dpkg-query -W -f='${Version}' ca-certificates)" ge 20250419~deb12u2 \
    && pip3 install --no-cache-dir --break-system-packages \
      pypdf python-docx python-pptx openpyxl PyYAML
COPY skills/package.json skills/package-lock.json /opt/openneko/skills/
RUN cd /opt/openneko/skills && npm ci --omit=dev --omit=optional --ignore-scripts --no-audit --no-fund \
    && npm cache clean --force
ENV NODE_PATH=/opt/openneko/skills/node_modules
COPY --from=build /out/openneko-sandbox /usr/local/bin/openneko-sandbox
# Root only to give each run its own user; compose drops every other capability.
USER 0:0
ENTRYPOINT ["/usr/local/bin/openneko-sandbox"]
