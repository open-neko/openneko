# Contributing OpenNeko evals

Eval configurations, dataset packs, cases, schemas, and compact results use the
same fork/branch/pull-request workflow as code.

## Run an eval from a pull request

From the repository root, install dependencies and provision the dataset named
by the submitted config. General AdventureWorks configs may use the standard
demo stack:

```sh
pnpm install
pnpm dev:setup
pnpm openneko eval validate --config evals/configs/<config>.yaml
pnpm openneko eval plan --config evals/configs/<config>.yaml --json
```

The OpenNeko backend benchmark is the exception. Use its dedicated frozen
runner; never start the product demo stack for that benchmark because the demo
may backfill AdventureWorks dates:

```sh
pnpm eval:backend --smoke-v4 --no-promote
```

This provider-free v4 control restores and fingerprints the immutable seed and
cannot inherit the simulator or scenario injector.

Read the plan before spending provider budget. It is the resolved statement of
which dataset, cases, variants, repetitions, models, and data paths will run.
Set only the environment variables referenced by `credential_ref` and the
dataset connection manifest, then begin with an unpromoted run:

```sh
export GEMINI_API_KEY=... # example; use the credential_ref named by the config
pnpm openneko eval run --config evals/configs/<config>.yaml --no-promote
```

An interrupted compatible run resumes automatically. Once the local run is
credible, run it with the config's normal promotion policy (or `--promote`),
verify the sanitized result directory, and include that directory in the PR.

## Add your own eval

Prefer extending an existing dataset and adapter. A normal contribution adds
or reuses these versioned files:

1. `evals/datasets/<dataset>/dataset.yaml` describes fixture identity,
   capabilities, snapshots, host-only oracle connection references, and the
   agent-visible connection. Keep cases and hidden oracle files beside it.
2. `evals/datasets/<dataset>/cases/<case>.yaml` declares stable semantic IDs,
   product path, input, oracle reference, deterministic assertions, gates, and
   side-effect constraints. A `metric` case input is deliberately limited to
   the natural user `question` and executive `role`:

   ```yaml
   input:
     role: CFO
     question: How many sales orders did we receive over the latest 12 months, compared with the preceding 12 months?
   ```

   Do not provide `slug`, `title`, `why`, `chart_hint`, table names, column
   names, formulas, anchor dates, or other answer-plan metadata. The production
   classifier must derive the card metadata, and the metric agent must discover
   the calculation. Put exact computation rules and expected values only in the
   host-side oracle and assertions.
3. `evals/suites/<suite>.yaml` selects cases and defines aggregate quality and
   safety gates. V4-style suites should reference a versioned threshold policy,
   attribute individual assertions to declared capabilities, and give each gate
   an owner, rationale, enforcement, severity, evidence minimum, and calibration
   history. Model-backed suites should require
   `min_token_usage_coverage: 1`; do not require dollar cost because local and
   self-hosted models may not have a meaningful USD price.
4. `evals/configs/<config>.yaml` selects the suite, dataset snapshot, backend,
   provider/model, direct or delegated GraphJin path, repetitions, budgets,
   pricing catalog, and result policy. A catalog entry enables an estimate; an
   unknown or local model remains valid with cost marked unavailable.

Copy the nearest existing example, validate it against `evals/schemas/`, and
inspect the expanded plan. Add a new adapter only when the contribution needs a
new product execution boundary or oracle/scorer kind. New product behavior must
also be represented in `evals/SEMANTICS.md` and the generated semantic registry.

## Before running a model

```sh
pnpm openneko eval validate --config evals/configs/<config>.yaml
pnpm openneko eval plan --config evals/configs/<config>.yaml --json
```

When authoring or reviewing oracles, resolve every case's ground truth
against the host-only oracle connection without any provider traffic:

```sh
pnpm openneko eval oracles --config evals/configs/<config>.yaml
pnpm openneko eval oracles --config evals/configs/<config>.yaml --out /tmp/oracles.json
```

Values are anchored to the live snapshot, so treat the output as an authoring
sanity check, never as a stored answer key — the runner re-resolves oracles at
run time (see `evals/GROUND-TRUTH.md`).

Review the expanded providers, models, data paths, case count, repetitions, and
budget. Credentials must be `env:NAME` references; literal secrets are rejected.
Dataset oracle connections are host-only and must not be exposed to the agent.
For metric cases, review `input.question` as the text a real user would submit;
classifier-generated `why` is output of the system under test, never case input.

## Run, interrupt, and resume

```sh
pnpm openneko eval run --config evals/configs/<config>.yaml
```

The runner auto-resumes the newest compatible incomplete run. Completed episode
files are integrity-checked and reused. An interrupted read is retried; an
interrupted watcher or mutation requires the adapter's reset hook before it is
replayed. Use `--restart` only when you deliberately want a new run.
Use `--no-promote` for a local verification run that should remain entirely
under `.openneko/evals/`; use `--promote` to override a config that normally
does not create a reviewable result.

Use `rescore` to apply scorer changes to stored output without spending model
calls:

```sh
pnpm openneko eval rescore --config evals/configs/<config>.yaml --run <run-id>
```

## Submit a result

Only the declared sanitized files in the promoted result directory belong in a
PR. Legacy results contain four files; v4 results add `technical.md`:

```text
evals/results/<config-id>/<run-id>/
  manifest.json
  results.jsonl
  summary.json
  summary.md
  technical.md  # v4 only
```

Run the verifier before committing:

```sh
pnpm openneko eval verify --result evals/results/<config-id>/<run-id>
pnpm --filter @neko/evals repo:check
```

It checks file digests, schema versions, expected slot coverage, duplicate
slots, secret-shaped values, absence of raw outputs/observations, and recomputes
the aggregate summary from `results.jsonl`, including macro/micro quality,
dataset/product-path groups, latency percentiles, token and cost totals, and
explicit measurement coverage. For v4 it also verifies the threshold-policy
digest, recomputes independent qualification and assertion-level capability
coverage, and re-renders both Markdown reports byte-for-byte. Raw episodes,
prompts, tool data,
oracles, and traces remain under ignored `.openneko/evals/` paths.
Missing token totals fail suites that declare the token-coverage gate. Missing
pricing does not fail verification and is rendered as `unavailable`, while any
reported estimated and provider-billed costs remain separate.
The PR workflow regenerates the semantic registry and JSON Schemas, validates
every configuration, checks that every semantic marked `eval` has configured
coverage, and verifies every checked-in result including its deterministic suite
gate classification. Accepted and rejected runs may both be submitted as
append-only evidence. A rejected run must retain the generated
`"accepted": false`; publishing it does not make it a passing qualification.

In the PR description, state whether the result is self-reported or CI-attested,
how the dataset was provisioned, any environment failures, and any capability or
safety gates that failed. Never add a new result by editing an older result
directory.

## Add coverage

- Add stable semantic IDs to `evals/SEMANTICS.md`, then run
  `pnpm --filter @neko/evals semantics:generate`.
- When adding or removing an `AgentEvent`, worker queue, or harness observation
  kind, update `evals/semantic-inventory.yaml`; CI requires an exact mapping to
  one or more semantic IDs.
- Add dataset-independent behavior tags and dataset-specific hidden oracles.
- Prefer deterministic assertions. A rubric judge is only for qualities a
  deterministic oracle cannot express.
- A watcher or mutation case must define safe reset, pre-state, readiness,
  post-state, collateral, audit, cleanup, and idempotency checks.
- Regenerate JSON Schemas with `pnpm --filter @neko/evals schemas:generate`.
- Add every reported failure to the curated regression corpus; use generators
  for breadth rather than copying many near-identical easy reads.
