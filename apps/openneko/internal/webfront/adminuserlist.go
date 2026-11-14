package webfront

import (
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
)

type adminUserGroup struct {
	Name     string `json:"name"`
	FromRule bool   `json:"fromRule"`
}

type adminUserRow struct {
	ID          string           `json:"id"`
	Email       string           `json:"email"`
	Name        *string          `json:"name"`
	Role        string           `json:"role"`
	Source      string           `json:"source"`
	Groups      []adminUserGroup `json:"groups"`
	Disabled    bool             `json:"disabled"`
	HasSignedIn bool             `json:"hasSignedIn"`
	LastLoginAt *string          `json:"lastLoginAt"`
	CreatedAt   *string          `json:"createdAt"`
}

func (a *API) adminUsers(w http.ResponseWriter, r *http.Request) {
	act, ok := a.settingsAdmin(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	mRows, err := a.DB.Query(ctx, `
		select m.user_id, g.name, bool_and(m.source <> 'local')
		from user_group_membership m
		join user_group g on g.id = m.group_id
		where m.org_id = $1
		group by m.user_id, g.name
		order by lower(g.name), g.name`, act.OrgID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	groupsByUser := map[string][]adminUserGroup{}
	var userID string
	var group adminUserGroup
	if _, err := pgx.ForEachRow(mRows, []any{&userID, &group.Name, &group.FromRule}, func() error {
		groupsByUser[userID] = append(groupsByUser[userID], group)
		return nil
	}); err != nil {
		a.fail(w, r, err)
		return
	}
	rows, err := a.DB.Query(ctx, `
		select u.id, u.email, u.name,
			case when exists (
				select 1 from user_group_membership m
				join user_group g on g.id = m.group_id
				where m.org_id = $1 and m.user_id = u.id and g.slug = 'administrators'
			) then 'admin' else 'member' end,
			u.source,
			u.disabled_at is not null,
			u.sub is not null or u.last_login_at is not null,
			u.last_login_at,
			u.created_at
		from app_user u
		where u.org_id = $1
		order by u.email asc`, act.OrgID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	users, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (adminUserRow, error) {
		var u adminUserRow
		var lastLogin, created *time.Time
		err := row.Scan(&u.ID, &u.Email, &u.Name, &u.Role, &u.Source, &u.Disabled, &u.HasSignedIn, &lastLogin, &created)
		u.Groups = groupsByUser[u.ID]
		if u.Groups == nil {
			u.Groups = []adminUserGroup{}
		}
		u.LastLoginAt, u.CreatedAt = jsTimePtr(lastLogin), jsTimePtr(created)
		return u, err
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	sendJSON(w, http.StatusOK, map[string]any{"users": users})
}
