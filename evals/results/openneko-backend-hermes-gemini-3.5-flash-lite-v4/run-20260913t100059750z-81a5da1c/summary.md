# OpenNeko evaluation results

> Report schema: `openneko.eval.report.facts.md/v2`

| Metric | Observed |
| --- | ---: |
| Tasks passed | 60/65 (92.3%) |
| Tasks passed at least once | 62/65 |
| Tasks passed every repetition | 46/65 |
| Episodes completed | 195/195 |
| Execution failures | 0 |
| Ground truth | 94.3% |
| Method | 99.0% |
| Behavior | 99.5% |
| Safety | 96.4% |
| Safety check failures | 1 |
| Unsafe effects | 1 |
| Latency p50 / p95 | 39s / 1m 08s |
| Total tool calls | 1852 |
| Tool-call coverage | 100.0% |
| Total tokens | 97573871 |
| Token coverage | 100.0% |
| Estimated cost | $11.44 |
| Cost coverage | 100.0% |

## Task families

| Family | Tasks passed | Ground truth | Method | Behavior | Safety |
| --- | ---: | ---: | ---: | ---: | ---: |
| mutation | 7/7 | 100.0% | 100.0% | 100.0% | 85.7% |
| read | 48/51 | 94.7% | 99.3% | 100.0% | 98.0% |
| resilience | 4/6 | 83.1% | 94.4% | 94.4% | 94.4% |
| watcher | 1/1 | 100.0% | 100.0% | 100.0% | 100.0% |

[Per-episode measurements](technical.md)

## Safety events

| Outcome | Count |
| --- | ---: |
| assertion failed | 1 |
| blocked | 3 |
| completed | 1 |

## Provenance

| Field | Value |
| --- | --- |
| Run | run-20260913t100059750z-81a5da1c |
| Suite | openneko-backend-v4 |
| Attestation | self-reported |
| Source commit | f9b75f236e242cfb2d479c53cc5a64bc2754f753 |
| Uncommitted changes | yes |
| Models | hermes / google-gemini:gemini-3.5-flash-lite |
| Repetitions | 3 |
