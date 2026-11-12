package webfront

import (
	"errors"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/jackc/pgx/v5"
)

type actionPolicy struct {
	ID                       string     `json:"id"`
	Name                     string     `json:"name"`
	Description              string     `json:"description"`
	AppliesToKinds           []string   `json:"appliesToKinds"`
	AppliesToScopes          []string   `json:"appliesToScopes"`
	Mode                     string     `json:"mode"`
	RiskThresholdAutoApprove *string    `json:"riskThresholdAutoApprove"`
	AllowedTargets           storedJSON `json:"allowedTargets"`
	DeniedTargets            storedJSON `json:"deniedTargets"`
	Limits                   storedJSON `json:"limits"`
	ApproverGroupID          *string    `json:"approverGroupId"`
	Priority                 int        `json:"priority"`
	Enabled                  bool       `json:"enabled"`
	CreatedByThreadID        any        `json:"createdByThreadId,omitempty"`
	CreatedByRunID           any        `json:"createdByRunId,omitempty"`
	CreatedAt                string     `json:"createdAt"`
	UpdatedAt                string     `json:"updatedAt"`
}

const actionPolicyColumns = `id::text, name, description, applies_to_kinds, applies_to_scopes, mode,
	risk_threshold_auto_approve, allowed_targets, denied_targets, limits, approver_group_id::text, priority, enabled,
	created_by_thread_id::text, created_by_run_id::text, created_at, updated_at`

func scanActionPolicy(row pgx.Row) (actionPolicy, error) {
	var p actionPolicy
	var created, updated time.Time
	var thread, run *string
	err := row.Scan(&p.ID, &p.Name, &p.Description, &p.AppliesToKinds, &p.AppliesToScopes, &p.Mode,
		&p.RiskThresholdAutoApprove, (*[]byte)(&p.AllowedTargets), (*[]byte)(&p.DeniedTargets), (*[]byte)(&p.Limits), &p.ApproverGroupID, &p.Priority,
		&p.Enabled, &thread, &run, &created, &updated)
	p.CreatedByThreadID, p.CreatedByRunID = thread, run
	if p.AppliesToKinds == nil {
		p.AppliesToKinds = []string{}
	}
	if p.AppliesToScopes == nil {
		p.AppliesToScopes = []string{}
	}
	if p.Limits == nil {
		p.Limits = storedJSON("{}")
	}
	p.CreatedAt, p.UpdatedAt = jsTime(created), jsTime(updated)
	return p, err
}

func (a *API) actionPolicies(w http.ResponseWriter, r *http.Request) {
	act, ok := a.accessAdmin(w, r)
	if !ok {
		return
	}
	rows, err := a.DB.Query(r.Context(), `select `+actionPolicyColumns+` from action_policy
		where org_id = $1 order by priority, name`, act.OrgID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	policies, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (actionPolicy, error) {
		p, err := scanActionPolicy(row)
		p.CreatedByThreadID, p.CreatedByRunID = nil, nil
		return p, err
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if policies == nil {
		policies = []actionPolicy{}
	}
	sendJSON(w, http.StatusOK, map[string]any{"policies": policies})
}

func (a *API) actionPolicyGet(w http.ResponseWriter, r *http.Request) {
	act, ok := a.accessAdmin(w, r)
	if !ok {
		return
	}
	id := r.PathValue("policyId")
	if !isUUID(id) {
		sendError(w, http.StatusNotFound, "not found")
		return
	}
	p, err := scanActionPolicy(a.DB.QueryRow(r.Context(), `select `+actionPolicyColumns+` from action_policy
		where org_id = $1 and id = $2`, act.OrgID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		sendError(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		a.fail(w, r, err)
		return
	}
	sendJSON(w, http.StatusOK, map[string]any{"policy": p})
}

const maxTargetPatterns = 200

// parseTargetPatterns is parseTargetPatterns(input, "value") in
// apps/web/src/lib/target-patterns.ts.
func parseTargetPatterns(input any) ([]string, string) {
	const listError = "patterns must be a list of strings"
	var raw []any
	switch v := input.(type) {
	case []any:
		raw = v
	case string:
		for _, s := range strings.FieldsFunc(v, func(r rune) bool { return r == '\n' || r == ',' }) {
			raw = append(raw, s)
		}
	default:
		return nil, listError
	}
	patterns := []string{}
	for _, item := range raw {
		s, ok := item.(string)
		if !ok {
			return nil, listError
		}
		trimmed := jsTrim(s)
		if trimmed == "" {
			continue
		}
		if len(utf16.Encode([]rune(trimmed))) > 500 || strings.IndexFunc(trimmed, jsSpace) >= 0 {
			return nil, `"` + trimmed + `" is not a valid pattern`
		}
		if !slices.Contains(patterns, trimmed) {
			patterns = append(patterns, trimmed)
		}
	}
	if len(patterns) > maxTargetPatterns {
		return nil, "use at most " + strconv.Itoa(maxTargetPatterns) + " patterns"
	}
	return patterns, ""
}

func (a *API) actionPolicyPatch(w http.ResponseWriter, r *http.Request) {
	act, ok := a.accessAdmin(w, r)
	if !ok {
		return
	}
	id := r.PathValue("policyId")
	raw, _ := io.ReadAll(io.LimitReader(r.Body, maxBody))
	body, _ := jsParse(raw)
	o, isObj := body.(*jsObj)
	if !isObj {
		sendError(w, http.StatusBadRequest, "send approverGroupId or allowedTargets")
		return
	}
	group, hasGroup := o.vals["approverGroupId"]
	targets, hasTargets := o.vals["allowedTargets"]
	if !hasGroup && !hasTargets {
		sendError(w, http.StatusBadRequest, "send approverGroupId or allowedTargets")
		return
	}
	ctx := r.Context()
	var sets []string
	args := []any{act.OrgID, id}
	if hasGroup {
		groupID, isString := group.(string)
		if group != nil && (!isString || groupID == "") {
			sendError(w, http.StatusBadRequest, "approverGroupId must be a group id or null")
			return
		}
		if isString {
			var found bool
			if isUUID(groupID) {
				if err := a.DB.QueryRow(ctx, `select exists (select 1 from user_group where org_id = $1 and id = $2)`,
					act.OrgID, groupID).Scan(&found); err != nil {
					a.fail(w, r, err)
					return
				}
			}
			if !found {
				sendError(w, http.StatusNotFound, "group not found")
				return
			}
			args = append(args, groupID)
		} else {
			args = append(args, nil)
		}
		sets = append(sets, "approver_group_id = $"+strconv.Itoa(len(args))+"::uuid")
	}
	if hasTargets {
		var stored any
		if targets != nil {
			var patternsIn, onMiss any
			if t, ok := targets.(*jsObj); ok {
				patternsIn, onMiss = t.vals["patterns"], t.vals["onMiss"]
			}
			patterns, problem := parseTargetPatterns(patternsIn)
			if problem != "" {
				sendError(w, http.StatusBadRequest, problem)
				return
			}
			if onMiss != "next" && onMiss != "deny" {
				sendError(w, http.StatusBadRequest, `onMiss must be "next" or "deny"`)
				return
			}
			if len(patterns) > 0 {
				value := &jsObj{}
				list := make([]any, len(patterns))
				for i, p := range patterns {
					list[i] = p
				}
				value.set("patterns", list)
				if onMiss == "next" {
					value.set("on_miss", "next")
				}
				stored = jsStringify(value, false)
			}
		}
		args = append(args, stored)
		sets = append(sets, "allowed_targets = $"+strconv.Itoa(len(args))+"::jsonb")
	}
	if !isUUID(id) {
		sendError(w, http.StatusNotFound, "not found")
		return
	}
	var out struct {
		ID              string     `json:"id"`
		ApproverGroupID *string    `json:"approverGroupId"`
		AllowedTargets  storedJSON `json:"allowedTargets"`
	}
	err := a.DB.QueryRow(ctx, `update action_policy set `+strings.Join(sets, ", ")+`, updated_at = now()
		where org_id = $1 and id = $2 returning id::text, approver_group_id::text, allowed_targets`, args...).
		Scan(&out.ID, &out.ApproverGroupID, (*[]byte)(&out.AllowedTargets))
	if errors.Is(err, pgx.ErrNoRows) {
		sendError(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		a.fail(w, r, err)
		return
	}
	sendJSON(w, http.StatusOK, map[string]any{"policy": out})
}
