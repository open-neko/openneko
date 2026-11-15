package webfront

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/open-neko/neko/apps/openneko/internal/config"
)

const (
	webhookDefaultMethod    = "POST"
	webhookDefaultTimeoutMs = 30_000
	webhookMaxTimeoutMs     = 120_000
	webhookMaxBodyCapture   = 4 * 1024
	codePatchMaxBytes       = 512_000
)

var (
	webhookAllowedMethods = []string{"GET", "POST", "PUT", "PATCH", "DELETE"}
	codeRepoPattern       = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	codePatchSlugInvalid  = regexp.MustCompile(`[^a-z0-9._-]+`)
	openAPISourceClean    = regexp.MustCompile(`[^a-z0-9_-]+`)
)

// executeSendWebhook matches webhookAdapter in
// packages/llm/src/workflows/adapters/webhook.ts.
func (a *API) executeSendWebhook(ctx context.Context, req *actionRequestRecord) (*actionExecutionOutcome, error) {
	payload := req.Payload
	if payload == nil {
		payload = &jsObj{vals: map[string]any{}}
	}
	rawURL := ""
	if u, ok := payload.vals["url"].(string); ok && len(u) > 0 {
		rawURL = u
	} else if req.Target != nil && len(*req.Target) > 0 {
		rawURL = *req.Target
	}
	if rawURL == "" {
		return nil, errors.New("webhook adapter requires a url (in payload.url or request.target)")
	}
	method := webhookDefaultMethod
	if m, ok := payload.vals["method"].(string); ok {
		method = strings.ToUpper(m)
	}
	if !slices.Contains(webhookAllowedMethods, method) {
		return nil, fmt.Errorf("webhook adapter does not support method %q (allowed: GET, POST, PUT, PATCH, DELETE)", method)
	}
	headers := map[string]string{"User-Agent": "OpenNeko-webhook-adapter/1.0"}
	if hObj, ok := payload.vals["headers"].(*jsObj); ok && hObj != nil {
		for _, k := range hObj.ordered() {
			if v, ok := hObj.vals[k].(string); ok {
				headers[k] = v
			}
		}
	}
	var bodyReader io.Reader
	if method != "GET" {
		if bodyVal, exists := payload.vals["body"]; exists && bodyVal != nil {
			var bodyText string
			if s, ok := bodyVal.(string); ok {
				bodyText = s
			} else {
				bodyText = jsStringify(bodyVal, false)
				if !hasHeaderFold(headers, "Content-Type") {
					headers["Content-Type"] = "application/json"
				}
			}
			bodyReader = strings.NewReader(bodyText)
		}
	}
	timeoutMs := webhookDefaultTimeoutMs
	if t, ok := payload.vals["timeout_ms"].(float64); ok && t > 0 {
		timeoutMs = int(math.Min(t, webhookMaxTimeoutMs))
	}
	reqCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMs)*time.Millisecond)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(reqCtx, method, rawURL, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("webhook to %s failed before response: %v", rawURL, err)
	}
	for k, v := range headers {
		httpReq.Header.Set(k, v)
	}
	resp, err := webhookClient().Do(httpReq)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(reqCtx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("webhook to %s timed out after %dms", rawURL, timeoutMs)
		}
		var blocked *webhookBlockedError
		if errors.As(err, &blocked) {
			return nil, fmt.Errorf("webhook to %s failed before response: %v", rawURL, blocked)
		}
		return nil, fmt.Errorf("webhook to %s failed before response: %v", rawURL, err)
	}
	defer resp.Body.Close()
	rawBody, err := io.ReadAll(io.LimitReader(resp.Body, webhookMaxBodyCapture+1024))
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(reqCtx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("webhook to %s timed out after %dms", rawURL, timeoutMs)
		}
		return nil, fmt.Errorf("webhook to %s failed before response: %v", rawURL, err)
	}
	capturedBody := string(rawBody)
	if len(capturedBody) > webhookMaxBodyCapture {
		capturedBody = capturedBody[:webhookMaxBodyCapture] + "…[truncated]"
	}
	respHeaders := &jsObj{vals: map[string]any{}}
	var externalRef *string
	for k, vals := range resp.Header {
		lk := strings.ToLower(k)
		joined := strings.Join(vals, ", ")
		respHeaders.set(lk, joined)
		if lk == "x-request-id" && joined != "" {
			ref := joined
			externalRef = &ref
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		statusText := strings.TrimSpace(strings.TrimPrefix(resp.Status, strconv.Itoa(resp.StatusCode)))
		if len(capturedBody) > 200 {
			capturedBody = capturedBody[:200]
		}
		return nil, fmt.Errorf("webhook to %s returned %d %s: %s", rawURL, resp.StatusCode, statusText, capturedBody)
	}
	cmd := method + " " + rawURL
	return &actionExecutionOutcome{
		Status:             "succeeded",
		CommandOrOperation: &cmd,
		ExternalRef:        externalRef,
		Result: jsObjOf(
			"status", float64(resp.StatusCode),
			"headers", respHeaders,
			"body", capturedBody,
		),
	}, nil
}

type webhookBlockedError struct{ ip string }

func (e *webhookBlockedError) Error() string {
	return "OpenNeko does not send a webhook to the loopback, link-local, multicast or unspecified address " + e.ip
}

var webhookAddressBlocked = func(ip net.IP) bool {
	if v4 := ip.To4(); v4 != nil && v4[0] == 0 {
		return true
	}
	return ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified()
}

func webhookClient() *http.Client {
	return guardedClient(func(ip net.IP) error {
		if webhookAddressBlocked(ip) {
			return &webhookBlockedError{ip: ip.String()}
		}
		return nil
	})
}

func hasHeaderFold(headers map[string]string, target string) bool {
	for k := range headers {
		if strings.EqualFold(k, target) {
			return true
		}
	}
	return false
}

// executeCodeCreateIssue matches makeCreateIssueAdapter in
// packages/llm/src/workflows/adapters/code.ts.
func (a *API) executeCodeCreateIssue(ctx context.Context, req *actionRequestRecord) (*actionExecutionOutcome, error) {
	payload := req.Payload
	if payload == nil {
		payload = &jsObj{vals: map[string]any{}}
	}
	repo := strings.TrimSpace(jsStr(payload.vals["repo"]))
	if !codeRepoPattern.MatchString(repo) {
		return nil, errors.New("code_create_issue: payload.repo must be 'owner/name'")
	}
	title := strings.TrimSpace(jsStr(payload.vals["title"]))
	if title == "" || len(title) > 256 {
		return nil, errors.New("code_create_issue: payload.title (1..256 chars) is required")
	}
	body := jsStr(payload.vals["body"])
	labels := []any{}
	if list, ok := payload.vals["labels"].([]any); ok {
		for _, item := range list {
			s, ok := item.(string)
			if ok && len(s) > 0 && len(s) <= 50 {
				labels = append(labels, s)
				if len(labels) == 20 {
					break
				}
			}
		}
	}
	token := strings.TrimSpace(os.Getenv("OPENNEKO_GITHUB_TOKEN"))
	if token == "" {
		token = strings.TrimSpace(os.Getenv("GITHUB_TOKEN"))
	}
	if token == "" {
		return nil, errors.New("code_create_issue: no forge token configured (set OPENNEKO_GITHUB_TOKEN)")
	}
	apiBase := strings.TrimRight(orDefault(strings.TrimSpace(os.Getenv("OPENNEKO_GITHUB_API_URL")), "https://api.github.com"), "/")
	reqBody := jsStringify(jsObjOf("title", title, "body", body, "labels", labels), false)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("%s/repos/%s/issues", apiBase, repo), strings.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Accept", "application/vnd.github+json")
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("User-Agent", "openneko-action-executor")
	httpReq.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		text := string(raw)
		if len(text) > 300 {
			text = text[:300]
		}
		return nil, fmt.Errorf("code_create_issue failed (%d): %s", resp.StatusCode, text)
	}
	parsed, _ := jsParse(raw)
	obj, _ := parsed.(*jsObj)
	var htmlURL *string
	var number any
	if obj != nil {
		if u, ok := obj.vals["html_url"].(string); ok && u != "" {
			htmlURL = &u
		}
		number = obj.vals["number"]
	}
	cmd := fmt.Sprintf("github:create_issue %s#%v", repo, number)
	return &actionExecutionOutcome{
		Status:             "succeeded",
		CommandOrOperation: &cmd,
		ExternalRef:        htmlURL,
		Result: jsObjOf(
			"repo", repo,
			"number", number,
			"url", orNilPtr(htmlURL),
		),
	}, nil
}

// executeCodeDraftPatch matches draftPatchAdapter in
// packages/llm/src/workflows/adapters/code.ts.
func (a *API) executeCodeDraftPatch(_ context.Context, req *actionRequestRecord) (*actionExecutionOutcome, error) {
	payload := req.Payload
	if payload == nil {
		payload = &jsObj{vals: map[string]any{}}
	}
	patch := jsStr(payload.vals["patch"])
	if strings.TrimSpace(patch) == "" {
		return nil, errors.New("code_draft_patch: payload.patch (unified diff) is required")
	}
	if len(patch) > codePatchMaxBytes {
		return nil, errors.New("code_draft_patch: patch exceeds 512 KB cap")
	}
	title := strings.TrimSpace(jsStr(payload.vals["title"]))
	if title == "" {
		title = "patch"
	}
	summary := strings.TrimSpace(jsStr(payload.vals["summary"]))
	slug := strings.Trim(codePatchSlugInvalid.ReplaceAllString(strings.ToLower(title), "-"), "-")
	if len(slug) > 48 {
		slug = slug[:48]
	}
	if slug == "" {
		slug = "patch"
	}
	dir := filepath.Join(orgAgentRoot(req.OrgID), "artifacts", "patches")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	prefix := req.ID
	if len(prefix) > 8 {
		prefix = prefix[:8]
	}
	file := filepath.Join(dir, fmt.Sprintf("%s-%s.patch", prefix, slug))
	lines := []string{fmt.Sprintf("# Title: %s", title)}
	if summary != "" {
		lines = append(lines, fmt.Sprintf("# Summary: %s", summary))
	}
	lines = append(lines,
		fmt.Sprintf("# Action-Request-Id: %s", req.ID),
		"# Generated by OpenNeko (draft — review and apply with `git apply`). OpenNeko never applies patches.",
		"",
	)
	content := strings.Join(lines, "\n") + patch + "\n"
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		return nil, err
	}
	cmd := fmt.Sprintf("draft patch → %s", file)
	return &actionExecutionOutcome{
		Status:             "succeeded",
		CommandOrOperation: &cmd,
		Result: jsObjOf(
			"artifactPath", file,
			"bytes", float64(len(patch)),
		),
	}, nil
}

// executeGroupAdmin matches registerGroupAdminAdapter in
// apps/worker/src/admin-actions/adapters.ts.
func (a *API) executeGroupAdmin(ctx context.Context, req *actionRequestRecord) (*actionExecutionOutcome, error) {
	payload := req.Payload
	if payload == nil {
		payload = &jsObj{vals: map[string]any{}}
	}
	action := jsStr(payload.vals["action"])
	text := func(key string) (string, error) {
		v, ok := payload.vals[key].(string)
		if !ok || strings.TrimSpace(v) == "" {
			return "", fmt.Errorf("group_admin %s: %s required", jsTemplate(payload.vals["action"]), key)
		}
		return strings.TrimSpace(v), nil
	}
	texts := func(keys ...string) ([]string, error) {
		out := make([]string, len(keys))
		for i, key := range keys {
			v, err := text(key)
			if err != nil {
				return nil, err
			}
			out[i] = v
		}
		return out, nil
	}
	done := func(result *jsObj, graphjin bool) (*actionExecutionOutcome, error) {
		if graphjin {
			a.scheduleGroupGrantsApply(ctx, req.OrgID)
		}
		cmd := action
		return &actionExecutionOutcome{Status: "succeeded", CommandOrOperation: &cmd, Result: result}, nil
	}

	switch action {
	case "create_group":
		name, err := text("name")
		if err != nil {
			return nil, err
		}
		var desc *string
		if d, ok := payload.vals["description"].(string); ok {
			desc = &d
		}
		group, err := a.createGroupActionRecord(ctx, req.OrgID, name, desc)
		if err != nil {
			return nil, err
		}
		return done(jsObjOf("groupId", group.ID, "slug", group.Slug), false)

	case "delete_group":
		groupID, err := text("groupId")
		if err != nil {
			return nil, err
		}
		if err := a.withAdministratorGuard(ctx, req.OrgID, func(tx pgx.Tx) error {
			g, err := requireGroup(ctx, tx, req.OrgID, groupID)
			if err != nil {
				return err
			}
			if g.kind == "builtin" {
				return groupErr("builtin", "built-in groups cannot be deleted")
			}
			_, err = tx.Exec(ctx, `delete from user_group where org_id = $1 and id = $2::uuid`, req.OrgID, groupID)
			return err
		}); err != nil {
			return nil, err
		}
		return done(jsObjOf("groupId", groupID), true)

	case "add_member":
		ids, err := texts("groupId", "userId")
		if err != nil {
			return nil, err
		}
		if err := a.addLocalMemberAction(ctx, req.OrgID, ids[0], ids[1]); err != nil {
			return nil, err
		}
		return done(jsObjOf("groupId", ids[0], "userId", ids[1]), false)

	case "remove_member":
		ids, err := texts("groupId", "userId")
		if err != nil {
			return nil, err
		}
		stillMember, err := a.removeLocalMemberAction(ctx, req.OrgID, ids[0], ids[1])
		if err != nil {
			return nil, err
		}
		return done(jsObjOf("stillMember", stillMember), false)

	case "add_idp_rule":
		ids, err := texts("ssoGroupId", "groupId")
		if err != nil {
			return nil, err
		}
		ssoGroupID, userGroupID := ids[0], ids[1]
		var ruleID string
		if err := a.withAdministratorGuard(ctx, req.OrgID, func(tx pgx.Tx) error {
			g, err := requireGroup(ctx, tx, req.OrgID, userGroupID)
			if err != nil {
				return err
			}
			if g.slug == "everyone" {
				return groupErr("implicit", "every active user is already in Everyone")
			}
			var idpExists bool
			if err := tx.QueryRow(ctx, `select exists (select 1 from sso_group where org_id = $1 and id = $2::uuid)`, req.OrgID, ssoGroupID).Scan(&idpExists); err != nil {
				return err
			}
			if !idpExists {
				return groupErr("not_found", "IdP group not found")
			}
			if err := tx.QueryRow(ctx, `
				insert into idp_group_rule (org_id, sso_group_id, user_group_id, created_by_user_id)
				values ($1, $2::uuid, $3::uuid, $4)
				on conflict (org_id, sso_group_id, user_group_id) do update set created_at = idp_group_rule.created_at
				returning id::text`, req.OrgID, ssoGroupID, userGroupID, nullableStringPtr(req.ActorUserID)).Scan(&ruleID); err != nil {
				return err
			}
			return recomputeRuleMemberships(ctx, tx, req.OrgID, nil)
		}); err != nil {
			return nil, err
		}
		return done(jsObjOf("id", ruleID), false)

	case "remove_idp_rule":
		ruleID, err := text("ruleId")
		if err != nil {
			return nil, err
		}
		if err := a.withAdministratorGuard(ctx, req.OrgID, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `delete from idp_group_rule where org_id = $1 and id = $2::uuid`, req.OrgID, ruleID); err != nil {
				return err
			}
			return recomputeRuleMemberships(ctx, tx, req.OrgID, nil)
		}); err != nil {
			return nil, err
		}
		return done(jsObjOf("ruleId", ruleID), false)

	case "grant_item", "revoke_item":
		itemType, err := text("itemType")
		if err != nil {
			return nil, err
		}
		if !isItemType(itemType) {
			return nil, fmt.Errorf("group_admin: unknown item type %s", itemType)
		}
		ids, err := texts("groupId", "itemId")
		if err != nil {
			return nil, err
		}
		groupID, itemID := ids[0], ids[1]
		if len(itemID) > 500 {
			return nil, groupErr("invalid", "item id must be 1 to 500 characters")
		}
		g, err := requireGrantableGroup(ctx, a.DB, req.OrgID, groupID)
		if err != nil {
			return nil, err
		}
		grant := action == "grant_item"
		query := `delete from item_grant
			where org_id = $1 and group_id = $2::uuid and item_type = $3 and item_id = $4
			returning item_id`
		args := []any{req.OrgID, g.id, itemType, itemID}
		auditAction := "revoke"
		if grant {
			query = `insert into item_grant (org_id, group_id, item_type, item_id, created_by_user_id, action_request_id)
				values ($1, $2::uuid, $3, $4, $5, $6::uuid)
				on conflict do nothing returning item_id`
			args = append(args, nullableStringPtr(req.ActorUserID), req.ID)
			auditAction = "grant"
		}
		var changed bool
		if err := pgx.BeginFunc(ctx, a.DB, func(tx pgx.Tx) error {
			var touched string
			qerr := tx.QueryRow(ctx, query, args...).Scan(&touched)
			if errors.Is(qerr, pgx.ErrNoRows) {
				return nil
			}
			if qerr != nil {
				return qerr
			}
			changed = true
			_, err := tx.Exec(ctx, `
				insert into item_grant_audit (org_id, actor_user_id, action, group_id, group_name, item_type, item_id, action_request_id)
				values ($1, $2, $3, $4::uuid, $5, $6, $7, $8::uuid)`,
				req.OrgID, nullableStringPtr(req.ActorUserID), auditAction, g.id, g.name, itemType, itemID, req.ID)
			return err
		}); err != nil {
			return nil, err
		}
		key := "removed"
		if grant {
			key = "created"
		}
		return done(jsObjOf(key, changed), itemType == "data_source" || itemType == "api_operation")

	case "set_table_access":
		ids, err := texts("table", "groupId", "source")
		if err != nil {
			return nil, err
		}
		table, groupID, source := ids[0], ids[1], ids[2]
		tableSchema, tableName := "", table
		if i := strings.LastIndex(table, "."); i >= 0 {
			tableSchema, tableName = table[:i], table[i+1:]
		}
		var cols []string
		if rawCols, ok := payload.vals["columns"].([]any); ok {
			seen := map[string]bool{}
			for _, item := range rawCols {
				c := strings.TrimSpace(jsTemplate(item))
				if c != "" && !seen[c] {
					seen[c] = true
					cols = append(cols, c)
				}
			}
		}
		var rowFilter any
		if raw := payload.vals["rowFilter"]; raw != nil {
			if rowFilter, err = parseRowFilter(plainJSON(raw)); err != nil {
				return nil, err
			}
		}
		var slug string
		if err := a.DB.QueryRow(ctx, `select slug from user_group where org_id = $1 and id = $2::uuid`, req.OrgID, groupID).Scan(&slug); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, groupErr("not_found", "group not found")
			}
			return nil, err
		}
		if slug == "administrators" {
			return nil, groupErr("builtin", "Administrators read every table; data access rules are not needed")
		}
		if len(cols) == 0 {
			return nil, groupErr("invalid", "choose at least one column")
		}
		if !sqlIdentifier.MatchString(tableName) {
			return nil, groupErr("invalid", "invalid table name")
		}
		var filterJSON any
		if rowFilter != nil {
			filterJSON = jsStringify(jsValueOf(rowFilter), false)
		}
		var ruleID string
		if err := a.DB.QueryRow(ctx, `
			insert into data_access_rule (org_id, group_id, source, table_schema, table_name, columns, row_filter, created_by_user_id)
			values ($1, $2::uuid, $3, $4, $5, $6, $7::jsonb, $8)
			on conflict (org_id, group_id, source, table_schema, table_name)
			do update set columns = excluded.columns, row_filter = excluded.row_filter, updated_at = now()
			returning id::text`,
			req.OrgID, groupID, source, tableSchema, tableName, cols, filterJSON, nullableStringPtr(req.ActorUserID)).Scan(&ruleID); err != nil {
			return nil, err
		}
		return done(jsObjOf("ruleId", ruleID), true)

	case "remove_table_access":
		ruleID, err := text("ruleId")
		if err != nil {
			return nil, err
		}
		tag, err := a.DB.Exec(ctx, `delete from data_access_rule where org_id = $1 and id = $2::uuid`, req.OrgID, ruleID)
		if err != nil {
			return nil, err
		}
		return done(jsObjOf("removed", tag.RowsAffected() > 0), true)

	default:
		return nil, fmt.Errorf("group_admin: unknown action \"%s\"", action)
	}
}

func (a *API) createGroupActionRecord(ctx context.Context, orgID, rawName string, description *string) (userGroup, error) {
	name := strings.TrimSpace(rawName)
	if name == "" || len([]rune(name)) > 120 {
		return userGroup{}, groupErr("invalid", "group name must be 1 to 120 characters")
	}
	var desc *string
	if description != nil {
		if trimmed := strings.TrimSpace(*description); trimmed != "" {
			desc = &trimmed
		}
	}
	var createdID string
	err := pgx.BeginFunc(ctx, a.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtext($1))`, "openneko.app_user:"+orgID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select slug, name from user_group where org_id = $1`, orgID)
		if err != nil {
			return err
		}
		defer rows.Close()
		taken := map[string]bool{}
		for rows.Next() {
			var s, n string
			if err := rows.Scan(&s, &n); err != nil {
				return err
			}
			taken[s] = true
			taken["name:"+strings.ToLower(n)] = true
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if taken["name:"+strings.ToLower(name)] {
			return groupErr("conflict", fmt.Sprintf("a group named %q already exists", name))
		}
		base := slugifyGroupName(name)
		if reservedGroupSlug(base) {
			return groupErr("conflict", fmt.Sprintf("%q is reserved for a built-in group", name))
		}
		slug := base
		for i := 2; taken[slug] || reservedGroupSlug(slug); i++ {
			slug = fmt.Sprintf("%s-%d", base, i)
		}
		return tx.QueryRow(ctx, `
			insert into user_group (org_id, slug, name, description, kind)
			values ($1, $2, $3, $4, 'custom')
			returning id::text`, orgID, slug, name, desc).Scan(&createdID)
	})
	if err != nil {
		return userGroup{}, err
	}
	groups, err := listUserGroups(ctx, a.DB, orgID)
	if err != nil {
		return userGroup{}, err
	}
	for _, g := range groups {
		if g.ID == createdID {
			return g, nil
		}
	}
	return userGroup{}, groupErr("not_found", "group not found")
}

func (a *API) addLocalMemberAction(ctx context.Context, orgID, groupID, userID string) error {
	return a.withAdministratorGuard(ctx, orgID, func(tx pgx.Tx) error {
		g, err := requireGroup(ctx, tx, orgID, groupID)
		if err != nil {
			return err
		}
		if g.slug == "everyone" {
			return groupErr("implicit", "every active user is already in Everyone")
		}
		var exists bool
		if err := tx.QueryRow(ctx, `select exists (select 1 from app_user where org_id = $1 and id = $2)`, orgID, userID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return groupErr("not_found", "user not found")
		}
		_, err = tx.Exec(ctx, `
			insert into user_group_membership (org_id, group_id, user_id, source)
			values ($1, $2::uuid, $3, 'local') on conflict do nothing`, orgID, groupID, userID)
		return err
	})
}

func (a *API) removeLocalMemberAction(ctx context.Context, orgID, groupID, userID string) (bool, error) {
	var stillMember bool
	err := a.withAdministratorGuard(ctx, orgID, func(tx pgx.Tx) error {
		g, err := requireGroup(ctx, tx, orgID, groupID)
		if err != nil {
			return err
		}
		if g.slug == "everyone" {
			return groupErr("implicit", "users cannot leave Everyone")
		}
		if _, err := tx.Exec(ctx, `
			delete from user_group_membership
			where org_id = $1 and group_id = $2::uuid and user_id = $3 and source = 'local'`,
			orgID, groupID, userID); err != nil {
			return err
		}
		var left int
		if err := tx.QueryRow(ctx, `
			select count(*)::int from user_group_membership
			where org_id = $1 and group_id = $2::uuid and user_id = $3`,
			orgID, groupID, userID).Scan(&left); err != nil {
			return err
		}
		stillMember = left > 0
		return nil
	})
	return stillMember, err
}

// executeUserAdmin matches registerUserAdminAdapter in
// apps/worker/src/admin-actions/adapters.ts.
func (a *API) executeUserAdmin(ctx context.Context, req *actionRequestRecord) (*actionExecutionOutcome, error) {
	payload := req.Payload
	if payload == nil {
		payload = &jsObj{vals: map[string]any{}}
	}
	action := jsStr(payload.vals["action"])
	orgID := req.OrgID
	role := "member"
	if payload.vals["role"] == "admin" {
		role = "admin"
	}
	outcome := func(cmd string, result *jsObj) (*actionExecutionOutcome, error) {
		return &actionExecutionOutcome{Status: "succeeded", CommandOrOperation: &cmd, Result: result}, nil
	}

	if action == "invite" {
		email := strings.ToLower(strings.TrimSpace(jsStr(payload.vals["email"])))
		if email == "" {
			return nil, errors.New("user_admin invite: email required")
		}
		var existing string
		err := a.DB.QueryRow(ctx, `select id from app_user where org_id = $1 and email = $2 limit 1`, orgID, email).Scan(&existing)
		if err == nil {
			return outcome("invite "+email, jsObjOf("userId", existing, "alreadyExisted", true))
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		id := randomUUID()
		if _, err := a.DB.Exec(ctx, `insert into app_user (id, email, org_id) values ($1, $2, $3)`, id, email, orgID); err != nil {
			return nil, err
		}
		if role == "admin" {
			if err := a.setLocalAdministratorAction(ctx, orgID, id, true); err != nil {
				return nil, err
			}
		}
		return outcome("invite "+email+" as "+role, jsObjOf("userId", id, "role", role))
	}

	userID := jsStr(payload.vals["userId"])
	if userID == "" {
		return nil, fmt.Errorf("user_admin %s: userId required", action)
	}

	switch action {
	case "set_role":
		var found bool
		if err := a.DB.QueryRow(ctx, `select exists (select 1 from app_user where org_id = $1 and id = $2)`, orgID, userID).Scan(&found); err != nil {
			return nil, err
		}
		if !found {
			return nil, fmt.Errorf("user %s not found", userID)
		}
		if err := a.setLocalAdministratorAction(ctx, orgID, userID, role == "admin"); err != nil {
			return nil, err
		}
		return outcome("set_role "+userID+" "+role, jsObjOf("userId", userID, "role", role))

	case "deactivate", "reactivate":
		disable := action == "deactivate"
		var tag pgconn.CommandTag
		if err := a.withAdministratorGuard(ctx, orgID, func(tx pgx.Tx) error {
			var err error
			tag, err = tx.Exec(ctx, `
				update app_user set disabled_at = case when $3 then now() else null end, updated_at = now()
				 where org_id = $1 and id = $2`, orgID, userID, disable)
			return err
		}); err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, fmt.Errorf("user %s not found", userID)
		}
		return outcome(action+" "+userID, jsObjOf("userId", userID, "disabled", disable))
	}
	return nil, fmt.Errorf("user_admin: unknown action \"%s\"", action)
}

func (a *API) setLocalAdministratorAction(ctx context.Context, orgID, userID string, administrator bool) error {
	var groupID string
	if err := a.DB.QueryRow(ctx, `select id::text from user_group where org_id = $1 and slug = 'administrators'`, orgID).Scan(&groupID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return groupErr("not_found", "built-in group administrators is missing")
		}
		return err
	}
	if administrator {
		return a.addLocalMemberAction(ctx, orgID, groupID, userID)
	}
	stillMember, err := a.removeLocalMemberAction(ctx, orgID, groupID, userID)
	if err != nil {
		return err
	}
	if stillMember {
		return groupErr("conflict", "this user is an administrator through an IdP rule; change the rule instead")
	}
	return nil
}

// executeChannelAdmin matches registerChannelAdminAdapter in
// apps/worker/src/admin-actions/adapters.ts.
func (a *API) executeChannelAdmin(ctx context.Context, req *actionRequestRecord) (*actionExecutionOutcome, error) {
	payload := req.Payload
	if payload == nil {
		payload = &jsObj{vals: map[string]any{}}
	}
	action := jsStr(payload.vals["action"])
	identityID := jsStr(payload.vals["identityId"])
	orgID := req.OrgID
	if identityID == "" {
		return nil, fmt.Errorf("channel_admin %s: identityId required", action)
	}
	outcome := func(cmd string, result *jsObj) (*actionExecutionOutcome, error) {
		return &actionExecutionOutcome{Status: "succeeded", CommandOrOperation: &cmd, Result: result}, nil
	}

	switch action {
	case "link":
		appUserID := jsStr(payload.vals["appUserId"])
		var userID string
		err := a.DB.QueryRow(ctx, `select id from app_user where org_id = $1 and id = $2 and disabled_at is null limit 1`, orgID, appUserID).Scan(&userID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("channel_admin link: unknown or disabled user %s", appUserID)
		}
		if err != nil {
			return nil, err
		}
		tag, err := a.DB.Exec(ctx, `
			update channel_identity set app_user_id = $3, status = 'linked', verified_at = now(), updated_at = now()
			 where org_id = $1 and id::text = $2`, orgID, identityID, userID)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, fmt.Errorf("channel identity %s not found", identityID)
		}
		return outcome("link "+identityID+" → "+appUserID, jsObjOf("identityId", identityID, "appUserId", appUserID, "status", "linked"))

	case "unlink", "block", "unblock":
		set := `app_user_id = null, status = 'unverified', verified_at = null`
		if action == "block" {
			set = `status = 'blocked'`
		}
		var status string
		err := a.DB.QueryRow(ctx, `
			update channel_identity set `+set+`, updated_at = now()
			 where org_id = $1 and id::text = $2
			 returning status`, orgID, identityID).Scan(&status)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("channel identity %s not found", identityID)
		}
		if err != nil {
			return nil, err
		}
		return outcome(action+" "+identityID, jsObjOf("identityId", identityID, "status", status))
	}
	return nil, fmt.Errorf("channel_admin: unknown action \"%s\"", action)
}

// executeDataSourceAdmin matches registerDataSourceAdminAdapter in
// apps/worker/src/admin-actions/adapters.ts.
func (a *API) executeDataSourceAdmin(ctx context.Context, req *actionRequestRecord) (*actionExecutionOutcome, error) {
	payload := req.Payload
	if payload == nil {
		payload = &jsObj{vals: map[string]any{}}
	}
	action := jsStr(payload.vals["action"])
	name := strings.TrimSpace(jsStr(payload.vals["name"]))
	orgID := req.OrgID
	if name == "" {
		return nil, fmt.Errorf("data_source_admin %s: name required", action)
	}
	outcome := func(cmd string, result *jsObj) (*actionExecutionOutcome, error) {
		return &actionExecutionOutcome{Status: "succeeded", CommandOrOperation: &cmd, Result: result}, nil
	}
	notFound := fmt.Errorf("data source %s not found", name)

	switch action {
	case "register":
		var exists bool
		if err := a.DB.QueryRow(ctx, `select exists (select 1 from data_source where org_id = $1 and name = $2)`, orgID, name).Scan(&exists); err != nil {
			return nil, err
		}
		if exists {
			return outcome("register "+name, jsObjOf("name", name, "alreadyExisted", true))
		}
		kind := "graphjin"
		if k, ok := payload.vals["sourceKind"].(string); ok && slices.Contains(dataSourceKinds, k) {
			kind = k
		}
		var label *string
		if l, ok := payload.vals["label"].(string); ok {
			label = &l
		}
		var id string
		if err := a.DB.QueryRow(ctx, `
			insert into data_source (org_id, kind, graphql_url, name, label, enabled)
			values ($1, $2, '', $3, $4, false)
			returning id::text`, orgID, kind, name, label).Scan(&id); err != nil {
			return nil, err
		}
		return outcome("register "+name+" (placeholder — complete in Settings)", jsObjOf("id", id, "name", name, "enabled", false))

	case "enable", "disable":
		enable := action == "enable"
		tag, err := a.DB.Exec(ctx, `update data_source set enabled = $3, updated_at = now() where org_id = $1 and name = $2`, orgID, name, enable)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, notFound
		}
		return outcome(action+" "+name, jsObjOf("name", name, "enabled", enable))

	case "set_default":
		if err := pgx.BeginFunc(ctx, a.DB, func(tx pgx.Tx) error {
			tag, err := tx.Exec(ctx, `update data_source set is_default = true, updated_at = now() where org_id = $1 and name = $2`, orgID, name)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return notFound
			}
			_, err = tx.Exec(ctx, `update data_source set is_default = false, updated_at = now() where org_id = $1 and name <> $2`, orgID, name)
			return err
		}); err != nil {
			return nil, err
		}
		return outcome("set_default "+name, jsObjOf("name", name))

	case "remove":
		var isDefault bool
		err := a.DB.QueryRow(ctx, `select is_default from data_source where org_id = $1 and name = $2 limit 1`, orgID, name).Scan(&isDefault)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, notFound
		}
		if err != nil {
			return nil, err
		}
		if isDefault {
			return nil, errors.New("cannot remove the default data source — set another default first")
		}
		if _, err := a.DB.Exec(ctx, `delete from data_source where org_id = $1 and name = $2`, orgID, name); err != nil {
			return nil, err
		}
		return outcome("remove "+name, jsObjOf("name", name, "removed", true))
	}
	return nil, fmt.Errorf("data_source_admin: unknown action \"%s\"", action)
}

// executeSourceConfigAdmin matches registerSourceConfigAdminAdapter /
// runSourceConfigAdmin in apps/worker/src/admin-actions/adapters.ts.
func (a *API) executeSourceConfigAdmin(ctx context.Context, req *actionRequestRecord) (*actionExecutionOutcome, error) {
	var passwords []string
	outcome, err := a.runSourceConfigAdminAction(ctx, req, &passwords)
	if err != nil {
		return nil, errors.New(scrubSourcePasswords(err.Error(), passwords))
	}
	return outcome, nil
}

func scrubSourcePasswords(message string, passwords []string) string {
	out := message
	for _, secret := range passwords {
		if secret != "" {
			out = strings.ReplaceAll(out, secret, redactedValue)
		}
	}
	return out
}

type openAPIActionAsset struct {
	id             string
	content        string
	checksumSha256 string
	title          string
	version        *string
}

func (a *API) runSourceConfigAdminAction(ctx context.Context, req *actionRequestRecord, passwords *[]string) (*actionExecutionOutcome, error) {
	payload := req.Payload
	if payload == nil {
		payload = &jsObj{vals: map[string]any{}}
	}
	action, _ := payload.vals["action"].(string)
	if action == "" {
		return nil, errors.New("source_config_admin requires payload.action")
	}
	orgID := req.OrgID
	if on, err := a.sourceConfigEnabled(ctx, orgID); err != nil {
		return nil, err
	} else if !on {
		return nil, errors.New("source_config_admin: GraphJin config capability is disabled")
	}

	explicitID := ""
	if s, ok := payload.vals["dataSourceId"].(string); ok && strings.TrimSpace(s) != "" {
		explicitID = strings.TrimSpace(s)
	}
	type sourceCandidate struct {
		id         string
		name       string
		graphqlURL *string
	}
	var rows pgx.Rows
	var err error
	if explicitID != "" {
		rows, err = a.DB.Query(ctx, `select id::text, name, graphql_url from data_source where org_id = $1 and enabled = true and id = $2::uuid limit 1`, orgID, explicitID)
	} else {
		rows, err = a.DB.Query(ctx, `select id::text, name, graphql_url from data_source where org_id = $1 and enabled = true order by is_default desc, created_at limit 3`, orgID)
	}
	if err != nil {
		return nil, err
	}
	sources, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (sourceCandidate, error) {
		var c sourceCandidate
		return c, row.Scan(&c.id, &c.name, &c.graphqlURL)
	})
	if err != nil {
		return nil, err
	}
	if explicitID == "" && len(sources) > 1 {
		return nil, errors.New("source_config_admin: multiple enabled data sources exist; an explicit dataSourceId is required")
	}
	if len(sources) == 0 || deref(sources[0].graphqlURL) == "" {
		return nil, errors.New("source_config_admin: no data source configured")
	}
	endpoint := graphqlEndpointFromURL(*sources[0].graphqlURL)
	parsedEndpoint, err := url.Parse(endpoint)
	if err != nil || parsedEndpoint.Host == "" {
		return nil, fmt.Errorf("source_config_admin: invalid data source URL %s", endpoint)
	}
	endpointHost := parsedEndpoint.Host
	if isInternalGraphjinURL(endpoint) {
		return nil, errors.New("source_config_admin: refusing to configure the OpenNeko internal GraphJin")
	}

	selectedKind, _ := firstOf(payload.vals["kind"]).(string)
	selectedBackend, _ := firstOf(payload.vals["backend"]).(string)
	isManagedOpenAPI := action == "register_source" && selectedKind == "api"
	isManagedLocalFile := action == "register_source" && selectedKind == "file" && selectedBackend == "local"
	needsDurableSourceConfig := action == "add_role" ||
		action == "register_source" ||
		action == "set_source_access" ||
		action == "set_source_tables" ||
		action == "set_source_capabilities" ||
		action == "expose_api_operation" ||
		action == "enable_api_writes"
	sourceName := strings.TrimSpace(jsStr(payload.vals["name"]))

	durableConfigFile := ""
	if needsDurableSourceConfig {
		durableConfigFile = strings.TrimSpace(os.Getenv("OPENNEKO_GRAPHJIN_CONFIG"))
		if durableConfigFile == "" {
			return nil, errors.New("source_config_admin: source changes require the worker to share GraphJin's config volume")
		}
	}

	var specAsset *openAPIActionAsset
	var managedRuntimeDir string
	var managedLocalFile string
	if isManagedOpenAPI {
		specAssetID, _ := payload.vals["specAssetId"].(string)
		if specAssetID == "" {
			return nil, errors.New("source_config_admin: API source has no managed OpenAPI asset")
		}
		var asset openAPIActionAsset
		err := a.DB.QueryRow(ctx, `
			select id::text, content, checksum_sha256, title, version
			  from openapi_spec_asset
			 where org_id = $1 and id = $2::uuid
			 limit 1`, orgID, specAssetID).Scan(&asset.id, &asset.content, &asset.checksumSha256, &asset.title, &asset.version)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errors.New("source_config_admin: managed OpenAPI asset is unavailable")
		}
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256([]byte(asset.content))
		if hex.EncodeToString(sum[:]) != asset.checksumSha256 {
			return nil, errors.New("source_config_admin: managed OpenAPI asset failed its integrity check")
		}
		specAsset = &asset
		runtimeRoot := strings.TrimRight(os.Getenv("OPENNEKO_GRAPHJIN_SPECS_DIR"), "/")
		if runtimeRoot == "" {
			runtimeRoot = "/config/specs"
		}
		managedRuntimeDir = runtimeRoot
		specFilename, err := managedOpenAPISpecFilename(orgID, sourceName)
		if err != nil {
			return nil, err
		}
		managedLocalFile = filepath.Join(filepath.Dir(durableConfigFile), "specs", specFilename)
	}

	var localFileManifest *jsObj
	var managedLocalFilesRoot string
	var managedFileSrc managedSource
	if isManagedLocalFile {
		manifest, err := parseManagedFileSourceManifest(payload.vals["localFiles"], sourceName)
		if err != nil {
			return nil, err
		}
		localFileManifest = manifest
		runtimeBase := strings.TrimSpace(os.Getenv("OPENNEKO_GRAPHJIN_FILES_DIR"))
		if runtimeBase == "" {
			runtimeBase = "/config/files"
		}
		managedLocalFilesRoot, err = managedFileSourceRuntimeRoot(orgID, sourceName, runtimeBase)
		if err != nil {
			return nil, err
		}
		managedFileSrc = newManagedSource(durableConfigFile, orgID, sourceName)
		dirName, err := managedFileSrc.dirName()
		if err != nil {
			return nil, err
		}
		if dirName != filepath.Base(managedLocalFilesRoot) {
			return nil, errors.New("source_config_admin: managed file storage paths are inconsistent")
		}
	}

	var secretName string
	update, err := buildGraphjinConfigUpdate(payload, func(ref string) (string, error) {
		secretName = ref
		var enc string
		err := a.DB.QueryRow(ctx, `select value_enc from data_source_secret where org_id = $1 and name = $2 limit 1`, orgID, ref).Scan(&enc)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", fmt.Errorf("register_source: no stored secret named %q — add it in GraphJin Config first", ref)
		}
		if err != nil {
			return "", err
		}
		decrypted, err := config.MaybeDecryptValue(a.SecretsDir, enc)
		if err != nil {
			return "", err
		}
		if decrypted != "" {
			*passwords = append(*passwords, decrypted)
		}
		return decrypted, nil
	}, configUpdateOptions{
		managedOpenAPISpecsDir: managedRuntimeDir,
		managedLocalFilesRoot:  managedLocalFilesRoot,
	})
	if err != nil {
		return nil, err
	}

	approvedPreview, ok := payload.vals["preview"].(*jsObj)
	if !ok || approvedPreview == nil {
		return nil, errors.New("source_config_admin: approved request is missing a validated preview")
	}
	credentialFreePayload := &jsObj{vals: map[string]any{}}
	for _, k := range payload.ordered() {
		if k != "preview" && k != "specSummary" {
			credentialFreePayload.set(k, payload.vals[k])
		}
	}
	patchHash := graphjinConfigPatchHash(credentialFreePayload)
	if jsStr(approvedPreview.vals["patchHash"]) != patchHash {
		return nil, errors.New("source_config_admin: approved payload no longer matches its preview")
	}

	if req.ApprovedBy != nil && *req.ApprovedBy != "" {
		var disabledAt *time.Time
		err := a.DB.QueryRow(ctx, `select disabled_at from app_user where id = $1 and org_id = $2 limit 1`, *req.ApprovedBy, orgID).Scan(&disabledAt)
		if errors.Is(err, pgx.ErrNoRows) || disabledAt != nil {
			return nil, errors.New("source_config_admin: approving user is no longer an active admin")
		}
		if err != nil {
			return nil, err
		}
		role, err := a.role(ctx, orgID, *req.ApprovedBy)
		if err != nil {
			return nil, err
		}
		if role != "admin" {
			return nil, errors.New("source_config_admin: approving user is no longer an active admin")
		}
	} else if deref(req.ActorRole) != "admin" {
		return nil, errors.New("source_config_admin: missing trusted admin approval provenance")
	}

	tokenUser := deref(req.ApprovedBy)
	if tokenUser == "" {
		tokenUser = deref(req.ActorUserID)
	}
	adminToken, err := a.mintGraphjinToken(graphjinClaims{
		orgID:  orgID,
		userID: tokenUser,
		role:   "admin",
	}, 120*time.Second)
	if err != nil {
		return nil, err
	}

	var restoreManagedSpec func() error
	var restoreManagedConfig func() error
	var appliedCatalogRevision string
	liveRegisteredSource := false
	var releaseConfigLock func()
	var releaseFileLock func()
	defer func() {
		if releaseFileLock != nil {
			releaseFileLock()
		}
		if releaseConfigLock != nil {
			releaseConfigLock()
		}
	}()

	runErr := func() error {
		if durableConfigFile != "" {
			lockRelease, err := acquireGraphjinConfigLock(durableConfigFile)
			if err != nil {
				return err
			}
			releaseConfigLock = lockRelease
		}
		if localFileManifest != nil {
			fileRelease, err := managedFileSrc.lock()
			if err != nil {
				return err
			}
			releaseFileLock = fileRelease
		}
		if specAsset != nil && managedLocalFile != "" {
			if err := os.MkdirAll(filepath.Dir(managedLocalFile), 0o755); err != nil {
				return err
			}
			var prevContent []byte
			var hadPrev bool
			if raw, err := os.ReadFile(managedLocalFile); err == nil {
				prevContent = raw
				hadPrev = true
			} else if !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			tmp := fmt.Sprintf("%s.%s.tmp", managedLocalFile, rand.Text())
			if err := os.WriteFile(tmp, []byte(specAsset.content), 0o600); err != nil {
				return err
			}
			if err := os.Rename(tmp, managedLocalFile); err != nil {
				_ = os.Remove(tmp)
				return err
			}
			restoreManagedSpec = func() error {
				if !hadPrev {
					if err := os.Remove(managedLocalFile); err != nil && !errors.Is(err, fs.ErrNotExist) {
						return err
					}
					return nil
				}
				rollback := fmt.Sprintf("%s.%s.rollback", managedLocalFile, rand.Text())
				if err := os.WriteFile(rollback, prevContent, 0o600); err != nil {
					return err
				}
				return os.Rename(rollback, managedLocalFile)
			}
		}
		if localFileManifest != nil {
			if err := managedFileSrc.assertMutable(); err != nil {
				return err
			}
			if err := managedFileSrc.verifyManifest(localFileManifest); err != nil {
				return err
			}
		}

		// Phase 0: read catalog_revision and sources.
		revData, revMsgs, err := gjPost(ctx, endpoint, adminToken, `query { gj_config(id: "current") { catalog_revision sources } }`)
		if err != nil {
			return err
		}
		revRow, _ := jsGet(revData, "gj_config").(*jsObj)
		rev := ""
		if revRow != nil {
			rev = strings.TrimSpace(jsStr(revRow.vals["catalog_revision"]))
		}
		if rev == "" {
			msg := strings.Join(revMsgs, "; ")
			if msg == "" {
				msg = "no revision"
			}
			return fmt.Errorf("source_config_admin: could not read catalog_revision: %s", msg)
		}
		if jsStr(approvedPreview.vals["catalogRevision"]) != rev {
			return errors.New("source_config_admin: GraphJin configuration changed after approval; create and approve a fresh preview")
		}
		var currentSources any
		if revRow != nil {
			currentSources = revRow.vals["sources"]
			if s, ok := currentSources.(string); ok {
				if parsed, err := jsParse([]byte(s)); err == nil {
					currentSources = parsed
				} else {
					currentSources = []any{}
				}
			}
		}
		if err := assertDatabaseSourcesStayReadOnly(update, currentSources); err != nil {
			return err
		}
		if action == "register_source" {
			if list, ok := currentSources.([]any); ok {
				for _, item := range list {
					if obj, ok := item.(*jsObj); ok && obj != nil && obj.vals["name"] == sourceName {
						return fmt.Errorf("source_config_admin: a GraphJin source named %q already exists", sourceName)
					}
				}
			}
		}

		// Phase 1: preview.
		previewUpdate := jsObjOf("mode", "preview", "expected_catalog_revision", rev)
		for _, k := range update.ordered() {
			previewUpdate.set(k, update.vals[k])
		}
		previewLiteral, previewVarDefs, previewVars := graphjinInputWithJSONVariables(previewUpdate)
		previewData, previewMsgs, err := gjPostVars(ctx, endpoint, adminToken,
			`mutation`+previewVarDefs+` { gj_config(id: "current", update: `+previewLiteral+`) { valid preview_id change_summary_json errors_json } }`,
			previewVars)
		if err != nil {
			return err
		}
		if len(previewMsgs) > 0 {
			return fmt.Errorf("source_config_admin preview failed: %s", strings.Join(previewMsgs, "; "))
		}
		pv, _ := jsGet(previewData, "gj_config").(*jsObj)
		previewID := ""
		if pv != nil {
			previewID = jsStr(pv.vals["preview_id"])
		}
		if pv == nil || pv.vals["valid"] != true || previewID == "" {
			errJSON := "no preview_id returned"
			if pv != nil && jsStr(pv.vals["errors_json"]) != "" {
				errJSON = jsStr(pv.vals["errors_json"])
			}
			return fmt.Errorf("source_config_admin preview invalid: %s", errJSON)
		}

		// Phase 2: apply.
		applyUpdate := jsObjOf("mode", "apply", "preview_id", previewID, "expected_catalog_revision", rev)
		for _, k := range update.ordered() {
			applyUpdate.set(k, update.vals[k])
		}
		applyLiteral, applyVarDefs, applyVars := graphjinInputWithJSONVariables(applyUpdate)
		applyData, applyMsgs, err := gjPostVars(ctx, endpoint, adminToken,
			`mutation`+applyVarDefs+` { gj_config(id: "current", update: `+applyLiteral+`) { applied catalog_revision errors_json } }`,
			applyVars)
		if err != nil {
			return err
		}
		if len(applyMsgs) > 0 {
			return fmt.Errorf("source_config_admin apply failed: %s", strings.Join(applyMsgs, "; "))
		}
		av, _ := jsGet(applyData, "gj_config").(*jsObj)
		if av == nil || av.vals["applied"] != true {
			errJSON := "unknown"
			if av != nil && jsStr(av.vals["errors_json"]) != "" {
				errJSON = jsStr(av.vals["errors_json"])
			}
			return fmt.Errorf("source_config_admin apply rejected: %s", errJSON)
		}
		appliedCatalogRevision = jsStr(av.vals["catalog_revision"])
		liveRegisteredSource = action == "register_source"

		if durableConfigFile != "" {
			prevConfig, err := os.ReadFile(durableConfigFile)
			if err != nil {
				return err
			}
			info, err := os.Stat(durableConfigFile)
			if err != nil {
				return err
			}
			prevMode := info.Mode().Perm()
			if err := persistGraphjinSourceConfigUpdate(durableConfigFile, update); err != nil {
				return err
			}
			restoreManagedConfig = func() error {
				rollback := fmt.Sprintf("%s.%s.rollback", durableConfigFile, rand.Text())
				if err := os.WriteFile(rollback, prevConfig, prevMode); err != nil {
					return err
				}
				return os.Rename(rollback, durableConfigFile)
			}
		}

		if localFileManifest != nil {
			if err := managedFileSrc.verifyManifest(localFileManifest); err != nil {
				return err
			}
		}

		var verifiedOperations []any
		if specAsset != nil {
			specFile, err := managedOpenAPISpecFilename(orgID, sourceName)
			if err != nil {
				return err
			}
			opPrefix := strings.TrimSuffix(specFile, ".yaml") + "_"
			for attempt := 0; attempt < 20; attempt++ {
				catData, catMsgs, err := gjPost(ctx, endpoint, adminToken,
					`query { gj_catalog(where: { kind: { eq: "table" } }, limit: 500) { id name summary } }`)
				if err != nil {
					return err
				}
				if len(catMsgs) > 0 {
					return fmt.Errorf("source_config_admin: catalog verification failed: %s", strings.Join(catMsgs, "; "))
				}
				verifiedOperations = nil
				if list, ok := jsGet(catData, "gj_catalog").([]any); ok {
					for _, item := range list {
						if obj, ok := item.(*jsObj); ok && obj != nil {
							idStr := jsStr(obj.vals["id"])
							nameStr := jsStr(obj.vals["name"])
							if strings.HasPrefix(idStr, opPrefix) || strings.HasPrefix(nameStr, opPrefix) {
								verifiedOperations = append(verifiedOperations, obj)
							}
						}
					}
				}
				if len(verifiedOperations) > 0 {
					break
				}
				if !sleepCtx(ctx, 500*time.Millisecond) {
					return ctx.Err()
				}
			}
			if len(verifiedOperations) == 0 {
				return errors.New("source_config_admin: GraphJin applied the source but exposed no operations from its OpenAPI document")
			}
			if err := pgx.BeginFunc(ctx, a.DB, func(tx pgx.Tx) error {
				if _, err := tx.Exec(ctx, `
					update openapi_spec_asset
					   set status = 'superseded', updated_at = now()
					 where org_id = $1 and source_name = $2 and status = 'active' and id <> $3::uuid`,
					orgID, sourceName, specAsset.id); err != nil {
					return err
				}
				_, err := tx.Exec(ctx, `
					update openapi_spec_asset
					   set status = 'active', source_name = $2, activated_at = now(), updated_at = now()
					 where org_id = $1 and id = $3::uuid`,
					orgID, sourceName, specAsset.id)
				return err
			}); err != nil {
				return err
			}
		}

		if localFileManifest != nil {
			verifiedFileSource := false
			for attempt := 0; attempt < 20; attempt++ {
				catData, catMsgs, err := gjPost(ctx, endpoint, adminToken,
					`query { gj_catalog(where: { kind: { eq: "table" } }, limit: 500) { id name summary } }`)
				if err != nil {
					return err
				}
				if len(catMsgs) > 0 {
					return fmt.Errorf("source_config_admin: file catalog verification failed: %s", strings.Join(catMsgs, "; "))
				}
				if list, ok := jsGet(catData, "gj_catalog").([]any); ok {
					for _, item := range list {
						if obj, ok := item.(*jsObj); ok && obj != nil {
							if jsStr(obj.vals["id"]) == sourceName || jsStr(obj.vals["name"]) == sourceName {
								verifiedFileSource = true
								break
							}
						}
					}
				}
				if verifiedFileSource {
					break
				}
				if !sleepCtx(ctx, 500*time.Millisecond) {
					return ctx.Err()
				}
			}
			if !verifiedFileSource {
				return errors.New("source_config_admin: GraphJin applied the local file source but did not expose its virtual table")
			}
			if err := managedFileSrc.markApproved(); err != nil {
				return err
			}
		}

		res := jsObjOf(
			"action", action,
			"host", endpointHost,
			"changeSummary", orNil(pv.vals["change_summary_json"]),
			"catalogRevision", orNilStr(appliedCatalogRevision),
		)
		if specAsset != nil {
			res.set("openApi", jsObjOf(
				"assetId", specAsset.id,
				"title", specAsset.title,
				"version", orNilPtr(specAsset.version),
				"operationCount", float64(len(verifiedOperations)),
				"checksumSha256", specAsset.checksumSha256,
			))
		}
		if localFileManifest != nil {
			files, _ := localFileManifest.vals["files"].([]any)
			res.set("fileSource", jsObjOf(
				"backend", "local",
				"fileCount", float64(len(files)),
				"totalSize", localFileManifest.vals["totalSize"],
				"readOnly", true,
			))
		}
		if secretName != "" {
			res.set("secretRef", secretName)
		}
		payload.set("_executionResult", res)
		return nil
	}()

	if runErr != nil {
		var rollbackFailures []string
		liveRollbackFailed := false
		if liveRegisteredSource && appliedCatalogRevision != "" && sourceName != "" {
			if rbErr := rollbackLiveGraphjinSource(ctx, endpoint, adminToken, appliedCatalogRevision, sourceName); rbErr != nil {
				liveRollbackFailed = true
				rollbackFailures = append(rollbackFailures, "GraphJin live-source rollback failed: "+rbErr.Error())
			}
		}
		if restoreManagedSpec != nil {
			if rbErr := restoreManagedSpec(); rbErr != nil {
				rollbackFailures = append(rollbackFailures, "OpenAPI file rollback failed: "+rbErr.Error())
			}
		}
		if restoreManagedConfig != nil {
			if rbErr := restoreManagedConfig(); rbErr != nil {
				rollbackFailures = append(rollbackFailures, "GraphJin config rollback failed: "+rbErr.Error())
			}
		}
		if liveRollbackFailed && localFileManifest != nil {
			if frzErr := managedFileSrc.markApproved(); frzErr != nil {
				rollbackFailures = append(rollbackFailures, "managed file source freeze failed: "+frzErr.Error())
			}
		}
		if len(rollbackFailures) > 0 {
			return nil, fmt.Errorf("%s; %s", runErr.Error(), strings.Join(rollbackFailures, "; "))
		}
		return nil, runErr
	}

	res, _ := payload.vals["_executionResult"].(*jsObj)
	delete(payload.vals, "_executionResult")
	cmd := fmt.Sprintf("%s on customer GraphJin (%s)", action, endpointHost)
	return &actionExecutionOutcome{
		Status:             "succeeded",
		CommandOrOperation: &cmd,
		Result:             res,
	}, nil
}

func rollbackLiveGraphjinSource(ctx context.Context, endpoint, adminToken, catalogRevision, sourceName string) error {
	previewInline, _, _ := graphjinInputWithJSONVariables(jsObjOf(
		"mode", "preview",
		"expected_catalog_revision", catalogRevision,
		"remove_sources", []any{sourceName},
	))
	previewData, previewMsgs, err := gjPost(ctx, endpoint, adminToken,
		`mutation { gj_config(id: "current", update: `+previewInline+`) { valid preview_id errors_json } }`)
	if err != nil {
		return err
	}
	pv, _ := jsGet(previewData, "gj_config").(*jsObj)
	previewID := ""
	if pv != nil {
		previewID = jsStr(pv.vals["preview_id"])
	}
	if len(previewMsgs) > 0 || pv == nil || pv.vals["valid"] != true || previewID == "" {
		msg := strings.Join(previewMsgs, "; ")
		if msg == "" && pv != nil {
			msg = jsStr(pv.vals["errors_json"])
		}
		if msg == "" {
			msg = "rollback preview was rejected"
		}
		return errors.New(msg)
	}
	applyInline, _, _ := graphjinInputWithJSONVariables(jsObjOf(
		"mode", "apply",
		"preview_id", previewID,
		"expected_catalog_revision", catalogRevision,
		"remove_sources", []any{sourceName},
	))
	applyData, applyMsgs, err := gjPost(ctx, endpoint, adminToken,
		`mutation { gj_config(id: "current", update: `+applyInline+`) { applied errors_json } }`)
	if err != nil {
		return err
	}
	av, _ := jsGet(applyData, "gj_config").(*jsObj)
	if len(applyMsgs) > 0 || av == nil || av.vals["applied"] != true {
		msg := strings.Join(applyMsgs, "; ")
		if msg == "" && av != nil {
			msg = jsStr(av.vals["errors_json"])
		}
		if msg == "" {
			msg = "rollback apply was rejected"
		}
		return errors.New(msg)
	}
	return nil
}

func graphqlEndpointFromURL(raw string) string {
	trimmed := strings.TrimRight(raw, "/")
	if strings.HasSuffix(trimmed, "/api/v1/graphql") {
		return trimmed
	}
	return trimmed + "/api/v1/graphql"
}

func managedOpenAPISpecFilename(orgID, sourceName string) (string, error) {
	cleaned := strings.Trim(openAPISourceClean.ReplaceAllString(strings.ToLower(strings.TrimSpace(sourceName)), "-"), "-")
	if len(cleaned) > 64 {
		cleaned = cleaned[:64]
	}
	if cleaned == "" {
		return "", errors.New("API source name must contain letters or digits")
	}
	sum := sha256.Sum256([]byte(orgID))
	org := hex.EncodeToString(sum[:])[:12]
	return fmt.Sprintf("org_%s_%s.yaml", org, strings.ReplaceAll(cleaned, "-", "_")), nil
}

func persistGraphjinSourceConfigUpdate(configFile string, update *jsObj) error {
	raw, err := os.ReadFile(configFile)
	if err != nil {
		return err
	}
	info, err := os.Stat(configFile)
	if err != nil {
		return err
	}
	doc, err := mergeGraphjinConfigUpdate(raw, update)
	if err != nil {
		return err
	}
	merged, err := encodeYAML(doc)
	if err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.%s.tmp", configFile, rand.Text())
	if err := os.WriteFile(tmp, []byte(merged), info.Mode().Perm()); err != nil {
		return err
	}
	if err := os.Rename(tmp, configFile); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func (s managedSource) markApproved() error {
	marker, err := s.controlPath("approved")
	if err != nil {
		return err
	}
	if err := writeNew(marker, []byte(jsTime(time.Now())), 0o600); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return errors.New("managed file source is already active")
		}
		return err
	}
	return nil
}

func (s managedSource) verifyManifest(manifest *jsObj) error {
	rawFiles, _ := manifest.vals["files"].([]any)
	if len(rawFiles) < 1 || len(rawFiles) > maxManagedFileSourceCount {
		return errors.New("managed file source manifest has an invalid file count")
	}
	dir, err := s.ensureDir()
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	names := map[string]bool{}
	var totalSize int64
	for _, item := range rawFiles {
		expected, _ := item.(*jsObj)
		if expected == nil {
			return errors.New("managed file source manifest has an invalid file entry")
		}
		name, err := safeManagedFileName(jsStr(expected.vals["name"]))
		if err != nil {
			return err
		}
		if names[name] {
			return errors.New("managed file source manifest has duplicate names")
		}
		names[name] = true
		target := filepath.Join(dir, name)
		info, err := os.Lstat(target)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("managed file %q is not a regular file", name)
		}
		expectedSize := int64(jsValueNumber(expected.vals["size"]))
		if info.Size() != expectedSize || info.Size() > maxManagedFileSize {
			return fmt.Errorf("managed file %q failed its size check", name)
		}
		totalSize += info.Size()
		content, err := os.ReadFile(target)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(content)
		if hex.EncodeToString(sum[:]) != jsStr(expected.vals["checksumSha256"]) {
			return fmt.Errorf("managed file %q failed its integrity check", name)
		}
	}
	if len(entries) != len(names) {
		return errors.New("managed file source contents do not match its approved manifest")
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !names[entry.Name()] {
			return errors.New("managed file source contents do not match its approved manifest")
		}
	}
	hasTotal := manifest.vals["totalSize"] != nil
	expectedTotal := int64(jsValueNumber(manifest.vals["totalSize"]))
	if totalSize > maxManagedFileSourceSize || (hasTotal && expectedTotal != totalSize) {
		return errors.New("managed file source manifest exceeds its storage limit")
	}
	return nil
}

func orNil(v any) any {
	if v == nil {
		return nil
	}
	if s, ok := v.(string); ok && s == "" {
		return nil
	}
	return v
}

func orNilStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func orNilPtr(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}
