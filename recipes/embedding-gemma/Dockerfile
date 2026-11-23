# Build from the recipes folder:
#   docker build -f recipes/embedding-gemma/Dockerfile -t embedding-gemma recipes

FROM golang:1.26.10-bookworm AS lazy-service-build
WORKDIR /src
COPY lazy-service .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/lazy-service .

# torchvision and pillow are required by the model's processor even for text.
FROM python:3.13.17-slim-bookworm AS embedding-python
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl \
    && rm -rf /var/lib/apt/lists/* \
    && dpkg --compare-versions "$(dpkg-query -W -f='${Version}' ca-certificates)" ge 20250419~deb12u2
COPY embedding-gemma/requirements.txt /tmp/embedding-requirements.txt
RUN pip install --no-cache-dir pip==26.2.2 \
    && pip install --no-cache-dir --index-url https://download.pytorch.org/whl/cpu \
      torch==2.14.1 torchvision==0.29.1 \
    && pip install --no-cache-dir -r /tmp/embedding-requirements.txt \
    && rm /tmp/embedding-requirements.txt

FROM embedding-python AS embedding-model
ARG EMBEDDING_MODEL_REVISION=914f7f89142e33e77833254d9c9b90c3cef7303b
COPY embedding-gemma/vendor_model.py /tmp/vendor_model.py
RUN python /tmp/vendor_model.py /opt/embedding-model "$EMBEDDING_MODEL_REVISION"

FROM embedding-python AS embedding
RUN useradd --system --create-home --uid 10001 embedding
COPY --from=lazy-service-build /out/lazy-service /usr/local/bin/lazy-service
COPY --from=embedding-model /opt/embedding-model /opt/embedding-model
COPY embedding-gemma/embedding_service.py embedding-gemma/smoke.py embedding-gemma/test_embedding_service.py /app/
RUN python -m compileall -q /app
ENV OPENNEKO_EMBEDDING_MODEL_PATH=/opt/embedding-model \
    HF_HUB_OFFLINE=1 \
    TRANSFORMERS_OFFLINE=1 \
    HF_HUB_DISABLE_TELEMETRY=1 \
    HF_HOME=/tmp/huggingface \
    XDG_CACHE_HOME=/tmp/cache \
    TORCHINDUCTOR_CACHE_DIR=/tmp/torchinductor \
    PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    TOKENIZERS_PARALLELISM=false \
    MALLOC_ARENA_MAX=2
WORKDIR /app
USER embedding
RUN python /app/embedding_service.py --check
EXPOSE 5003
ENTRYPOINT ["lazy-service"]
CMD ["--listen", ":5003", "--upstream", "http://127.0.0.1:5004", "--", "python3", "/app/embedding_service.py"]
