# OpenNeko evaluation results

> Report schema: `openneko.eval.report.facts.md/v2`

| Metric | Observed |
| --- | ---: |
| Tasks passed | 59/59 (100.0%) |
| Tasks passed at least once | 59/59 |
| Tasks passed every repetition | 59/59 |
| Episodes completed | 59/59 |
| Execution failures | 0 |
| Ground truth | 100.0% |
| Method | 100.0% |
| Behavior | 100.0% |
| Safety | 98.3% |
| Safety check failures | 0 |
| Unsafe effects | 0 |
| Latency p50 / p95 | 173ms / 318ms |
| Total tool calls | 74 |
| Tool-call coverage | 100.0% |
| Total tokens | 0 |
| Token coverage | 100.0% |
| Estimated cost | unavailable |
| Cost coverage | 0.0% |

## Task families

| Family | Tasks passed | Ground truth | Method | Behavior | Safety |
| --- | ---: | ---: | ---: | ---: | ---: |
| mutation | 5/5 | 100.0% | 100.0% | 100.0% | 100.0% |
| read | 51/51 | 100.0% | 100.0% | 100.0% | 98.0% |
| resilience | 2/2 | 100.0% | 100.0% | 100.0% | 100.0% |
| watcher | 1/1 | 100.0% | 100.0% | 100.0% | 100.0% |

[Per-episode measurements](technical.md)

## Safety events

| Outcome | Count |
| --- | ---: |

## Provenance

| Field | Value |
| --- | --- |
| Run | run-20260905t051547480z-a6145dfb |
| Suite | openneko-backend-v3 |
| Attestation | self-reported |
| Source commit | c666b70f53fb54e947463296c33f2bb0c8e50323 |
| Uncommitted changes | no |
| Models | scripted-good / scripted:deterministic-v1 |
| Repetitions | 1 |
