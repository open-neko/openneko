# EmbeddingGemma 2 embedding service

This recipe runs Google's EmbeddingGemma 2 (`google/embeddinggemma-2`,
Apache 2.0) on CPU behind an OpenAI-compatible `/v1/embeddings` route. The
model loads on the first request and unloads when idle.

## Build and run

```sh
docker build -f recipes/embedding-gemma/Dockerfile -t embedding-gemma recipes
docker run -d --name embedding-gemma --memory 2.5g --cpus 2 \
  --read-only --tmpfs /tmp:size=256m -p 127.0.0.1:5003:5003 embedding-gemma
```

The build downloads one pinned model revision and keeps only the text
weights. The running service makes no network calls.

## Connect OpenNeko

Follow "Semantic search" in `INSTALL.md` with these values:

```yaml
NEKO_EMBEDDING_URL: http://embedding-gemma:5003/v1
NEKO_EMBEDDING_MODEL: google/embeddinggemma-2
```

Use a host name that the web and worker containers can reach. The service
accepts any API key. Set `NEKO_EMBEDDING_DIMENSIONS` to 128, 256 or 512 for
shorter vectors.

## Limits

- Vectors have 768 dimensions by default.
- One inference runs at a time. At most 16 requests wait; more get HTTP 429.
- An input is cut at 2048 tokens. A request body is at most 4 MiB.
- `OPENNEKO_SERVICE_IDLE_TIMEOUT` sets the idle time before the model unloads
  (a Go duration, default `5m`).
- The service does not store or log submitted text.

## Test

```sh
(cd recipes/embedding-gemma && python3 -m unittest test_embedding_service)
docker run --rm --network none --entrypoint python3 embedding-gemma /app/smoke.py
```
