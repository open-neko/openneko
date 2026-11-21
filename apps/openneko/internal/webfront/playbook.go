package webfront

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/open-neko/neko/apps/openneko/internal/llm"
)

// The playbook is the org's list of short rules for every run. A rule comes
// from a mined test that failed, and it becomes active only when the test
// passes with it and the tests that passed without it still pass.
const (
	playbookItemMax     = 400
	playbookCheckTests  = 4
	playbookProposeMax  = 3
	playbookAnswerShown = 2000
)

var playbookReflector = llm.Program{
	Signature:   `playbookInput:string "a question, the agent's answer, the right value and the agent's queries" -> playbookRule:string "a JSON object"`,
	Instruction: "You write one short rule that would have led the agent to the right answer. Reply with JSON only. No tools.",
}

const playbookRules = `Return only JSON of this shape: {"rule": "one or two sentences, or empty"}

Rules:
- The rule must help every question of this kind, not only this one. Name the tables, columns, filters or business terms it is about.
- Write the rule as an instruction to the agent, in plain words.
- Leave out the right value and any figure from the answer.
- Return {"rule": ""} when no general rule explains the miss.`

func playbookSection(items []string) string {
	if len(items) == 0 {
		return ""
	}
	return section("playbook", "items", "- "+strings.Join(items, "\n- "))
}

func (a *API) activePlaybook(ctx context.Context, orgID string) ([]string, error) {
	rows, err := a.DB.Query(ctx, `select body from playbook_item where org_id = $1 and state = 'active' order by decided_at, id`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			return nil, err
		}
		out = append(out, body)
	}
	return out, rows.Err()
}

// playbookContext is the playbook block of a turn prompt, or "".
func (a *API) playbookContext(ctx context.Context, orgID string) string {
	items, err := a.activePlaybook(ctx, orgID)
	if err != nil {
		log.Printf("[playbook] org=%s: %v", orgID, err)
		return ""
	}
	return playbookSection(items)
}

func playbookInput(t minedTest, r minedReplay) string {
	var b strings.Builder
	b.WriteString(playbookRules + "\n\nQuestion: " + t.Question + "\n")
	if t.Correction != "" {
		b.WriteString("A person corrected an earlier answer to this question: " + t.Correction + "\n")
	}
	fmt.Fprintf(&b, "Right value: %s\nAgent's answer: %s\nAgent's queries:\n", r.Oracle, jsSlice(answerFence.ReplaceAllString(r.Answer, ""), playbookAnswerShown))
	for _, q := range r.Calls {
		b.WriteString("- " + jsSlice(q, 800) + "\n")
	}
	return b.String()
}

func parsePlaybookRule(raw string) string {
	var out struct {
		Rule string `json:"rule"`
	}
	start, end := strings.Index(raw, "{"), strings.LastIndex(raw, "}")
	if start < 0 || end <= start || json.Unmarshal([]byte(raw[start:end+1]), &out) != nil {
		return ""
	}
	rule := jsTrim(out.Rule)
	if len(rule) > playbookItemMax || learnedSecret.MatchString(rule) || learnedEmail.MatchString(rule) {
		return ""
	}
	return rule
}

// PlaybookSweep runs the learning pass of every org with an enabled source.
func (a *API) PlaybookSweep(ctx context.Context) error {
	rows, err := a.DB.Query(ctx, `select distinct org_id from data_source where enabled = true order by org_id`)
	if err != nil {
		return err
	}
	var orgs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		orgs = append(orgs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, org := range orgs {
		res, err := a.PlaybookLearn(ctx, org)
		if err != nil {
			log.Printf("[playbook] org=%s: %v", org, err)
			continue
		}
		log.Printf("[playbook] org=%s mined=%d replayed=%d passed=%d rules=%d", org, res.Mine.Mined, res.Replayed, res.Passed, len(res.Items))
	}
	return ctx.Err()
}

// PlaybookResult is what one learning pass did.
type PlaybookResult struct {
	Mine     MineResult       `json:"mine"`
	Replayed int              `json:"replayed"`
	Passed   int              `json:"passed"`
	Items    []PlaybookChoice `json:"items"`
}

// PlaybookChoice is the decision on one proposed rule.
type PlaybookChoice struct {
	Rule     string `json:"rule"`
	Source   string `json:"sourceRunId"`
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
}

type playbookDeps struct {
	replay  func(t minedTest, playbook []string) (minedReplay, error)
	propose func(input string) (string, error)
}

// PlaybookLearn mines new tests, runs the tests that never ran, and tries
// one rule for each failed test that has none yet.
func (a *API) PlaybookLearn(ctx context.Context, orgID string) (PlaybookResult, error) {
	return a.playbookLearn(ctx, orgID, playbookDeps{
		replay: func(t minedTest, playbook []string) (minedReplay, error) {
			return a.replayMinedTest(ctx, orgID, t, playbook)
		},
		propose: func(input string) (string, error) {
			out, err := a.hostCalls().Generate(ctx, orgID, "playbook", playbookReflector, map[string]any{"playbookInput": input})
			if err != nil {
				return "", err
			}
			return jsValueString(out.Outputs["playbookRule"]), nil
		},
	})
}

func (a *API) playbookLearn(ctx context.Context, orgID string, d playbookDeps) (PlaybookResult, error) {
	var res PlaybookResult
	var err error
	if res.Mine, err = a.mineTests(ctx, orgID); err != nil {
		return res, err
	}
	tests, err := a.loadMinedTests(ctx, orgID)
	if err != nil {
		return res, err
	}
	playbook, err := a.activePlaybook(ctx, orgID)
	if err != nil {
		return res, err
	}
	var passing []minedTest
	var failing []struct {
		t minedTest
		r minedReplay
	}
	for _, t := range tests {
		var ran bool
		var pass *bool
		if err := a.DB.QueryRow(ctx, `select last_run_at is not null, last_pass from mined_test where run_id = $1::uuid`, t.RunID).Scan(&ran, &pass); err != nil {
			return res, err
		}
		if ran {
			if pass != nil && *pass {
				passing = append(passing, t)
			}
			continue
		}
		r, err := d.replay(t, playbook)
		if err != nil {
			log.Printf("[playbook] org=%s test=%s: %v", orgID, t.RunID, err)
			continue
		}
		res.Replayed++
		if _, err := a.DB.Exec(ctx, `update mined_test set last_pass = $2, last_queries = $3, last_run_at = now() where run_id = $1::uuid`,
			t.RunID, r.Pass, r.Queries); err != nil {
			return res, err
		}
		if r.Pass {
			res.Passed++
			passing = append(passing, t)
		} else {
			failing = append(failing, struct {
				t minedTest
				r minedReplay
			}{t, r})
		}
	}
	for _, f := range failing[:min(len(failing), playbookProposeMax)] {
		choice, err := a.tryPlaybookRule(ctx, orgID, d, playbook, f.t, f.r, passing)
		if err != nil {
			return res, err
		}
		if choice.Decision == "active" {
			playbook = append(playbook, choice.Rule)
		}
		res.Items = append(res.Items, choice)
	}
	return res, nil
}

// tryPlaybookRule proposes a rule from a failed test and keeps it when the
// test passes with it and the checked passing tests still pass.
func (a *API) tryPlaybookRule(ctx context.Context, orgID string, d playbookDeps, playbook []string, t minedTest, r minedReplay, passing []minedTest) (PlaybookChoice, error) {
	choice := PlaybookChoice{Source: t.RunID}
	raw, err := d.propose(playbookInput(t, r))
	if err != nil {
		return choice, err
	}
	if choice.Rule = parsePlaybookRule(raw); choice.Rule == "" {
		choice.Decision, choice.Reason = "none", "no general rule"
		return choice, nil
	}
	with := append(append([]string{}, playbook...), choice.Rule)
	evidence := map[string]any{"source": map[string]any{"before": r.Pass, "queriesBefore": r.Queries}}
	decide := func(state, reason string) (PlaybookChoice, error) {
		choice.Decision, choice.Reason = state, reason
		raw, _ := json.Marshal(evidence)
		_, err := a.DB.Exec(ctx, `insert into playbook_item (org_id, body, state, source_run_id, evidence, decided_at)
			values ($1, $2, $3, $4::uuid, $5::jsonb, now())`, orgID, choice.Rule, map[bool]string{true: "active", false: "rejected"}[state == "active"],
			t.RunID, string(raw))
		return choice, err
	}
	after, err := d.replay(t, with)
	if err != nil {
		return choice, err
	}
	evidence["source"] = map[string]any{"before": r.Pass, "after": after.Pass, "queriesBefore": r.Queries, "queriesAfter": after.Queries}
	if !after.Pass {
		return decide("rejected", "the failed test still fails with the rule")
	}
	var checked []map[string]any
	for _, p := range passing[:min(len(passing), playbookCheckTests)] {
		got, err := d.replay(p, with)
		if err != nil {
			return choice, err
		}
		checked = append(checked, map[string]any{"runId": p.RunID, "pass": got.Pass, "queries": got.Queries})
		if !got.Pass {
			evidence["checked"] = checked
			return decide("rejected", "a test that passed before fails with the rule")
		}
	}
	evidence["checked"] = checked
	return decide("active", "the failed test passes and the checked tests still pass")
}
