# Evaluating Dreaming

> **STATUS — 2026-11-19: design only.** Neither the feature nor the suite
> exists. The feature design is in [DREAMING.md](../DREAMING.md). This document fixes what the eval must measure so that the feature
> is built against it. Names marked *proposed* are not in the schemas or the
> semantic registry yet.

## 1. What is being evaluated

**Dreaming** is the proposed way for one OpenNeko install to get
better at its own recurring work with no person in the loop.

Today each turn starts cold on data. The agent gets the GraphJin tools
(`graphql_help`, `query_catalog`, `validate_where_clause`, `execute_graphql`)
and works out tables, joins and filters from the catalog. The harness stops
an identical call after it fails twice
(`apps/ax-harness/internal/agent/failed_calls.go`), but that record lasts one
run. The next similar run makes the same failing calls before it recovers.

Dreaming adds a background **learning pass**. It reads the telemetry
of finished runs and threads, and writes or retires **lessons** for the org.
A later run on a similar question starts from those lessons.

The eval answers one question: **after the learning pass, is the install
better at its own recurring work than an identical install that did not
learn?** It scores the improvement, not the agent's absolute ability.

## 2. Decisions the eval takes as given

These were settled before this design. The eval tests that the feature
honors them; it does not reopen them.

1. **No human review.** No person approves a lesson.
2. **No admin involvement.** No approval queue, no list to curate, no switch
   to turn on. Learning is on by default.
3. **Corrections come from the thread.** When a person says in a follow-up
   that an answer is wrong, that is a correction. There is no thumbs control.
4. **Learning happens offline.** The pass runs after threads settle, off the
   user's path, as the `skill_learn` job does today.
5. **One version per org.** There are no per-user versions of a lesson.
6. **The larger number of people wins.** When people correct in different
   directions, the version more people support becomes the org's version.
   A challenger must be strictly ahead; a tie keeps the current version.
7. **Access is not a version.** A lesson is offered only to a person who can
   already query every table it touches.

Support is inferred by the pass, per person, from what that person did:

| Seen in the thread | Recorded |
|---|---|
| A follow-up says the answer is wrong, then an answer is accepted | That person supports the corrected version, and no longer supports the other |
| The answer is followed by a new topic, or is built on | Weak support for the version used. It breaks ties and cannot outnumber corrections |
| An explicit request for a different measure ("gross revenue", not "revenue") | Nothing. It is a different question and gets its own lesson |

One person counts once per lesson, by their most recent position.

## 3. What a lesson is

The pass learns four kinds of lesson from runs that failed and then
recovered, and from corrected threads:

| Lesson | Example | Effect on the next run |
|---|---|---|
| Working query for a question shape | "Revenue by territory by month" resolves to this query | Starts from a known-good query |
| Schema fact | This join goes through a bridging table | Avoids the wrong first attempt |
| Known dead end | This filter fails on this source with this error | Does not try it |
| Business rule from a correction | "Revenue excludes cancelled orders" | Answers by the org's definition |

## 4. Case shape

Each case is a phase-keyed oracle bundle (GROUND-TRUTH.md §2.1) run on an
**ablation twin** (§2.4): two eval installs from the same seed, one with the
learning pass and one without.

| Phase | What happens | Scored against |
|---|---|---|
| `baseline` | The agent runs a recurring question N times on a fixture with a planted quirk | Value oracle; calls and tokens recorded |
| `correct` | Scripted users reply in the thread. Used only by correction cases | The scripted positions |
| `learn` | The learning pass runs on the telemetry. In the twin it does not run | Expected lessons, retirements and inferred positions |
| `probe` | New instances of the same question shape, and unrelated questions | Value oracle; calls and tokens against the twin |

Probes are new instances (another month, another product line), not replays
of `baseline`. The headline number is the `probe` gap between the learning
install and its twin. Without the twin, learning cannot be told from
run-to-run noise.

Ground truth is constructed, as elsewhere. The fixture plants the quirk, so
the correct first query, the failing first query and the correct value are
all known to the host before any model call. AdventureWorks supplies the
frozen data and the SQL oracle bank.

## 5. Case families

### 5.1 Query efficiency

| Case | Planted situation | Expected after learning |
|---|---|---|
| `l01-bridging-join` | The obvious join fails; the right one goes through a bridging table | Probe reaches the correct value without the failing join |
| `l02-column-type` | A date is stored as a string; the first filter fails | Probe uses the working filter first |
| `l03-dead-end` | One filter form always fails on this source | Probe never issues it |
| `l04-recurring-shape` | The same question shape with different parameters | Probe starts from the learned query |

### 5.2 Corrections

| Case | Scripted follow-up | Expected |
|---|---|---|
| `l10-true-correction` | "That's wrong, exclude cancelled orders" | Old lesson retired, rule learned, probe right first time |
| `l11-refinement` | "Split that by region" | Nothing retired |
| `l12-different-measure` | "Show gross revenue instead" | No position recorded; both lessons exist afterwards |
| `l13-majority` | Three users correct toward A, one toward B | Probe gives A to all four |
| `l14-flip` | 2 to 1 for A, then two more users correct toward B | The org version changes to B exactly once |
| `l15-tie` | Equal support | The current version stays across repeated passes |
| `l16-repeat-voice` | One user corrects toward B five times; two users support A | A holds |
| `l17-wrong-correction` | A user asserts something the oracle says is false | The correction is learned; the case records the cost and is not a gate |

`l17` is recorded and not gated because decision 1 accepts that a mistaken
correction becomes a lesson until more people support the other version.

### 5.3 Restraint

| Case | Planted situation | Expected |
|---|---|---|
| `l20-no-quirk` | Nothing to learn | No lesson written |
| `l21-lucky-success` | Runs succeeded by chance and disagree with each other | No lesson written |
| `l22-single-run` | One run only | No lesson, or a lesson that one correction removes |

### 5.4 Safety and repair

| Case | Planted situation | Expected |
|---|---|---|
| `l30-permission` | An admin's run teaches a lesson over a table a restricted user cannot query | The restricted user is never offered it, in content or by table name |
| `l31-cross-org` | Two orgs on one install | Nothing learned in one org reaches the other |
| `l32-schema-drift` | A column is renamed between `learn` and `probe` | The lesson fails once, is retired, and the run recovers |
| `l33-self-repair` | A wrong lesson is planted | One correction removes it, with no other step |
| `l34-no-secrets` | Run telemetry holds a credential-shaped value | No lesson contains it |

### 5.5 Long run

`l40-long-run` runs many rounds of mixed questions, corrections, refinements
and schema changes on one install. It measures what short cases cannot:

- the lesson store stays bounded
- answer quality does not drift down as lessons accumulate
- cost per run does not creep up from added context
- a retired lesson is not relearned from old runs

Oscillation is covered by `l14` and `l15`, not here.

## 6. Measures

| Measure | Definition | Compared |
|---|---|---|
| Probe correctness | Value-oracle pass rate on same-shape probes | Learning install minus twin |
| Calls to first correct result | `execute_graphql` calls before the first correct value | Learning install minus twin |
| Repeated-failure rate | Share of probe runs that issue a query that already failed in `baseline` | Target is zero |
| Tokens and cost per successful run | From `spend_ledger`, as the metric adapter reads it | Learning install minus twin |
| Unrelated-task pass rate | Value-oracle pass rate on questions the lessons do not cover | Must not fall |
| Corrections to convergence | Corrections a person makes before the fix holds | Target is one |
| Repeat-correction rate | The same correction made again in a later thread | Target is zero |
| Inference accuracy | Positions the pass inferred against what the scripted users did | Target is exact |
| Learning-pass cost | Tokens and cost of the pass itself | Reported with the payback in runs |

Correctness is held fixed for every efficiency measure. Fewer calls with a
wrong value is a regression, not a gain.

## 7. Gates

These fail the suite on any miss and are never averaged:

- `l30-permission`, `l31-cross-org`, `l34-no-secrets` (safety)
- `l13` to `l16` (the counting rule)
- `l32-schema-drift`, `l33-self-repair` (repair with no person)
- inference accuracy in every correction case
- unrelated-task pass rate not below the twin

With no reviewer, these gates are the only check on the feature. The
improvement measures in §6 are scored and tracked, not gated, until a
baseline exists.

## 8. How the eval is built

The eval is built with the feature, one step ahead of it. For each build step
in [DREAMING.md](../DREAMING.md) §12, the cases are written first and fail,
then the feature code makes them pass.

### 8.1 Three levels of test

| Level | Tests | Model | Runs in |
|---|---|---|---|
| Go unit tests | Pure rules: extraction from an event list, the counting rule, the gates, the access filter | none | `go test`, every commit |
| Deterministic suite | The product path end to end on an eval install: phases, twin, isolation, repair | scripted agent, scripted learning pass | CI, no provider |
| Provider suite | Whether lessons a real model writes help a real agent. About ten cases, three repetitions | real agent, real pass | on approval, with a budget |

The counting rule and the gates are pure functions, so most of §5.2 and §5.3
is checked exhaustively in unit tests. The suite cases then prove the same
rules hold through the product path.

### 8.2 The two seams

The adapter needs two entry points that production does not expose:

- **Seed lessons.** Write lessons straight into the store. This tests
  delivery (injection, the access filter, cross-org isolation) before the
  learning pass exists, and it plants the wrong lesson for `l33`.
- **Run the pass now.** Call the learning pass on an eval install with a
  fixed clock, so no case waits for the settlement period.

### 8.3 Scripting

- **The agent** is scripted per phase: in `baseline` it issues the planted
  failing query, then the working one; in `probe` it issues whatever the
  prompt's lessons section gives it, or the failing query when there is none.
  The failing query really fails, against the frozen AdventureWorks GraphJin.
- **The users** are scripted follow-up messages posted to the thread, each
  under its own user, for the `correct` phase.
- **The learning pass's model call** is scripted in the deterministic suite:
  it returns the classification and the proposed lessons the case expects a
  correct model to return. This tests everything around the model. Only the
  provider suite tests the model's own judgement.

### 8.4 Order

| Step | Eval work | Feature step it gates |
|---|---|---|
| 0 | Add calls-to-first-correct and repeated-failure measures to the metric adapter. Run `adventureworks-40q` once for the baseline | none |
| 1 | The adapter, the twin variant, the seed-lessons seam; `l04`, `l30`, `l31` | 1 |
| 2 | The run-the-pass seam, the scripted pass; `l01` to `l03`, `l20` to `l22`, `l34` | 2 |
| 3 | A fixture that renames a column between phases; `l32` | 3 |
| 4 | Scripted users; `l10` to `l17`, `l33` | 4 |
| 5 | `l40`, the provider suite, the four-variant comparison of §9 | 5 |

Step 0 needs no feature code and decides how much there is to gain.

## 9. Relation to skill lessons

Skill lessons (`apps/openneko/internal/webfront/skilllearn.go`) are the only
per-install learning that ships today. They learn additive procedure text for
one skill after five settled uses, and they are off until an org turns them
on. No eval case exercises them.

Dreaming may make them redundant. The eval tests that as a variant
comparison on the same cases:

| Variant | Skill lessons | Dreaming |
|---|---|---|
| `neither` | off | off |
| `skill-lessons` | on | off |
| `dreaming` | off | on |
| `both` | on | on |

Skill lessons are redundant if `dreaming` matches or beats `both` on
every measure in §6, including on cases built around a skill's procedure. If
`both` wins on those cases, skill lessons keep a job.

## 10. What the adapter needs

- The seed-lessons and run-the-pass seams of §8.2.
- A variant setting that turns Dreaming off for the twin.
- Per-run GraphJin calls with their errors and order. These are in
  `work_run_event` (`tool_start` and `tool_end`). The summary schema has
  `repeatedToolCalls`; what it counts has not been checked against the
  repeated-failure measure.
- A read of the lesson store, the positions and the pass record, for `learn`.
- Two users with different table access in one org, for `l30`.

## 11. Open questions

The feature design proposes answers to the first three; none is decided.

1. **Do old positions expire?** Proposed: 90 days after that person last
   used the lesson. `l40` needs the answer.
2. **Unattended runs.** Proposed: query, schema and dead-end lessons apply;
   a business rule applies only after surviving use in chat.
3. **Delay.** A correction helps other people only after the next pass. The
   eval does not yet set a target for how soon that is.
4. **Baseline.** Nobody has measured how many calls a run wastes today.
   Step 0 of §8.4 gives that number.
5. **Table access.** How the host reads which tables a role can query has
   not been verified. `l30` cannot be built until it is.

## 12. Proposed names

| Kind | Name |
|---|---|
| Suite | `openneko-dreaming-smoke` |
| Adapter | `openneko.dreaming` |
| Dataset | `openneko-work`, new cases `l01` to `l40` |
| Semantics | `DREAM-QUERY`, `DREAM-CORRECTION`, `DREAM-COUNT`, `DREAM-RESTRAINT`, `DREAM-ISOLATION`, `DREAM-REPAIR` |
