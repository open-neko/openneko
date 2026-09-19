# M2 — real OpenShell transport qualification

This is the first part of M2, not its worker/queue acceptance gate.

Prerequisites: local Docker with host-shared home paths, Go, Bash, and a separately
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

Successful final output:

```json
{"check":"credential_stream","ok":true,"events":1}
{"check":"destination_rejected","ok":true,"status":403}
{"check":"openshell_transport_suite","ok":true}
```

Optional consumer lifecycle checks live in [the OpenNeko adapter](../adapters/openneko/README.md).
Pass its integration script as the runner argument to include them; the default
suite runs independently of every consumer.

## Still required before M2 closes

The fixture currently serves plain HTTP, not intercepted HTTPS. Qualify Go CA
trust/TLS interception, query authentication, managed expiry/refresh, static-key
rotation/detach, two providers, SSE idle/cancellation and actual OTLP export.
The gateway emits structured trace/log metadata, but that is not proof of
collector delivery. Linux CI execution has not occurred.

Run the unchanged launcher through the external adapter in a real worker/queue
with the broker. Package existing entrypoint and executable-policy contracts in
the harness image without changing OpenNeko source. Re-test
the existing Hermes path. The web and seeded GraphJin flows enter in M3. These are
explicit remaining gates; this script does not substitute a probe for those services.
