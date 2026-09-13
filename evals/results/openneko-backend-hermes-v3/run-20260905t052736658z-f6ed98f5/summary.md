# OpenNeko evaluation results

> Report schema: `openneko.eval.report.facts.md/v1`

| Metric | Observed |
| --- | ---: |
| Tasks passed | 53/59 (89.8%) |
| Tasks passed at least once | 58/59 |
| Tasks passed every repetition | 50/59 |
| Episodes completed | 166/177 |
| Execution failures | 11 |
| Ground truth | 91.8% |
| Method | 93.8% |
| Behavior | 93.8% |
| Safety | 91.9% |
| Safety check failures | 1 |
| Unsafe effects | 0 |
| Latency p50 / p95 | 1m 12s / 2m 18s |
| Total tokens | 128024009 |
| Token coverage | 93.8% |
| Estimated cost | $53.21 |
| Cost coverage | 93.8% |

## Task families

| Family | Tasks passed | Ground truth | Method | Behavior | Safety |
| --- | ---: | ---: | ---: | ---: | ---: |
| mutation | 5/5 | 100.0% | 100.0% | 100.0% | 100.0% |
| read | 46/51 | 91.2% | 93.5% | 93.5% | 91.5% |
| resilience | 1/2 | 83.3% | 83.3% | 83.3% | 79.2% |
| watcher | 1/1 | 100.0% | 100.0% | 100.0% | 100.0% |

## Safety events

| Outcome | Count |
| --- | ---: |

## Provenance

| Field | Value |
| --- | --- |
| Run | run-20260905t052736658z-f6ed98f5 |
| Suite | openneko-backend-v3 |
| Attestation | self-reported |
| Source commit | de7ff04272b18785186bed1003b698a4b5003004 |
| Uncommitted changes | no |
| Models | hermes / google-gemini:gemini-3.6-flash |
| Repetitions | 3 |
