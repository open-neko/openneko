# Build: docker build -t openneko-sandbox recipes/sandbox

FROM golang:1.26.10-bookworm AS build
WORKDIR /src
COPY go.mod *.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/openneko-sandbox .

FROM node:24.22.1-bookworm-slim
# The script skills' toolchain. Keep it in step with KNOWN_SKILL_DEPS in
# packages/llm/src/work/skill-deps.ts.
ENV NODE_PATH=/usr/local/lib/node_modules
RUN printf 'Acquire::Retries "5";\nAcquire::http::Timeout "30";\n' > /etc/apt/apt.conf.d/80-retries \
    && apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates git jq ripgrep file python3 python3-pip \
      libreoffice-core libreoffice-common libreoffice-writer libreoffice-calc \
      libreoffice-impress libreoffice-draw poppler-utils qpdf tesseract-ocr \
    && rm -rf /var/lib/apt/lists/* \
    && pip3 install --no-cache-dir --break-system-packages \
      openpyxl python-pptx Pillow python-docx pypdf pdfplumber reportlab PyYAML \
      defusedxml lxml \
    && npm install --global --omit=dev --no-audit --no-fund \
      docx@9.7.1 pptxgenjs@4.0.1 react@19.2.8 react-dom@19.2.8 \
      react-icons@5.7.0 sharp@0.35.3 \
    && npm cache clean --force
COPY --from=build /out/openneko-sandbox /usr/local/bin/openneko-sandbox
# Root only to give each run its own user; compose drops every other capability.
USER 0:0
ENTRYPOINT ["/usr/local/bin/openneko-sandbox"]
