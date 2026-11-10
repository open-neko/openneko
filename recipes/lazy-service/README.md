# Lazy service listener

The librarian (Docling) image and the EmbeddingGemma 2 recipe keep a small Go
HTTP listener running. Their heavy processing children start on the first
processing request and exit after five idle minutes. The containers stay up;
model processes and their heaps do not. This uses no Docker socket, privileged
API, or in-host inference fallback.

- `OPENNEKO_LIBRARIAN_IDLE_TIMEOUT` overrides the Compose default (a Go
  duration such as `5m` or `30s`). Both images also accept
  `OPENNEKO_SERVICE_IDLE_TIMEOUT` directly.
- Concurrent callers share one startup. The first request waits for child
  readiness; failed startup returns 503 and a later request may retry.
- Health checks report listener availability plus `sleeping`, `starting`, or
  `running`. They never start a child or extend its idle timer.
- Shutdown requires no active HTTP requests and a successful backend idle
  check. Pending/running Docling jobs prevent shutdown; completed results do
  not. An unavailable idle check prevents eviction.
- Child crashes fail affected requests. The next request starts a new child;
  requests are not silently replayed. Existing library task checkpoints handle
  missing Docling task IDs by resubmitting the document.

Compose exposes only internal service ports. The worker uses
`NEKO_LIBRARIAN_URL=http://librarian:5001`. The embedding recipe is described in
[recipes/embedding-gemma](../embedding-gemma/README.md).

Extraction admission runs before multipart parsing and permits at most two
uploads/in-flight tasks: one conversion and one waiting task. Excess requests
return 429 and remain queued in pg-boss without consuming failure retries.
These processing limits are separate from chat sandbox admission. Internal
authentication is unchanged.

Completed Docling results and status are atomically published to the disk-backed
`librarian-results` volume at `/var/lib/neko-librarian/results`. Files and their
directory are flushed before publication; the extraction slot is then released.
The Go listener streams results directly from disk, without loading the model or
extending its idle timer. Results survive child/container restart, and repeated
reads return the same result until expiry (collection does not delete the only
copy before a caller durably saves it). Unknown or expired task lookups return
404 without waking Docling; the existing durable host job resubmits if needed.

Retention defaults to 15 minutes, 512 MiB of payload files, 256 results, and
8 MiB per result. Compose exposes `OPENNEKO_LIBRARIAN_RESULT_TTL_SECONDS` and
`OPENNEKO_LIBRARIAN_RESULT_MAX_BYTES` for the time/byte limits. Admission reserves
space for in-flight results; a full spool pauses new submissions without
removing unexpired results. Expiry runs in the listener even while Docling sleeps.
Input uploads remain on the bounded tmpfs and are removed after processing.
Disk-write failures cannot produce a successful task; if even the error cannot
be persisted, it stays in memory until observed or expired. The spool is a
retry cache, not the permanent document archive.

Limits are ceilings, not reservations. Docling retains its existing CPU, memory, upload-size, and queue limits. Idle
shutdown saves unused memory; it does not guarantee a 16 GiB host can handle
all configured ceilings simultaneously. The ten-sandbox startup measurements
in `apps/worker/AGENT_STARTUP.md` do not cover concurrent heavy document work.

Verification:

```sh
(cd recipes/lazy-service && go test -race ./...)
pnpm --filter @neko/llm exec vitest run test/library-extract.test.ts
docker build --target librarian-test -f apps/librarian/Dockerfile .
docker build --target librarian -t openneko-librarian:lazy -f apps/librarian/Dockerfile .
docker run --rm --network none --entrypoint python3 openneko-librarian:lazy /app/smoke.py
```

The durable queue regression test uses a separate disposable PostgreSQL database
with pgvector (it creates minimal tables):

```sh
EMBEDDING_TEST_DATABASE_URL=postgres://USER@HOST/TEST_DATABASE pnpm --filter @neko/worker exec vitest run test/embedding-jobs.test.ts
```
