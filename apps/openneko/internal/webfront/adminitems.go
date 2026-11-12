package webfront

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/open-neko/neko/apps/openneko/internal/config"
)

var itemTypes = []struct{ Type, Label, Plural string }{
	{"skill", "Skill", "Skills"},
	{"workflow", "Workflow", "Workflows"},
	{"metric", "Metric", "Metrics"},
	{"dashboard", "Dashboard", "Dashboards"},
	{"watcher", "Watcher", "Watchers"},
	{"team_memory", "Team memory", "Team memory"},
	{"data_source", "Data source", "Data sources"},
	{"saved_query", "Saved query", "Saved queries"},
	{"api_operation", "API operation", "API operations"},
	{"action", "Action", "Actions"},
	{"integration", "Integration", "Integrations"},
	{"channel", "Channel", "Channels"},
	{"pack", "Pack", "Packs"},
}

func isItemType(v any) bool {
	s, ok := v.(string)
	return ok && slices.ContainsFunc(itemTypes, func(t struct{ Type, Label, Plural string }) bool { return t.Type == s })
}

type itemOption struct {
	ID     string  `json:"id"`
	Label  string  `json:"label"`
	Detail *string `json:"detail,omitempty"`
}

func option(id, label, detail string) itemOption { return itemOption{id, label, &detail} }

func errorMessage(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Message
	}
	return err.Error()
}

func (a *API) adminItems(w http.ResponseWriter, r *http.Request) {
	act, ok := a.accessAdmin(w, r)
	if !ok {
		return
	}
	typ := r.URL.Query().Get("type")
	if typ == "" {
		type label struct {
			Type   string `json:"type"`
			Label  string `json:"label"`
			Plural string `json:"plural"`
		}
		out := make([]label, len(itemTypes))
		for i, t := range itemTypes {
			out[i] = label(t)
		}
		sendJSON(w, http.StatusOK, map[string]any{"types": out})
		return
	}
	if !isItemType(typ) {
		sendError(w, http.StatusBadRequest, "unknown item type")
		return
	}
	items, err := a.listItemOptions(r.Context(), act.OrgID, typ)
	if err != nil {
		sendError(w, http.StatusInternalServerError, errorMessage(err))
		return
	}
	sendJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) queryOptions(ctx context.Context, sql string, args ...any) ([]itemOption, error) {
	rows, err := a.DB.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (itemOption, error) {
		var id, label, detail string
		err := row.Scan(&id, &label, &detail)
		return option(id, label, detail), err
	})
}

// listItemOptions is listItemOptions() in apps/web/src/lib/groups-admin.ts.
func (a *API) listItemOptions(ctx context.Context, orgID, typ string) ([]itemOption, error) {
	out := []itemOption{}
	switch typ {
	case "skill":
		roots, err := orgWorkspace(orgID)
		if err != nil {
			return nil, err
		}
		for _, s := range listWorkSkills(roots) {
			out = append(out, option(s.Name, s.Name, s.Description))
		}
		return out, nil
	case "workflow":
		return a.queryOptions(ctx, `select id::text, name, description from workflow_definition
			where org_id = $1 and owner_user_id = '' order by updated_at desc`, orgID)
	case "metric":
		return a.queryOptions(ctx, `select id::text, title, role from metric where org_id = $1 and active = true order by title`, orgID)
	case "dashboard":
		roles, err := collectStrings(ctx, a, `select distinct role from metric where org_id = $1
			union select distinct role_template from operator_profile where org_id = $1 and role_template <> ''`, orgID)
		if err != nil {
			return nil, err
		}
		roles = slices.DeleteFunc(roles, func(s string) bool { return s == "" })
		slices.Sort(roles)
		for _, role := range roles {
			out = append(out, itemOption{ID: role, Label: role + " dashboard"})
		}
		return out, nil
	case "watcher":
		return a.queryOptions(ctx, `select id::text, name, description from watcher where org_id = $1 order by name`, orgID)
	case "team_memory", "data_source", "saved_query":
		kind := "database"
		if typ == "saved_query" {
			kind = "saved_query"
		}
		rows, err := a.graphjinCatalog(ctx, orgID, kind, nil)
		if err != nil {
			return nil, err
		}
		if typ == "team_memory" {
			out = append(out, itemOption{ID: "global", Label: "Global company memory"})
		}
		for _, row := range rows {
			name := row.ID
			if row.Name != nil {
				name = *row.Name
			}
			if typ == "team_memory" {
				out = append(out, itemOption{ID: "database:" + name, Label: "Memory for " + name})
			} else {
				out = append(out, itemOption{ID: name, Label: name, Detail: row.Summary})
			}
		}
		return out, nil
	case "api_operation":
		var body struct {
			Operations []string `json:"operations"`
		}
		if _, reply, err := a.workerDo(ctx, http.MethodGet, "/admin/graphjin/api-operations", workerAdminTimeout, nil); err == nil {
			_ = json.Unmarshal(reply, &body)
		}
		for _, id := range body.Operations {
			parts := strings.Split(id, ":")
			out = append(out, option(id, strings.Join(parts[1:], " · "), parts[0]))
		}
		return out, nil
	case "action":
		out, err := a.queryOptions(ctx, `select kind, kind, description from pack_action_definition where org_id = $1`, orgID)
		slices.SortStableFunc(out, func(x, y itemOption) int { return localeCompare(x.Label, y.Label) })
		return out, err
	case "integration":
		var body struct {
			Providers []struct {
				PluginName    string `json:"pluginName"`
				ProviderLabel string `json:"providerLabel"`
			} `json:"providers"`
		}
		if status, reply, err := a.workerDo(ctx, http.MethodGet, "/admin/connect/providers", 2*time.Second, nil); err == nil && status >= 200 && status < 300 {
			_ = json.Unmarshal(reply, &body)
		}
		for _, p := range body.Providers {
			out = append(out, option(p.PluginName, p.ProviderLabel, p.PluginName))
		}
		return out, nil
	case "channel":
		var body struct {
			Providers []struct {
				ID            string `json:"id"`
				ProviderLabel string `json:"providerLabel"`
			} `json:"providers"`
		}
		if _, reply, err := a.workerDo(ctx, http.MethodGet, "/admin/channels/providers", workerAdminTimeout, nil); err == nil {
			_ = json.Unmarshal(reply, &body)
		}
		labels := map[string]string{}
		for _, p := range body.Providers {
			labels[p.ID] = p.ProviderLabel
		}
		plugins, err := collectStrings(ctx, a, `select distinct channel_plugin from channel_identity where org_id = $1`, orgID)
		if err != nil {
			return nil, err
		}
		for _, p := range plugins {
			if _, ok := labels[p]; !ok {
				labels[p] = p
			}
		}
		for id, label := range labels {
			out = append(out, option(id, label, id))
		}
		slices.SortFunc(out, func(x, y itemOption) int { return localeCompare(x.ID, y.ID) })
		return out, nil
	case "pack":
		return a.queryOptions(ctx, `select pack_id, pack_id, 'v' || version from pack_install where org_id = $1 and status <> 'removed'`, orgID)
	}
	return out, nil
}

type catalogRow struct {
	ID           string  `json:"id"`
	Name         *string `json:"name"`
	Summary      *string `json:"summary"`
	DatabaseName *string `json:"database_name"`
	SchemaName   *string `json:"schema_name"`
	TableName    *string `json:"table_name"`
	ColumnName   *string `json:"column_name"`
}

func (a *API) graphjinAdminToken(orgID string, ttl time.Duration) (string, error) {
	return a.graphjinToken(orgID, "admin", ttl)
}

// graphjinToken is mintGraphjinToken({ role, userId: null }) in
// packages/llm/src/graphjin/token.ts. GraphJin keys the HMAC with the base64
// text of the derived secret, not with its bytes.
func (a *API) graphjinToken(orgID, role string, ttl time.Duration) (string, error) {
	secret, err := config.SigningSecret(a.SecretsDir, "graphjin:"+orgID)
	if err != nil {
		return "", err
	}
	now := time.Now().Unix()
	payload, _ := json.Marshal(struct {
		Sub       string   `json:"sub"`
		Role      string   `json:"role"`
		Roles     []string `json:"roles"`
		AccountID string   `json:"account_id"`
		OrgID     string   `json:"org_id"`
		Iat       int64    `json:"iat"`
		Exp       int64    `json:"exp"`
	}{"service", role, []string{role}, orgID, orgID, now, now + int64(ttl/time.Second)})
	enc := base64.RawURLEncoding
	unsigned := enc.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`)) + "." + enc.EncodeToString(payload)
	mac := hmac.New(sha256.New, []byte(base64.StdEncoding.EncodeToString(secret)))
	mac.Write([]byte(unsigned))
	return unsigned + "." + enc.EncodeToString(mac.Sum(nil)), nil
}

func catalogWhere(kind string, databaseName *string) string {
	byKind := fmt.Sprintf("{ kind: { eq: %s } }", jsStringify(kind, false))
	if databaseName == nil {
		return byKind
	}
	return fmt.Sprintf("{ and: [%s, { database_name: { eq: %s } }] }", byKind, jsStringify(*databaseName, false))
}

// graphjinCatalog is graphjinCatalog() in apps/web/src/lib/groups-admin.ts:
// GraphJin that does not answer gives the rows read so far.
func (a *API) graphjinCatalog(ctx context.Context, orgID, kind string, databaseName *string) ([]catalogRow, error) {
	var graphqlURL *string
	err := a.DB.QueryRow(ctx, `select graphql_url from data_source where org_id = $1 and enabled = true
		order by is_default desc, created_at limit 1`, orgID).Scan(&graphqlURL)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && (graphqlURL == nil || *graphqlURL == "") {
		return []catalogRow{}, nil
	}
	if err != nil {
		return nil, err
	}
	endpoint := strings.TrimRight(*graphqlURL, "/")
	if !strings.HasSuffix(endpoint, "/api/v1/graphql") {
		endpoint += "/api/v1/graphql"
	}
	token, err := a.graphjinAdminToken(orgID, 120*time.Second)
	if err != nil {
		return nil, err
	}
	const pageSize = 1000
	rows := []catalogRow{}
	for offset := 0; offset < 200_000; offset += pageSize {
		query := fmt.Sprintf("query AdminCatalog { gj_catalog(where: %s, limit: %d, offset: %d, order_by: { id: asc }) { id name summary database_name schema_name table_name column_name } }",
			catalogWhere(kind, databaseName), pageSize, offset)
		page, ok := catalogPage(ctx, endpoint, token, query)
		if !ok {
			return rows, nil
		}
		rows = append(rows, page...)
		if len(page) < pageSize {
			break
		}
	}
	return rows, nil
}

func catalogPage(ctx context.Context, endpoint, token, query string) ([]catalogRow, bool) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	payload, _ := json.Marshal(map[string]any{"query": query, "variables": map[string]any{}})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, false
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := graphjinClient.Do(req)
	if err != nil {
		return nil, false
	}
	defer res.Body.Close()
	text, err := io.ReadAll(res.Body)
	if err != nil || res.StatusCode < 200 || res.StatusCode > 299 {
		return nil, false
	}
	var body struct {
		Data *struct {
			Catalog []catalogRow `json:"gj_catalog"`
		} `json:"data"`
		Errors json.RawMessage `json:"errors"`
	}
	if json.Unmarshal(text, &body) != nil {
		return nil, false
	}
	var errs []json.RawMessage
	if json.Unmarshal(body.Errors, &errs) == nil && len(errs) > 0 {
		return nil, false
	}
	if body.Data == nil {
		return []catalogRow{}, true
	}
	return body.Data.Catalog, true
}

type sourceTable struct {
	Schema  string   `json:"schema"`
	Table   string   `json:"table"`
	Columns []string `json:"columns"`
}

func (a *API) adminDataAccessTables(w http.ResponseWriter, r *http.Request) {
	act, ok := a.accessAdmin(w, r)
	if !ok {
		return
	}
	source := r.URL.Query().Get("source")
	if source == "" {
		sendError(w, http.StatusBadRequest, "source is required")
		return
	}
	rows, err := a.graphjinCatalog(r.Context(), act.OrgID, "column", &source)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	tables := []*sourceTable{}
	byKey := map[string]*sourceTable{}
	for _, row := range rows {
		if row.TableName == nil || *row.TableName == "" || row.ColumnName == nil || *row.ColumnName == "" {
			continue
		}
		schema := ""
		if row.SchemaName != nil {
			schema = *row.SchemaName
		}
		key := schema + "." + *row.TableName
		t := byKey[key]
		if t == nil {
			t = &sourceTable{Schema: schema, Table: *row.TableName, Columns: []string{}}
			byKey[key] = t
			tables = append(tables, t)
		}
		if !slices.Contains(t.Columns, *row.ColumnName) {
			t.Columns = append(t.Columns, *row.ColumnName)
		}
	}
	slices.SortStableFunc(tables, func(x, y *sourceTable) int {
		return localeCompare(x.Schema+"."+x.Table, y.Schema+"."+y.Table)
	})
	sendJSON(w, http.StatusOK, map[string]any{"tables": tables})
}

var packItemKinds = map[string]struct{ kind, idExpr, join string }{
	"skill":       {"skill", "a.target_ref", ""},
	"saved_query": {"saved_query", "a.target_ref", ""},
	"action":      {"action", "a.target_ref", ""},
	"data_source": {"source", "a.target_ref", ""},
	"workflow":    {"workflow", "w.id::text", "join workflow_definition w on w.org_id = a.org_id and w.name = a.target_ref"},
	"watcher":     {"watcher", "w.id::text", "join watcher w on w.org_id = a.org_id and w.name = a.target_ref"},
	"metric":      {"metric", "m.id::text", "join metric m on m.org_id = a.org_id and m.slug = a.target_ref"},
}

func (a *API) packsContaining(ctx context.Context, orgID, typ, itemID string) ([]string, error) {
	spec, ok := packItemKinds[typ]
	if !ok {
		return []string{}, nil
	}
	return collectStrings(ctx, a, `
		select distinct i.pack_id
		from pack_artifact a
		join pack_install i on i.id = a.pack_install_id and i.status <> 'removed'
		`+spec.join+`
		where a.org_id = $1 and a.artifact_kind = $2 and a.ownership <> 'detached' and `+spec.idExpr+` = $3`,
		orgID, spec.kind, itemID)
}

type itemHolder struct {
	GroupID string `json:"groupId"`
	Name    string `json:"name"`
	Slug    string `json:"slug"`
	Via     string `json:"via"`
}

func (a *API) adminItemHolders(w http.ResponseWriter, r *http.Request) {
	act, ok := a.accessAdmin(w, r)
	if !ok {
		return
	}
	typ, itemID := r.URL.Query().Get("itemType"), r.URL.Query().Get("itemId")
	if !isItemType(typ) || itemID == "" {
		sendError(w, http.StatusBadRequest, "itemType and itemId are required")
		return
	}
	holders, err := a.whoHolds(r.Context(), act.OrgID, typ, itemID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	sendJSON(w, http.StatusOK, map[string]any{"holders": holders})
}

func (a *API) whoHolds(ctx context.Context, orgID, typ, itemID string) ([]itemHolder, error) {
	packs, err := a.packsContaining(ctx, orgID, typ, itemID)
	if err != nil {
		return nil, err
	}
	holders := []itemHolder{}
	var admins itemHolder
	err = a.DB.QueryRow(ctx, `select id::text, name from user_group where org_id = $1 and slug = 'administrators'`, orgID).
		Scan(&admins.GroupID, &admins.Name)
	if err == nil {
		admins.Slug, admins.Via = "administrators", "administrators"
		holders = append(holders, admins)
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	rows, err := a.DB.Query(ctx, `
		select g.id::text, g.name, g.slug, ig.item_type, ig.item_id
		from item_grant ig join user_group g on g.id = ig.group_id
		where ig.org_id = $1
		  and ((ig.item_type = $2 and ig.item_id in ($3, '*'))
		    or (ig.item_type = 'pack' and (ig.item_id = '*' and $4 or ig.item_id = any($5::text[]))))
		order by lower(g.name)`, orgID, typ, itemID, len(packs) > 0, packs)
	if err != nil {
		return nil, err
	}
	found, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (itemHolder, error) {
		var h itemHolder
		var grantType, grantID string
		err := row.Scan(&h.GroupID, &h.Name, &h.Slug, &grantType, &grantID)
		switch {
		case grantType == "pack":
			h.Via = "pack:" + grantID
		case grantID == "*":
			h.Via = "all"
		default:
			h.Via = "item"
		}
		return h, err
	})
	return append(holders, found...), err
}

func requireGrantableGroup(ctx context.Context, q querier, orgID, groupID string) (groupRef, error) {
	g, err := requireGroup(ctx, q, orgID, groupID)
	if err == nil && g.slug == "administrators" {
		err = groupErr("builtin", "Administrators hold every item; grants are not needed")
	}
	return g, err
}

func (a *API) adminItemGrant(w http.ResponseWriter, r *http.Request) {
	act, ok := a.accessAdmin(w, r)
	if !ok {
		return
	}
	grant := r.Method == http.MethodPost
	body := readObject(r)
	groupID, okGroup := body["groupId"].(string)
	itemID, okItem := body["itemId"].(string)
	itemIDs, okList := body["itemIds"].([]any)
	if body == nil || !okGroup || !isItemType(body["itemType"]) || !okItem && !(grant && okList) {
		sendError(w, http.StatusBadRequest, "groupId, itemType and itemId or itemIds are required")
		return
	}
	typ := body["itemType"].(string)
	var result any
	var err error
	switch {
	case grant && okList:
		result, err = a.grantItems(r.Context(), act, groupID, typ, itemIDs)
	case grant:
		result, err = a.grantItem(r.Context(), act, groupID, typ, itemID)
	default:
		result, err = a.revokeItem(r.Context(), act, groupID, typ, itemID)
	}
	if err != nil {
		a.groupFailure(w, r, err)
		return
	}
	if typ == "data_source" || typ == "api_operation" {
		a.scheduleGroupGrantsApply(r.Context())
	}
	sendJSON(w, http.StatusOK, result)
}

func validItemID(id string) bool { return id != "" && jsLength(id) <= 500 }

func (a *API) grantItem(ctx context.Context, act actor, groupID, typ, itemID string) (any, error) {
	itemID = jsTrim(itemID)
	if !validItemID(itemID) {
		return nil, groupErr("invalid", "item id must be 1 to 500 characters")
	}
	g, err := requireGrantableGroup(ctx, a.DB, act.OrgID, groupID)
	if err != nil {
		return nil, err
	}
	var created bool
	err = pgx.BeginFunc(ctx, a.DB, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `insert into item_grant (org_id, group_id, item_type, item_id, created_by_user_id)
			values ($1, $2, $3, $4, $5) on conflict do nothing`, act.OrgID, g.id, typ, itemID, act.UserID)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		created = true
		_, err = tx.Exec(ctx, `insert into item_grant_audit (org_id, actor_user_id, action, group_id, group_name, item_type, item_id)
			values ($1, $2, 'grant', $3, $4, $5, $6)`, act.OrgID, act.UserID, g.id, g.name, typ, itemID)
		return err
	})
	return map[string]bool{"created": created}, err
}

func (a *API) grantItems(ctx context.Context, act actor, groupID, typ string, raw []any) (any, error) {
	if len(raw) < 1 || len(raw) > 500 {
		return nil, groupErr("invalid", "select 1 to 500 items to grant")
	}
	ids := []string{}
	for _, v := range raw {
		s, _ := v.(string)
		if s = jsTrim(s); !slices.Contains(ids, s) {
			ids = append(ids, s)
		}
	}
	if slices.ContainsFunc(ids, func(s string) bool { return !validItemID(s) }) {
		return nil, groupErr("invalid", "item id must be 1 to 500 characters")
	}
	g, err := requireGrantableGroup(ctx, a.DB, act.OrgID, groupID)
	if err != nil {
		return nil, err
	}
	tag, err := a.DB.Exec(ctx, `
		with created as (
		  insert into item_grant (org_id, group_id, item_type, item_id, created_by_user_id)
		  select $1, $2, $3, item_id, $4 from unnest($5::text[]) as selected(item_id)
		  on conflict do nothing
		  returning item_id
		)
		insert into item_grant_audit (org_id, actor_user_id, action, group_id, group_name, item_type, item_id)
		select $1, $4, 'grant', $2, $6, $3, item_id from created`, act.OrgID, g.id, typ, act.UserID, ids, g.name)
	return map[string]int64{"created": tag.RowsAffected()}, err
}

func (a *API) revokeItem(ctx context.Context, act actor, groupID, typ, itemID string) (any, error) {
	g, err := requireGrantableGroup(ctx, a.DB, act.OrgID, groupID)
	if err != nil {
		return nil, err
	}
	var removed bool
	err = pgx.BeginFunc(ctx, a.DB, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `delete from item_grant where org_id = $1 and group_id = $2 and item_type = $3 and item_id = $4`,
			act.OrgID, g.id, typ, itemID)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		removed = true
		_, err = tx.Exec(ctx, `insert into item_grant_audit (org_id, actor_user_id, action, group_id, group_name, item_type, item_id)
			values ($1, $2, 'revoke', $3, $4, $5, $6)`, act.OrgID, act.UserID, g.id, g.name, typ, itemID)
		return err
	})
	return map[string]bool{"removed": removed}, err
}

func (a *API) groupGrantsEnabled(ctx context.Context, orgID string) (bool, error) {
	var enabled bool
	err := a.DB.QueryRow(ctx, `select group_grants_enabled from data_access_settings where org_id = $1`, orgID).Scan(&enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return enabled, err
}

func (a *API) adminDataAccess(w http.ResponseWriter, r *http.Request) {
	act, ok := a.accessAdmin(w, r)
	if !ok {
		return
	}
	var groupID *string
	if q := r.URL.Query(); q.Has("groupId") {
		v := q.Get("groupId")
		groupID = &v
	}
	rules, err := a.dataAccessRules(r.Context(), act.OrgID, groupID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	enabled, err := a.groupGrantsEnabled(r.Context(), act.OrgID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	sendJSON(w, http.StatusOK, struct {
		Rules   []dataAccessRule `json:"rules"`
		Enabled bool             `json:"enabled"`
	}{rules, enabled})
}

var sqlIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_$]*$`)

func (a *API) adminDataAccessCreate(w http.ResponseWriter, r *http.Request) {
	act, ok := a.accessAdmin(w, r)
	if !ok {
		return
	}
	body := readObject(r)
	groupID, ok1 := body["groupId"].(string)
	source, ok2 := body["source"].(string)
	tableName, ok3 := body["tableName"].(string)
	rawColumns, ok4 := body["columns"].([]any)
	columns := make([]string, 0, len(rawColumns))
	for _, c := range rawColumns {
		s, ok := c.(string)
		ok4 = ok4 && ok
		columns = append(columns, s)
	}
	if body == nil || !ok1 || !ok2 || !ok3 || !ok4 {
		sendError(w, http.StatusBadRequest, "groupId, source, tableName and columns are required")
		return
	}
	var rowFilter []byte
	if raw := body["rowFilter"]; raw != nil {
		parsed, err := parseRowFilter(raw)
		if err != nil {
			sendError(w, http.StatusBadRequest, err.Error())
			return
		}
		rowFilter, _ = json.Marshal(parsed)
	}
	tableSchema, _ := body["tableSchema"].(string)
	rule, err := a.upsertDataAccessRule(r.Context(), act, groupID, source, tableSchema, tableName, columns, rowFilter)
	if err != nil {
		a.groupFailure(w, r, err)
		return
	}
	a.scheduleGroupGrantsApply(r.Context())
	sendJSON(w, http.StatusCreated, map[string]any{"rule": rule})
}

func (a *API) upsertDataAccessRule(ctx context.Context, act actor, groupID, source, tableSchema, tableName string, raw []string, rowFilter []byte) (*dataAccessRule, error) {
	g, err := requireGroup(ctx, a.DB, act.OrgID, groupID)
	if err != nil {
		return nil, err
	}
	if g.slug == "administrators" {
		return nil, groupErr("builtin", "Administrators read every table; data access rules are not needed")
	}
	columns := []string{}
	for _, c := range raw {
		if c = jsTrim(c); c != "" && !slices.Contains(columns, c) {
			columns = append(columns, c)
		}
	}
	if len(columns) == 0 {
		return nil, groupErr("invalid", "choose at least one column")
	}
	if !sqlIdentifier.MatchString(tableName) {
		return nil, groupErr("invalid", "invalid table name")
	}
	var id string
	err = a.DB.QueryRow(ctx, `
		insert into data_access_rule (org_id, group_id, source, table_schema, table_name, columns, row_filter, created_by_user_id)
		values ($1, $2, $3, $4, $5, $6, $7::jsonb, $8)
		on conflict (org_id, group_id, source, table_schema, table_name)
		do update set columns = excluded.columns, row_filter = excluded.row_filter, updated_at = now()
		returning id::text`, act.OrgID, g.id, source, tableSchema, tableName, columns, rowFilter, act.UserID).Scan(&id)
	if err != nil {
		return nil, err
	}
	rules, err := a.dataAccessRules(ctx, act.OrgID, &g.id)
	if err != nil {
		return nil, err
	}
	for i := range rules {
		if rules[i].ID == id {
			return &rules[i], nil
		}
	}
	return nil, errors.New("saved rule not found")
}

func (a *API) adminDataAccessDelete(w http.ResponseWriter, r *http.Request) {
	act, ok := a.accessAdmin(w, r)
	if !ok {
		return
	}
	tag, err := a.DB.Exec(r.Context(), `delete from data_access_rule where org_id = $1 and id = $2`, act.OrgID, r.PathValue("ruleId"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if tag.RowsAffected() == 0 {
		sendError(w, http.StatusNotFound, "rule not found")
		return
	}
	a.scheduleGroupGrantsApply(r.Context())
	sendJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *API) adminDataAccessSettings(w http.ResponseWriter, r *http.Request) {
	act, ok := a.accessAdmin(w, r)
	if !ok {
		return
	}
	enabled, err := a.groupGrantsEnabled(r.Context(), act.OrgID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	sendJSON(w, http.StatusOK, map[string]bool{"enabled": enabled})
}

func (a *API) adminDataAccessSettingsPatch(w http.ResponseWriter, r *http.Request) {
	act, ok := a.accessAdmin(w, r)
	if !ok {
		return
	}
	enabled, ok := readObject(r)["enabled"].(bool)
	if !ok {
		sendError(w, http.StatusBadRequest, "enabled must be true or false")
		return
	}
	path := "/admin/graphjin/group-grants/disable"
	if enabled {
		path = "/admin/graphjin/group-grants/enable"
	}
	payload, _ := json.Marshal(map[string]string{"actorUserId": act.UserID})
	a.relayWorker(w, r, path, payload)
}

const (
	rowFilterMaxDepth = 6
	rowFilterMaxNodes = 100
)

var (
	rowFilterOps       = []string{"eq", "neq", "gt", "gte", "lt", "lte", "in", "nin", "is_null"}
	rowFilterVariables = []string{"user_id", "user_groups", "account_id"}
)

type rowFilterError string

func (e rowFilterError) Error() string { return string(e) }

func isRowFilterLiteral(v any) bool {
	switch v.(type) {
	case string, bool, float64:
		return true
	}
	return false
}

// parseRowFilter is parseRowFilter() in packages/llm/src/graphjin/row-filter.ts
// without the column list.
func parseRowFilter(input any) (any, error) {
	nodes := 0
	var visit func(value any, depth int, path string) (any, error)
	visit = func(value any, depth int, path string) (any, error) {
		if nodes++; nodes > rowFilterMaxNodes {
			return nil, rowFilterError("row filter has too many conditions")
		}
		if depth > rowFilterMaxDepth {
			return nil, rowFilterError("row filter is nested too deeply")
		}
		obj, ok := value.(map[string]any)
		if !ok {
			return nil, rowFilterError(path + " must be an object")
		}
		for _, group := range []string{"and", "or"} {
			children, has := obj[group]
			if !has {
				continue
			}
			if len(obj) != 1 {
				return nil, rowFilterError(fmt.Sprintf("%s.%s must be the only key", path, group))
			}
			list, ok := children.([]any)
			if !ok || len(list) == 0 {
				return nil, rowFilterError(fmt.Sprintf("%s.%s needs at least one condition", path, group))
			}
			parsed := make([]any, len(list))
			for i, child := range list {
				p, err := visit(child, depth+1, fmt.Sprintf("%s.%s[%d]", path, group, i))
				if err != nil {
					return nil, err
				}
				parsed[i] = p
			}
			return map[string]any{group: parsed}, nil
		}
		column, ok := obj["column"].(string)
		if !ok || !sqlIdentifier.MatchString(column) {
			return nil, rowFilterError(path + ".column is not a valid column name")
		}
		op, ok := obj["op"].(string)
		if !ok || !slices.Contains(rowFilterOps, op) {
			return nil, rowFilterError(fmt.Sprintf("%s.op must be one of %s", path, strings.Join(rowFilterOps, ", ")))
		}
		operand := obj["value"]
		leaf := func(v any) (any, error) { return map[string]any{"column": column, "op": op, "value": v}, nil }
		if op == "is_null" {
			if _, ok := operand.(bool); !ok {
				return nil, rowFilterError(path + ".value must be true or false for is_null")
			}
			return leaf(operand)
		}
		listOp := op == "in" || op == "nin"
		if v, ok := operand.(map[string]any); ok {
			variable, ok := v["var"].(string)
			if len(v) != 1 || !ok || !slices.Contains(rowFilterVariables, variable) {
				return nil, rowFilterError(fmt.Sprintf("%s.value.var must be one of %s", path, strings.Join(rowFilterVariables, ", ")))
			}
			if listVariable := variable == "user_groups"; listVariable != listOp {
				need := "a single-value operator"
				if listVariable {
					need = "in or nin"
				}
				return nil, rowFilterError(fmt.Sprintf("%s: $%s needs %s", path, variable, need))
			}
			return leaf(map[string]any{"var": variable})
		}
		if listOp {
			list, ok := operand.([]any)
			if !ok || len(list) == 0 || slices.ContainsFunc(list, func(x any) bool { return !isRowFilterLiteral(x) }) {
				return nil, rowFilterError(fmt.Sprintf("%s.value must be a non-empty list of values for %s", path, op))
			}
			return leaf(list)
		}
		if !isRowFilterLiteral(operand) {
			return nil, rowFilterError(path + ".value must be a string, number or boolean")
		}
		return leaf(operand)
	}
	return visit(input, 0, "filter")
}
