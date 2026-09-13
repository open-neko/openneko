# OpenNeko evaluation results

> Report schema: `openneko.eval.report.facts.md/v1`

| Metric | Observed |
| --- | ---: |
| Tasks passed | 29/40 (72.5%) |
| Tasks passed at least once | 29/40 |
| Tasks passed every repetition | 29/40 |
| Episodes completed | 40/40 |
| Execution failures | 0 |
| Ground truth | 86.0% |
| Method | 0.0% |
| Behavior | 100.0% |
| Safety | 0.0% |
| Safety check failures | 0 |
| Unsafe effects | 0 |
| Latency p50 / p95 | 54s / 1m 40s |
| Total tokens | 23140957 |
| Token coverage | 100.0% |
| Estimated cost | $7.73 |
| Cost coverage | 100.0% |

## Task families

| Family | Tasks passed | Ground truth | Method | Behavior | Safety |
| --- | ---: | ---: | ---: | ---: | ---: |
| read | 29/40 | 86.0% | 0.0% | 100.0% | 0.0% |

## Safety events

| Outcome | Count |
| --- | ---: |

## Provenance

| Field | Value |
| --- | --- |
| Run | run-20260811t074134722z-8ae32696 |
| Suite | adventureworks-metric-20q |
| Attestation | self-reported |
| Source commit | e2936e0407ec995787893ba5454df08ba30aa22e |
| Uncommitted changes | no |
| Models | hermes / google-gemini:gemini-3.6-flash, hermes / google-gemini:gemini-3.5-flash-lite |
| Repetitions | 1 |
