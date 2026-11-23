package webfront

import (
	"context"
	"errors"
	"math"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf16"
)

type compatWebhookRow struct {
	ID             string   `json:"reckonWorkflowId"`
	WorkflowID     string   `json:"workflowId"`
	WorkflowName   string   `json:"workflowName"`
	Enabled        bool     `json:"enabled"`
	Params         []string `json:"params"`
	BatchChunkSize int      `json:"batchChunkSize"`
	UpdatedAt      string   `json:"updatedAt"`
}

// compatLimits are the ceilings this deployment allows for the
// webhook-compatibility callers, in limitNames order; -1 keeps the stored value.
var compatLimits = apiLimits{60, 60, 100, 5, -1, -1, 256 << 10, -1, 50 << 20, 1800, 1024, 1024, 3000000, 100000000, -1, -1, -1, 168}

var (
	compatIDPattern    = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,120}$`)
	compatParamPattern = regexp.MustCompile(`^[A-Za-z0-9_]{1,64}$`)
)

type compatImportError string

func (e compatImportError) Error() string { return string(e) }

func (a *API) listCompatWebhooks(ctx context.Context, orgID string) ([]compatWebhookRow, error) {
	rows, err := a.DB.Query(ctx, `select compat.reckon_workflow_id, compat.workflow_id::text, workflow.name, compat.enabled,
	        compat.params, compat.batch_chunk_size, compat.updated_at
	   from compat_webhook compat
	   join workflow_definition workflow on workflow.id = compat.workflow_id
	  where compat.org_id = $1
	  order by workflow.name`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []compatWebhookRow{}
	for rows.Next() {
		var c compatWebhookRow
		var updated time.Time
		if err := rows.Scan(&c.ID, &c.WorkflowID, &c.WorkflowName, &c.Enabled, &c.Params, &c.BatchChunkSize, &updated); err != nil {
			return nil, err
		}
		c.UpdatedAt = jsTime(updated)
		out = append(out, c)
	}
	return out, rows.Err()
}

func (a *API) adminCompatWebhooks(w http.ResponseWriter, r *http.Request) {
	act, ok := a.settingsAdmin(w, r)
	if !ok {
		return
	}
	hooks, err := a.listCompatWebhooks(r.Context(), act.OrgID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	sendJSON(w, http.StatusOK, map[string]any{"webhooks": hooks})
}

func (a *API) adminCompatWebhookImport(w http.ResponseWriter, r *http.Request) {
	act, ok := a.settingsAdmin(w, r)
	if !ok {
		return
	}
	body, _ := readBody(r)
	m, _ := body.(map[string]any)
	id, idOK := m["reckonWorkflowId"].(string)
	workflowID, workflowOK := m["workflowId"].(string)
	token, tokenOK := m["token"].(string)
	if !idOK || !workflowOK || !tokenOK {
		sendError(w, http.StatusBadRequest, "reckonWorkflowId, workflowId and token are required.")
		return
	}
	var params []string
	if list, isList := m["params"].([]any); isList {
		params = make([]string, len(list))
		for i, p := range list {
			params[i], _ = p.(string)
		}
	}
	chunk := 1000.0
	if n, isNumber := m["batchChunkSize"].(float64); isNumber {
		chunk = n
	}
	enabled := true
	if b, isBool := m["enabled"].(bool); isBool {
		enabled = b
	}
	hook, err := a.importCompatWebhook(r.Context(), act, strings.TrimSpace(id), workflowID, token, params, chunk, enabled)
	var refused compatImportError
	if errors.As(err, &refused) {
		sendError(w, http.StatusBadRequest, refused.Error())
		return
	}
	if err != nil {
		a.fail(w, r, err)
		return
	}
	sendJSON(w, http.StatusCreated, map[string]any{"webhook": hook})
}

func (a *API) importCompatWebhook(ctx context.Context, act actor, id, workflowID, token string, params []string, chunk float64, enabled bool) (*compatWebhookRow, error) {
	switch {
	case !compatIDPattern.MatchString(id):
		return nil, compatImportError("The Reckon workflow id must be 1 to 120 plain identifier characters.")
	case len(utf16.Encode([]rune(strings.TrimSpace(token)))) < 16:
		return nil, compatImportError("The webhook token must be at least 16 characters.")
	case chunk != math.Trunc(chunk) || chunk < 1 || chunk > 1000:
		return nil, compatImportError("The batch chunk size must be a whole number from 1 to 1000.")
	}
	for _, p := range params {
		if !compatParamPattern.MatchString(p) {
			return nil, compatImportError("Each parameter name must be 1 to 64 letters, numbers or underscores.")
		}
	}
	access, err := apiAccess(ctx, a.DB, act.OrgID, workflowID)
	if err != nil {
		return nil, err
	}
	if access == nil {
		return nil, compatImportError("Workflow not found.")
	}
	if !access.Enabled {
		if _, err := a.issueAPIToken(ctx, act, workflowID, "enable"); err != nil {
			return nil, err
		}
	}
	patch := map[int]int64{}
	for i, v := range compatLimits {
		if v >= 0 {
			patch[i] = v
		}
	}
	if _, err := a.updateAPILimits(ctx, act, workflowID, patch, nil); err != nil {
		return nil, err
	}
	if _, err := a.DB.Exec(ctx, `insert into compat_webhook (
	     reckon_workflow_id, org_id, workflow_id, token_sha256, enabled, params,
	     batch_chunk_size, created_by_user_id, updated_at
	   ) values ($1, $2, $3::uuid, $4, $5, $6, $7, $8, now())
	   on conflict (reckon_workflow_id) do update
	     set org_id = excluded.org_id,
	         workflow_id = excluded.workflow_id,
	         token_sha256 = excluded.token_sha256,
	         enabled = excluded.enabled,
	         params = excluded.params,
	         batch_chunk_size = excluded.batch_chunk_size,
	         updated_at = now()`,
		id, act.OrgID, workflowID, sha256Hex(token), enabled, params, int(chunk), nullable(act.UserID)); err != nil {
		return nil, err
	}
	var auditParams any
	if params != nil {
		list := make([]any, len(params))
		for i, p := range params {
			list[i] = p
		}
		auditParams = list
	}
	a.recordAudit(ctx, auditEvent{OrgID: act.OrgID, EntityKind: "compat_webhook", EntityID: id, Event: "imported",
		Payload: map[string]any{"actorUserId": auditActor(act.UserID), "workflowId": workflowID, "params": auditParams, "batchChunkSize": int(chunk)}})
	hooks, err := a.listCompatWebhooks(ctx, act.OrgID)
	if err != nil {
		return nil, err
	}
	for i := range hooks {
		if hooks[i].ID == id {
			return &hooks[i], nil
		}
	}
	return nil, compatImportError("The webhook could not be read back after saving.")
}

func (a *API) adminCompatWebhookRemove(w http.ResponseWriter, r *http.Request) {
	act, ok := a.settingsAdmin(w, r)
	if !ok {
		return
	}
	ctx, id := r.Context(), r.PathValue("reckonWorkflowId")
	tag, err := a.DB.Exec(ctx, `delete from compat_webhook where org_id = $1 and reckon_workflow_id = $2`, act.OrgID, id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if tag.RowsAffected() == 0 {
		sendError(w, http.StatusNotFound, "Webhook not found.")
		return
	}
	a.recordAudit(ctx, auditEvent{OrgID: act.OrgID, EntityKind: "compat_webhook", EntityID: id, Event: "removed",
		Payload: map[string]any{"actorUserId": auditActor(act.UserID)}})
	sendJSON(w, http.StatusOK, map[string]any{"ok": true})
}
