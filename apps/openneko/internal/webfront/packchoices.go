package webfront

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
)

// packModeChoice is a pack rule that an earlier pack version installed in
// auto mode, before the pack asked the admin at install.
type packModeChoice struct {
	PackID, Input, Policy, Question, Line string
}

var upgradeModeChoices = []packModeChoice{{
	PackID: "parallel-search", Input: "parallel_search.approval", Policy: "Parallel web search",
	Question: "Parallel web search runs each search with no approval.",
	Line:     "Each search sends its search text out of this install to Parallel. Choose whether a person approves each search.",
}}

type packChoiceOut struct {
	PolicyID string `json:"policyId"`
	PackID   string `json:"packId"`
	Question string `json:"question"`
	Line     string `json:"line"`
	choice   packModeChoice
}

func (a *API) pendingPackChoices(ctx context.Context, orgID string) ([]packChoiceOut, error) {
	out := []packChoiceOut{}
	for _, c := range upgradeModeChoices {
		var id string
		err := a.DB.QueryRow(ctx, `select p.id::text from action_policy p where p.org_id = $1 and p.name = $3 and p.mode = 'auto_approve'
			and exists (select 1 from pack_install i where i.org_id = $1 and i.pack_id = $2 and i.removed_at is null
				and not jsonb_exists(i.config, $4))
			limit 1`, orgID, c.PackID, c.Policy, c.Input).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, packChoiceOut{PolicyID: id, PackID: c.PackID, Question: c.Question, Line: c.Line, choice: c})
	}
	return out, nil
}

// packChoicePost is POST /api/v1/policies/{policyId}/pack-choice: the admin
// answers ask or auto once, and the pack install keeps the answer.
func (a *API) packChoicePost(w http.ResponseWriter, r *http.Request) {
	act, ok := a.accessAdmin(w, r)
	if !ok {
		return
	}
	body, _ := readBody(r)
	mode, _ := field(body, "mode")
	if mode != "ask" && mode != "auto" {
		sendError(w, http.StatusBadRequest, `mode must be "ask" or "auto"`)
		return
	}
	ctx := r.Context()
	pending, err := a.pendingPackChoices(ctx, act.OrgID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var found *packChoiceOut
	for i := range pending {
		if pending[i].PolicyID == r.PathValue("policyId") {
			found = &pending[i]
		}
	}
	if found == nil {
		sendError(w, http.StatusNotFound, "No choice waits for this rule.")
		return
	}
	c := found.choice
	err = pgx.BeginFunc(ctx, a.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `update action_policy set mode = $3, updated_at = now() where org_id = $1 and id = $2::uuid`,
			act.OrgID, found.PolicyID, packPolicyMode(mode)); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `update pack_install set config = jsonb_set(coalesce(config, '{}'::jsonb), array[$3::text], to_jsonb($4::text)), updated_at = now()
			where org_id = $1 and pack_id = $2 and removed_at is null`, act.OrgID, c.PackID, c.Input, mode)
		return err
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.recordAudit(ctx, auditEvent{OrgID: act.OrgID, EntityKind: "action_policy", EntityID: found.PolicyID, Event: "policy:pack_choice",
		Payload: map[string]any{"packId": c.PackID, "input": c.Input, "mode": mode, "decidedBy": act.UserID}})
	sendJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
