# M6 budget evaluation

The default shadow classifier and its extensions do not change a live run's
admission limits. An experimental host-only `HARNESS_BUDGET_MODE=canary` makes
the proposed profile an additional admission ceiling; the original hard caps
still apply. The mode is pinned in the checkpoint, so it cannot be changed for
an in-flight run or during recovery. If classification is skipped or uncertain,
the fixed hard caps remain in force. A candidate denial returns
`dynamic_budget_exceeded` before dispatching another model or GraphJin request.
The exported budget trace identifies fixed versus canary mode. The
counterfactual evaluator refuses canary traces because they cannot serve as
fixed-budget controls. `harness-budget-eval` scores stopped, validated checkpoints against
independently verified outcomes. It exports only budget events, route/model
metadata, usage, prices, and the classifier's content-free distribution. It
does not export prompts, tool arguments/results, runtime state, or answers.

For each case, a reviewer records the actual work required to reach the
verified outcome: `short`, `investigation`, or `artifact`. The reviewer must
inspect the artifact or evidence receipt, not infer complexity from prompt
length, model text, or the classifier result. Mark `verified_success` only
after the task-specific acceptance check passes; otherwise use
`verified_failure` or `unverified`. Keep calibration and held-out cases
separate before adjusting thresholds or budget profiles.

The manifest is a local JSON file with no task content:

```json
{
  "version": 1,
  "cases": [
    {
      "id": "artifact-001",
      "root": "/absolute/trusted-run-directory/.harness",
      "run_id": "accepted-run-id",
      "checkpoint_sha256": "64-lowercase-hex-characters-from-the-final-checkpoint",
      "split": "held_out",
      "source": "live",
      "label": {"task_class": "artifact", "outcome": "verified_success", "wall_ms": 10000}
    }
  ]
}
```

The checkpoint filename is SHA-256 of the run ID plus `.json`. After outcome
verification, hash that final file with `shasum -a 256` and record the digest
in the manifest. The evaluator refuses a checkpoint that changes after
labelling, a running session, an invalid journal, a repeated case/run ID, or a
claimed success whose run did not complete. Run:

```sh
go run ./cmd/harness-budget-eval < /absolute/path/to/manifest.json > /absolute/path/to/report.json
```

The report gives the first model or GraphJin admission the proposed profile
would have blocked, classification false-lows by task class, conservative
token/cost accounting, usage coverage, classifier overhead, and fixed-run
cost per verified success. A failed counterfactual means a verified fixed run
would have been interrupted by the proposed profile; an extension after that
point cannot rescue it. The replay uses the actual fixed-run route reservation
as a conservative cost bound. It cannot predict changed model behavior or
actual savings from a smaller cap.

The canary mechanism is implemented but **not qualified for production**.
OpenNeko exposes it only when the operator sets both
`OPENNEKO_HARNESS_TRIAGE_SHADOW=1` and
`OPENNEKO_HARNESS_BUDGET_CANARY=1` for a workflow with a pinned cost ceiling.
Keep those settings stable until admitted canary runs have finished or been
reconciled; changing mode while a run is in flight intentionally fails its
checkpoint identity check. The default remains shadow mode.

Before enabling a canary, collect held-out short, investigation, and Daily Lead-style
artifact runs with the same model and task fixtures under the fixed budget.
Include misleading short prompts, uncertain/failed classifier calls, missing
usage, crash/resume, and extension cases. Report false-lows and premature
budget failures per class, verified completion, token/cost coverage, latency,
and classifier overhead separately for calibration and held-out splits.
Confirm that a GraphJin intent and preflight extension are both journaled
before the remote reservation when a candidate cap is too small.
Then compare a small, switchable dynamic-budget canary against fixed-budget runs using cost
per verified success and false-low rates. This offline evaluator deliberately
never marks a canary ready or changes admission.

Use `harness-budget-compare` for the paired comparison. Give each pair a shared
case ID, split (`calibration` or `held_out`), source (`synthetic` or `live`), and
independently verified task class (`short`, `investigation`, or `artifact`).
For each fixed and canary run, record its `.harness` directory, run ID, final
checkpoint SHA-256, independently verified outcome, and wall time in
milliseconds. Both runs must use the same task, data snapshot, approved model
routes, tool grants, and terminal acceptance check; the budget mode is the
experimental difference. A manifest has this shape:

```json
{
  "version": 1,
  "pairs": [{
    "id": "artifact-001", "split": "held_out", "source": "live",
    "task_class": "artifact",
    "fixed": {"root": "/absolute/fixed/.harness", "run_id": "fixed-run-id", "checkpoint_sha256": "64-lowercase-hex-characters", "outcome": "verified_success", "wall_ms": 10000},
    "canary": {"root": "/absolute/canary/.harness", "run_id": "canary-run-id", "checkpoint_sha256": "64-lowercase-hex-characters", "outcome": "verified_success", "wall_ms": 9000}
  }]
}
```

Run `go run ./cmd/harness-budget-compare < manifest.json > comparison.json`.
The command validates each stopped checkpoint, its digest, run identity and
fixed/canary mode. It also rejects pairs with different trusted route/price
manifests, admitted capability and gate definitions, or hard model-call,
token, cost or operation ceilings. The comparable catalog fingerprint excludes
the run's tenant scope; the separate scoped catalog remains authoritative for
resume. Older checkpoints without a comparable fingerprint cannot be paired.
This check does not establish that the two prompts, data snapshots or live
tenant entitlements were equivalent; the reviewer must still verify those
independently. It reports verified successes, canary regressions by task
class, actual charged cost per verified success, wall time and usage coverage
for calibration and held-out splits. A cheaper failed canary is counted as a
regression, not a saving. Pair equivalence and outcome labels still require
independent review; the command never enables the canary.

The OpenShell 0.1.2 calibration gate now exercises three synthetic paired API
workflows: a short finding, an investigation with a pending approval, and a
52,008-byte CSV artifact. All six finished with matching verified outcomes
and complete priced usage. A connected rerun served the public API over HTTP:
both artifact runs returned exact CSV bytes and attachment headers, both short
runs returned 404 for the file endpoint, and invalid bearer tokens returned
401. The first short policy (two model calls and 2,000
micros) failed before the finding could be recorded because the classifier
and Ax stages share the run allowance. The gate now uses a four-call,
5,000-micro short proposal within the same fixed hard cap. This is a fixture
calibration result only; retain shadow mode until the specified held-out live
evaluation and real canary comparison are complete.
