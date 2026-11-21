package webfront

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
)

type policyProposal struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Rule       storedJSON        `json:"rule"`
	ProposedBy *string           `json:"proposedBy"`
	RunID      *string           `json:"runId"`
	CreatedAt  string            `json:"createdAt"`
	Replaces   *policyRuleBrief  `json:"replaces"`
	Overrides  []policyRuleBrief `json:"overrides"`
}

type policyRuleBrief struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Mode     string `json:"mode"`
	Priority int    `json:"priority"`
}

// policyProposals is GET /api/v1/policies/proposals: each pending rule with
// the rule of the same name that it replaces and the enabled rules that it
// outranks for the same kinds and scopes.
func (a *API) policyProposals(w http.ResponseWriter, r *http.Request) {
	act, ok := a.accessAdmin(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	rows, err := a.DB.Query(ctx, `select p.id::text, p.name, p.rule::text, coalesce(u.email, p.proposed_by_user_id), p.proposed_by_run_id::text, p.created_at
		from action_policy_proposal p left join app_user u on u.id = p.proposed_by_user_id
		where p.org_id = $1 and p.status = 'pending' order by p.created_at desc`, act.OrgID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	proposals, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (policyProposal, error) {
		var p policyProposal
		var created time.Time
		var rule string
		err := row.Scan(&p.ID, &p.Name, &rule, &p.ProposedBy, &p.RunID, &created)
		p.Rule, p.CreatedAt = storedJSON(rule), jsTime(created)
		return p, err
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	rows, err = a.DB.Query(ctx, `select `+actionPolicyColumns+` from action_policy where org_id = $1 order by priority, name`, act.OrgID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	policies, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (actionPolicy, error) { return scanActionPolicy(row) })
	if err != nil {
		a.fail(w, r, err)
		return
	}
	for i := range proposals {
		proposals[i].Replaces, proposals[i].Overrides = proposalEffects(proposals[i], policies)
	}
	if proposals == nil {
		proposals = []policyProposal{}
	}
	sendJSON(w, http.StatusOK, map[string]any{"proposals": proposals})
}

func proposalEffects(p policyProposal, policies []actionPolicy) (*policyRuleBrief, []policyRuleBrief) {
	rule, _ := jsParse(p.Rule)
	kinds, scopes := jsStrings(jsGet(rule, "appliesToKinds")), jsStrings(jsGet(rule, "appliesToScopes"))
	priority := int(numberOf(jsGet(rule, "priority")))
	overlaps := func(a, b []string) bool {
		return len(a) == 0 || len(b) == 0 || slices.ContainsFunc(a, func(x string) bool { return slices.Contains(b, x) })
	}
	var replaces *policyRuleBrief
	overrides := []policyRuleBrief{}
	for _, q := range policies {
		brief := policyRuleBrief{ID: q.ID, Name: q.Name, Mode: q.Mode, Priority: q.Priority}
		if q.Name == p.Name {
			replaces = &brief
			continue
		}
		if q.Enabled && q.Priority >= priority && overlaps(kinds, q.AppliesToKinds) && overlaps(scopes, q.AppliesToScopes) {
			overrides = append(overrides, brief)
		}
	}
	return replaces, overrides
}

func (a *API) pendingProposal(ctx context.Context, w http.ResponseWriter, r *http.Request, orgID string) (string, *jsObj, bool) {
	id := r.PathValue("proposalId")
	var name string
	var raw []byte
	err := pgx.ErrNoRows
	if isUUID(id) {
		err = a.DB.QueryRow(ctx, `select name, rule from action_policy_proposal where org_id = $1 and id = $2 and status = 'pending'`,
			orgID, id).Scan(&name, &raw)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		sendError(w, http.StatusNotFound, "No pending rule proposal has this id.")
		return "", nil, false
	}
	if err != nil {
		a.fail(w, r, err)
		return "", nil, false
	}
	rule, err := jsParse(raw)
	in, isObj := rule.(*jsObj)
	if err != nil || !isObj {
		a.fail(w, r, errors.New("the rule proposal is not an object"))
		return "", nil, false
	}
	return id, in, true
}

// policyProposalConfirm is POST /api/v1/policies/proposals/{id}/confirm.
func (a *API) policyProposalConfirm(w http.ResponseWriter, r *http.Request) {
	act, ok := a.accessAdmin(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	id, in, ok := a.pendingProposal(ctx, w, r, act.OrgID)
	if !ok {
		return
	}
	name, _ := in.vals["name"].(string)
	if err := a.packRuleRefusal(ctx, act.OrgID, name); err != nil {
		if refusal := ruleRefusal(""); errors.As(err, &refusal) {
			sendError(w, http.StatusConflict, refusal.Error())
			return
		}
		a.fail(w, r, err)
		return
	}
	var runID, threadID *string
	if err := a.DB.QueryRow(ctx, `select p.proposed_by_run_id::text, r.thread_id::text from action_policy_proposal p
		left join work_run r on r.id = p.proposed_by_run_id where p.id = $1`, id).Scan(&runID, &threadID); err != nil {
		a.fail(w, r, err)
		return
	}
	in.set("orgId", act.OrgID)
	in.set("createdByRunId", jsOr(deref(runID), runID != nil, nil))
	in.set("createdByThreadId", jsOr(deref(threadID), threadID != nil, nil))
	saved, err := a.applyPolicyRule(ctx, act.OrgID, in)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	policyID := jsTemplate(jsLookup(jsGet(saved, "policy"), "id"))
	if _, err := a.DB.Exec(ctx, `update action_policy_proposal set status = 'confirmed', decided_by_user_id = $2, decided_at = now(),
		policy_id = $3::uuid where id = $1`, id, act.UserID, policyID); err != nil {
		a.fail(w, r, err)
		return
	}
	a.recordAudit(ctx, auditEvent{OrgID: act.OrgID, EntityKind: "action_policy_proposal", EntityID: id, Event: "policy:confirmed",
		Payload: map[string]any{"name": name, "policyId": policyID, "decidedBy": act.UserID}})
	sendJSON(w, http.StatusOK, map[string]any{"policy": jsGet(saved, "policy"), "action": jsGet(saved, "action")})
}

// policyProposalReject is POST /api/v1/policies/proposals/{id}/reject.
func (a *API) policyProposalReject(w http.ResponseWriter, r *http.Request) {
	act, ok := a.accessAdmin(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	id, in, ok := a.pendingProposal(ctx, w, r, act.OrgID)
	if !ok {
		return
	}
	if _, err := a.DB.Exec(ctx, `update action_policy_proposal set status = 'rejected', decided_by_user_id = $2, decided_at = now()
		where id = $1`, id, act.UserID); err != nil {
		a.fail(w, r, err)
		return
	}
	a.recordAudit(ctx, auditEvent{OrgID: act.OrgID, EntityKind: "action_policy_proposal", EntityID: id, Event: "policy:rejected",
		Payload: map[string]any{"name": in.vals["name"], "decidedBy": act.UserID}})
	sendJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
