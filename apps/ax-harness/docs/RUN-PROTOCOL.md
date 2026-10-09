# Run protocol

Ax Harness runs one agent turn per process. The host writes one JSON run
specification to stdin. The harness writes one JSON event per line to stdout.
The host owns history, compaction, approvals, the sandbox and scheduling.

Build the binary with `go build -o bin/ax-harness ./cmd/ax-harness`.

## Input

Send exactly one object, at most 128 KiB. Unknown fields fail the run.

| Field | Type | Rule |
| --- | --- | --- |
| `version` | integer | Must be `1`. |
| `run_id` | string | Required. 128 characters or fewer. |
| `input_id` | string | Required. 128 characters or fewer. |
| `prompt` | string | Required. 64 KiB or less. It carries the whole turn, including history. |
| `stream_responses` | boolean | Optional. Sends `answer.delta` events from the responder. |
| `skill_query` | string | Optional. 8 KiB or less. Used to pick a staged skill. |
| `max_operations` | integer | Optional. Tool calls per run, 1 to 32. Default 4. |
| `max_model_calls` | integer | Optional. Model calls per run, 1 to 64. Default 16. |
| `max_model_tokens` | integer | Optional. Token ceiling, up to 10,000,000. 0 means no ceiling. |
| `max_cost_micros` | integer | Optional. Cost ceiling in USD micros. Needs a priced route profile. |

The run has a fixed wall clock of 2 minutes. The agent has 8 actor steps. A
child agent has 3.

## Output events

Every event has `version`, `run_id`, `input_id`, `sequence` and `type`.
`sequence` counts up from 1. Model events carry no prompt text. No event carries
credentials. `tool.finished` carries the tool result in `data`.

| Type | Meaning |
| --- | --- |
| `run.started` | The run started. |
| `child.admitted` | The run has a read-only child agent. |
| `model.request.started` | A model call passed admission. `name` is the model, `origin` the route, `call_id` the count. |
| `model.request.finished` | A model call ended. `usage` holds the tokens; `error` is set on failure. |
| `model.route.fallback` | A transient provider error moved the call from `name` to `origin`. |
| `executor.step.failed` | Actor code failed. Later executor calls can use the escalation route. |
| `skill.selected` | A staged skill was picked as a hint, or not (`error`). |
| `tool.input.rejected` | Tool input failed its schema. The tool did not run. |
| `tool.started`, `tool.finished` | One tool call, with `operation_id`, `effect` and, when finished, `data` or `error`. |
| `observation.retrieved` | Actor code read a saved tool result by `operation_id`. |
| `span.started`, `span.finished` | Ax span lifecycle. |
| `child.started`, `child.finished` | Child agent lifecycle. |
| `run.finished` | The last event. `result` holds the outcome. |

`answer.delta` is a live event with `sequence` 0. Its `data` is
`{version, index, text}`. A new `version` replaces the earlier text. Treat it as
provisional; only `run.finished` holds the answer.

## Result

| Field | Values |
| --- | --- |
| `status` | `completed`, `failed`, `cancelled` |
| `kind` | `answer`, `clarification`, `partial`, `failure` |
| `answer` | The final text, unchanged, with any fences. |
| `code` | Set when the run did not complete (see below). |
| `usage` | Requests, reported calls, tokens and `coverage`: `complete`, `partial` or `unavailable`. |
| `cost` | Present when `max_cost_micros` is set. |

Codes: `model_failed`, `model_http_<status>`, `actor_steps_exhausted`,
`invalid_output`, `incomplete_result`, `model_budget_exceeded`,
`model_token_budget_exceeded`, `cost_budget_exceeded`, `deadline_exceeded`,
`cancelled`.

A tool with effect `pause` ends the turn as `clarification` with the answer
"Awaiting operator input." A failed tool turns a completed answer into
`failed` with kind `partial`.

## Exit codes and signals

| Code | Meaning |
| --- | --- |
| 0 | The run completed. |
| 1 | The run did not complete, or event delivery failed. |
| 2 | The input or the configuration is invalid. Nothing ran. |

SIGINT and SIGTERM cancel the run. The run then finishes with status
`cancelled`.

## Model configuration

The host sets the model. Run input cannot change it.

- One model: `HARNESS_MODEL_URL`, `HARNESS_MODEL` and `HARNESS_MODEL_API_KEY`.
- Several routes: `HARNESS_MODEL_ROUTES`, a JSON object:

```json
{"context":"cheap","executor":"work","responder":"work","skill":"cheap",
 "fallbacks":[{"from":"work","to":"spare"}],
 "routes":[
  {"key":"cheap","model":"model-a","url":"https://provider.example/v1","api_key_env":"HARNESS_CHEAP_KEY"},
  {"key":"work","model":"model-b","url":"https://provider.example/v1","api_key_env":"HARNESS_WORK_KEY"},
  {"key":"spare","model":"model-c","url":"https://provider.example/v1","api_key_env":"HARNESS_SPARE_KEY"}]}
```

`executor_escalation` and `executor_after_errors` (1 to 8) name a stronger
executor route after actor-code errors. `pricing_version` and a `price` on
every route enable the cost ceiling. Each route names the environment variable
that holds its key. Ax client retries are off; the harness owns fallback.

## OpenNeko entry

`cmd/ax-harness` reads these environment variables:

| Variable | Use |
| --- | --- |
| `OPENNEKO_MCP_ORG_ID`, `OPENNEKO_MCP_THREAD_ID` | Required run scope. |
| `OPENNEKO_MCP_BRIDGE` | Path of the OpenNeko MCP bridge. Starts `node` with it. |
| `OPENNEKO_MCP_SERVERS` | Comma list of bridge servers to start. |
| `OPENNEKO_BROKER_URL`, `OPENNEKO_BROKER_TOKEN` | Passed to the bridge only, then removed from the harness process. |
| `OPENNEKO_HARNESS_WORKSPACE_DIR` | Adds `file_read`, `file_edit`, `file_write`, `file_search`. |
| `OPENNEKO_HARNESS_UPLOADS_DIR` | Adds read-only upload tools. |
| `OPENNEKO_HARNESS_SKILLS_READ`, `OPENNEKO_MCP_SKILLS_ROOT` | Adds skill tools and, with a `skill` route, the skill catalog. |
| `OPENNEKO_HARNESS_CHILD_READS` | Comma list of read tools for the child agent. |

The bridge gets a clean environment: `PATH`, every `OPENNEKO_MCP_*` variable,
the broker binding and the proxy variables. Every bridge tool becomes
`mcp_neko_<tool>`. `interaction_ask_user_question` has effect `pause`.
