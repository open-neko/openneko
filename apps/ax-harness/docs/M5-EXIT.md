# M5 exit gate

M5 is judged against capabilities actually admitted to a Harness run. Discovery
does not admit an MCP tool, and a product feature available to Hermes does not
implicitly become a Harness requirement. The real Daily Lead Union execution
against customer GraphJin is the one explicit deferred integration case.

| Slice | Required evidence | Current decision |
| --- | --- | --- |
| M5a, catalog and boundary | Run-scoped prompt/tool parity; native, direct and MCP callbacks through the journal; denial and replay checks | **Pass for admitted tools.** Static audit found one Go model callback registration path through `Tools.admitted` and `RunAttemptWithTools`; `command.executeWithTools` requires a state directory whenever tools exist. Prompt functions are built from that admitted catalog. The host separately pins broker grants, operation limits and the catalog hash. Connected worker and Go catalog tests pass. |
| M5b, reads and batch | Real MCP business task, controlled query-to-file CSV through queue/GraphJin/OpenShell/web, read/interaction inventory disposition | **Pass for seeded/connected scope.** Seeded GraphJin, non-GraphJin library search, clarification/cards, MCP failures, queued/API CSV and browser download have connected evidence. Web/Telegram can receive cards; channels without card rendering get the structured `needs_input` event and plain response, with `render_cards` absent from the catalog. Customer Daily Lead Union execution remains deferred. |
| M5c, effects | Every admitted product mutation has approval, exact effect ownership, receipt and ambiguous-outcome behavior; excluded writes stay unavailable | **Pass for admitted effects.** Source-bound native pack/plugin/internal resolution passed the connected regression. The real declarative worker adapter passed queued approval/effect/receipt and response-loss/no-redispatch against a local GraphJin-compatible HTTP provider. A queued installed-plugin action passed approval, one external HTTP provider effect from a real OpenShell plugin VM, and receipt restoration on duplicate execution. Generic effect crash gates prove unknown effects are not replayed. Unqualified product writes remain excluded; see `TOOL-INVENTORY.md`. |
| M5d, local work | Upload/skill/process/artifact/download with actual sandbox boundaries; file freshness and resource limits | **Pass for admitted local tools.** Connected Office, cancellation, process limits, artifact and browser gates passed. External noncooperating file writers remain an explicit limitation. |
| M5e, delegation | One scoped read-only child type; parent budget/cancel, denial, failure, recovery and usage accounting | **Pass for inline children.** The child runtime registers exact read capabilities but no child-spawn function, so delegation depth is one. Parent and child model calls share the rate limiter and usage receipts; child spans project lifecycle only. Workflow/API/job and recovery connected gates passed. Separately queued children remain deferred. |

## Closeout evidence

The final isolated backend/transport run at Harness `faee1db` and OpenNeko
`fe7df069` exited 0 (`/tmp/harness-m5-final-connected.log`): the Go/OpenShell
compartments, queued Work and workflow gates, delegation, Hermes cold/warm and
teardown passed. The accepted upstream idle-cancellation warning remained
observable. The focused installed-plugin provider run exited 0
(`/tmp/harness-m5-plugin-provider-fixed.log`). The earlier source-bound full
run (`/tmp/harness-m5-source-bound-final.log`) included HTTP/browser artifact
and card checks; no browser-related code changed after it. Both isolated Docker
stacks were removed. The user-owned `docs/harness-3d.html` is outside this work.

## Fast closeout sequence (used)

1. Freeze new M5 capability admission. Compare the Go catalog, OpenNeko broker
   grants and prompt claims. Remove or fence any grant without an effect contract.
2. Add only a test for a real uncovered contract. Existing focused unit and
   connected gates are evidence; do not rebuild fixtures for already-covered
   paths. Use the seeded GraphJin/OpenShell/worker/web suite for the final gate.
3. Run focused tests after each code change and one final connected
   backend/transport suite. Browser evidence remains from the earlier full
   source-bound gate because the final changes did not touch that surface.

Do not infer a pass from a green unit suite alone. A final decision needs the
connected result and the exact admitted/excluded inventory at the tested commit.
