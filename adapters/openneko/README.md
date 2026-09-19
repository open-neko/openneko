# OpenNeko adapter

Optional consumer integration. Core packages do not import this directory, and
building this adapter requires no OpenNeko checkout or application libraries.
The optional product change adds a Harness backend while preserving Hermes as the
default. See [M3 acceptance](../../integration/m3/README.md) for the concrete
launcher, broker, queue and browser checks. Build the product image with
`./adapters/openneko/build-image.sh /absolute/OpenNeko-checkout`.

## Implemented: OpenShell cold-launch compatibility

From the repository root:

```sh
go build -o bin/openshell ./adapters/openneko/cmd/openshell-compat
OPENSHELL_TEST_CLI=/absolute/path/to/openshell-0.0.116 ./integration/run.sh adapters/openneko/integration.sh
```

Set `HARNESS_OPENSHELL_BIN` to the absolute path of the real 0.0.116 CLI. A worker
packaging configuration can put this adapter on its PATH as `openshell`; nothing
here installs it globally or changes an active gateway. The gateway must use the
qualified matching release. This adapter is not needed by standalone Harness.

The exact legacy cold-create `/bin/sh -lc true` command becomes detached creation
with a live main process, followed by uploads. Failed uploads delete the created
sandbox. Other commands pass through. Failed creation remains the caller's
responsibility. The live suite checks the original failure, staged bytes, exec,
deletion and cleanup after a missing upload source.

## Original integration contract (M3 implementation described above)

Translate the existing launch/job format into the harness run specification,
project neutral events/results into `__openneko_event__` and
`__openneko_agent_result__`, and propagate cancellation. Bind GraphJin through the
existing server-side agent broker capability; caller identity and approvals remain
OpenNeko-owned. Map neutral observations into product telemetry without double
counting model or delegated usage.

These bindings are design requirements, not implemented features. Existing
entrypoint, binary egress policy and Hermes warm lifecycle still require image and
packaging qualification. Real worker/queue/broker/web gates remain open. If a
contract cannot be met externally, document the specific blocker before proposing
an OpenNeko change; do not silently couple the core to product internals.
