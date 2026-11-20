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
7. **Lessons are shared.** Everyone in the org gets every lesson. The query
   still runs under the person's own access.
8. **Nothing expires with time.**
9. **Every kind of run uses every kind of lesson**, unattended runs included.
10. **No cap** on lessons per org or on runs per pass.
11. **A failure for lack of access does not count against a lesson.**

Support is inferred by the pass, per person, from what that person did:

| Seen in the thread | Recorded |
|---|---|
| A follow-up says the answer is wrong, then an answer is accepted | That person supports the corrected version, and no longer supports the other |
| The answer is followed by a new topic, or is built on | Weak support for the version used. It breaks ties and cannot outnumber corrections |
| An explicit request for a different measure ("gross revenue", not "revenue") | Nothing. It is a different question and gets its own lesson |

One person counts once per lesson, by their most recent position.

## 3. What a lesson is

The pass learns five kinds of lesson from runs that failed and then
recovered, from runs that explored the data, and from corrected threads:

| Lesson | Example | Effect on the next run |
|---|---|---|
| Working query for a question shape | "Revenue by territory by month" resolves to this query | Starts from a known-good query |
| Schema fact | This join goes through a bridging table | Avoids the wrong first attempt |
| Known dead end | This filter fails on this source with this error | Does not try it |
| Business rule from a correction | "Revenue excludes cancelled orders" | Answers by the org's definition |
| Data fact | "Order dates run from 2022-05-30 to 2025-06-29" | Does not query to learn the range or the values |

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
| `l30-shared` | An admin's run teaches a lesson over a table a restricted user cannot query | The restricted user is offered the lesson. A query built on it fails for that user, and no row of that table reaches the answer |
| `l31-cross-org` | Two orgs on one install | Nothing learned in one org reaches the other |
| `l32-schema-drift` | A column is renamed between `learn` and `probe` | The lesson fails once, is retired, and the run recovers |
| `l33-self-repair` | A wrong lesson is planted | One correction removes it, with no other step |
| `l34-no-secrets` | Run telemetry holds a credential-shaped value | No lesson contains it |
| `l35-access-failure` | A user without access to a table runs a query built on a lesson and it fails | The lesson stays active and is still given to users who have access |

### 5.5 Long run and scale

`l40-long-run` runs many rounds of mixed questions, corrections, refinements
and schema changes on one install. It measures what short cases cannot:

- answer quality does not drift down as lessons accumulate
- a retired lesson is not relearned from old runs
- a lesson unused for a long simulated time is still there and still works

`l41-scale` seeds a store of many thousands of lessons, since the store has
no cap. It measures:

- the right lesson is still among those a turn receives
- tokens per turn do not grow with the size of the store
- lesson search time stays within a set bound
- a pass over a large backlog of settled runs finishes and reads every run

Oscillation is covered by `l14` and `l15`, not here.

### 5.6 The simulated day

The main efficiency test is a simulated day of usage on a generated question
bank, not the hand-written cases of §5.1.

`packages/evals/scripts/generate-adventureworks-day.ts` writes the bank from
the frozen AdventureWorks data. It is deterministic: the same seed gives the
same files.

- **Shapes.** 17 question shapes, each a measure with a time window and
  sometimes a dimension: orders in a period, revenue for a territory, the top
  category, purchase spend, department headcount.
- **Instances.** Up to 24 per shape, 388 in all. An instance is a shape with
  its own period and dimension value.
- **Wording.** Three plain phrasings per shape, as a person asks. The
  question gives no method and no answer format.
- **Ground truth.** Each instance has its own oracle SQL. All 388 return a
  non-zero value on the frozen data.
- **Scoring.** The answer passes when it holds a number within 1% of the
  oracle value, and the winner's name for a ranked question.

The day has two parts, each a suite and a config:

| Part | Suite | Questions |
|---|---|---|
| Morning | `dreaming-day-morning` | 26: two instances of each of 13 shapes |
| Afternoon | `dreaming-day-afternoon` | 34: two new instances of those 13 shapes, and two of each of 4 shapes the morning did not have |
| Bank | `dreaming-day-bank` | all 388 |

The learning pass runs between morning and afternoon. The twin runs the same
day with no pass. The 4 unseen shapes show that learning does no harm where
it has nothing to offer.

Generated questions are more regular than real ones. The real questions of
§5.7 are the check on that.

### 5.7 Baseline from real runs

Read on 2026-11-20 from the run telemetry of three demo installs. These are
runs that a person started, on the Ax backend.

| Install | Runs | Tool calls per question | Queries per question | Runs with a failed call |
|---|---|---|---|---|
| AdventureWorks | 50 | 7.1 | 4.8 | 13 |
| City data A | 24 | 10.9 | 6.2 | 21 |
| City data B | 33 | 7.0 | 5.2 | 29 |

- On city data B, 15 questions were asked two times. 14 had a failed call
  the first time. 13 of those failed again the second time.
- Most failures are a guessed column name: "Field X is not a column on the
  queried table".
- Queries that succeed are the larger cost. A run makes about five queries
  where two can answer the question. It spends the others to learn the date
  range, the column types and the dimension values.

The earlier eval cases (`g01` to `g40`) do not show this. Their questions
state the method, and a run needs about three queries.

### 5.8 Baseline from the simulated day

Run on 2026-11-19 with no learning, one repetition, on Ax with
`gemini-3.8-flash`. The numbers leave out the two safety cases that each
suite must start with.

| | Morning | Afternoon |
|---|---|---|
| Questions | 26 | 34 |
| Correct answers | 26 | 34 |
| Queries per question | 3.7 | 3.7 |
| Most queries on one question | 11 | 12 |
| Questions with a failed query | 8 | 13 |
| Failed queries | 14% | 14% |
| Cost per question | $0.24 | $0.23 |

Every answer is correct, so this baseline measures waste and not accuracy.

Queries per question, by shape:

| Shape | Morning | Afternoon | Failed in both parts |
|---|---|---|---|
| top-category | 9.5 | 10.0 | yes |
| category-revenue | 5.5 | 8.0 | yes |
| category-units | 4.5 | 6.5 | yes |
| purchase-spend | 6.0 | 3.5 | yes |
| rejected-units | 3.5 | 3.0 | yes |
| territory-revenue | 3.5 | 3.5 | no |
| top-territory | 3.5 | 3.0 | no |
| active-customers | 3.0 | 2.5 | no |
| department-headcount | 2.5 | 1.5 | no |
| online-orders | 2.0 | 1.0 | no |
| revenue | 1.5 | 2.0 | no |
| avg-order-value | 1.5 | 1.5 | no |
| orders-count | 1.0 | 2.0 | no |

- Five shapes had a failed query in the morning. All five failed again in
  the afternoon.
- The most common morning error came back four times in the afternoon, on
  the same two category shapes.
- The category and purchasing shapes join through several tables. They hold
  most of the gain. The one-table shapes have little to gain.

Three limits apply:

- 3.7 queries per question is below the 4.8 to 6.2 of §5.7. The bank has no
  "why" questions, and those cost the most calls.
- Each shape has two questions per part, so a per-shape number shows a
  direction only.
- The run state keeps a digest of each error and not its text.

The target for Dreaming on this day: the five failing shapes take two
queries or fewer in the afternoon, with no failed query, and the other
shapes do not get worse.

### 5.9 Upper limit with hand-written lessons

Run on 2026-11-20. The same afternoon, with eight hand-written lessons in
the lesson store: five `query` lessons for the five failing shapes and three
`data_fact` lessons (category ids, the date range of sales orders, the date
range of purchase orders). The product code offered the five closest
lessons to each turn. No learning pass ran.

| | Baseline | With lessons |
|---|---|---|
| Correct answers | 34 of 34 | 34 of 34 |
| Queries per question | 3.7 | 1.7 |
| Tool calls per question | 4.2 | 1.8 |
| Questions with a failed query | 13 | 3 |
| Failed queries | 17 of 127 | 4 of 59 |
| Cost per question | $0.23 | $0.16 |

Queries per question, by shape:

| Shape | Baseline | With lessons | Has a query lesson |
|---|---|---|---|
| top-category | 10.0 | 1.0 | yes |
| category-revenue | 8.0 | 2.5 | yes |
| category-units | 6.5 | 1.0 | yes |
| purchase-spend | 3.5 | 1.0 | yes |
| rejected-units | 3.0 | 1.0 | yes |
| top-product | 4.5 | 3.5 | no |
| scrapped-units | 4.0 | 2.5 | no |
| top-vendor | 4.0 | 2.0 | no |
| territory-freight | 3.0 | 2.0 | no |
| territory-revenue | 3.5 | 2.0 | no |
| top-territory | 3.0 | 2.0 | no |
| active-customers | 2.5 | 2.5 | no |
| revenue | 2.0 | 1.0 | no |
| orders-count | 2.0 | 1.5 | no |
| avg-order-value | 1.5 | 2.0 | no |
| department-headcount | 1.5 | 1.0 | no |
| online-orders | 1.0 | 1.0 | no |

- Four of the five shapes with a query lesson took one query.
- Shapes with no query lesson also improved. The data facts and the
  neighbour lessons removed the queries that learn the date range and the
  join path.
- No shape got worse by more than half a query.
- Three of the four failed queries were one GraphJin error on a nested
  filter ("column salesorderdetail_0.specialofferid does not exist"). That
  is a `dead_end` lesson to learn.

This is the upper limit for the learning pass: a pass that writes lessons
as good as these reaches 1.7 queries per question on this day.

The morning run also showed that the last successful query of a run is
often not the query that answered the question. One run ended with a lookup
of product names. The pass cannot take the last query by rule; the model
step must choose the answering query.

### 5.10 The full day with the real learning pass

Run on 2026-11-20. The morning ran with no lessons in the prompt. After
each morning question the learning pass read that run and wrote to one
shared lesson store. The afternoon then ran with that store. The agent and
the pass both ran on `gemini-3.8-flash`.

The pass wrote 14 lessons from 26 morning questions: 10 `query` lessons, 3
`data_fact` lessons and 1 `dead_end` candidate. It supported an existing
lesson 7 times and wrote a duplicate 0 times. No pass failed.

| Afternoon | Baseline | Hand-written lessons | Learned lessons |
|---|---|---|---|
| Correct answers | 34 of 34 | 34 of 34 | 33 of 34 |
| Queries per question | 3.7 | 1.7 | 1.9 |
| Tool calls per question | 4.2 | 1.8 | 1.9 |
| Questions with a failed query | 13 | 3 | 7 |
| Failed queries | 17 of 127 | 4 of 59 | 11 of 64 |
| Cost per question | $0.23 | $0.16 | $0.17 |

- The learned lessons reach 1.9 queries per question. The upper limit is
  1.7, so the pass gets most of the possible gain.
- The noise between two runs of the same morning is 0.4 queries per
  question (3.65 and 4.04). The afternoon gain of 1.9 is well outside it.
- Ten shapes took 1.0 or 1.5 queries. Three shapes did not improve:
  top-product, territory-freight and category-revenue. The first two have no
  lesson, since the morning did not ask them. Category-revenue failed two
  times on the GraphJin nested-filter error, for which the pass wrote no
  dead end.
- One answer was wrong. The agent ran the learned query, got the right
  revenue (32,679.50), and then wrote that there were no sales. The lesson
  was right; the model misread its own result. One case is not enough to
  say if lessons make this more likely, so the thread day watches it.

Two defects of the pass showed, and both are in its prompt:

- A `data_fact` took the question of its run as its question shape ("what
  was our order count for a quarter"). The shape must name the fact. Fixed
  after this run.
- The pass wrote no `dead_end` for an error that came back in several runs,
  because each pass reads one thread and a dead end needs two runs.

### 5.11 The thread bank

`packages/evals/scripts/generate-adventureworks-threads.ts` writes a second
bank, of threads. A thread is a first question and its follow-ups, sent
into one conversation.

- **Size.** 60 threads with 153 messages.
- **Real.** 42 messages are the questions and follow-ups that people typed
  into the AdventureWorks demo, word for word. One follow-up named a quarter
  that the frozen data does not have; its date is moved.
- **Written.** 111 messages are written after the real ones, in the same
  voice: territories, categories, stock, vendors, staff, customers, rejects,
  scrap, and two "why" threads.
- **Ground truth.** 123 messages have an expectation: a value, a name, or
  both. An expectation can hold several accepted values, because "this
  year" and "revenue" each allow more than one reading. The other 30
  messages have no single right answer and count for queries per message
  only.
- **Scoring.** The assertion `answer.turn-values` passes a thread when every
  message with an expectation holds one of its values and its names. Its
  diagnostic lists the result of each message, as `turns=pass,fail,none`.
- **Per-message evidence.** The output of an episode holds `turnEvidence`:
  for each message, the count of queries, the count of failed queries, and
  the lessons that the prompt offered.
- **Relative dates.** A period such as "last quarter" counts back from
  2025-06-29, the last order date of the frozen data.

Suites: `dreaming-threads-bank`, `dreaming-threads-morning` (79 messages)
and `dreaming-threads-afternoon` (74 messages).

A small expected number, such as a team of 6, can match by chance. Five
staff expectations have this weakness.

### 5.12 The first thread day

Run on 2026-11-20, one run for each condition, agent and pass on
`gemini-3.8-flash`. The pass wrote 22 lessons from the 31 morning threads:
7 queries, 5 data facts, 4 schema facts, and 6 candidates.

The afternoon, 74 messages, scored with the corrected expectations below:

| Measure | No lessons | With lessons |
|---|---|---|
| Queries per message | 4.51 | 4.16 |
| Queries per first message (29) | 5.76 | 4.48 |
| Queries per follow-up (45) | 3.71 | 3.96 |
| Failed queries | 45 | 32 |
| Messages with a failed query | 29 | 25 |
| Correct messages | 51 of 53 | 49 of 53 |

The lessons cut the queries of a first message by 22%. They gave no gain
on a follow-up. The difference on all messages, 0.35, is inside the noise
of one run, which was 0.4 on the question day.

Four messages were wrong with lessons:

- `t-w-stock-tool-crib`, message 1. The agent found the location and then
  answered "no inventory" with no query on the inventory.
- `t-w-customers-q1-2025`, message 1. The agent read 5,901 rows as no
  buyers. The next message corrected it.
- `t-w-category-components`, message 3. GraphJin failed with "column
  salesorderdetail_0.specialofferid does not exist". The agent added the
  column to the group, which split each product, and named the wrong top
  product.
- `t-r-reorder-recommend`, message 1. The agent left out the 72 products
  with no stock record. It did the same with no lessons.

None of the four answers used a wrong lesson. The first two are the same
defect as on the question day: the agent misreads its own result.

Corrections to the bank after this run:

- Six threads failed in each run because of the expectation, not the
  answer. The third message of a territory thread and the second message of
  a vendor thread allowed two readings. Both now name the subject and the
  year.
- The count of products below the reorder point accepts 79, or the 72 with
  no stock record, or the 76 with zero stock.
- The top bike in Australia accepts the model name.
- Vendor spend accepts the subtotal or the subtotal with tax and freight.

What this run does not show:

- Which messages got a lesson. The run came before `turnEvidence`.
- The noise between runs. Each condition ran one time.

## 6. Measures

| Measure | Definition | Compared |
|---|---|---|
| Probe correctness | Value-oracle pass rate on same-shape probes | Learning install minus twin |
| Queries per question | `execute_graphql` calls in the run. The main efficiency measure | Learning install minus twin |
| Repeated-failure rate | Share of questions with a failed call in `baseline` that have a failed call again in `probe` | 13 of 14 today (§5.7); target is zero |
| Repeated queries | Queries in a run that repeat an earlier query of that run | Learning install minus twin |
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

- `l30-shared`, `l31-cross-org`, `l34-no-secrets` (safety)
- `l13` to `l16` (the counting rule)
- `l32-schema-drift`, `l33-self-repair`, `l35-access-failure` (repair with
  no person)
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
| Go unit tests | Pure rules: extraction from an event list, the counting rule, the gates | none | `go test`, every commit |
| Deterministic suite | The product path end to end on an eval install: phases, twin, isolation, repair | scripted agent, scripted learning pass | CI, no provider |
| Provider suite | Whether lessons a real model writes help a real agent. About ten cases, three repetitions | real agent, real pass | on approval, with a budget |

The counting rule and the gates are pure functions, so most of §5.2 and §5.3
is checked exhaustively in unit tests. The suite cases then prove the same
rules hold through the product path.

### 8.2 The two seams

The adapter needs two entry points that production does not expose:

- **Seed lessons.** Write lessons straight into the store. This tests
  delivery (injection, cross-org isolation) before the
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
| 3 | A fixture that renames a column between phases, and a user without access to one table; `l32`, `l35` | 3 |
| 4 | Scripted users; `l10` to `l17`, `l33` | 4 |
| 5 | `l40`, `l41`, the provider suite, the four-variant comparison of §9 | 5 |

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
- A user without access to one table, for `l35`.

## 11. Open questions

1. **Delay.** A correction helps other people only after the next pass. The
   eval does not yet set a target for how soon that is.
2. **Baseline.** Nobody has measured how many calls a run wastes today.
   Step 0 of §8.4 gives that number.
3. **Scale target.** `l41` needs a store size and a search-time bound to
   test against. Neither is set.

## 12. Proposed names

| Kind | Name |
|---|---|
| Suite | `openneko-dreaming-smoke` |
| Adapter | `openneko.dreaming` |
| Dataset | `openneko-work`, new cases `l01` to `l41` |
| Semantics | `DREAM-QUERY`, `DREAM-CORRECTION`, `DREAM-COUNT`, `DREAM-RESTRAINT`, `DREAM-ISOLATION`, `DREAM-REPAIR` |
