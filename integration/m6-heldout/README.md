# Frozen M6 held-out data

This separate, synthetic Postgres dataset is for real-model M6 qualification.
It is not the existing deterministic calibration model. The case prompts in
`cases.json` are fixed before any Gemini run, and the verifier and oracle stay
on the trusted host, outside the agent sandbox and model context.

The target day is **2026-09-15 UTC**. The three lead sources contain 1,016
distinct normalized email addresses on that day. Two hundred appear in more
than one source; 50 appear in all three. The seed includes mixed-case and
whitespace duplicates and rows immediately outside both day boundaries. The
artifact case requires one sorted, five-column CSV rather than a prose copy of
the rows.

Run the independent database and artifact-oracle gate with:

```sh
bash integration/m6-heldout/check.sh
```

It starts one memory-limited Postgres container, checks the frozen snapshot,
verifies an expected CSV, rejects a tampered CSV, and removes the container.
The frozen snapshot hash is
`6be7827bc6103868c95850c729db173dac43c76f717cc7f8ba2e4e01dbd30970`.
It covers the ordered lead oracle and reference label; changing either requires
a new dataset version. When a real run publishes a CSV, pass its downloaded
absolute path to `verify.py --artifact /path/to/result.csv` with libpq `PG*`
variables pointing to the same isolated dataset. Record the returned snapshot
hash in both fixed and canary `graphjin_environment` attestations.

To check the existing GraphJin image against this dataset without a model key:

```sh
OPENNEKO_TEST_SOURCE=/absolute/OpenNeko-feature-checkout \
  bash integration/m6-heldout/check-graphjin.sh
```

That gate starts only isolated Postgres and GraphJin containers, verifies all
three lead roots through GraphQL, and tears down its containers, network, and
volumes. The seed can also be selected in the larger OpenNeko integration stack
with `HARNESS_M6_BUSINESS_SEED=/absolute/path/to/seed.sql`; the default seed
and its existing calibration gates are unchanged.

Before each real fixed or canary run, use `attest.py` against GraphJin's
`/api/v1/agent/status` endpoint while `PG*` points at the held-out database.
Pass the approved non-secret provider, model and reasoning level explicitly.
The preflight requires a ready, read-only server-side agent and prints only
the effective profile, GraphJin's evaluation fingerprint and the frozen data
snapshot hash. A remote status URL must use HTTPS; if it requires a bearer
token, pass the *name* of its environment variable with `--token-env`. Save
the output separately for fixed and canary, and copy its
`graphjin_environment` fields into the paired manifest. The comparator rejects
a changed fingerprint or snapshot. These checks do not send a model request.

These gates establish data and artifact acceptance only. The M6 exit still
requires queued real-Gemini fixed and canary runs, a server-owned strong
GraphJin model/reasoning profile verified from the deployment, independent
outcome labels, and cost/latency/quality comparison. The short, misleading
short, investigation, and artifact tasks must all be run against the same
dataset snapshot and approved routes. Do not tune budget thresholds on these
held-out outcomes before recording the initial comparison.

## Queued real-model gate

The optional `HARNESS_M6_HELDOUT_ONLY=1` consumer gate now submits every frozen
case once in fixed mode and once in canary mode through the OpenNeko workflow
API queue. It runs only against the isolated OpenNeko worktree and disposable
metadata/business databases. The Go Harness, Ax routing, OpenShell 0.1.2
broker, and GraphJin server agent all participate. Unlike the calibration
fixture, this path has **not yet been run with real provider credentials**.
Do not treat a recorded receipt as a verified outcome.
The broker profile preflight can be rerun without real keys:
`HARNESS_M5_FAST=1 OPENSHELL_TEST_CLI=/absolute/openshell-0.1.2
./integration/run.sh integration/m6-heldout/profile-check.sh`.
For a full no-key queue smoke, set `HARNESS_M5_FAST=1`,
`HARNESS_M6_HELDOUT_SMOKE_ONLY=1`, `GRAPHJIN_AGENT_REASONING=high`, the frozen
absolute `HARNESS_M6_BUSINESS_SEED`, `OPENNEKO_TEST_SOURCE`, and the matched
`OPENSHELL_TEST_CLI`, then run `./integration/openneko/run.sh`. It uses the
deterministic short-finding model and asserts the API, checkpoint, triage and
GraphJin attestation path. Its answer is deliberately a fixture answer and
must never be counted as held-out quality evidence.

Set `HARNESS_M6_HELDOUT_SMOKE_ALL=1` for the full no-key plumbing gate. It
submits all four cases in fixed and canary modes through the queued API,
checks each checkpoint and GraphJin attestation, and runs the offline
comparator on all four pairs. It resets the model fixture between runs so its
response sequence is independent for each case. The fixture intentionally
answers every case incorrectly; the generated reviews are verified failures,
and `build-manifest.py --source synthetic` marks the manifest accordingly.
The builder requires an explicit source, checks that every receipt records the
same source, and refuses known fixture model profiles for a live manifest.
The connected eight-run gate passed with a matched OpenShell 0.1.2 tuple on
2026-10-02. `integration/run.sh` rejects a CLI whose reported version differs
from the selected gateway version.

Supply these settings in the invocation environment; the secret values must
stay out of command arguments, route JSON, and Git:

| Setting | Purpose |
| --- | --- |
| `HARNESS_M6_MODEL_SOURCE_KEY` | Real chat-model key, consumed by OpenShell `provider create --credential` through environment lookup. |
| `HARNESS_M6_TRIAGE_SOURCE_KEY` | Separate Ax Typesafe `SystemOne` classifier key; Gemini alone does not exercise this classifier. |
| `GRAPHJIN_AGENT_API_KEY` | Server-side GraphJin agent key, passed only to the isolated GraphJin container. |
| `HARNESS_M6_MODEL_URL`, `HARNESS_M6_MODEL_NAME` | HTTPS OpenAI-compatible model base URL and model code. |
| `HARNESS_M6_TRIAGE_URL`, `HARNESS_M6_TRIAGE_MODEL` | HTTPS Typesafe base URL and classifier model code. Use `https://api.typesafe.ai` for the native service; Ax appends `/v1/systemone`. Do not include that operation path in the base URL. |
| `GRAPHJIN_AGENT_PROVIDER`, `GRAPHJIN_AGENT_MODEL`, `GRAPHJIN_AGENT_REASONING`, `GRAPHJIN_AGENT_BASE_URL` | Approved strong, server-owned GraphJin profile. Use a real endpoint, not the fixture model. |
| `HARNESS_M6_MODEL_INPUT_PRICE`, `HARNESS_M6_MODEL_OUTPUT_PRICE`, `HARNESS_M6_TRIAGE_INPUT_PRICE`, `HARNESS_M6_TRIAGE_OUTPUT_PRICE`, `HARNESS_M6_GRAPHJIN_INPUT_PRICE`, `HARNESS_M6_GRAPHJIN_OUTPUT_PRICE` | Positive integer microcurrency per million tokens for admission and comparison. Pin the same values across all eight runs. |
| `HARNESS_M6_OUTPUT_DIR` | New absolute directory outside the temporary Harness state; it retains local receipts and downloaded artifacts. |

Set `GRAPHJIN_AGENT_MAX_STEPS` and `GRAPHJIN_AGENT_TIMEOUT_SECONDS` for the
approved server agent (for example, 16 and 180); the synthetic defaults remain
6 and 30. Set `HARNESS_M6_BUSINESS_SEED` to the absolute path of this
directory's `seed.sql`. With those settings and `OPENNEKO_TEST_SOURCE` pointing
at the harness worktree, run:

```sh
HARNESS_M5_FAST=1 HARNESS_M6_HELDOUT_ONLY=1 \
  OPENSHELL_TEST_CLI=/absolute/openshell-0.1.2 \
  ./integration/openneko/run.sh
```

The gate refuses missing settings and an existing output directory before
building images or starting Docker. It also refuses non-HTTPS
route URLs, or a GraphJin status whose effective profile differs from the
approved one. Before **each** admission, it checks the database snapshot and
GraphJin evaluation fingerprint. It writes one mode/case receipt with final
checkpoint hash, status, wall time, attestation, answer for local review, and
any API-retrieved CSV. The `agent-home` checkpoint tree also stays under this
output directory so the offline comparator can reopen every run. The route
generator writes no keys; OpenShell replaces the credential at the broker.
The temporary containers and gateway are removed on exit. Keep the output
directory for independent review.

The CSV case is checked against `verify.py` while the held-out database is
still up; its receipt carries `artifact_verified: true` only when the full
database-derived CSV matches. Review fact answers against the independent
oracle and inspect investigation citations; label failures as
failures, including completed runs with wrong answers or missing artifacts.
Record the review separately from the run receipts. The review JSON has
`{"version":1,"cases":{"reference-short-001":{"task_class":"short",
"runs":{"fixed":{"reviewed":true,"outcome":"verified_success"},
"canary":{"reviewed":true,"outcome":"verified_success"}}},...}}`;
include all four case IDs and both modes. The `task_class` is the independently
reviewed work class, not the classifier's suggestion. Use `verified_failure` or
`unverified` when appropriate. Build a manifest only after that review:

```sh
python3 integration/m6-heldout/build-manifest.py \
  --receipts /absolute/heldout-output --review /absolute/review.json \
  --source live \
  --output /absolute/heldout-manifest.json
go run ./cmd/harness-budget-compare < /absolute/heldout-manifest.json
```

The builder rehashes each persisted checkpoint, requires all eight reviews,
rejects a success without a completed run or a verified CSV, and rejects a
GraphJin profile/snapshot mismatch across the eight runs. Inspect the comparison report
as described in
[`docs/M6-BUDGET-EVAL.md`](../../docs/M6-BUDGET-EVAL.md). The fixed and canary
routes, dataset fingerprint, tool grants, and hard caps must match. No canary
rollout follows automatically from this gate.
