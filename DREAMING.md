# Dreaming

> **STATUS — 2026-11-19: design only.** Nothing here is built. The eval that
> the build is tested against is in [evals/DREAMING.md](evals/DREAMING.md).
> Items marked *proposal* answer a question that is still open.

Dreaming lets one OpenNeko install get better at its own recurring work with
no person in the loop. A background **learning pass** reads the telemetry of
finished runs and threads and writes **lessons** for the org. A later run on
a similar question starts from those lessons.

## 1. The problem

Each turn starts cold on data. The agent gets the catalog and the GraphJin
tools and works out tables, joins and filters again. The harness stops an
identical call after it fails twice
(`apps/ax-harness/internal/agent/failed_calls.go`), but that record lasts one
run, so the next similar run makes the same failing calls before it recovers.
When a person corrects an answer in the thread, the fix stays in that thread.

## 2. Decisions already made

1. No human review of a lesson.
2. No admin involvement. Dreaming is on by default.
3. A follow-up in the thread that says an answer is wrong is a correction.
   There is no thumbs control.
4. Learning happens offline, after threads settle.
5. One version of a lesson per org. No per-user versions.
6. When people correct in different directions, the version more people
   support wins. A challenger must be strictly ahead; a tie keeps the current
   version.
7. A lesson is offered only to a person who can already query every table it
   touches.

## 3. What exists to build on

| Need | What the product has today |
|---|---|
| Every GraphJin call of a run, with its query and its error | `work_run_event` rows of type `tool_start` (`input`) and `tool_end` (`result`, `error`), already scrubbed of secrets (`turnscrub.go`) |
| The thread's follow-ups | `work_message` (`role`, `content`, `run_id`) |
| A background job after a run | `skill_learn` on River, queued from `jobs/work_run.go`, with a periodic-job list in `jobs/jobs.go` |
| A host model call with its own route and spend row | The inference gateway and `llm_task_route`; `skill_lessons` is one task |
| Context the host adds to a turn by similarity to the question | `memoryPromptContext` in `turnmemory.go`: pinned memories plus the five closest by embedding |
| A user's data access | GraphJin enforces it from `X-User-Role` and the group claims (`turndata.go`) |
| A sign that the schema changed | `catalogRevision` in `turnknowledge.go` |
| A record of each learning decision | `skill_learn_event` (decision, reason, rationale, evidence run ids) |

Dreaming needs no change to the Ax harness. All of it runs in `openneko web`
and its jobs.

## 4. Lessons

| Kind | Holds | Learned from |
|---|---|---|
| `query` | A question shape and the query that answered it | The last successful query of a run whose answer stood |
| `schema_fact` | One fact about the source ("orders join territories through a bridging table") | A failed call followed by a successful one that differs in that fact |
| `dead_end` | A call pattern and the error it always gives | The same failure seen in several runs |
| `business_rule` | A rule in the person's words ("revenue excludes cancelled orders") | A correction in a thread |

A lesson holds query text and rule text. It never holds result rows or
values.

Each lesson records the tables it touches, the catalog revision it was
learned under, the runs it came from, and its state: `active`, `retired`, or
`challenger` (a competing version that is not ahead).

Lessons get their own tables, not `work_memory`. Memories are what an
operator chose to save and have per-user layers; lessons are derived, have
one version per org, and carry support counts and a table list.

### Tables *(proposal)*

| Table | One row per | Main columns |
|---|---|---|
| `dream_lesson` | lesson version | `org_id`, `kind`, `question_shape`, `body`, `tables[]`, `catalog_revision`, `state`, `supersedes_id`, `embedding`, `use_count`, `last_used_at` |
| `dream_position` | person and lesson group | `org_id`, `lesson_group_id`, `user_id`, `lesson_id` supported, `strength` (`correction` or `use`), `run_id`, `updated_at` |
| `dream_lesson_use` | run and lesson | `run_id`, `lesson_id`, `offered`, `outcome` (`succeeded`, `failed`, `corrected`) |
| `dream_pass` | learning pass | `org_id`, event watermark, decisions, rationale, evidence run ids, tokens, cost |

`dream_pass` plays the part of `skill_learn_event`. Nobody approves from it.
It exists so a wrong lesson can be traced to the runs that produced it, and
so the eval can read what the pass decided.

## 5. The learning pass

A periodic job, `dream_sweep`, queues one `dream_pass` per org that has
settled activity since its last watermark. A thread is settled when it has
had no message for two hours, the wait `skill_learn` already uses.

The pass has four steps. Only step 2 calls a model.

**Step 1. Extract (no model).** For each settled run, read its GraphJin
events in order and build:

- each failed call with its error, and the next successful call
- the last successful query before the answer
- which lessons the run was offered, and whether a query built on one failed

**Step 2. Interpret (model).** One host call per settled thread, through a
new `dreaming` task route. It is given the raw thread and the extracted
calls, not a summary. It returns:

- for each follow-up: `correction`, `refinement`, `different_measure` or
  `other`
- for a correction: the rule in the person's words and which answer it
  corrects
- the question shape of each answered question
- proposed lessons, each with the run ids it rests on

The pass runs on a strong model. It is off the user's path, and a weak
proposer writes weak lessons.

**Step 3. Consolidate (no model).** Match each proposed lesson to existing
lesson groups by embedding of the question shape, then:

- record or update each person's position (§6)
- write a new lesson, or add support to an existing one
- recount the group and set the active version
- retire a lesson whose query failed in a run, or that a correction replaced

**Step 4. Gate (no model).** A lesson is written only if all hold:

- a `query` lesson's query ran without error in its source run
- it contains no value the secret scrubber redacts
- every table it names is in the current catalog
- `schema_fact` and `dead_end` rest on at least two runs; a `business_rule`
  rests on one correction

A lesson that fails a gate is recorded in `dream_pass` as rejected, with the
reason.

## 6. Counting

A person's position on a lesson group is inferred, never asked for.

| Seen | Position |
|---|---|
| The person corrected toward a version | Supports it with strength `correction`. Their support for the other version is removed |
| The person got an answer from a version and moved on or built on it | Supports it with strength `use` |
| The person asked for a different measure | None. A separate lesson group |

One person holds one position per group, their most recent.

The active version is the one with the most `correction` positions. `use`
positions break a tie in `correction` positions and cannot outnumber them. If
still tied, the current active version stays.

The first correction of a lesson nobody has corrected is 1 to 0, so it takes
effect for the org at the next pass.

*Proposal:* a position expires 90 days after that person last used the
lesson. Without expiry, people who have left can hold a version in place.

## 7. Using lessons in a run

At the start of a turn the host adds a lessons section to the prompt, next to
the memory section, built the way `memoryPromptContext` builds its own:

1. Embed the user's message.
2. Take the active lessons closest to it, at most five.
3. Drop any lesson that names a table this person cannot query.
4. Add them to the prompt as known-good starting points, each with its id.
5. Record a `dream_lesson_use` row per lesson offered.

The agent still runs the query and sees the result. A lesson is a starting
point, not an answer, and the query still executes under the person's own
GraphJin identity.

When a `business_rule` lesson shaped the answer, the answer states the rule
it used ("revenue, net of returns"), so a person who expected the other
definition can see why the number differs.

The host adds lessons to the prompt; the agent does not search for them. That
is reliable, and it gives the eval one switch to turn off for the twin.

## 8. Repair with no person

| Event | Effect |
|---|---|
| A query built on a lesson fails | The use is marked `failed` at once; the next pass retires the lesson |
| A person corrects an answer that used a lesson | The use is marked `corrected`; the next pass recounts the group |
| The catalog revision changes | Lessons naming a changed or missing table are retired by the next pass |
| A lesson goes 90 days unused | It is retired |
| A retired lesson is proposed again from runs older than its retirement | Rejected |

A `failed` mark takes the lesson out of the prompt before the next pass runs,
so one schema change costs one failed call, not one per run until the pass.

## 9. Unattended runs *(proposal)*

Scheduled workflows and metric refreshes have no thread, so nobody corrects
them.

- `query`, `schema_fact` and `dead_end` lessons are used in unattended runs.
  A wrong one fails loudly and is retired.
- A `business_rule` lesson is used in an unattended run only after it has
  been used in chat by at least one person who did not correct it. A wrong
  one would change a number silently.

Unattended runs are read by the pass like any other: their failed and
recovered calls teach `query`, `schema_fact` and `dead_end` lessons.

## 10. Cost and limits

- The pass is a host call, so it writes a `spend_ledger` row under the
  `dreaming` task.
- *Proposal:* a pass reads at most 200 settled runs; the rest wait for the
  next pass. One org has at most 2,000 active lessons; past that, the least
  recently used are retired.
- Five lessons per turn bounds the added prompt tokens.

## 11. What it does not do

- It does not change harness code. That is the separate, product-wide loop.
- It does not learn across orgs or send anything out of the install.
- It does not create tools or change which tools a turn gets.
- It does not replace skill lessons yet. The eval compares the two
  (evals/DREAMING.md §9), and that result decides.

## 12. Build order

Each step ends with named eval cases passing. The cases are written first.

| Step | Builds | Passes |
|---|---|---|
| 0 | Nothing. Measure failed and repeated GraphJin calls per run today | The baseline number |
| 1 | `dream_lesson`, `dream_lesson_use`, prompt injection, the access filter | Delivery cases with seeded lessons: `l04`, `l30`, `l31` |
| 2 | Extraction, the `dreaming` task, the pass for `query`, `schema_fact`, `dead_end`, the gates | `l01` to `l03`, `l20` to `l22`, `l34` |
| 3 | Failure marks, catalog-revision retirement | `l32` |
| 4 | Follow-up classification, `business_rule`, `dream_position`, counting | `l10` to `l17`, `l33` |
| 5 | Limits, expiry, unattended-run rule | `l40`, the provider tier, the skill-lessons comparison |

## 13. Open questions

1. **Table access check.** §7 step 3 needs the set of tables a person's role
   can query. How to read that from GraphJin per role has not been verified.
2. **Position expiry**, **unattended runs** and the **limits** above are
   proposals.
3. **Delay.** A correction reaches other people at the next pass, at least
   two hours later. Whether a correction should trigger a pass for that
   thread sooner is undecided.
4. **Question shapes.** Matching by embedding alone may merge two questions
   that differ in one word ("gross" and "net"). `l12` tests this; the fix may
   need the model to confirm a match.
