# M6 budget evaluation

The shadow classifier and its extensions do not change a live run's hard
limits. `harness-budget-eval` scores stopped, validated checkpoints against
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

Before a canary, collect held-out short, investigation, and Daily Lead-style
artifact runs with the same model and task fixtures under the fixed budget.
Include misleading short prompts, uncertain/failed classifier calls, missing
usage, crash/resume, and extension cases. Report false-lows and premature
budget failures per class, verified completion, token/cost coverage, latency,
and classifier overhead separately for calibration and held-out splits.
Confirm that a GraphJin intent and preflight extension are both journaled
before the remote reservation when a candidate cap is too small.
Then compare a
small, switchable dynamic-budget canary against fixed-budget runs using cost
per verified success and false-low rates. This offline evaluator deliberately
never marks a canary ready or changes admission.
