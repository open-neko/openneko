# M2 — real OpenShell transport qualification

The standalone suite qualifies OpenShell transport and credential lifecycle.
The optional M3 consumer suite also covers the real broker and queue worker.

Prerequisites: local Docker with host-shared home paths, Go, Bash, OpenSSL, Python 3, and a separately
installed, checksum-verified OpenShell **0.0.116** CLI. Do not upgrade the active
CLI/gateway to run this check. The Docker build installs packages from Debian;
the fixture and sandbox contain no real provider credentials.

From the standalone Harness root:

```sh
OPENSHELL_TEST_CLI=/absolute/path/to/openshell-0.0.116 ./integration/run.sh
```

The runner cross-compiles the probe for the Docker daemon architecture and creates
an isolated gateway, mTLS PKI, fixture service and actual sandbox. It reserves
`127.0.0.1:18116` and network `harness-m2` (`172.30.116.0/24`). It refuses
to reuse an existing test network. Do not run concurrent instances. Its exit trap
deletes test sandboxes and Compose services/network; temporary state is removed
only after network cleanup succeeds. No active OpenNeko services are restarted.
The probe image and downloaded OpenShell images remain cached locally.

## Verified locally, 2026-09-19

- Platform: macOS arm64, OrbStack Docker 29.4.0; OpenShell CLI/gateway/supervisor 0.0.116.
- Gateway image manifest digest: `sha256:05cf77bbb022a739aed6f22daa0e7e164415f4ab273b5f84319e46d91eb8f645`.
- Supervisor digest reported by this gateway: `sha256:44619ddcbf2786261066031bd0b3a5e9a250269c7ec557053a72e45229a8ca14`.
- mTLS lifecycle RPC succeeds using independent test certificates/state.
- Ax receives streamed HTTP model output through the sandbox proxy.
- The workload receives an opaque placeholder in credential slot `api_key`, aliased
  to `MODEL_API_KEY`; the fixture verifies the real synthetic Bearer credential.
- A path outside the credential binding gets HTTP 403, even though network policy
  allows that path. A DNS/transport error does not count as a passing denial test.
- `curl`, which is absent from the executable allowlist, also gets HTTP 403.
- Cleanup leaves no test containers or test network.

Baseline output before the HTTPS/cancellation extension:

```json
{"check":"credential_stream","ok":true,"events":1}
{"check":"destination_rejected","ok":true,"status":403}
{"check":"openshell_transport_suite","ok":true}
```

Optional consumer lifecycle checks live in [the OpenNeko adapter](../adapters/openneko/README.md).
Pass its integration script as the runner argument to include them; the default
suite runs independently of every consumer.

## Accepted upstream cancellation limitation (2026-09-20)

HTTPS interception and Go CA trust now pass against a fixture signed by an
isolated test CA. The CA is installed in the test image for upstream verification;
no verification is disabled. Removing the OpenShell interception CA from the Go
workload rejects the connection with an unknown-authority error.

**Observed upstream defect:** after an initial nonterminal SSE event, cancelling the
Go/Ax request returns locally within two seconds, but the HTTPS upstream does not
observe cancellation within ten seconds through OpenShell 0.0.116. The runner
previously exited nonzero and printed `upstream_stream_cancelled` with `ok:false`.
The user has accepted this as nonblocking: the runner now emits an explicit
`upstream_idle_cancellation` warning and reports the observed boolean in the suite
summary. Provider tokens may continue until upstream closure or sandbox teardown. A direct
HTTPS control using the same fixture observes cancellation successfully. The [source trace](../docs/OPENSHELL.md#source-trace-idle-response-cancellation)
confirms the relay awaits upstream reads without concurrently observing client
closure; inspected upstream main retains that behavior. Consumer checks run before the final upstream-cancellation observation, so that known
limitation does not prevent qualification of the remaining consumer path.

Rerun on 2026-09-20 completed with exit 0 on the pinned 0.0.116 tuple.
Credential lifecycle, gateway restart, OTLP delivery, HTTPS trust/denial and local
cancellation passed. The final summary reported
`upstream_idle_cancellation_observed:false` alongside the accepted warning;
`sandbox_delete_closes_upstream` passed. Owned containers and network were removed.
Local evidence: `/tmp/harness-m2-accepted-cancellation.log`.

## Additional live qualification, 2026-09-19

The expanded suite passed these checks on the same 0.0.116 tuple:

- Query-parameter placeholder replacement through Go HTTP; Ax uses Bearer auth.
- Static credential rotation while a sandbox remains live.
- Two distinct credential slots and endpoint bindings prebound to one sandbox.
- Provider detach rejects a previously saved placeholder with HTTP 403, then
  reattach restores access. Detach propagation measured about ten seconds; it is
  **not instantaneous revocation**. Rotation/reattach checks poll boundedly too.
- An expired managed credential is renewed by the gateway through a synthetic
  OAuth client-credentials endpoint. The profile owns the endpoint and refresh
  material stays gateway-side. The pinned refresh worker ticks every 60 seconds,
  so this test allows up to 75 bounded polls. Pending expiry is not a reason to
  inject a real token into a workload.
- Gateway restart recovers the live sandbox, its saved filesystem marker and
  model access after sandbox readiness returns.
- A pinned OpenTelemetry Collector receives actual gateway and Docker-driver
  spans with trace/span IDs. The check scans exported data for synthetic keys,
  refresh secrets and credential placeholders. One standalone run exported 1,081
  spans from `harness-m2-gateway` and `openshell-driver-docker`, with no scanned
  credential material found.

The collector is isolated at `172.30.116.4`, with no published collector port.
Synthetic OAuth listens only in the test gateway's loopback network namespace.
The image is pinned by digest in `compose.yml`; collector evidence is inspected
before cleanup and only content-free counts/names enter the test log.

The cumulative consumer suite also passes real Hermes cold execution and two
warm executions reusing one sandbox. OpenShell 0.0.116 limits sandbox names to
19 bytes; the shared launcher now bounds long names while preserving short ones.
A separate cancellation check proves deleting the sandbox closes the idle upstream
stream. This required cleanup check remains fatal on failure; it does not claim
context-only cancellation passed.

Deferred: the upstream idle-stream fix, Linux/hosted CI qualification, and credential
strategies beyond the tested static and OAuth client-credentials flows. Adopt the
upstream fix when available; no OpenShell fork is required. No active installation
was upgraded. Current upstream main was rechecked at
`fa0bfa490e42c87a74a70be6ebb40faee7fb8faa`; its ordinary response path still accepts
only an `AsyncWrite` downstream and cannot concurrently observe its EOF.
