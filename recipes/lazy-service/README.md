# Lazy service listener

The EmbeddingGemma 2 recipe keeps a small Go HTTP listener running. The model
process starts on the first processing request and exits after five idle
minutes. The container stays up; the model process and its heap do not. This
uses no Docker socket, privileged API, or in-host inference fallback.

- `OPENNEKO_SERVICE_IDLE_TIMEOUT` sets the idle time (a Go duration such as
  `5m` or `30s`).
- Concurrent callers share one startup. The first request waits for child
  readiness; failed startup returns 503 and a later request may retry.
- Health checks report listener availability plus `sleeping`, `starting`, or
  `running`. They never start a child or extend its idle timer.
- Shutdown requires no active HTTP requests and a successful backend idle
  check. An unavailable idle check prevents eviction.
- Child crashes fail affected requests. The next request starts a new child;
  requests are not silently replayed.

The recipe is described in [recipes/embedding-gemma](../embedding-gemma/README.md).

Limits are ceilings, not reservations. Idle shutdown saves unused memory; it
does not guarantee a host can handle all configured ceilings at once.

Verification:

```sh
(cd recipes/lazy-service && go test -race ./...)
```
