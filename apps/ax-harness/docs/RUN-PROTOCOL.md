# Run protocol

Ax Harness runs one agent turn. The host writes one JSON run specification.
The harness writes one JSON event per line. The host owns history, compaction
across turns, approvals, the sandbox and scheduling.

A turn runs only in `openneko serve --agent-socket <path>`, one turn per
connection, each in a confined runner process. The `ax-harness` binary
refuses a turn; it runs `catalog`, `generate` and `embed` only. The frames
are in `serve/serve.go`. Serve removes `OPENNEKO_MCP_BRIDGE`, every
`OPENNEKO_SANDBOX_*` variable and every `*_API_KEY` variable from the turn's
environment. Serve holds no model key. The start frame carries a key door
token in place of each model key, and serve refuses a frame with a real key.
`openneko web` keeps the keys in its key door. The turn's tools and terminal
never see a key or a token.

## Input

Send exactly one object, at most 16 MiB. Unknown fields fail the run.

| Field | Type | Rule |
| --- | --- | --- |
| `version` | integer | Must be `1`. |
| `run_id` | string | Required. 128 characters or fewer. |
| `input_id` | string | Required. 128 characters or fewer. |
| `instructions` | string | Optional. The org prompt: the same text for every turn of the org. The model sees it first, as `Instructions:`. |
| `prompt` | string | Required. The rest of the turn, including history and the user message. The model sees it next, as `Question:`. Without `instructions`, it carries the whole turn. Only the model context window bounds the two together. |
| `skill_query` | string | Optional. The user message. The host skill index recalls up to 10 guides by meaning. When it recalls two or more, one model call on the context route keeps the two or fewer that fit the request. A `discover()` search that names a staged guide loads only that guide; a free-text search gets the same rerank. |
| `skill_rerank` | string | Optional. `off` keeps the index order and makes no rerank call. The harness also skips the call when `max_model_calls` is 1 to 3. |
| `stream_responses` | boolean | Optional. Sends live `answer.delta`, `thought.delta`, `executor.thought.delta`, `actor.step` and `step.quiet` events. |
| `timeout_ms` | integer | Optional. Run wall clock, 1,000 to 1,800,000. Default 540,000 (9 minutes). |
| `max_actor_steps` | integer | Optional. Actor steps, 1 to 500. Default 25. |
| `max_child_steps` | integer | Optional. Child agent steps, 1 to 500. Default 50. |
| `max_operations` | integer | Optional. Tool calls per run, 0 to 4,000. 0 means no cap. |
| `max_model_calls` | integer | Optional. Model calls per run, 0 to 2,000. 0 means no cap. |
| `max_model_tokens` | integer | Optional. Token ceiling, up to 10,000,000. 0 means no ceiling. |
| `max_cost_micros` | integer | Optional. Cost ceiling in USD micros at Ax catalog rates. Every route needs a catalog price. |
| `reasoning_effort` | string | Optional. `low`, `medium` or `high`. |
| `responder_reasoning_effort` | string | Optional. `minimal`, `low`, `medium` or `high`. Replaces `reasoning_effort` for the responder stage only. OpenNeko sends the org's answer effort, `low` by default. |
| `context_window_tokens` | integer | Optional. The model context window. A prompt larger than this estimate fails before any model call. |
| `max_output_tokens` | integer | Optional. Maximum output tokens per model call. Must be below `context_window_tokens`. |

## Limits

OpenNeko sets the limits in `agentLimitsFor`, in
`apps/openneko/internal/webfront/turnax.go`.
The defaults below apply only when the host does not set a value.

| Limit | Ax Harness |
| --- | --- |
| Prompt size | Model context window only; 16 MiB stdin sanity limit |
| Context window, output tokens | `context_window_tokens`, `max_output_tokens` |
| Actor steps | Default 25, 1 to 500 |
| Child agent steps | Default 50, 1 to 500 |
| Tool calls, model calls | No cap unless set |
| Run wall clock | Default 9 minutes, maximum 30 minutes |
| Actor code time | The run wall clock, including tool waits |
| Inline tool result | 100,000 characters |
| Saved result preview | 1,500 characters |
| Inline results per actor step | No limit: results go to actor code, and only printed output reaches the model |
| Stored tool result | 16 MiB; MCP results 8 MiB, below the MCP SDK frame limit |
| Terminal timeout | Default 180 s, maximum 600 s |
| Terminal output | 50,000 characters, head and tail |
| File read | 100,000 characters, 2,000 lines, 2,000 characters per line |
| File write and edit | 1 MiB of content; readable files up to 10 MiB |
| Skills catalog | 256 skills; descriptions up to 1,024 characters; bodies up to 64 KiB |

A result above the inline limit, or above the step budget, returns a run-local
reference with a preview. Actor code reads the full result with
`harnessSavedOperation(id)`.

## Output events

Every event has `version`, `run_id`, `input_id`, `sequence` and `type`.
`sequence` counts up from 1. Model events carry no prompt text. No event carries
credentials. `tool.finished` carries the tool result in `data`.

| Type | Meaning |
| --- | --- |
| `run.started` | The run started. |
| `child.admitted` | The run has a read-only child agent. |
| `model.request.started` | A model call passed admission. `name` is the configured model, `origin` the route, `provider` the provider type, `call_id` the count, `stage` the Ax execution path (`root/executor`, `root/responder`) and `streaming` whether Ax streams the call. |
| `model.request.finished` | A model call ended, with the same `stage` and `streaming`. `usage` holds the tokens and the call's cost, `observed_model` the model the provider reported, and `error` is set on failure. A streamed call ends when its stream ends, and `duration_ms` covers the whole stream. When a stream stops early, `usage` holds the tokens the provider sent so far, with `coverage` `partial`. |
| `model.route.fallback` | A transient provider error moved the call from `name` to `origin`. |
| `executor.step.failed` | Actor code failed. Later executor calls can use the escalation route. |
| `reply.written` | A reply from `replyAsWritten` or a tool's `reply_as_written` field became the answer. `data.explicit` is true for `replyAsWritten`; `data.responder_text` is true when the answer kept the responder's own text with the reply's cards. |
| `skill.used` | The agent declared that the staged skill `name` influenced the turn. |
| `tool.input.rejected` | Tool input failed its schema. The tool did not run. |
| `tool.started`, `tool.finished` | One tool call, with `operation_id`, `effect` and, when finished, `data` or `error`. |
| `observation.retrieved` | Actor code read a saved tool result by `operation_id`. |
| `span.started`, `span.finished` | Ax span lifecycle. |
| `child.started`, `child.finished` | Child agent lifecycle. |
| `run.finished` | The last event. `result` holds the outcome. |

Live events have `sequence` 0 and appear only with `stream_responses`. Treat
them as provisional; only `run.finished` holds the answer.

| Type | `data` |
| --- | --- |
| `answer.delta` | `{version, index, text}` from the responder. A new `version` replaces the earlier text. |
| `thought.delta` | `{version, index, text}`. The responder's thinking text, from routes with `reasoning_effort` set: thought summaries from Anthropic and Gemini, raw reasoning from DeepSeek, OpenRouter and Together. Other providers send none through the pinned Ax. When a fallback route serves the call and its provider sends raw reasoning, the harness removes the thinking text from that call. The harness sends the text as the provider wrote it. |
| `executor.thought.delta` | `{version, index, text}`. One note from the thought summary of the context stage (`root/distiller`) or the executor, on Anthropic and Gemini routes with `reasoning_effort` set. The harness reads both stages as streams for these notes. `version` numbers the call and `index` the note in the run. A bold span that fills a whole line starts a new note, and the note carries that heading and the text below it, as the provider wrote them. The harness sends a note when the next heading or the end of the call closes it, and sends at most 100 notes per run. When a fallback route serves the call and its provider sends raw reasoning, the call gives no note. |
| `actor.step` | `{text}`. The short request the actor writes when it hands work to the executor, with answer contract names such as `neko_cards` written as plain words. |
| `step.quiet` | `{text}`. A plain step line when an executor or responder model call shows nothing new for 3 seconds: "Working out the first step" before the first tool result, "Putting the findings together" after it, and "Writing the answer" for the responder. A thinking note, an answer delta, an `actor.step` or a tool event inside the 3 seconds cancels it. |

## Result

| Field | Values |
| --- | --- |
| `status` | `completed`, `failed`, `cancelled` |
| `kind` | `answer`, `summary`, `clarification`, `failure` |
| `answer` | The final text, unchanged, with any fences. |
| `code` | Set when the run did not complete, or names the budget for a `summary`. |
| `usage` | Requests, reported calls, tokens, `coverage` (`complete`, `partial` or `unavailable`) and the fields in [Cost](#cost). The tokens include those of a stream that stopped early; that call does not count as reported. `early` holds the requests, tokens and cost of those streams apart, so a host scales only the reported calls and adds the early ones once. The token budget charges no reservation for such a call. |
| `cost` | Present when `max_cost_micros` is set: `catalog_version`, `charged_micros` (reservations for calls without usage included) and `budget_micros`. |

A tool error is a normal result. The model sees it and can adapt; the run does
not fail because of it.

When the run stops at `max_operations`, `max_actor_steps` or `max_model_calls`,
the harness makes one more model call with no tools. That call answers from the
tool results. The result is `completed` with kind `summary` and code
`operations_exhausted`, `actor_steps_exhausted` or `model_calls_exhausted`.
When `max_model_calls` is set, the agent stops one call early to keep that call
inside the cap. A token, cost, deadline or cancel stop never makes this call.

Failure codes: `model_failed`, `model_http_<status>`, `model_context_overflow`,
`model_output_truncated`, `actor_steps_exhausted`, `operations_exhausted`,
`model_calls_exhausted`, `invalid_output`, `model_budget_exceeded`,
`model_token_budget_exceeded`, `cost_budget_exceeded`, `deadline_exceeded`,
`cancelled`.

A tool with effect `pause` ends the turn as `clarification` with the answer
"Awaiting operator input."

## Cost

The harness prices each model call from the model catalog of the pinned Ax Go
module, for the route that served the call. A fallback call is priced at the
fallback route's rates. Cache reads and cache writes use their catalog rates.
A call whose input, cache reads and cache writes exceed the model's
`longContextThreshold` uses the long-context rates. Tokens in the total beyond
input, output and cache are reasoning tokens, billed at the output rate. An
`openai-compatible` or `azure-openai` route uses OpenAI catalog prices, and a
`vertex-ai` route uses Gemini or Anthropic prices. With `max_cost_micros`,
each call reserves its token reservation at the model's highest rate, and the
catalog cost replaces the reservation when the provider reports usage.

| Usage field | Meaning |
| --- | --- |
| `cost_micros` | USD micros for the reported calls, each rounded up. |
| `priced_by` | `catalog` when every reported call had a catalog price, otherwise `unpriced`. |
| `catalog_version` | The pinned Ax module version. |

`go run ./cmd/ax-harness catalog` writes the same prices as JSON.

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

- One model: `HARNESS_MODEL`, `HARNESS_MODEL_API_KEY`, and optionally
  `HARNESS_MODEL_PROVIDER`, `HARNESS_MODEL_URL` and `HARNESS_MODEL_OPTIONS`.
  `HARNESS_MODEL_URL` is required for the default provider, `openai-compatible`.
- Several routes: `HARNESS_MODEL_ROUTES`, a JSON object:

```json
{"context":"cheap","executor":"work","responder":"work",
 "fallbacks":[{"from":"work","to":"spare"}],
 "routes":[
  {"key":"cheap","model":"model-a","url":"https://provider.example/v1","api_key_env":"HARNESS_CHEAP_KEY"},
  {"key":"work","provider":"anthropic","model":"model-b","api_key_env":"HARNESS_WORK_KEY"},
  {"key":"spare","provider":"google-gemini","model":"model-c","api_key_env":"HARNESS_SPARE_KEY"}]}
```

`provider` is `openai-compatible` (the default, which needs `url`) or any Ax
provider profile, for example `anthropic`, `google-gemini`, `openai`,
`azure-openai`, `mistral`, `groq`, `deepseek` or `openrouter`. A profile keeps
its provider's auth header, request shape and model rules. A native provider
uses its default URL when `url` is absent. An unknown provider is a
configuration error.

`HARNESS_MODEL_OPTIONS`, or `options` on a route, is a JSON object of string
provider settings. Azure needs `resource_name` and `deployment_name`, and
takes `api_version`; Ax builds the endpoint from them. Options cannot set the
key, the URL, the model or the transport.

`executor_escalation` and `executor_after_errors` (1 to 8) name a stronger
executor route after actor-code errors. Each route names the environment
variable that holds its key. Ax client retries are off; the harness owns fallback.

`reasoning_effort` sets `reasoning_effort` on OpenAI routes and Ax's thinking
budget on other providers.

Each stage's system prompt is identical for every turn, and `instructions`
starts the first user message, so every provider sees a stable prefix. On
`anthropic`, `openai` and `openai-responses` routes the harness turns on Ax's
context cache and sends `instructions` as a user message of its own with a
cache mark. Anthropic then puts a `cache_control` breakpoint on it, and OpenAI
GPT-5.6 and GPT-6 models put an explicit cache breakpoint on it, so a new turn
of the same org reuses the cached org prompt. Other routes, Gemini included,
use the provider's implicit prefix cache. The harness does not create Gemini
cache objects: a cache object would need a registry across turns, and Ax creates it outside the metered model calls.

## OpenNeko entry

The `harness` package reads these variables from the turn's environment:

| Variable | Use |
| --- | --- |
| `OPENNEKO_MCP_ORG_ID`, `OPENNEKO_MCP_THREAD_ID` | Required run scope. |
| `OPENNEKO_MCP_BRIDGE`, `OPENNEKO_MCP_SERVERS`, `OPENNEKO_BROKER_URL`, `OPENNEKO_BROKER_TOKEN` | No effect in a turn. Serve removes `OPENNEKO_MCP_BRIDGE` and carries the OpenNeko tools as MCP over the turn's link. The harness starts no bridge process. |
| `OPENNEKO_HARNESS_WORKSPACE_DIR` | Adds `file_read`, `file_edit`, `file_write`, `file_search`. |
| `OPENNEKO_HARNESS_SHELL` | `1` adds `terminal`. Needs the workspace and a sandbox. |
| `OPENNEKO_SANDBOX_URL`, `OPENNEKO_SANDBOX_KEY` | The sandbox service that runs `terminal` commands: `unix:///path`, `http://` or `https://`. In serve, only serve's own values count. Without a URL there is no `terminal`. |
| `OPENNEKO_HARNESS_TERMINAL_TIMEOUT_SECONDS` | Default terminal timeout, 1 to 600. Default 180. |
| `OPENNEKO_HARNESS_TERMINAL_MAX_OUTPUT` | Terminal output cap in characters, 1,000 to 1,000,000. Default 50,000. |
| `OPENNEKO_HARNESS_WEB_HOSTS` | Comma list of hosts for `web_fetch`. `*.example.com` matches subdomains; `*` matches any public host. Unset means no `web_fetch`. The turn needs the tool bridge: web makes each fetch and checks the URL against its own list for the run. |
| `OPENNEKO_HARNESS_UPLOADS_DIR` | Adds read-only upload tools. |
| `OPENNEKO_HARNESS_SKILLS_READ`, `OPENNEKO_MCP_SKILLS_ROOT` | Adds the staged skills to the Ax skills catalog, and `skill_read` and `skill_search` for supporting files. |
| `OPENNEKO_HARNESS_CHILD_TOOLS` | `*` or a comma list of tools for the child agent. Pause tools stay with the parent. |

`terminal` sends each command to the sandbox service. The harness never runs
a command itself. The request carries the command, the workspace, the
skill folder and only `RUN_DIR`, `ARTIFACT_DIR`, `SKILLS_DIR` and `PYTHONPATH`. No broker
binding and no model credential leave the harness. The sandbox runs
`/bin/sh -c` in the workspace and kills the command's process group on a
timeout or a cancel. A non-zero exit is a normal result with `exit_code` and
`output`. The run ends in the sandbox when the turn ends.
`recipes/sandbox/README.md` describes the service's API.

`file_edit` replaces `old_string` with `new_string`. The file must be read in
this run and unchanged since the last read or edit. `old_string` must match
once unless `replace_all` is true.

`file_search` takes an RE2 `pattern`, a `glob`, or both. A pattern returns
`{path, line, text}` matches; a glob alone returns paths. The default limit is
50 and the maximum is 200.

`web_fetch` runs in `openneko web`: the harness sends each call over the
bridge as the hidden tool `openneko_web_fetch`, and web logs the URL with
the organization and the run. It sends a GET with no cookies or credentials. Each redirect must stay
on the allowlist. Without a proxy, connections reach public addresses only.
HTML returns as plain text. The cap is 10 MiB of body and 2,000,000 characters.

Every tool is a JavaScript function in the Goja actor. One actor step can call
many tools, loop over results and filter them, so extra tool calls add no model
calls. A call to the child agent is different: the step ends when the
child returns, and the parent reads the result on its next model call. A turn
with two child calls makes two more executor calls than a turn with none.
