package webfront

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// listUsers is InProcessControlPlane.listUsers().
func (r *runTools) listUsers(ctx context.Context, in orgIn) (any, error) {
	db := r.api.DB
	admins := map[string]bool{}
	rows, err := db.Query(ctx, `select m.user_id from user_group_membership m
		join user_group g on g.id = m.group_id
		where m.org_id = $1 and g.slug = 'administrators'`, in.OrgID)
	if err != nil {
		return nil, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		admins[id] = true
	}
	type membership struct{ user, group string }
	rows, err = db.Query(ctx, `select user_id, group_id::text from sso_group_membership where org_id = $1`, in.OrgID)
	if err != nil {
		return nil, err
	}
	memberships, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (membership, error) {
		var m membership
		return m, row.Scan(&m.user, &m.group)
	})
	if err != nil {
		return nil, err
	}
	type user struct {
		ID          string   `json:"id"`
		Email       string   `json:"email"`
		Name        *string  `json:"name"`
		DisabledAt  any      `json:"disabledAt"`
		LastLoginAt any      `json:"lastLoginAt"`
		Role        string   `json:"role"`
		GroupIDs    []string `json:"groupIds"`
	}
	rows, err = db.Query(ctx, `select id, email, name, disabled_at, last_login_at from app_user where org_id = $1`, in.OrgID)
	if err != nil {
		return nil, err
	}
	users, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (user, error) {
		var u user
		var disabled, login *time.Time
		if err := row.Scan(&u.ID, &u.Email, &u.Name, &disabled, &login); err != nil {
			return u, err
		}
		u.DisabledAt, u.LastLoginAt = jsTimeOrNil(disabled), jsTimeOrNil(login)
		u.Role = "member"
		if admins[u.ID] {
			u.Role = "admin"
		}
		u.GroupIDs = []string{}
		for _, m := range memberships {
			if m.user == u.ID {
				u.GroupIDs = append(u.GroupIDs, m.group)
			}
		}
		return u, nil
	})
	if err != nil {
		return nil, err
	}
	type ssoGroup struct {
		ID          string  `json:"id"`
		Provider    string  `json:"provider"`
		TenantID    string  `json:"tenantId"`
		ExternalID  string  `json:"externalId"`
		DisplayName *string `json:"displayName"`
		Active      bool    `json:"active"`
	}
	rows, err = db.Query(ctx, `select id::text, provider, tenant_id, external_id, display_name, active
		from sso_group where org_id = $1`, in.OrgID)
	if err != nil {
		return nil, err
	}
	groups, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (ssoGroup, error) {
		var g ssoGroup
		return g, row.Scan(&g.ID, &g.Provider, &g.TenantID, &g.ExternalID, &g.DisplayName, &g.Active)
	})
	if err != nil {
		return nil, err
	}
	if users == nil {
		users = []user{}
	}
	if groups == nil {
		groups = []ssoGroup{}
	}
	return jsFromGo(struct {
		Users  []user     `json:"users"`
		Groups []ssoGroup `json:"groups"`
	}{users, groups}), nil
}

// listGroups is InProcessControlPlane.listGroups().
func (r *runTools) listGroups(ctx context.Context, in orgIn) (any, error) {
	a := r.api
	groups, err := listUserGroups(ctx, a.DB, in.OrgID)
	if err != nil {
		return nil, err
	}
	rules, err := a.dataAccessRules(ctx, in.OrgID, nil)
	if err != nil {
		return nil, err
	}
	idpRules, err := a.idpGroupRules(ctx, in.OrgID)
	if err != nil {
		return nil, err
	}
	enabled, err := a.groupGrantsEnabled(ctx, in.OrgID)
	if err != nil {
		return nil, err
	}
	type member struct {
		UserID  string   `json:"userId"`
		Email   string   `json:"email"`
		Sources []string `json:"sources"`
	}
	type grant struct {
		ItemType string `json:"itemType"`
		ItemID   string `json:"itemId"`
	}
	type access struct {
		ID        string     `json:"id"`
		Source    string     `json:"source"`
		Table     string     `json:"table"`
		Columns   []string   `json:"columns"`
		RowFilter storedJSON `json:"rowFilter"`
	}
	type group struct {
		ID          string   `json:"id"`
		Slug        string   `json:"slug"`
		Name        string   `json:"name"`
		Kind        string   `json:"kind"`
		MemberCount int      `json:"memberCount"`
		Members     []member `json:"members"`
		Grants      []grant  `json:"grants"`
		DataAccess  []access `json:"dataAccess"`
	}
	out := make([]group, len(groups))
	for i, g := range groups {
		row := group{ID: g.ID, Slug: g.Slug, Name: g.Name, Kind: g.Kind, MemberCount: g.MemberCount,
			Members: []member{}, Grants: []grant{}, DataAccess: []access{}}
		if g.Slug != "everyone" {
			members, err := a.groupMembers(ctx, in.OrgID, groupRef{id: g.ID, slug: g.Slug, kind: g.Kind, name: g.Name})
			if err != nil {
				return nil, err
			}
			for _, m := range members {
				row.Members = append(row.Members, member{UserID: m.UserID, Email: m.Email, Sources: m.Sources})
			}
		}
		grants, err := a.groupItemGrants(ctx, in.OrgID, g.ID)
		if err != nil {
			return nil, err
		}
		for _, gr := range grants {
			row.Grants = append(row.Grants, grant{ItemType: gr.ItemType, ItemID: gr.ItemID})
		}
		for _, rule := range rules {
			if rule.GroupID != g.ID {
				continue
			}
			table := rule.TableName
			if rule.TableSchema != "" {
				table = rule.TableSchema + "." + rule.TableName
			}
			row.DataAccess = append(row.DataAccess, access{ID: rule.ID, Source: rule.Source, Table: table,
				Columns: rule.Columns, RowFilter: rule.RowFilter})
		}
		out[i] = row
	}
	type idpRule struct {
		ID            string `json:"id"`
		IdpGroupName  string `json:"idpGroupName"`
		SSOGroupID    string `json:"ssoGroupId"`
		UserGroupID   string `json:"userGroupId"`
		UserGroupName string `json:"userGroupName"`
	}
	idp := make([]idpRule, len(idpRules))
	for i, rule := range idpRules {
		idp[i] = idpRule{ID: rule.ID, IdpGroupName: rule.IdpGroupName, SSOGroupID: rule.SSOGroupID,
			UserGroupID: rule.UserGroupID, UserGroupName: rule.UserGroupName}
	}
	return jsFromGo(struct {
		Groups                 []group   `json:"groups"`
		IdpRules               []idpRule `json:"idpRules"`
		GroupDataAccessEnabled bool      `json:"groupDataAccessEnabled"`
	}{out, idp, enabled}), nil
}

// listChannels is InProcessControlPlane.listChannels().
func (r *runTools) listChannels(ctx context.Context, in orgIn) (any, error) {
	type workspace struct {
		ChannelPlugin string `json:"channelPlugin"`
		WorkspaceID   string `json:"workspaceId"`
		CreatedAt     any    `json:"createdAt"`
	}
	rows, err := r.api.DB.Query(ctx, `select channel_plugin, workspace_id, created_at from channel_workspace where org_id = $1`, in.OrgID)
	if err != nil {
		return nil, err
	}
	workspaces, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (workspace, error) {
		var w workspace
		var created *time.Time
		err := row.Scan(&w.ChannelPlugin, &w.WorkspaceID, &created)
		w.CreatedAt = jsTimeOrNil(created)
		return w, err
	})
	if err != nil {
		return nil, err
	}
	type identity struct {
		ID            string  `json:"id"`
		ChannelPlugin string  `json:"channelPlugin"`
		WorkspaceID   string  `json:"workspaceId"`
		ChannelUserID string  `json:"channelUserId"`
		DisplayName   *string `json:"displayName"`
		Email         *string `json:"email"`
		Status        string  `json:"status"`
		AppUserID     *string `json:"appUserId"`
		FirstSeenAt   any     `json:"firstSeenAt"`
		VerifiedAt    any     `json:"verifiedAt"`
	}
	rows, err = r.api.DB.Query(ctx, `select id::text, channel_plugin, workspace_id, channel_user_id, display_name, email,
		status, app_user_id, first_seen_at, verified_at from channel_identity where org_id = $1`, in.OrgID)
	if err != nil {
		return nil, err
	}
	identities, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (identity, error) {
		var i identity
		var seen, verified *time.Time
		err := row.Scan(&i.ID, &i.ChannelPlugin, &i.WorkspaceID, &i.ChannelUserID, &i.DisplayName, &i.Email,
			&i.Status, &i.AppUserID, &seen, &verified)
		i.FirstSeenAt, i.VerifiedAt = jsTimeOrNil(seen), jsTimeOrNil(verified)
		return i, err
	})
	if err != nil {
		return nil, err
	}
	if workspaces == nil {
		workspaces = []workspace{}
	}
	if identities == nil {
		identities = []identity{}
	}
	return jsFromGo(struct {
		Workspaces []workspace `json:"workspaces"`
		Identities []identity  `json:"identities"`
	}{workspaces, identities}), nil
}

// urlHostname is new URL(url).hostname, or nil when the URL does not parse.
func urlHostname(raw *string) *string {
	if raw == nil || *raw == "" {
		return nil
	}
	u, err := url.Parse(strings.TrimSpace(*raw))
	if err != nil || u.Scheme == "" || u.Opaque != "" {
		return nil
	}
	host := strings.ToLower(u.Hostname())
	if strings.HasPrefix(u.Host, "[") {
		host = "[" + host + "]"
	}
	return &host
}

// listDataSources is InProcessControlPlane.listDataSources(): hostnames
// only, so no connection string or credential reaches the model.
func (r *runTools) listDataSources(ctx context.Context, in orgIn) (any, error) {
	type source struct {
		ID        string  `json:"id"`
		Name      string  `json:"name"`
		Label     *string `json:"label"`
		Kind      string  `json:"kind"`
		AuthMode  string  `json:"authMode"`
		IsDefault bool    `json:"isDefault"`
		Enabled   bool    `json:"enabled"`
		Host      *string `json:"host"`
		HasMcp    bool    `json:"hasMcp"`
	}
	rows, err := r.api.DB.Query(ctx, `select id::text, name, label, kind, auth_mode, is_default, enabled, graphql_url, mcp_url
		from data_source where org_id = $1`, in.OrgID)
	if err != nil {
		return nil, err
	}
	sources, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (source, error) {
		var s source
		var graphqlURL, mcpURL *string
		err := row.Scan(&s.ID, &s.Name, &s.Label, &s.Kind, &s.AuthMode, &s.IsDefault, &s.Enabled, &graphqlURL, &mcpURL)
		s.Host, s.HasMcp = urlHostname(graphqlURL), deref(mcpURL) != ""
		return s, err
	})
	if err != nil {
		return nil, err
	}
	if sources == nil {
		sources = []source{}
	}
	return jsFromGo(map[string]any{"sources": sources}), nil
}

// listAuditTrail is InProcessControlPlane.listAuditTrail(). Only a run whose
// actor is an admin reads it; the gate reads the run from the database.
func (r *runTools) listAuditTrail(ctx context.Context, in auditTrailIn) (any, error) {
	db := r.api.DB
	denied := jsObjOf("denied", true)
	if in.RunID == nil {
		return denied, nil
	}
	var role *string
	err := db.QueryRow(ctx, `select actor_role from work_run where id::text = $1`, *in.RunID).Scan(&role)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if deref(role) != "admin" {
		return denied, nil
	}
	limit := 50
	if in.Limit != nil {
		limit = min(max(*in.Limit, 1), 200)
	}
	type request struct {
		ID           string  `json:"id"`
		Kind         string  `json:"kind"`
		Target       *string `json:"target"`
		Status       string  `json:"status"`
		Scope        string  `json:"scope"`
		Summary      *string `json:"summary"`
		ActorUserID  *string `json:"actorUserId"`
		ActorRole    *string `json:"actorRole"`
		ActorBackend *string `json:"actorBackend"`
		CreatedAt    string  `json:"createdAt"`
	}
	rows, err := db.Query(ctx, `select id::text, kind, target, status, scope, summary, actor_user_id, actor_role, actor_backend, created_at
		from action_request where org_id = $1 order by created_at desc limit $2`, in.OrgID, limit)
	if err != nil {
		return nil, err
	}
	requests, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (request, error) {
		var q request
		var created time.Time
		err := row.Scan(&q.ID, &q.Kind, &q.Target, &q.Status, &q.Scope, &q.Summary, &q.ActorUserID, &q.ActorRole, &q.ActorBackend, &created)
		q.CreatedAt = jsTime(created)
		return q, err
	})
	if err != nil {
		return nil, err
	}
	type alert struct {
		ID             string `json:"id"`
		Kind           string `json:"kind"`
		Subject        string `json:"subject"`
		Observed       int    `json:"observed"`
		Threshold      int    `json:"threshold"`
		WindowSeconds  int    `json:"windowSeconds"`
		AcknowledgedAt any    `json:"acknowledgedAt"`
		CreatedAt      string `json:"createdAt"`
	}
	rows, err = db.Query(ctx, `select id::text, kind, subject, observed, threshold, window_seconds, acknowledged_at, created_at
		from behavior_alert where org_id = $1 order by created_at desc limit $2`, in.OrgID, limit)
	if err != nil {
		return nil, err
	}
	alerts, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (alert, error) {
		var al alert
		var acknowledged *time.Time
		var created time.Time
		err := row.Scan(&al.ID, &al.Kind, &al.Subject, &al.Observed, &al.Threshold, &al.WindowSeconds, &acknowledged, &created)
		al.AcknowledgedAt, al.CreatedAt = jsTimeOrNil(acknowledged), jsTime(created)
		return al, err
	})
	if err != nil {
		return nil, err
	}
	type gatewayRow struct {
		RunID     *string `json:"runId"`
		Backend   *string `json:"backend"`
		ActorRole *string `json:"actorRole"`
		Calls     int     `json:"calls"`
	}
	rows, err = db.Query(ctx, `select run_id::text, backend, actor_role, count(*)::int
		from control_plane_audit where org_id = $1 and created_at >= now() - interval '24 hours'
		group by run_id, backend, actor_role order by count(*) desc limit $2`, in.OrgID, limit)
	if err != nil {
		return nil, err
	}
	gateway, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (gatewayRow, error) {
		var g gatewayRow
		return g, row.Scan(&g.RunID, &g.Backend, &g.ActorRole, &g.Calls)
	})
	if err != nil {
		return nil, err
	}
	if requests == nil {
		requests = []request{}
	}
	if alerts == nil {
		alerts = []alert{}
	}
	if gateway == nil {
		gateway = []gatewayRow{}
	}
	return jsFromGo(struct {
		Requests       []request    `json:"requests"`
		Alerts         []alert      `json:"alerts"`
		GatewaySummary []gatewayRow `json:"gatewaySummary"`
	}{requests, alerts, gateway}), nil
}
