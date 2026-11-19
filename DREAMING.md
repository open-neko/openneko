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
8. Nothing expires with time. A lesson or a position stays until a
   correction, a recount or a schema change removes it.
9. Every kind of run uses every kind of lesson, unattended runs included.
10. There is no cap on how many lessons an org holds or how many runs a pass
    reads.
11. A query that fails because the person lacks access does not count against
    the lesson.

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
| `dream_lesson_use` | run and lesson | `run_id`, `lesson_id`, `offered`, `outcome` (`succeeded`, `failed_source`, `failed_access`, `corrected`) |
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
- retire a lesson that a correction replaced, or that no longer matches the
  source (§8)

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

A position does not expire. *Proposal:* a position counts only while its
person is a member of the org, so people who have left do not hold a version
in place. That is a membership rule, not a time limit.

## 7. Using lessons in a run

At the start of a turn the host adds a lessons section to the prompt, next to
the memory section, built the way `memoryPromptContext` builds its own:

1. Embed the user's message.
2. Take the active lessons closest to it (see "Selection" below).
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

**Selection.** The store has no cap, so a large org may hold many thousands
of lessons. A turn cannot carry them all, and most have nothing to do with
the question asked. So each turn takes only the lessons closest to its
question. This is a choice by relevance to one question, made fresh each
turn. It never removes a lesson or ranks one above another in the store.
*Proposal:* the closest five. The number is the one bound left in the
design, and the scale case in the eval tests it.

## 8. Repair with no person

A lesson is removed for one of two reasons only: people corrected it, or the
source no longer matches it.

| Event | Effect |
|---|---|
| A person corrects an answer that used a lesson | The use is marked `corrected`; the next pass recounts the group |
| A query built on a lesson fails | The host checks the lesson against the source (below) |
| The catalog revision changes | The next pass checks every lesson that names a changed or missing table |
| A retired lesson is proposed again from runs older than its retirement | Rejected |

**Why a failure alone does not retire a lesson.** A query can fail because
the person asking has no access to a table: someone outside HR asking an HR
question. The lesson is still right for the people who do have access.

So when a query built on a lesson fails, the host checks the lesson itself,
not the person's result: it validates the lesson's query against the current
catalog under the service identity.

| Check result | Meaning | Effect |
|---|---|---|
| The lesson still validates | The failure was this person's access, or a passing fault | The lesson stays. The use is marked `failed_access` and counts for nothing |
| The lesson no longer validates | The source changed | The lesson leaves the prompt at once and the next pass retires it |

The check reads the catalog, not the error text, so it does not depend on
how a source words its errors.

Decision 7 means a person should seldom be offered a lesson they cannot use.
The check covers the cases where the filter is wrong or access changed
between the filter and the call.

## 9. Unattended runs

Scheduled workflows and metric refreshes use every kind of lesson, as chat
does. The pass reads them like any other run, and their failed and recovered
calls teach lessons.

They have no thread, so nobody corrects them there. A wrong business rule in
an unattended run is fixed when someone corrects the same question in chat,
and the recount then applies to the unattended run too.

## 10. Cost and scale

- The pass is a host call, so it writes a `spend_ledger` row under the
  `dreaming` task.
- There is no cap on lessons per org or on runs per pass. A pass reads every
  settled run since its watermark, in batches, until none is left.
- What keeps a large org workable is selection (§7): the cost of a turn
  depends on how many lessons it takes, not on how many the org holds. The
  lesson search needs an index that stays fast as the store grows.

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
| 3 | The source check on failure, catalog-revision retirement | `l32`, `l35` |
| 4 | Follow-up classification, `business_rule`, `dream_position`, counting | `l10` to `l17`, `l33` |
| 5 | Batching, the lesson search index at scale | `l40`, `l41`, the provider tier, the skill-lessons comparison |

## 13. Open questions

1. **Table access check.** §7 step 3 needs the set of tables a person's role
   can query. How to read that from GraphJin per role has not been verified.
2. **Service-identity validation.** §8 needs the host to validate a query
   against the catalog without running it for the person. Whether GraphJin
   offers that for a whole query has not been verified.
3. **Members only.** Whether positions of people who left the org still
   count (§6) is a proposal.
4. **Lessons per turn.** Five is a proposal (§7).
5. **Delay.** A correction reaches other people at the next pass, at least
   two hours later. Whether a correction should trigger a pass for that
   thread sooner is undecided.
6. **Question shapes.** Matching by embedding alone may merge two questions
   that differ in one word ("gross" and "net"). `l12` tests this; the fix may
   need the model to confirm a match.
