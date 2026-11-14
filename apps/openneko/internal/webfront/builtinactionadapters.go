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
	unsafeSegmentChars    = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)
	dataSourceNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)
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
	headers := map[string]string{}
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
		return nil, fmt.Errorf("webhook request to %s failed: %v", rawURL, err)
	}
	for k, v := range headers {
		httpReq.Header.Set(k, v)
	}
	resp, err := (&http.Client{}).Do(httpReq)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(reqCtx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("webhook request to %s timed out after %dms", rawURL, timeoutMs)
		}
		return nil, fmt.Errorf("webhook request to %s failed: %v", rawURL, err)
	}
	defer resp.Body.Close()
	rawBody, err := io.ReadAll(io.LimitReader(resp.Body, webhookMaxBodyCapture+1024))
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(reqCtx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("webhook request to %s timed out after %dms", rawURL, timeoutMs)
		}
		return nil, fmt.Errorf("webhook request to %s failed: %v", rawURL, err)
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
		return nil, fmt.Errorf("webhook %s %s returned %d %s: %s", method, rawURL, resp.StatusCode, statusText, capturedBody)
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
	dir := filepath.Join(agentsRootDir(), "orgs", sanitizePathSegment(req.OrgID), "artifacts", "patches")
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
	action := strings.TrimSpace(jsStr(payload.vals["action"]))
	text := func(key string) (string, error) {
		v, ok := payload.vals[key].(string)
		if !ok || strings.TrimSpace(v) == "" {
			return "", fmt.Errorf("group_admin %s: %s required", action, key)
		}
		return strings.TrimSpace(v), nil
	}
	done := func(result *jsObj, graphjin bool) (*actionExecutionOutcome, error) {
		if graphjin {
			a.scheduleGroupGrantsApply(ctx)
		}
		cmd := action
		return &actionExecutionOutcome{
			Status:             "succeeded",
			CommandOrOperation: &cmd,
			Result:             result,
		}, nil
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
		groupVal, _ := jsFromGo(group).(*jsObj)
		return done(jsObjOf("group", groupVal), true)

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
		return done(jsObjOf("ok", true), true)

	case "add_member":
		groupID, err := text("groupId")
		if err != nil {
			return nil, err
		}
		userID, err := text("userId")
		if err != nil {
			return nil, err
		}
		if err := a.addLocalMemberAction(ctx, req.OrgID, groupID, userID); err != nil {
			return nil, err
		}
		return done(jsObjOf("ok", true), false)

	case "remove_member":
		groupID, err := text("groupId")
		if err != nil {
			return nil, err
		}
		userID, err := text("userId")
		if err != nil {
			return nil, err
		}
		stillMember, err := a.removeLocalMemberAction(ctx, req.OrgID, groupID, userID)
		if err != nil {
			return nil, err
		}
		return done(jsObjOf("ok", true, "stillMember", stillMember), false)

	case "add_idp_rule":
		ssoGroupID, err := text("ssoGroupId")
		if err != nil {
			return nil, err
		}
		userGroupID, err := text("userGroupId")
		if err != nil {
			return nil, err
		}
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
				returning id::text`, req.OrgID, ssoGroupID, userGroupID, nullableStringPtr(req.ApprovedBy)).Scan(&ruleID); err != nil {
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
		return done(jsObjOf("ok", true), false)

	case "grant_item", "revoke_item":
		itemType, err := text("itemType")
		if err != nil {
			return nil, err
		}
		if !isItemType(itemType) {
			return nil, fmt.Errorf("group_admin: unknown itemType %s", itemType)
		}
		groupID, err := text("groupId")
		if err != nil {
			return nil, err
		}
		itemID, err := text("itemId")
		if err != nil {
			return nil, err
		}
		if action == "grant_item" {
			itemID = strings.TrimSpace(itemID)
			if itemID == "" || len(itemID) > 500 {
				return nil, groupErr("invalid", "item id must be 1 to 500 characters")
			}
			g, err := requireGrantableGroup(ctx, a.DB, req.OrgID, groupID)
			if err != nil {
				return nil, err
			}
			var created bool
			err = pgx.BeginFunc(ctx, a.DB, func(tx pgx.Tx) error {
				var inserted string
				qerr := tx.QueryRow(ctx, `
					insert into item_grant (org_id, group_id, item_type, item_id, created_by_user_id, action_request_id)
					values ($1, $2::uuid, $3, $4, $5, $6::uuid)
					on conflict do nothing returning item_id`,
					req.OrgID, g.id, itemType, itemID, nullableStringPtr(req.ApprovedBy), req.ID).Scan(&inserted)
				if qerr != nil && !errors.Is(qerr, pgx.ErrNoRows) {
					return qerr
				}
				if qerr == nil {
					created = true
					_, err := tx.Exec(ctx, `
						insert into item_grant_audit (org_id, actor_user_id, action, group_id, group_name, item_type, item_id, action_request_id)
						values ($1, $2, 'grant', $3::uuid, $4, $5, $6, $7::uuid)`,
						req.OrgID, nullableStringPtr(req.ApprovedBy), g.id, g.name, itemType, itemID, req.ID)
					return err
				}
				return nil
			})
			if err != nil {
				return nil, err
			}
			return done(jsObjOf("created", created), false)
		}
		g, err := requireGrantableGroup(ctx, a.DB, req.OrgID, groupID)
		if err != nil {
			return nil, err
		}
		var removed bool
		err = pgx.BeginFunc(ctx, a.DB, func(tx pgx.Tx) error {
			var deleted string
			qerr := tx.QueryRow(ctx, `
				delete from item_grant
				where org_id = $1 and group_id = $2::uuid and item_type = $3 and item_id = $4
				returning item_id`, req.OrgID, g.id, itemType, itemID).Scan(&deleted)
			if qerr != nil && !errors.Is(qerr, pgx.ErrNoRows) {
				return qerr
			}
			if qerr == nil {
				removed = true
				_, err := tx.Exec(ctx, `
					insert into item_grant_audit (org_id, actor_user_id, action, group_id, group_name, item_type, item_id, action_request_id)
					values ($1, $2, 'revoke', $3::uuid, $4, $5, $6, $7::uuid)`,
					req.OrgID, nullableStringPtr(req.ApprovedBy), g.id, g.name, itemType, itemID, req.ID)
				return err
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		return done(jsObjOf("removed", removed), false)

	case "upsert_data_access_rule":
		groupID, err := text("groupId")
		if err != nil {
			return nil, err
		}
		source, err := text("source")
		if err != nil {
			return nil, err
		}
		tableName, err := text("tableName")
		if err != nil {
			return nil, err
		}
		tableSchema := ""
		if s, ok := payload.vals["tableSchema"].(string); ok {
			tableSchema = s
		}
		var cols []string
		if rawCols, ok := payload.vals["columns"].([]any); ok {
			seen := map[string]bool{}
			for _, item := range rawCols {
				if s, ok := item.(string); ok {
					trimmed := strings.TrimSpace(s)
					if trimmed != "" && !seen[trimmed] {
						seen[trimmed] = true
						cols = append(cols, trimmed)
					}
				}
			}
		}
		rowFilter, err := parseRowFilter(plainJSON(payload.vals["rowFilter"]))
		if err != nil {
			return nil, err
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
			req.OrgID, groupID, source, tableSchema, tableName, cols, filterJSON, nullableStringPtr(req.ApprovedBy)).Scan(&ruleID); err != nil {
			return nil, err
		}
		rules, err := a.dataAccessRules(ctx, req.OrgID, &groupID)
		if err != nil {
			return nil, err
		}
		var matched any
		for _, r := range rules {
			if r.ID == ruleID {
				matched = jsFromGo(r)
				break
			}
		}
		return done(jsObjOf("rule", matched), true)

	case "delete_data_access_rule":
		ruleID, err := text("ruleId")
		if err != nil {
			return nil, err
		}
		tag, err := a.DB.Exec(ctx, `delete from data_access_rule where org_id = $1 and id = $2::uuid`, req.OrgID, ruleID)
		if err != nil {
			return nil, err
		}
		return done(jsObjOf("deleted", tag.RowsAffected() > 0), true)

	default:
		return nil, fmt.Errorf("group_admin: unknown action %s", jsTemplate(payload.vals["action"]))
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
	email := strings.ToLower(strings.TrimSpace(jsStr(payload.vals["email"])))
	userID := strings.TrimSpace(jsStr(payload.vals["userId"]))

	var targetID, targetEmail string
	var err error
	if userID != "" {
		err = a.DB.QueryRow(ctx, `select id, email from app_user where org_id = $1 and id = $2 limit 1`, req.OrgID, userID).Scan(&targetID, &targetEmail)
	} else if email != "" {
		err = a.DB.QueryRow(ctx, `select id, email from app_user where org_id = $1 and email = $2 limit 1`, req.OrgID, email).Scan(&targetID, &targetEmail)
	} else {
		err = pgx.ErrNoRows
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("user_admin: target user not found in org %s", req.OrgID)
	}
	if err != nil {
		return nil, err
	}

	switch action {
	case "set_role":
		role := jsStr(payload.vals["role"])
		if role != "admin" && role != "member" {
			return nil, fmt.Errorf("user_admin: invalid role %q", jsTemplate(payload.vals["role"]))
		}
		var adminGroupID string
		if err := a.DB.QueryRow(ctx, `select id::text from user_group where org_id = $1 and slug = 'administrators'`, req.OrgID).Scan(&adminGroupID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, groupErr("not_found", "built-in group administrators is missing")
			}
			return nil, err
		}
		if role == "admin" {
			if err := a.addLocalMemberAction(ctx, req.OrgID, adminGroupID, targetID); err != nil {
				return nil, err
			}
		} else {
			stillMember, err := a.removeLocalMemberAction(ctx, req.OrgID, adminGroupID, targetID)
			if err != nil {
				return nil, err
			}
			if stillMember {
				return nil, groupErr("conflict", "this user is an administrator through an IdP rule; change the rule instead")
			}
		}
		if _, err := a.DB.Exec(ctx, `update app_user set updated_at = now() where id = $1`, targetID); err != nil {
			return nil, err
		}
		cmd := fmt.Sprintf("set role of %s to %s", targetEmail, role)
		return &actionExecutionOutcome{
			Status:             "succeeded",
			CommandOrOperation: &cmd,
			ExternalRef:        &targetID,
			Result:             jsObjOf("userId", targetID, "email", targetEmail, "role", role),
		}, nil

	case "disable":
		if err := a.withAdministratorGuard(ctx, req.OrgID, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `update app_user set disabled_at = now(), updated_at = now() where id = $1`, targetID)
			return err
		}); err != nil {
			return nil, err
		}
		cmd := fmt.Sprintf("disabled %s", targetEmail)
		return &actionExecutionOutcome{
			Status:             "succeeded",
			CommandOrOperation: &cmd,
			ExternalRef:        &targetID,
			Result:             jsObjOf("userId", targetID, "email", targetEmail, "disabled", true),
		}, nil

	case "enable":
		if _, err := a.DB.Exec(ctx, `update app_user set disabled_at = null, updated_at = now() where id = $1`, targetID); err != nil {
			return nil, err
		}
		cmd := fmt.Sprintf("enabled %s", targetEmail)
		return &actionExecutionOutcome{
			Status:             "succeeded",
			CommandOrOperation: &cmd,
			ExternalRef:        &targetID,
			Result:             jsObjOf("userId", targetID, "email", targetEmail, "disabled", false),
		}, nil

	default:
		return nil, fmt.Errorf("user_admin: unknown action %s", jsTemplate(payload.vals["action"]))
	}
}

// executeChannelAdmin matches registerChannelAdminAdapter in
// apps/worker/src/admin-actions/adapters.ts.
func (a *API) executeChannelAdmin(ctx context.Context, req *actionRequestRecord) (*actionExecutionOutcome, error) {
	payload := req.Payload
	if payload == nil {
		payload = &jsObj{vals: map[string]any{}}
	}
	action := jsStr(payload.vals["action"])

	switch action {
	case "upsert_workspace":
		plugin := strings.TrimSpace(jsStr(payload.vals["channelPlugin"]))
		workspaceID := strings.TrimSpace(jsStr(payload.vals["workspaceId"]))
		if plugin == "" || workspaceID == "" {
			return nil, errors.New("channel_admin upsert_workspace requires channelPlugin + workspaceId")
		}
		var existingOrg string
		err := a.DB.QueryRow(ctx, `select org_id from channel_workspace where channel_plugin = $1 and workspace_id = $2 limit 1`, plugin, workspaceID).Scan(&existingOrg)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		if err == nil && existingOrg != req.OrgID {
			return nil, fmt.Errorf("workspace %s:%s is already bound to another org", plugin, workspaceID)
		}
		var id string
		if err := a.DB.QueryRow(ctx, `
			insert into channel_workspace (org_id, channel_plugin, workspace_id)
			values ($1, $2, $3)
			on conflict (channel_plugin, workspace_id)
			do update set org_id = channel_workspace.org_id
			returning id::text`, req.OrgID, plugin, workspaceID).Scan(&id); err != nil {
			return nil, err
		}
		cmd := fmt.Sprintf("bound %s workspace %s", plugin, workspaceID)
		return &actionExecutionOutcome{
			Status:             "succeeded",
			CommandOrOperation: &cmd,
			ExternalRef:        &id,
			Result:             jsObjOf("id", id, "channelPlugin", plugin, "workspaceId", workspaceID),
		}, nil

	case "remove_workspace":
		id := strings.TrimSpace(jsStr(payload.vals["id"]))
		if id == "" {
			return nil, errors.New("channel_admin remove_workspace requires id")
		}
		if _, err := a.DB.Exec(ctx, `delete from channel_workspace where id = $1::uuid and org_id = $2`, id, req.OrgID); err != nil {
			return nil, err
		}
		cmd := fmt.Sprintf("unbound workspace %s", id)
		return &actionExecutionOutcome{
			Status:             "succeeded",
			CommandOrOperation: &cmd,
			ExternalRef:        &id,
			Result:             jsObjOf("id", id, "removed", true),
		}, nil

	case "link_identity":
		identityID := strings.TrimSpace(jsStr(payload.vals["identityId"]))
		userEmail := strings.ToLower(strings.TrimSpace(jsStr(payload.vals["userEmail"])))
		userID := strings.TrimSpace(jsStr(payload.vals["userId"]))
		if identityID == "" || (userEmail == "" && userID == "") {
			return nil, errors.New("channel_admin link_identity requires identityId + userEmail or userId")
		}
		var foundID, foundEmail string
		var err error
		if userID != "" {
			err = a.DB.QueryRow(ctx, `select id, email from app_user where org_id = $1 and id = $2 limit 1`, req.OrgID, userID).Scan(&foundID, &foundEmail)
		} else {
			err = a.DB.QueryRow(ctx, `select id, email from app_user where org_id = $1 and email = $2 limit 1`, req.OrgID, userEmail).Scan(&foundID, &foundEmail)
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errors.New("channel_admin link_identity: target user not found in org")
		}
		if err != nil {
			return nil, err
		}
		if _, err := a.DB.Exec(ctx, `update channel_identity set app_user_id = $1, status = 'linked' where id = $2::uuid and org_id = $3`, foundID, identityID, req.OrgID); err != nil {
			return nil, err
		}
		cmd := fmt.Sprintf("linked identity %s → %s", identityID, foundEmail)
		return &actionExecutionOutcome{
			Status:             "succeeded",
			CommandOrOperation: &cmd,
			ExternalRef:        &identityID,
			Result:             jsObjOf("identityId", identityID, "userId", foundID, "email", foundEmail, "status", "linked"),
		}, nil

	case "block_identity":
		identityID := strings.TrimSpace(jsStr(payload.vals["identityId"]))
		if identityID == "" {
			return nil, errors.New("channel_admin block_identity requires identityId")
		}
		if _, err := a.DB.Exec(ctx, `update channel_identity set app_user_id = null, status = 'blocked' where id = $1::uuid and org_id = $2`, identityID, req.OrgID); err != nil {
			return nil, err
		}
		cmd := fmt.Sprintf("blocked identity %s", identityID)
		return &actionExecutionOutcome{
			Status:             "succeeded",
			CommandOrOperation: &cmd,
			ExternalRef:        &identityID,
			Result:             jsObjOf("identityId", identityID, "status", "blocked"),
		}, nil

	case "reset_identity":
		identityID := strings.TrimSpace(jsStr(payload.vals["identityId"]))
		if identityID == "" {
			return nil, errors.New("channel_admin reset_identity requires identityId")
		}
		if _, err := a.DB.Exec(ctx, `update channel_identity set app_user_id = null, status = 'pending' where id = $1::uuid and org_id = $2`, identityID, req.OrgID); err != nil {
			return nil, err
		}
		cmd := fmt.Sprintf("reset identity %s to pending", identityID)
		return &actionExecutionOutcome{
			Status:             "succeeded",
			CommandOrOperation: &cmd,
			ExternalRef:        &identityID,
			Result:             jsObjOf("identityId", identityID, "status", "pending"),
		}, nil

	default:
		return nil, fmt.Errorf("channel_admin: unknown action %s", jsTemplate(payload.vals["action"]))
	}
}

type dataSourceActionTarget struct {
	id         string
	name       string
	graphqlURL string
	enabled    bool
}

func (a *API) resolveDataSourceActionTarget(ctx context.Context, orgID string, payload *jsObj) (*dataSourceActionTarget, error) {
	id := strings.TrimSpace(jsStr(payload.vals["id"]))
	name := strings.ToLower(strings.TrimSpace(jsStr(payload.vals["name"])))
	var row dataSourceActionTarget
	var graphqlURL *string
	var err error
	if id != "" {
		err = a.DB.QueryRow(ctx, `select id::text, name, graphql_url, enabled from data_source where org_id = $1 and id = $2::uuid limit 1`, orgID, id).
			Scan(&row.id, &row.name, &graphqlURL, &row.enabled)
	} else if name != "" {
		err = a.DB.QueryRow(ctx, `select id::text, name, graphql_url, enabled from data_source where org_id = $1 and name = $2 limit 1`, orgID, name).
			Scan(&row.id, &row.name, &graphqlURL, &row.enabled)
	} else {
		return nil, nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	row.graphqlURL = deref(graphqlURL)
	return &row, nil
}

// executeDataSourceAdmin matches registerDataSourceAdminAdapter in
// apps/worker/src/admin-actions/adapters.ts.
func (a *API) executeDataSourceAdmin(ctx context.Context, req *actionRequestRecord) (*actionExecutionOutcome, error) {
	payload := req.Payload
	if payload == nil {
		payload = &jsObj{vals: map[string]any{}}
	}
	action := jsStr(payload.vals["action"])

	switch action {
	case "register_placeholder":
		name := strings.ToLower(strings.TrimSpace(jsStr(payload.vals["name"])))
		var label *string
		if s, ok := payload.vals["label"].(string); ok && strings.TrimSpace(s) != "" {
			trimmed := strings.TrimSpace(s)
			label = &trimmed
		}
		kind := jsStr(payload.vals["kind"])
		if kind == "" {
			kind = "graphjin"
		}
		if !dataSourceNamePattern.MatchString(name) {
			return nil, errors.New("data_source_admin register_placeholder: name must match /^[a-z][a-z0-9_-]{0,62}$/")
		}
		if kind != "graphjin" && kind != "rest" && kind != "graphql" {
			return nil, fmt.Errorf("data_source_admin: unsupported kind %s", kind)
		}
		var exists bool
		if err := a.DB.QueryRow(ctx, `select exists (select 1 from data_source where org_id = $1 and name = $2)`, req.OrgID, name).Scan(&exists); err != nil {
			return nil, err
		}
		if exists {
			return nil, fmt.Errorf("data_source_admin: source %q already exists in org", name)
		}
		var id string
		if err := a.DB.QueryRow(ctx, `
			insert into data_source (org_id, name, label, kind, graphql_url, auth_mode, enabled, is_default)
			values ($1, $2, $3, $4, '', 'none', false, false)
			returning id::text`, req.OrgID, name, label, kind).Scan(&id); err != nil {
			return nil, err
		}
		cmd := fmt.Sprintf("registered disabled placeholder data source %s", name)
		return &actionExecutionOutcome{
			Status:             "succeeded",
			CommandOrOperation: &cmd,
			ExternalRef:        &id,
			Result: jsObjOf(
				"id", id,
				"name", name,
				"kind", kind,
				"enabled", false,
				"nextStep", "Fill in the GraphQL URL + auth on /admin/settings/data-sources and enable.",
			),
		}, nil

	case "set_enabled":
		enabled, hasEnabled := payload.vals["enabled"].(bool)
		target, err := a.resolveDataSourceActionTarget(ctx, req.OrgID, payload)
		if err != nil {
			return nil, err
		}
		if target == nil || !hasEnabled {
			return nil, errors.New("data_source_admin set_enabled: target source not found or enabled flag missing")
		}
		if enabled && strings.TrimSpace(target.graphqlURL) == "" {
			return nil, fmt.Errorf("data_source_admin: cannot enable %q until its GraphQL URL is configured in settings", target.name)
		}
		if _, err := a.DB.Exec(ctx, `update data_source set enabled = $1, updated_at = now() where id = $2::uuid and org_id = $3`, enabled, target.id, req.OrgID); err != nil {
			return nil, err
		}
		verb := "disabled"
		if enabled {
			verb = "enabled"
		}
		cmd := fmt.Sprintf("%s data source %s", verb, target.name)
		return &actionExecutionOutcome{
			Status:             "succeeded",
			CommandOrOperation: &cmd,
			ExternalRef:        &target.id,
			Result:             jsObjOf("id", target.id, "name", target.name, "enabled", enabled),
		}, nil

	case "set_default":
		target, err := a.resolveDataSourceActionTarget(ctx, req.OrgID, payload)
		if err != nil {
			return nil, err
		}
		if target == nil {
			return nil, errors.New("data_source_admin set_default: target source not found")
		}
		if !target.enabled {
			return nil, fmt.Errorf("data_source_admin: enable %q before marking it default", target.name)
		}
		if _, err := a.DB.Exec(ctx, `update data_source set is_default = false, updated_at = now() where org_id = $1`, req.OrgID); err != nil {
			return nil, err
		}
		if _, err := a.DB.Exec(ctx, `update data_source set is_default = true, updated_at = now() where id = $1::uuid and org_id = $2`, target.id, req.OrgID); err != nil {
			return nil, err
		}
		cmd := fmt.Sprintf("set %s as default data source", target.name)
		return &actionExecutionOutcome{
			Status:             "succeeded",
			CommandOrOperation: &cmd,
			ExternalRef:        &target.id,
			Result:             jsObjOf("id", target.id, "name", target.name, "isDefault", true),
		}, nil

	case "remove":
		target, err := a.resolveDataSourceActionTarget(ctx, req.OrgID, payload)
		if err != nil {
			return nil, err
		}
		if target == nil {
			return nil, errors.New("data_source_admin remove: target source not found")
		}
		if _, err := a.DB.Exec(ctx, `delete from data_source where id = $1::uuid and org_id = $2`, target.id, req.OrgID); err != nil {
			return nil, err
		}
		cmd := fmt.Sprintf("removed data source %s", target.name)
		return &actionExecutionOutcome{
			Status:             "succeeded",
			CommandOrOperation: &cmd,
			ExternalRef:        &target.id,
			Result:             jsObjOf("id", target.id, "name", target.name, "removed", true),
		}, nil

	default:
		return nil, fmt.Errorf("data_source_admin: unknown action %s", jsTemplate(payload.vals["action"]))
	}
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
		rows, err = a.DB.Query(ctx, `select id::text, name, graphql_url from data_source where org_id = $1 and enabled = true limit 3`, orgID)
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
				time.Sleep(50 * time.Millisecond)
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
				time.Sleep(50 * time.Millisecond)
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

func agentsRootDir() string {
	return orDefault(strings.TrimSpace(os.Getenv("OPENNEKO_AGENTS_ROOT")), "/var/lib/openneko/agents")
}

func sanitizePathSegment(value string) string {
	cleaned := strings.TrimLeft(unsafeSegmentChars.ReplaceAllString(value, "-"), ".")
	if len(cleaned) > 64 {
		cleaned = cleaned[:64]
	}
	if cleaned == "" {
		return "default"
	}
	return cleaned
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
