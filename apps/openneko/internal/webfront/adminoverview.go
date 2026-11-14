package webfront

import (
	"net/http"

	"github.com/open-neko/neko/apps/openneko/internal/llm"
)

type countPair struct {
	Total int `json:"total"`
	On    int `json:"on"`
}

type adminOverview struct {
	SetupComplete        bool      `json:"setupComplete"`
	ActiveUsers          int       `json:"activeUsers"`
	ActiveAdmins         int       `json:"activeAdmins"`
	EnabledSources       int       `json:"enabledSources"`
	JwtSources           int       `json:"jwtSources"`
	Policies             countPair `json:"policies"`
	Workflows            countPair `json:"workflows"`
	PendingActions       int       `json:"pendingActions"`
	DataReady            bool      `json:"dataReady"`
	PrimaryReady         bool      `json:"primaryReady"`
	ResearchOn           bool      `json:"researchOn"`
	CustomWorkflowBudget bool      `json:"customWorkflowBudget"`
}

func (a *API) adminOverview(w http.ResponseWriter, r *http.Request) {
	act, ok := a.settingsAdmin(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	var o adminOverview
	err := a.DB.QueryRow(ctx, `select
		(select setup_complete_at is not null from organization where id = $1),
		(select count(*)::int from app_user where org_id = $1 and disabled_at is null),
		(select count(*)::int from user_group_membership m
			join user_group g on g.id = m.group_id
			join app_user u on u.id = m.user_id and u.disabled_at is null
			where m.org_id = $1 and g.slug = 'administrators'),
		(select count(*)::int from data_source where org_id = $1 and enabled),
		(select count(*)::int from data_source where org_id = $1 and enabled and auth_mode = 'jwt'),
		(select count(*)::int from action_policy where org_id = $1),
		(select count(*)::int from action_policy where org_id = $1 and enabled),
		(select count(*)::int from workflow_definition where org_id = $1),
		(select count(*)::int from workflow_definition where org_id = $1 and enabled),
		(select count(*)::int from action_request where org_id = $1 and status = 'pending_approval'),
		exists (select 1 from workflow_api_org_limits where org_id = $1)`, act.OrgID).Scan(
		&o.SetupComplete, &o.ActiveUsers, &o.ActiveAdmins, &o.EnabledSources, &o.JwtSources,
		&o.Policies.Total, &o.Policies.On, &o.Workflows.Total, &o.Workflows.On,
		&o.PendingActions, &o.CustomWorkflowBudget)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	source, err := a.loadDataSource(ctx, act.OrgID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if source != nil {
		o.DataReady = len(dataSourceDraft{source.graphqlURL, source.mcpURL, source.label}.problems()) == 0
	}
	if o.PrimaryReady, err = llm.SetupComplete(ctx, a.DB, a.SecretsDir, act.OrgID); err != nil {
		a.fail(w, r, err)
		return
	}
	research, err := llm.ResearchFor(ctx, a.DB, a.SecretsDir, act.OrgID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	o.ResearchOn = !research.Off()
	sendJSON(w, http.StatusOK, o)
}
