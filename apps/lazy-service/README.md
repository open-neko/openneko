# Lazy document services

Embedding and Docling containers keep a small Go HTTP listener running. Their
heavy processing children start on the first processing request and exit after five
idle minutes. The containers stay up; model processes and their heaps do not.
This uses no Docker socket, privileged API, or in-host inference fallback.

- `OPENNEKO_EMBEDDING_IDLE_TIMEOUT` and `OPENNEKO_LIBRARIAN_IDLE_TIMEOUT` override
  Compose defaults (Go durations such as `5m` or `30s`). Both images also accept
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

The embedding service owns the unchanged `Xenova/all-MiniLM-L6-v2` q8 model,
mean pooling, normalization, and 384-dimensional vectors. The image build
vendors and verifies the model; runtime downloads are disabled. One inference
runs at a time, with a bounded queue. Model calls never happen in web, worker,
or chat sandboxes. Text search and ordinary database access need neither model.
The embedding service does not persist text, access tenant databases, or log
submitted text. Docling persists completed results as described below.

Compose exposes only internal service ports. Web and worker use
`NEKO_EMBEDDING_URL=http://embedding:5003`; the worker uses
`NEKO_LIBRARIAN_URL=http://librarian:5001`. For host development, configure those
URLs to reachable service addresses; `scripts/dev-web-stack.sh` supplies the
embedding address from the selected running stack. Missing embedding service
configuration fails explicitly, with the existing memory/library fallback
behavior retained for interactive search. Background indexing retries durably.

Memory and concept writes now clear their vector in the same database write.
The null vector is the durable backlog: a pg-boss sweep every minute dispatches
up to 32 jobs, with at most 128 outstanding jobs. Jobs contain row identifiers,
not document text. Dispatch interleaves organizations, and a PostgreSQL advisory
lock allows only one background embedding request across worker replicas.
Each job reads current content and writes its vector only if that content is
still current and eligible. Deleted, archived, expired, and suppressed memories
are skipped. Missing vectors remain discoverable after crashes and exhausted
retries. Keyword search is immediate; semantic indexing is eventual. Interactive query
embeddings have a 15-second deadline; durable jobs allow 150 seconds for cold
startup and inference.

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

Limits are ceilings, not reservations. Embeddings default to one CPU and 1 GiB.
Docling retains its existing CPU, memory, upload-size, and queue limits. Idle
shutdown saves unused memory; it does not guarantee a 16 GiB host can handle
all configured ceilings simultaneously. The ten-sandbox startup measurements
in `apps/worker/AGENT_STARTUP.md` do not cover concurrent heavy document work.

Verification:

```sh
(cd apps/lazy-service && go test -race ./...)
node --test apps/embedding/test.mjs
pnpm --filter @neko/llm exec vitest run test/embedding.test.ts test/library-extract.test.ts
docker build --target embedding -t openneko-embedding:lazy .
docker run --rm --network none --entrypoint node openneko-embedding:lazy /app/smoke.mjs
docker build --target librarian-test -f apps/librarian/Dockerfile .
docker build --target librarian -t openneko-librarian:lazy -f apps/librarian/Dockerfile .
docker run --rm --network none --entrypoint python3 openneko-librarian:lazy /app/smoke.py
```

The durable queue regression test uses a separate disposable PostgreSQL database
with pgvector (it creates minimal tables):

```sh
EMBEDDING_TEST_DATABASE_URL=postgres://USER@HOST/TEST_DATABASE pnpm --filter @neko/worker exec vitest run test/embedding-jobs.test.ts
```
