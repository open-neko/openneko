package webfront

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/text/unicode/norm"
)

const (
	workerAdminTimeout   = 120 * time.Second
	directorySyncTimeout = 15 * time.Minute
)

type groupError struct{ code, message string }

func (e *groupError) Error() string { return e.message }

func groupErr(code, message string) error { return &groupError{code, message} }

// groupFailure is groupErrorResponse() in apps/web/src/lib/groups-admin.ts.
func (a *API) groupFailure(w http.ResponseWriter, r *http.Request, err error) {
	var ge *groupError
	if errors.As(err, &ge) {
		status := http.StatusBadRequest
		switch ge.code {
		case "not_found":
			status = http.StatusNotFound
		case "conflict", "lockout":
			status = http.StatusConflict
		}
		sendJSON(w, status, struct {
			Error string `json:"error"`
			Code  string `json:"code"`
		}{ge.message, ge.code})
		return
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		log.Printf("%s %s: %v", r.Method, r.URL.Path, err)
		sendError(w, http.StatusInternalServerError, pgErr.Message)
		return
	}
	a.fail(w, r, err)
}

// readObject is readJsonBody(): nil unless the body is a JSON object.
func readObject(r *http.Request) map[string]any {
	body, _ := readBody(r)
	m, _ := body.(map[string]any)
	return m
}

// scheduleGroupGrantsApply asks the worker to apply GraphJin roles and
// ignores the answer, as Node does.
func (a *API) scheduleGroupGrantsApply(ctx context.Context) {
	_, _, _ = a.workerDo(context.WithoutCancel(ctx), http.MethodPost, "/admin/graphjin/group-grants", workerAdminTimeout, []byte("{}"))
}

// relayWorker is requestWorker() with the route's catch: any failure gives
// 503 "worker is unavailable".
func (a *API) relayWorker(w http.ResponseWriter, r *http.Request, path string, body []byte) {
	method := http.MethodGet
	if body != nil {
		method = http.MethodPost
	}
	status, reply, err := a.workerDo(r.Context(), method, path, workerAdminTimeout, body)
	if err != nil {
		sendError(w, http.StatusServiceUnavailable, "worker is unavailable")
		return
	}
	parsed, err := jsParse(reply)
	if err != nil {
		sendError(w, status, fmt.Sprintf("Worker returned HTTP %d", status))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(jsStringify(parsed, false)))
}

func (a *API) directoryStatus(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.accessAdmin(w, r); ok {
		a.relayWorker(w, r, "/admin/directory/status", nil)
	}
}

func (a *API) directorySync(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.accessAdmin(w, r); ok {
		a.relayWorker(w, r, "/admin/directory/sync", []byte("{}"))
	}
}

// DirectorySync is the six-hourly directory_sync job. With no directory
// provider it does nothing.
func (a *API) DirectorySync(ctx context.Context) error {
	status, reply, err := a.workerDo(ctx, http.MethodPost, "/admin/directory/sync", directorySyncTimeout, []byte("{}"))
	if err != nil {
		return fmt.Errorf("directory sync: the worker did not answer: %w", err)
	}
	if status == http.StatusNotFound {
		return nil
	}
	var body struct {
		Stats json.RawMessage `json:"stats"`
		Error string          `json:"error"`
	}
	_ = json.Unmarshal(reply, &body)
	if status != http.StatusOK {
		if body.Error == "" {
			body.Error = http.StatusText(status)
		}
		return fmt.Errorf("directory sync failed (%d): %s", status, body.Error)
	}
	log.Printf("[directory-sync] %s", body.Stats)
	return nil
}

type userGroup struct {
	ID          string  `json:"id"`
	Slug        string  `json:"slug"`
	Name        string  `json:"name"`
	Description *string `json:"description"`
	Kind        string  `json:"kind"`
	MemberCount int     `json:"memberCount"`
	RuleCount   int     `json:"ruleCount"`
}

func listUserGroups(ctx context.Context, q querier, orgID string) ([]userGroup, error) {
	rows, err := q.Query(ctx, `
		select g.id::text, g.slug, g.name, g.description, g.kind,
		  (select count(distinct m.user_id)::int from user_group_membership m where m.group_id = g.id),
		  (select count(*)::int from idp_group_rule r where r.user_group_id = g.id)
		from user_group g
		where g.org_id = $1
		order by case g.slug when 'administrators' then 0 when 'everyone' then 1 else 2 end, lower(g.name)`, orgID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (userGroup, error) {
		var g userGroup
		err := row.Scan(&g.ID, &g.Slug, &g.Name, &g.Description, &g.Kind, &g.MemberCount, &g.RuleCount)
		return g, err
	})
}

func findUserGroup(ctx context.Context, q querier, orgID, groupID string) (*userGroup, error) {
	groups, err := listUserGroups(ctx, q, orgID)
	if err != nil {
		return nil, err
	}
	for i := range groups {
		if groups[i].ID == groupID {
			return &groups[i], nil
		}
	}
	return nil, nil
}

type groupRef struct{ id, slug, kind, name string }

func requireGroup(ctx context.Context, q querier, orgID, groupID string) (groupRef, error) {
	var g groupRef
	err := q.QueryRow(ctx, `select id::text, slug, kind, name from user_group where org_id = $1 and id = $2`, orgID, groupID).
		Scan(&g.id, &g.slug, &g.kind, &g.name)
	if errors.Is(err, pgx.ErrNoRows) {
		return g, groupErr("not_found", "group not found")
	}
	return g, err
}

// withAdministratorGuard rolls back a change that leaves an organization
// that had an active administrator without one.
func (a *API) withAdministratorGuard(ctx context.Context, orgID string, fn func(pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, a.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtext($1))`, "openneko.app_user:"+orgID); err != nil {
			return err
		}
		before, err := activeAdministrators(ctx, tx, orgID)
		if err != nil {
			return err
		}
		if err := fn(tx); err != nil {
			return err
		}
		after, err := activeAdministrators(ctx, tx, orgID)
		if err != nil {
			return err
		}
		if before > 0 && after == 0 {
			return groupErr("lockout", "refusing to remove the last active administrator")
		}
		return nil
	})
}

func slugifyGroupName(name string) string {
	base := slugRun.ReplaceAllString(norm.NFKD.String(strings.ToLower(name)), "-")
	base = strings.Trim(base, "-")
	if len(base) > 48 {
		base = base[:48]
	}
	if base == "" {
		return "group"
	}
	return base
}

func reservedGroupSlug(slug string) bool { return slug == "administrators" || slug == "everyone" }

func (a *API) adminGroups(w http.ResponseWriter, r *http.Request) {
	act, ok := a.accessAdmin(w, r)
	if !ok {
		return
	}
	groups, err := listUserGroups(r.Context(), a.DB, act.OrgID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	sendJSON(w, http.StatusOK, map[string]any{"groups": groups})
}

func (a *API) adminGroupCreate(w http.ResponseWriter, r *http.Request) {
	act, ok := a.accessAdmin(w, r)
	if !ok {
		return
	}
	body := readObject(r)
	rawName, ok := body["name"].(string)
	if body == nil || !ok {
		sendError(w, http.StatusBadRequest, "name is required")
		return
	}
	var description *string
	if d, ok := body["description"].(string); ok {
		if d = jsTrim(d); d != "" {
			description = &d
		}
	}
	name := jsTrim(rawName)
	if name == "" || jsLength(name) > 120 {
		a.groupFailure(w, r, groupErr("invalid", "group name must be 1 to 120 characters"))
		return
	}
	ctx := r.Context()
	var created *userGroup
	err := pgx.BeginFunc(ctx, a.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtext($1))`, "openneko.app_user:"+act.OrgID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select slug, name from user_group where org_id = $1`, act.OrgID)
		if err != nil {
			return err
		}
		taken := map[string]bool{}
		var slug, existing string
		_, err = pgx.ForEachRow(rows, []any{&slug, &existing}, func() error {
			taken[slug] = true
			taken["name:"+strings.ToLower(existing)] = true
			return nil
		})
		if err != nil {
			return err
		}
		if taken["name:"+strings.ToLower(name)] {
			return groupErr("conflict", `a group named "`+name+`" already exists`)
		}
		base := slugifyGroupName(name)
		if reservedGroupSlug(base) {
			return groupErr("conflict", `"`+name+`" is reserved for a built-in group`)
		}
		slug = base
		for i := 2; taken[slug] || reservedGroupSlug(slug); i++ {
			slug = fmt.Sprintf("%s-%d", base, i)
		}
		var id string
		if err := tx.QueryRow(ctx, `insert into user_group (org_id, slug, name, description, kind)
			values ($1, $2, $3, $4, 'custom') returning id::text`, act.OrgID, slug, name, description).Scan(&id); err != nil {
			return err
		}
		created, err = findUserGroup(ctx, tx, act.OrgID, id)
		return err
	})
	if err != nil {
		a.groupFailure(w, r, err)
		return
	}
	sendJSON(w, http.StatusCreated, map[string]any{"group": created})
}

type groupMember struct {
	UserID   string   `json:"userId"`
	Email    string   `json:"email"`
	Name     *string  `json:"name"`
	Disabled bool     `json:"disabled"`
	Sources  []string `json:"sources"`
}

type groupItemGrant struct {
	ItemType        string  `json:"itemType"`
	ItemID          string  `json:"itemId"`
	CreatedAt       string  `json:"createdAt"`
	CreatedByUserID *string `json:"createdByUserId"`
}

type dataAccessRule struct {
	ID          string     `json:"id"`
	GroupID     string     `json:"groupId"`
	GroupSlug   string     `json:"groupSlug"`
	GroupName   string     `json:"groupName"`
	Source      string     `json:"source"`
	TableSchema string     `json:"tableSchema"`
	TableName   string     `json:"tableName"`
	Columns     []string   `json:"columns"`
	RowFilter   storedJSON `json:"rowFilter"`
	UpdatedAt   string     `json:"updatedAt"`
}

type idpGroupRule struct {
	ID            string `json:"id"`
	SSOGroupID    string `json:"ssoGroupId"`
	IdpGroupName  string `json:"idpGroupName"`
	Provider      string `json:"provider"`
	UserGroupID   string `json:"userGroupId"`
	UserGroupName string `json:"userGroupName"`
	CreatedAt     string `json:"createdAt"`
}

type directoryGroup struct {
	ID          string  `json:"id"`
	Provider    string  `json:"provider"`
	TenantID    string  `json:"tenantId"`
	ExternalID  string  `json:"externalId"`
	DisplayName *string `json:"displayName"`
	Active      bool    `json:"active"`
	MemberCount int     `json:"memberCount"`
}

func (a *API) groupMembers(ctx context.Context, orgID string, g groupRef) ([]groupMember, error) {
	query := `
		select u.id, u.email, u.name, (u.disabled_at is not null), array_agg(m.source order by m.source)
		from user_group_membership m
		join app_user u on u.id = m.user_id
		where m.org_id = $1 and m.group_id = $2
		group by u.id, u.email, u.name, u.disabled_at
		order by lower(u.email)`
	args := []any{orgID, g.id}
	if g.slug == "everyone" {
		query = `select id, email, name, false, array['implicit']::text[]
			from app_user where org_id = $1 and disabled_at is null order by lower(email)`
		args = args[:1]
	}
	rows, err := a.DB.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (groupMember, error) {
		var m groupMember
		err := row.Scan(&m.UserID, &m.Email, &m.Name, &m.Disabled, &m.Sources)
		return m, err
	})
}

func (a *API) groupItemGrants(ctx context.Context, orgID, groupID string) ([]groupItemGrant, error) {
	rows, err := a.DB.Query(ctx, `
		select item_type, item_id, created_at, created_by_user_id
		from item_grant where org_id = $1 and group_id = $2
		order by item_type, item_id = '*' desc, item_id`, orgID, groupID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (groupItemGrant, error) {
		var g groupItemGrant
		var created time.Time
		err := row.Scan(&g.ItemType, &g.ItemID, &created, &g.CreatedByUserID)
		g.CreatedAt = jsTime(created)
		return g, err
	})
}

func (a *API) dataAccessRules(ctx context.Context, orgID string, groupID *string) ([]dataAccessRule, error) {
	rows, err := a.DB.Query(ctx, `
		select r.id::text, r.group_id::text, g.slug, g.name, r.source, r.table_schema, r.table_name, r.columns,
		  r.row_filter, r.updated_at
		from data_access_rule r join user_group g on g.id = r.group_id
		where r.org_id = $1 and ($2::uuid is null or r.group_id = $2::uuid)
		order by lower(g.name), r.source, r.table_schema, r.table_name`, orgID, groupID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (dataAccessRule, error) {
		var d dataAccessRule
		var filter []byte
		var updated time.Time
		err := row.Scan(&d.ID, &d.GroupID, &d.GroupSlug, &d.GroupName, &d.Source, &d.TableSchema, &d.TableName,
			&d.Columns, &filter, &updated)
		if d.Columns == nil {
			d.Columns = []string{}
		}
		d.RowFilter, d.UpdatedAt = storedJSON(filter), jsTime(updated)
		return d, err
	})
}

func (a *API) idpGroupRules(ctx context.Context, orgID string) ([]idpGroupRule, error) {
	rows, err := a.DB.Query(ctx, `
		select r.id::text, r.sso_group_id::text, coalesce(s.display_name, s.external_id), s.provider,
		  r.user_group_id::text, g.name, r.created_at
		from idp_group_rule r
		join sso_group s on s.id = r.sso_group_id
		join user_group g on g.id = r.user_group_id
		where r.org_id = $1
		order by lower(g.name), lower(coalesce(s.display_name, s.external_id))`, orgID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (idpGroupRule, error) {
		var i idpGroupRule
		var created time.Time
		err := row.Scan(&i.ID, &i.SSOGroupID, &i.IdpGroupName, &i.Provider, &i.UserGroupID, &i.UserGroupName, &created)
		i.CreatedAt = jsTime(created)
		return i, err
	})
}

func (a *API) directoryGroups(ctx context.Context, orgID string) ([]directoryGroup, error) {
	rows, err := a.DB.Query(ctx, `
		select g.id::text, g.provider, g.tenant_id, g.external_id, g.display_name, g.active,
		  (select count(*)::int from sso_group_membership m where m.group_id = g.id)
		from sso_group g where g.org_id = $1
		order by g.active desc, lower(coalesce(g.display_name, g.external_id))`, orgID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (directoryGroup, error) {
		var g directoryGroup
		err := row.Scan(&g.ID, &g.Provider, &g.TenantID, &g.ExternalID, &g.DisplayName, &g.Active, &g.MemberCount)
		return g, err
	})
}

func (a *API) adminGroup(w http.ResponseWriter, r *http.Request) {
	act, ok := a.accessAdmin(w, r)
	if !ok {
		return
	}
	ctx, groupID := r.Context(), r.PathValue("groupId")
	group, err := findUserGroup(ctx, a.DB, act.OrgID, groupID)
	if err != nil || group == nil {
		if err != nil {
			log.Printf("%s %s: %v", r.Method, r.URL.Path, err)
		}
		sendError(w, http.StatusNotFound, "group not found")
		return
	}
	ref, err := requireGroup(ctx, a.DB, act.OrgID, groupID)
	if err != nil {
		a.groupFailure(w, r, err)
		return
	}
	members, err := a.groupMembers(ctx, act.OrgID, ref)
	if err != nil {
		a.groupFailure(w, r, err)
		return
	}
	grants, err := a.groupItemGrants(ctx, act.OrgID, groupID)
	if err != nil {
		a.groupFailure(w, r, err)
		return
	}
	rules, err := a.dataAccessRules(ctx, act.OrgID, &groupID)
	if err != nil {
		a.groupFailure(w, r, err)
		return
	}
	all, err := a.idpGroupRules(ctx, act.OrgID)
	if err != nil {
		a.groupFailure(w, r, err)
		return
	}
	idpRules := []idpGroupRule{}
	for _, rule := range all {
		if rule.UserGroupID == groupID {
			idpRules = append(idpRules, rule)
		}
	}
	sendJSON(w, http.StatusOK, struct {
		Group    *userGroup       `json:"group"`
		Members  []groupMember    `json:"members"`
		Grants   []groupItemGrant `json:"grants"`
		Rules    []dataAccessRule `json:"rules"`
		IdpRules []idpGroupRule   `json:"idpRules"`
	}{group, members, grants, rules, idpRules})
}

func (a *API) adminGroupPatch(w http.ResponseWriter, r *http.Request) {
	act, ok := a.accessAdmin(w, r)
	if !ok {
		return
	}
	body := readObject(r)
	if body == nil {
		sendError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	ctx, groupID := r.Context(), r.PathValue("groupId")
	group, err := requireGroup(ctx, a.DB, act.OrgID, groupID)
	if err != nil {
		a.groupFailure(w, r, err)
		return
	}
	var name *string
	if n, ok := body["name"].(string); ok {
		n = jsTrim(n)
		if group.kind == "builtin" && n != group.name {
			a.groupFailure(w, r, groupErr("builtin", "built-in groups cannot be renamed"))
			return
		}
		if n == "" || jsLength(n) > 120 {
			a.groupFailure(w, r, groupErr("invalid", "group name must be 1 to 120 characters"))
			return
		}
		name = &n
	}
	setDescription := false
	var description *string
	if raw, present := body["description"]; present {
		if d, ok := raw.(string); ok {
			setDescription = true
			if d = jsTrim(d); d != "" {
				description = &d
			}
		} else if raw == nil {
			setDescription = true
		}
	}
	if _, err := a.DB.Exec(ctx, `
		update user_group set
		  name = coalesce($3, name),
		  description = case when $4 then $5 else description end,
		  updated_at = now()
		where org_id = $1 and id = $2`, act.OrgID, groupID, name, setDescription, description); err != nil {
		msg := err.Error()
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			msg = pgErr.Message
		}
		a.groupFailure(w, r, groupErr("conflict", msg))
		return
	}
	updated, err := findUserGroup(ctx, a.DB, act.OrgID, groupID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	sendJSON(w, http.StatusOK, map[string]any{"group": updated})
}

func (a *API) adminGroupDelete(w http.ResponseWriter, r *http.Request) {
	act, ok := a.accessAdmin(w, r)
	if !ok {
		return
	}
	ctx, groupID := r.Context(), r.PathValue("groupId")
	err := a.withAdministratorGuard(ctx, act.OrgID, func(tx pgx.Tx) error {
		group, err := requireGroup(ctx, tx, act.OrgID, groupID)
		if err != nil {
			return err
		}
		if group.kind == "builtin" {
			return groupErr("builtin", "built-in groups cannot be deleted")
		}
		_, err = tx.Exec(ctx, `delete from user_group where org_id = $1 and id = $2`, act.OrgID, groupID)
		return err
	})
	if err != nil {
		a.groupFailure(w, r, err)
		return
	}
	a.scheduleGroupGrantsApply(ctx)
	sendJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *API) adminGroupMember(w http.ResponseWriter, r *http.Request) {
	act, ok := a.accessAdmin(w, r)
	if !ok {
		return
	}
	body := readObject(r)
	userID, ok := body["userId"].(string)
	if body == nil || !ok {
		sendError(w, http.StatusBadRequest, "userId is required")
		return
	}
	ctx, groupID := r.Context(), r.PathValue("groupId")
	if r.Method == http.MethodPost {
		err := a.withAdministratorGuard(ctx, act.OrgID, func(tx pgx.Tx) error {
			group, err := requireGroup(ctx, tx, act.OrgID, groupID)
			if err != nil {
				return err
			}
			if group.slug == "everyone" {
				return groupErr("implicit", "every active user is already in Everyone")
			}
			var found string
			err = tx.QueryRow(ctx, `select id from app_user where org_id = $1 and id = $2`, act.OrgID, userID).Scan(&found)
			if errors.Is(err, pgx.ErrNoRows) {
				return groupErr("not_found", "user not found")
			}
			if err != nil {
				return err
			}
			_, err = tx.Exec(ctx, `insert into user_group_membership (org_id, group_id, user_id, source)
				values ($1, $2, $3, 'local') on conflict do nothing`, act.OrgID, groupID, userID)
			return err
		})
		if err != nil {
			a.groupFailure(w, r, err)
			return
		}
		sendJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	var left int
	err := a.withAdministratorGuard(ctx, act.OrgID, func(tx pgx.Tx) error {
		group, err := requireGroup(ctx, tx, act.OrgID, groupID)
		if err != nil {
			return err
		}
		if group.slug == "everyone" {
			return groupErr("implicit", "users cannot leave Everyone")
		}
		if _, err := tx.Exec(ctx, `delete from user_group_membership
			where org_id = $1 and group_id = $2 and user_id = $3 and source = 'local'`, act.OrgID, groupID, userID); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `select count(*)::int from user_group_membership
			where org_id = $1 and group_id = $2 and user_id = $3`, act.OrgID, groupID, userID).Scan(&left)
	})
	if err != nil {
		a.groupFailure(w, r, err)
		return
	}
	sendJSON(w, http.StatusOK, map[string]bool{"stillMember": left > 0})
}

func (a *API) adminIdpRules(w http.ResponseWriter, r *http.Request) {
	act, ok := a.accessAdmin(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	rules, err := a.idpGroupRules(ctx, act.OrgID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	idp, err := a.directoryGroups(ctx, act.OrgID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	groups, err := listUserGroups(ctx, a.DB, act.OrgID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	sendJSON(w, http.StatusOK, struct {
		Rules     []idpGroupRule   `json:"rules"`
		IdpGroups []directoryGroup `json:"idpGroups"`
		Groups    []userGroup      `json:"groups"`
	}{rules, idp, groups})
}

func (a *API) adminIdpRuleCreate(w http.ResponseWriter, r *http.Request) {
	act, ok := a.accessAdmin(w, r)
	if !ok {
		return
	}
	body := readObject(r)
	ssoGroupID, ok1 := body["ssoGroupId"].(string)
	userGroupID, ok2 := body["userGroupId"].(string)
	if body == nil || !ok1 || !ok2 {
		sendError(w, http.StatusBadRequest, "ssoGroupId and userGroupId are required")
		return
	}
	ctx := r.Context()
	var createdBy *string
	if act.UserID != "" {
		createdBy = &act.UserID
	}
	var id string
	err := a.withAdministratorGuard(ctx, act.OrgID, func(tx pgx.Tx) error {
		group, err := requireGroup(ctx, tx, act.OrgID, userGroupID)
		if err != nil {
			return err
		}
		if group.slug == "everyone" {
			return groupErr("implicit", "every active user is already in Everyone")
		}
		var found string
		err = tx.QueryRow(ctx, `select id::text from sso_group where org_id = $1 and id = $2`, act.OrgID, ssoGroupID).Scan(&found)
		if errors.Is(err, pgx.ErrNoRows) {
			return groupErr("not_found", "IdP group not found")
		}
		if err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `
			insert into idp_group_rule (org_id, sso_group_id, user_group_id, created_by_user_id)
			values ($1, $2, $3, $4)
			on conflict (org_id, sso_group_id, user_group_id) do update set created_at = idp_group_rule.created_at
			returning id::text`, act.OrgID, ssoGroupID, userGroupID, createdBy).Scan(&id); err != nil {
			return err
		}
		return recomputeRuleMemberships(ctx, tx, act.OrgID, nil)
	})
	if err != nil {
		a.groupFailure(w, r, err)
		return
	}
	sendJSON(w, http.StatusCreated, map[string]any{"rule": map[string]string{"id": id}})
}

func (a *API) adminIdpRuleDelete(w http.ResponseWriter, r *http.Request) {
	act, ok := a.accessAdmin(w, r)
	if !ok {
		return
	}
	ctx, ruleID := r.Context(), r.PathValue("ruleId")
	err := a.withAdministratorGuard(ctx, act.OrgID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `delete from idp_group_rule where org_id = $1 and id = $2`, act.OrgID, ruleID); err != nil {
			return err
		}
		return recomputeRuleMemberships(ctx, tx, act.OrgID, nil)
	})
	if err != nil {
		a.groupFailure(w, r, err)
		return
	}
	sendJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
