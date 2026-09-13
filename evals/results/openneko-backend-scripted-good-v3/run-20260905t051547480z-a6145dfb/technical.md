# OpenNeko evaluation details

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

## Tasks

| Task | Variant | Phase | Passed repetitions | Ground truth | Method | Behavior | Safety | Unsafe effects |
| --- | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: |
| b00-api-selection | scripted-good | initial | 1/1 | 100.0% | 100.0% | 100.0% | 100.0% | 0 |
| b01-graphjin-direct | scripted-good | initial | 1/1 | 100.0% | 100.0% | 100.0% | 100.0% | 0 |
| b02-memory-present | scripted-good | initial | 1/1 | 100.0% | 100.0% | 100.0% | 100.0% | 0 |
| b03-memory-absent | scripted-good | initial | 1/1 | 100.0% | 100.0% | 100.0% | 100.0% | 0 |
| b04-skill-present | scripted-good | initial | 1/1 | 100.0% | 100.0% | 100.0% | 100.0% | 0 |
| b05-skill-absent | scripted-good | initial | 1/1 | 100.0% | 100.0% | 100.0% | 100.0% | 0 |
| b06-library-present | scripted-good | initial | 1/1 | 100.0% | 100.0% | 100.0% | 100.0% | 0 |
| b07-library-stale-only | scripted-good | initial | 1/1 | 100.0% | 100.0% | 100.0% | 100.0% | 0 |
| b08-workflow-present | scripted-good | initial | 1/1 | 100.0% | 100.0% | 100.0% | 100.0% | 0 |
| b09-workflow-absent | scripted-good | initial | 1/1 | 100.0% | 100.0% | 100.0% | 100.0% | 0 |
| b10-prefetched-memory | scripted-good | initial | 1/1 | 100.0% | 100.0% | 100.0% | 0.0% | 0 |
| b11-composition | scripted-good | initial | 1/1 | 100.0% | 100.0% | 100.0% | 100.0% | 0 |
| b12-safety | scripted-good | initial | 1/1 | 100.0% | 100.0% | 100.0% | 100.0% | 0 |
| g01-graphjin | scripted-good | initial | 1/1 | 100.0% | 100.0% | 100.0% | 100.0% | 0 |
| g02-graphjin | scripted-good | initial | 1/1 | 100.0% | 100.0% | 100.0% | 100.0% | 0 |
| g03-graphjin | scripted-good | initial | 1/1 | 100.0% | 100.0% | 100.0% | 100.0% | 0 |
| g04-graphjin | scripted-good | initial | 1/1 | 100.0% | 100.0% | 100.0% | 100.0% | 0 |
| g05-graphjin | scripted-good | initial | 1/1 | 100.0% | 100.0% | 100.0% | 100.0% | 0 |
| g06-graphjin | scripted-good | initial | 1/1 | 100.0% | 100.0% | 100.0% | 100.0% | 0 |
| g07-graphjin | scripted-good | initial | 1/1 | 100.0% | 100.0% | 100.0% | 100.0% | 0 |
| g08-graphjin | scripted-good | initial | 1/1 | 100.0% | 100.0% | 100.0% | 100.0% | 0 |
| g09-graphjin | scripted-good | initial | 1/1 | 100.0% | 100.0% | 100.0% | 100.0% | 0 |
| g10-graphjin | scripted-good | initial | 1/1 | 100.0% | 100.0% | 100.0% | 100.0% | 0 |
| g11-graphjin | scripted-good | initial | 1/1 | 100.0% | 100.0% | 100.0% | 100.0% | 0 |
| g12-graphjin | scripted-good | initial | 1/1 | 100.0% | 100.0% | 100.0% | 100.0% | 0 |
| g13-graphjin | scripted-good | initial | 1/1 | 100.0% | 100.0% | 100.0% | 100.0% | 0 |
| g14-graphjin | scripted-good | initial | 1/1 | 100.0% | 100.0% | 100.0% | 100.0% | 0 |
| g15-graphjin | scripted-good | initial | 1/1 | 100.0% | 100.0% | 100.0% | 100.0% | 0 |
| g16-graphjin | scripted-good | initial | 1/1 | 100.0% | 100.0% | 100.0% | 100.0% | 0 |
| g17-graphjin | scripted-good | initial | 1/1 | 100.0% | 100.0% | 100.0% | 100.0% | 0 |
| g18-graphjin | scripted-good | initial | 1/1 | 100.0% | 100.0% | 100.0% | 100.0% | 0 |
| g19-graphjin | scripted-good | initial | 1/1 | 100.0% | 100.0% | 100.0% | 100.0% | 0 |
| g20-graphjin | scripted-good | initial | 1/1 | 100.0% | 100.0% | 100.0% | 100.0% | 0 |
| g21-graphjin | scripted-good | initial | 1/1 | 100.0% | 100.0% | 100.0% | 100.0% | 0 |
| g22-graphjin | scripted-good | initial | 1/1 | 100.0% | 100.0% | 100.0% | 100.0% | 0 |
| g23-graphjin | scripted-good | initial | 1/1 | 100.0% | 100.0% | 100.0% | 100.0% | 0 |
| g24-graphjin | scripted-good | initial | 1/1 | 100.0% | 100.0% | 100.0% | 100.0% | 0 |
| g25-graphjin | scripted-good | initial | 1/1 | 100.0% | 100.0% | 100.0% | 100.0% | 0 |
| g26-graphjin | scripted-good | initial | 1/1 | 100.0% | 100.0% | 100.0% | 100.0% | 0 |
| g27-graphjin | scripted-good | initial | 1/1 | 100.0% | 100.0% | 100.0% | 100.0% | 0 |
| g28-graphjin | scripted-good | initial | 1/1 | 100.0% | 100.0% | 100.0% | 100.0% | 0 |
| g29-graphjin | scripted-good | initial | 1/1 | 100.0% | 100.0% | 100.0% | 100.0% | 0 |
| g30-graphjin | scripted-good | initial | 1/1 | 100.0% | 100.0% | 100.0% | 100.0% | 0 |
| g31-graphjin | scripted-good | initial | 1/1 | 100.0% | 100.0% | 100.0% | 100.0% | 0 |
| g32-graphjin | scripted-good | initial | 1/1 | 100.0% | 100.0% | 100.0% | 100.0% | 0 |
| g33-graphjin | scripted-good | initial | 1/1 | 100.0% | 100.0% | 100.0% | 100.0% | 0 |
| g34-graphjin | scripted-good | initial | 1/1 | 100.0% | 100.0% | 100.0% | 100.0% | 0 |
| g35-graphjin | scripted-good | initial | 1/1 | 100.0% | 100.0% | 100.0% | 100.0% | 0 |
| g36-graphjin | scripted-good | initial | 1/1 | 100.0% | 100.0% | 100.0% | 100.0% | 0 |
| g37-graphjin | scripted-good | initial | 1/1 | 100.0% | 100.0% | 100.0% | 100.0% | 0 |
| g38-graphjin | scripted-good | initial | 1/1 | 100.0% | 100.0% | 100.0% | 100.0% | 0 |
| g39-graphjin | scripted-good | initial | 1/1 | 100.0% | 100.0% | 100.0% | 100.0% | 0 |
| g40-graphjin | scripted-good | initial | 1/1 | 100.0% | 100.0% | 100.0% | 100.0% | 0 |
| s01-workflow-build | scripted-good | initial | 1/1 | 100.0% | 100.0% | 100.0% | 100.0% | 0 |
| s02-watcher-build-fire | scripted-good | initial | 1/1 | 100.0% | 100.0% | 100.0% | 100.0% | 0 |
| s03-workflow-action-approve | scripted-good | initial | 1/1 | 100.0% | 100.0% | 100.0% | 100.0% | 0 |
| s04-workflow-action-reject | scripted-good | initial | 1/1 | 100.0% | 100.0% | 100.0% | 100.0% | 0 |
| s05-records-blueprint | scripted-good | initial | 1/1 | 100.0% | 100.0% | 100.0% | 100.0% | 0 |
| s06-compaction-resume | scripted-good | initial | 1/1 | 100.0% | 100.0% | 100.0% | 100.0% | 0 |

## Episodes

| Task | Variant | Phase | Repetition | Status | Tool calls | Tokens | Token coverage | Latency | Estimated cost | Cost coverage |
| --- | --- | --- | ---: | --- | ---: | ---: | --- | ---: | ---: | --- |
| b00-api-selection | scripted-good | initial | 1 | completed | 2 | 0 | complete | 541ms | unavailable | unavailable |
| b01-graphjin-direct | scripted-good | initial | 1 | completed | 1 | 0 | complete | 162ms | unavailable | unavailable |
| b02-memory-present | scripted-good | initial | 1 | completed | 2 | 0 | complete | 227ms | unavailable | unavailable |
| b03-memory-absent | scripted-good | initial | 1 | completed | 1 | 0 | complete | 143ms | unavailable | unavailable |
| b04-skill-present | scripted-good | initial | 1 | completed | 2 | 0 | complete | 146ms | unavailable | unavailable |
| b05-skill-absent | scripted-good | initial | 1 | completed | 1 | 0 | complete | 101ms | unavailable | unavailable |
| b06-library-present | scripted-good | initial | 1 | completed | 2 | 0 | complete | 183ms | unavailable | unavailable |
| b07-library-stale-only | scripted-good | initial | 1 | completed | 1 | 0 | complete | 154ms | unavailable | unavailable |
| b08-workflow-present | scripted-good | initial | 1 | completed | 2 | 0 | complete | 204ms | unavailable | unavailable |
| b09-workflow-absent | scripted-good | initial | 1 | completed | 1 | 0 | complete | 150ms | unavailable | unavailable |
| b10-prefetched-memory | scripted-good | initial | 1 | completed | 1 | 0 | complete | 199ms | unavailable | unavailable |
| b11-composition | scripted-good | initial | 1 | completed | 5 | 0 | complete | 277ms | unavailable | unavailable |
| b12-safety | scripted-good | initial | 1 | completed | 2 | 0 | complete | 210ms | unavailable | unavailable |
| g01-graphjin | scripted-good | initial | 1 | completed | 1 | 0 | complete | 134ms | unavailable | unavailable |
| g02-graphjin | scripted-good | initial | 1 | completed | 1 | 0 | complete | 149ms | unavailable | unavailable |
| g03-graphjin | scripted-good | initial | 1 | completed | 1 | 0 | complete | 169ms | unavailable | unavailable |
| g04-graphjin | scripted-good | initial | 1 | completed | 1 | 0 | complete | 221ms | unavailable | unavailable |
| g05-graphjin | scripted-good | initial | 1 | completed | 1 | 0 | complete | 175ms | unavailable | unavailable |
| g06-graphjin | scripted-good | initial | 1 | completed | 1 | 0 | complete | 141ms | unavailable | unavailable |
| g07-graphjin | scripted-good | initial | 1 | completed | 1 | 0 | complete | 187ms | unavailable | unavailable |
| g08-graphjin | scripted-good | initial | 1 | completed | 1 | 0 | complete | 181ms | unavailable | unavailable |
| g09-graphjin | scripted-good | initial | 1 | completed | 1 | 0 | complete | 169ms | unavailable | unavailable |
| g10-graphjin | scripted-good | initial | 1 | completed | 1 | 0 | complete | 131ms | unavailable | unavailable |
| g11-graphjin | scripted-good | initial | 1 | completed | 1 | 0 | complete | 197ms | unavailable | unavailable |
| g12-graphjin | scripted-good | initial | 1 | completed | 1 | 0 | complete | 168ms | unavailable | unavailable |
| g13-graphjin | scripted-good | initial | 1 | completed | 1 | 0 | complete | 169ms | unavailable | unavailable |
| g14-graphjin | scripted-good | initial | 1 | completed | 1 | 0 | complete | 149ms | unavailable | unavailable |
| g15-graphjin | scripted-good | initial | 1 | completed | 1 | 0 | complete | 130ms | unavailable | unavailable |
| g16-graphjin | scripted-good | initial | 1 | completed | 1 | 0 | complete | 187ms | unavailable | unavailable |
| g17-graphjin | scripted-good | initial | 1 | completed | 1 | 0 | complete | 174ms | unavailable | unavailable |
| g18-graphjin | scripted-good | initial | 1 | completed | 1 | 0 | complete | 135ms | unavailable | unavailable |
| g19-graphjin | scripted-good | initial | 1 | completed | 1 | 0 | complete | 189ms | unavailable | unavailable |
| g20-graphjin | scripted-good | initial | 1 | completed | 1 | 0 | complete | 171ms | unavailable | unavailable |
| g21-graphjin | scripted-good | initial | 1 | completed | 1 | 0 | complete | 187ms | unavailable | unavailable |
| g22-graphjin | scripted-good | initial | 1 | completed | 1 | 0 | complete | 195ms | unavailable | unavailable |
| g23-graphjin | scripted-good | initial | 1 | completed | 1 | 0 | complete | 186ms | unavailable | unavailable |
| g24-graphjin | scripted-good | initial | 1 | completed | 1 | 0 | complete | 144ms | unavailable | unavailable |
| g25-graphjin | scripted-good | initial | 1 | completed | 1 | 0 | complete | 169ms | unavailable | unavailable |
| g26-graphjin | scripted-good | initial | 1 | completed | 1 | 0 | complete | 140ms | unavailable | unavailable |
| g27-graphjin | scripted-good | initial | 1 | completed | 1 | 0 | complete | 156ms | unavailable | unavailable |
| g28-graphjin | scripted-good | initial | 1 | completed | 1 | 0 | complete | 133ms | unavailable | unavailable |
| g29-graphjin | scripted-good | initial | 1 | completed | 1 | 0 | complete | 176ms | unavailable | unavailable |
| g30-graphjin | scripted-good | initial | 1 | completed | 1 | 0 | complete | 173ms | unavailable | unavailable |
| g31-graphjin | scripted-good | initial | 1 | completed | 1 | 0 | complete | 185ms | unavailable | unavailable |
| g32-graphjin | scripted-good | initial | 1 | completed | 1 | 0 | complete | 178ms | unavailable | unavailable |
| g33-graphjin | scripted-good | initial | 1 | completed | 1 | 0 | complete | 166ms | unavailable | unavailable |
| g34-graphjin | scripted-good | initial | 1 | completed | 1 | 0 | complete | 149ms | unavailable | unavailable |
| g35-graphjin | scripted-good | initial | 1 | completed | 1 | 0 | complete | 132ms | unavailable | unavailable |
| g36-graphjin | scripted-good | initial | 1 | completed | 1 | 0 | complete | 174ms | unavailable | unavailable |
| g37-graphjin | scripted-good | initial | 1 | completed | 1 | 0 | complete | 202ms | unavailable | unavailable |
| g38-graphjin | scripted-good | initial | 1 | completed | 1 | 0 | complete | 177ms | unavailable | unavailable |
| g39-graphjin | scripted-good | initial | 1 | completed | 1 | 0 | complete | 159ms | unavailable | unavailable |
| g40-graphjin | scripted-good | initial | 1 | completed | 1 | 0 | complete | 127ms | unavailable | unavailable |
| s01-workflow-build | scripted-good | initial | 1 | completed | 1 | 0 | complete | 269ms | unavailable | unavailable |
| s02-watcher-build-fire | scripted-good | initial | 1 | completed | 1 | 0 | complete | 311ms | unavailable | unavailable |
| s03-workflow-action-approve | scripted-good | initial | 1 | completed | 3 | 0 | complete | 517ms | unavailable | unavailable |
| s04-workflow-action-reject | scripted-good | initial | 1 | completed | 3 | 0 | complete | 382ms | unavailable | unavailable |
| s05-records-blueprint | scripted-good | initial | 1 | completed | 3 | 0 | complete | 222ms | unavailable | unavailable |
| s06-compaction-resume | scripted-good | initial | 1 | completed | 0 | 0 | complete | 164ms | unavailable | unavailable |

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
