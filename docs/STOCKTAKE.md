# Harness stocktake

Reviewed 2026-09-20. This replaces the earlier pre-M3 inventory.

## Implemented and verified locally

The standalone Go runtime uses pinned Ax and has no dependency on an OpenNeko
checkout. Its optional OpenNeko adapter provides server-side GraphJin lookup and
governed action proposals; Hermes remains the default backend.

| Area | Completed scope | Evidence |
| --- | --- | --- |
| M1 execution | Ax streaming, governed callbacks, tool pairing, cancellation, bounded actor work and run-scoped telemetry | Go race tests, vet and HTTP/Goja compatibility tests |
| M2 transport | Real OpenShell 0.0.116, TLS/CA trust, credential replacement, destination/binary restrictions, rotation/detach, two slots, OAuth refresh, gateway restart and OTLP delivery | [Transport record](../integration/README.md) |
| M3 integration | Browser and queue entrypoints, scoped GraphJin lookup, accepted-input deduplication, result/error projection, cancellation and Hermes regression coverage | [Consumer acceptance](../integration/m3/README.md) |
| M4 recovery | Host/session ownership, durable operation receipts, bounded continuation, sandbox/host terminal reconciliation, worker-death redelivery and one restored answer | [Recovery record](M4-RECOVERY.md) |
| M4 effects | Frozen human approvals, fresh authorization before dispatch, durable execution claims, optional provider-status recovery and explicit unknown outcomes without redispatch | Real process-kill, queue, controlled HTTP effect and browser approval/reload checks in the recovery record |

The cumulative live suite passes the consumer checks but deliberately exits 1 at
the remaining raw proxy cancellation gate. Successful consumer checks do not make
the overall suite green. The actual Harness cancellation path deletes its sandbox,
which closes the upstream request; cleanup failure is reported explicitly.

## Remaining release blocker

OpenShell's idle HTTP response relay does not promptly notice downstream
disconnect. The direct HTTPS control cancels; the proxied request remains open
beyond the ten-second observation window. See the exact
[source trace](OPENSHELL.md#source-trace-idle-response-cancellation).

GitHub's latest-release API was checked on 2026-09-20 and still reports
[v0.0.116](https://github.com/NVIDIA/OpenShell/releases/tag/v0.0.116), published
2026-08-28. There is no newer stable release to qualify. The previously inspected
main revision also retained this response path; that is source evidence only.

Closing M2 requires an OpenShell relay fix and a passing rerun of the existing
integration gate. It must preserve HTTP buffering/pipelining and define half-close
behavior: reading EOF alone cannot distinguish a TCP write-half-close from a
client abandoning its response. No speculative OpenShell fork, timeout workaround,
waived test or active gateway upgrade is included in this delivery.

## Deferred work

- M5: local file/process tools, freshness checks and artifact delivery.
- M6: approved model routing/fallback, context compaction and aggregate budgets.
- M7: task-quality evaluation, concurrent tenant/load checks and operational limits.
- M8: deployment upgrade, canary and rollback qualification.
- Hosted PR/CI checks and a separately budgeted live-provider smoke test. Current
  model/effect fixtures are deterministic; they establish correctness, not quality.

Recovery is bounded to persisted evidence and new Ax attempts, not arbitrary VM
resumption. Production adapters need explicit read-only status implementations
before ambiguous effects can reconcile automatically. Unknown effects, unfinished
approvals and unreconciled sandboxes are retained for operator resolution; no
purge is enabled. Queue death tests expire after the original sandbox finishes;
overlap refusal is separately tested at the live launcher boundary.

## Repository and delivery state

Both repositories use `feat/openneko-harness`. OpenNeko integration changes are in
`../Open-Neko/OpenNeko-harness-m3`, committed through `88992f7`. The original
OpenNeko checkout was not changed by this delivery. Harness implementation and
acceptance records are committed through `2fd91e0`, followed by this inventory
update. Nothing has been pushed; Harness has no configured remote. No PR or main
merge has been made. Earlier main commits documented in README are historical.

All owned M2/M3 test services and volumes were cleaned up. No additional cumulative
run is needed for this documentation-only update; the final retained acceptance
commands, logs and correlated run/request IDs are in the linked records.
