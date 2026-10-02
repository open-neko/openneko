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

These gates establish data and artifact acceptance only. The M6 exit still
requires queued real-Gemini fixed and canary runs, a server-owned strong
GraphJin model/reasoning profile verified from the deployment, independent
outcome labels, and cost/latency/quality comparison. The short, misleading
short, investigation, and artifact tasks must all be run against the same
dataset snapshot and approved routes. Do not tune budget thresholds on these
held-out outcomes before recording the initial comparison.
