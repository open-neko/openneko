package webfront

import (
	"context"
	"net/http"
	"regexp"

	"github.com/jackc/pgx/v5"
)

// actor matches getCurrentActor() in apps/web/src/lib/actor.ts. UserID is
// empty when no user is signed in.
type actor struct {
	OrgID  string
	UserID string
	Admin  bool
	// PackID marks the service actor of a run that a pack's workflow
	// started. It holds only that pack's items.
	PackID string
}

func (a *API) currentActor(ctx context.Context, r *http.Request) (actor, error) {
	orgID, err := a.OrgID(ctx)
	if err != nil {
		return actor{}, err
	}
	u, err := a.currentUser(ctx, r, orgID)
	if err != nil || u == nil {
		return actor{OrgID: orgID}, err
	}
	role, err := a.role(ctx, orgID, u.ID)
	return actor{OrgID: orgID, UserID: u.ID, Admin: role == "admin"}, err
}

// heldSet is the result of heldItems(): All, or the ids in IDs.
type heldSet struct {
	All bool
	IDs map[string]bool
}

func (h heldSet) has(id string) bool { return h.All || h.IDs[id] }

var packItemJoins = map[string]struct{ kind, idExpr, join string }{
	"action":   {"action", "a.target_ref", ""},
	"skill":    {"skill", "a.target_ref", ""},
	"workflow": {"workflow", "w.id::text", "join workflow_definition w on w.org_id = a.org_id and w.name = a.target_ref"},
}

// heldItems matches heldItemIds() in apps/web/src/lib/entitlements.ts for the
// item types in packItemJoins.
func (a *API) heldItems(ctx context.Context, act actor, itemType string) (heldSet, error) {
	if act.Admin {
		return heldSet{All: true}, nil
	}
	held := heldSet{IDs: map[string]bool{}}
	if act.PackID != "" {
		spec, ok := packItemJoins[itemType]
		if !ok {
			return held, nil
		}
		ids, err := collectStrings(ctx, a, `
			select distinct `+spec.idExpr+`
			from pack_artifact a
			join pack_install i on i.id = a.pack_install_id and i.status <> 'removed'
			`+spec.join+`
			where a.org_id = $1 and a.artifact_kind = $2 and a.ownership <> 'detached' and i.pack_id = $3`,
			act.OrgID, spec.kind, act.PackID)
		for _, id := range ids {
			held.IDs[id] = true
		}
		return held, err
	}
	if act.UserID == "" {
		return held, nil
	}
	rows, err := a.DB.Query(ctx, `
		select gr.item_type, gr.item_id from item_grant gr
		where gr.org_id = $1 and gr.group_id in (
		  select g.id from user_group g
		  join app_user u on u.id = $2 and u.org_id = g.org_id and u.disabled_at is null
		  where g.org_id = $1 and (g.slug = 'everyone'
		    or exists (select 1 from user_group_membership m where m.group_id = g.id and m.user_id = u.id)))`,
		act.OrgID, act.UserID)
	if err != nil {
		return held, err
	}
	var packs []string
	allPacks := false
	for rows.Next() {
		var typ, id string
		if err := rows.Scan(&typ, &id); err != nil {
			rows.Close()
			return held, err
		}
		if typ == itemType {
			if id == "*" {
				rows.Close()
				return heldSet{All: true}, nil
			}
			held.IDs[id] = true
		}
		if typ == "pack" {
			packs = append(packs, id)
			allPacks = allPacks || id == "*"
		}
	}
	if err := rows.Err(); err != nil {
		return held, err
	}
	spec, ok := packItemJoins[itemType]
	if len(packs) == 0 || !ok {
		return held, nil
	}
	ids, err := collectStrings(ctx, a, `
		select distinct `+spec.idExpr+`
		from pack_artifact a
		join pack_install i on i.id = a.pack_install_id and i.status <> 'removed'
		`+spec.join+`
		where a.org_id = $1 and a.artifact_kind = $2 and a.ownership <> 'detached'
		  and ($3 or i.pack_id = any($4::text[]))`,
		act.OrgID, spec.kind, allPacks, packs)
	for _, id := range ids {
		held.IDs[id] = true
	}
	return held, err
}

func collectStrings(ctx context.Context, a *API, sql string, args ...any) ([]string, error) {
	rows, err := a.DB.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// workflowVisibility matches workflowVisibility() in
// apps/web/src/lib/entitlements.ts.
func (a *API) workflowVisibility(ctx context.Context, act actor) (func(id, owner string) bool, error) {
	if act.Admin {
		return func(string, string) bool { return true }, nil
	}
	if act.UserID == "" {
		return func(string, string) bool { return false }, nil
	}
	held, err := a.heldItems(ctx, act, "workflow")
	if err != nil {
		return nil, err
	}
	return func(id, owner string) bool {
		if owner != "" {
			return owner == act.UserID
		}
		return held.has(id)
	}, nil
}

func (a *API) workflowOwners(ctx context.Context, orgID string) (map[string]string, error) {
	rows, err := a.DB.Query(ctx, `select id::text, coalesce(owner_user_id, '') from workflow_definition where org_id = $1`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	owners := map[string]string{}
	for rows.Next() {
		var id, owner string
		if err := rows.Scan(&id, &owner); err != nil {
			return nil, err
		}
		owners[id] = owner
	}
	return owners, rows.Err()
}

// actionRequestVisibility matches actionRequestVisibility() in
// apps/web/src/lib/entitlements.ts. An empty workflowID means none.
func (a *API) actionRequestVisibility(ctx context.Context, act actor) (func(kind, scope, workflowID string) bool, error) {
	visible, err := a.workflowVisibility(ctx, act)
	if err != nil {
		return nil, err
	}
	owners, err := a.workflowOwners(ctx, act.OrgID)
	if err != nil {
		return nil, err
	}
	held, err := a.heldItems(ctx, act, "action")
	if err != nil {
		return nil, err
	}
	packKinds := map[string]bool{}
	if !held.All {
		kinds, err := collectStrings(ctx, a, `select kind from pack_action_definition where org_id = $1`, act.OrgID)
		if err != nil {
			return nil, err
		}
		for _, k := range kinds {
			packKinds[k] = true
		}
	}
	return func(kind, scope, workflowID string) bool {
		if workflowID != "" {
			owner, ok := owners[workflowID]
			if !ok || !visible(workflowID, owner) {
				return false
			}
		}
		if held.All || (scope != "external" && !packKinds[kind]) {
			return true
		}
		return held.IDs[kind]
	}, nil
}

// actionRecordVisibility matches actionRecordVisibility(): the stored
// request names a run, and the run names the workflow.
func (a *API) actionRecordVisibility(ctx context.Context, act actor, runIDs []string) (func(kind, scope, runID string) bool, error) {
	visible, err := a.actionRequestVisibility(ctx, act)
	if err != nil {
		return nil, err
	}
	workflowOf := map[string]string{}
	if len(runIDs) > 0 {
		rows, err := a.DB.Query(ctx, `select id::text, workflow_id::text from workflow_run where org_id = $1 and id = any($2::uuid[])`, act.OrgID, runIDs)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id, wf string
			if err := rows.Scan(&id, &wf); err != nil {
				rows.Close()
				return nil, err
			}
			workflowOf[id] = wf
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return func(kind, scope, runID string) bool {
		return visible(kind, scope, workflowOf[runID])
	}, nil
}

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func isUUID(s string) bool { return uuidPattern.MatchString(s) }
