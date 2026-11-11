package webfront

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
)

const soloEmailDomain = "@solo.openneko.invalid"

type user struct {
	ID    string  `json:"id"`
	Email string  `json:"email"`
	Name  *string `json:"name"`
}

// OrgID matches getOrgId() in packages/db/src/org.ts, with the same lock key.
func (a *API) OrgID(ctx context.Context) (string, error) {
	a.orgMu.Lock()
	defer a.orgMu.Unlock()
	if a.orgID != "" {
		return a.orgID, nil
	}
	const first = `select id from organization order by created_at, id limit 1`
	var id string
	err := a.DB.QueryRow(ctx, first).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		err = pgx.BeginFunc(ctx, a.DB, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtext('openneko.organization.bootstrap'))`); err != nil {
				return err
			}
			err := tx.QueryRow(ctx, first).Scan(&id)
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			id, err = newUUID()
			if err != nil {
				return err
			}
			_, err = tx.Exec(ctx, `insert into organization (id, name) values ($1, 'My Workspace')`, id)
			return err
		})
	}
	if err != nil {
		return "", err
	}
	a.orgID = id
	return id, nil
}

func newUUID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}

func randomURL(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// currentUser matches getCurrentUser() in apps/web/src/lib/auth.ts.
func (a *API) currentUser(ctx context.Context, r *http.Request, orgID string) (*user, error) {
	if !a.Gate.Status(ctx).Live() {
		return a.soloAdmin(ctx, orgID)
	}
	var userID string
	if c, err := r.Cookie(SessionCookie); err == nil && c.Value != "" {
		userID = a.Gate.SessionUserID(c.Value)
	} else {
		_, id, err := a.Gate.Device(ctx, r.Header.Get("Authorization"))
		if err != nil {
			return nil, err
		}
		userID = id
	}
	if userID == "" {
		return nil, nil
	}
	u := &user{}
	err := a.DB.QueryRow(ctx, `select id, email, name from app_user where id = $1 and disabled_at is null`, userID).
		Scan(&u.ID, &u.Email, &u.Name)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return u, nil
}

func isAdmin(ctx context.Context, q pgx.Tx, orgID, userID string) (bool, error) {
	var admin bool
	err := q.QueryRow(ctx, `
		select exists (
		  select 1 from user_group_membership m
		  join user_group g on g.id = m.group_id
		  where m.org_id = $1 and m.user_id = $2 and g.slug = 'administrators')`, orgID, userID).Scan(&admin)
	return admin, err
}

func (a *API) role(ctx context.Context, orgID, userID string) (string, error) {
	var admin bool
	err := a.DB.QueryRow(ctx, `
		select exists (
		  select 1 from user_group g
		  join app_user u on u.id = $2 and u.org_id = g.org_id and u.disabled_at is null
		  join user_group_membership m on m.group_id = g.id and m.user_id = u.id
		  where g.org_id = $1 and g.slug = 'administrators')`, orgID, userID).Scan(&admin)
	if err != nil {
		return "", err
	}
	if admin {
		return "admin", nil
	}
	return "member", nil
}

// soloAdmin matches getOrCreateSoloAdmin() in packages/db/src/solo-admin.ts.
// The caller hides an unclaimed solo address.
func (a *API) soloAdmin(ctx context.Context, orgID string) (*user, error) {
	var owner *user
	err := pgx.BeginFunc(ctx, a.DB, func(tx pgx.Tx) error {
		found, err := readOwner(ctx, tx, orgID)
		if err != nil || found != nil {
			owner = found
			return err
		}
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtext($1))`, "openneko.app_user:"+orgID); err != nil {
			return err
		}
		var ownerID *string
		if err := tx.QueryRow(ctx, `select solo_admin_user_id from organization where id = $1`, orgID).Scan(&ownerID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return errNoOrg
			}
			return err
		}
		if ownerID == nil {
			rows, err := tx.Query(ctx, `
				select u.id from app_user u
				join user_group_membership m on m.user_id = u.id
				join user_group g on g.id = m.group_id
				where u.org_id = $1 and g.slug = 'administrators' and u.sub is null and u.disabled_at is null
				limit 2`, orgID)
			if err != nil {
				return err
			}
			candidates, err := pgx.CollectRows(rows, pgx.RowTo[string])
			if err != nil {
				return err
			}
			var id string
			if len(candidates) == 1 {
				id = candidates[0]
			} else {
				suffix, err := randomURL(9)
				if err != nil {
					return err
				}
				id = "usr_" + suffix
				if _, err := tx.Exec(ctx, `insert into app_user (id, org_id, name, email) values ($1, $2, 'Solo administrator', $3)`,
					id, orgID, id+soloEmailDomain); err != nil {
					return err
				}
			}
			if _, err := tx.Exec(ctx, `
				insert into user_group_membership (org_id, group_id, user_id, source)
				select $1, g.id, $2, 'local' from user_group g
				where g.org_id = $1 and g.slug = 'administrators'
				on conflict do nothing`, orgID, id); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `update organization set solo_admin_user_id = $2 where id = $1`, orgID, id); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `
				update work_thread t set created_by_user_id = $2
				where t.org_id = $1 and t.channel = 'web' and t.created_by_user_id is null
				  and not exists (select 1 from workflow_run r where r.thread_id = t.id)`, orgID, id); err != nil {
				return err
			}
		}
		owner, err = readOwner(ctx, tx, orgID)
		return err
	})
	if err != nil || owner == nil {
		return nil, err
	}
	if strings.HasSuffix(owner.Email, soloEmailDomain) {
		owner.Email = ""
	}
	return owner, nil
}

func readOwner(ctx context.Context, tx pgx.Tx, orgID string) (*user, error) {
	u := &user{}
	var disabled bool
	err := tx.QueryRow(ctx, `
		select u.id, u.email, u.name, u.disabled_at is not null
		from organization o join app_user u on u.id = o.solo_admin_user_id
		where o.id = $1 and u.org_id = $1`, orgID).Scan(&u.ID, &u.Email, &u.Name, &disabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	admin, err := isAdmin(ctx, tx, orgID, u.ID)
	if err != nil || !admin || disabled {
		return nil, err
	}
	return u, nil
}
