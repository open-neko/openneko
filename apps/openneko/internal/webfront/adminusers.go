package webfront

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/open-neko/neko/apps/openneko/internal/config"
)

const noSignInMessage = "set up sign-in before you add people"

var deviceIDPattern = regexp.MustCompile(`(?i)^[0-9a-f-]{36}$`)

// userAdmin is requireAdminActor() followed by the sign-in check of the
// user routes.
func (a *API) userAdmin(w http.ResponseWriter, r *http.Request) (actor, bool) {
	act, ok := a.accessAdmin(w, r)
	if !ok {
		return act, false
	}
	if st := a.Gate.Status(r.Context()); st.Provider == nil && st.Pending == nil {
		sendError(w, http.StatusConflict, noSignInMessage)
		return act, false
	}
	return act, true
}

// readUserBody is `await request.json()` with the routes' catch. A JSON
// value that is not an object reads as an empty object.
func readUserBody(w http.ResponseWriter, r *http.Request) (map[string]any, bool) {
	body, ok := readBody(r)
	if !ok {
		sendError(w, http.StatusBadRequest, "invalid JSON body")
		return nil, false
	}
	m, _ := body.(map[string]any)
	if m == nil {
		m = map[string]any{}
	}
	return m, true
}

func validEmail(email string) bool {
	at := strings.IndexByte(email, '@')
	if at <= 0 || strings.IndexFunc(email, jsSpace) >= 0 || strings.Count(email, "@") != 1 {
		return false
	}
	domain := email[at+1:]
	return len(domain) >= 3 && strings.Contains(domain[1:len(domain)-1], ".")
}

func duplicateEmail(w http.ResponseWriter, email string) {
	sendError(w, http.StatusConflict, fmt.Sprintf("a user with email %s already exists", email))
}

func (a *API) adminUserCreate(w http.ResponseWriter, r *http.Request) {
	act, ok := a.userAdmin(w, r)
	if !ok {
		return
	}
	body, ok := readUserBody(w, r)
	if !ok {
		return
	}
	email, _ := body["email"].(string)
	email = strings.ToLower(jsTrim(email))
	if !validEmail(email) || jsLength(email) > 320 {
		sendError(w, http.StatusBadRequest, "a valid email address is required")
		return
	}
	role, _ := body["role"].(string)
	if role != "admin" && role != "member" {
		sendError(w, http.StatusBadRequest, "role must be admin or member")
		return
	}
	var name *string
	if s, ok := body["name"].(string); ok && jsTrim(s) != "" {
		trimmed := utf16Prefix(jsTrim(s), 200)
		name = &trimmed
	}
	solo := body["updateSoloAccount"] == true

	ctx := r.Context()
	var existing string
	err := a.DB.QueryRow(ctx, `select id from app_user where org_id = $1 and lower(email) = $2 limit 1`, act.OrgID, email).Scan(&existing)
	if err == nil {
		duplicateEmail(w, email)
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		a.fail(w, r, err)
		return
	}
	suffix, err := randomURL(9)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	id := "usr_" + suffix
	created := true
	err = pgx.BeginFunc(ctx, a.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtext($1))`, "openneko.app_user:"+act.OrgID); err != nil {
			return err
		}
		if !solo {
			_, err := tx.Exec(ctx, `insert into app_user (id, sub, email, name, org_id) values ($1, null, $2, $3, $4)`, id, email, name, act.OrgID)
			return err
		}
		var owner *string
		if err := tx.QueryRow(ctx, `select solo_admin_user_id from organization where id = $1`, act.OrgID).Scan(&owner); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var ownerEmail string
		var ownerSub *string
		err := tx.QueryRow(ctx, `select email, sub from app_user where org_id = $1 and id = $2`, act.OrgID, act.UserID).Scan(&ownerEmail, &ownerSub)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if owner == nil || *owner != act.UserID || err != nil || ownerSub != nil || !strings.HasSuffix(ownerEmail, soloEmailDomain) || role != "admin" {
			created = false
			return nil
		}
		id = act.UserID
		_, err = tx.Exec(ctx, `update app_user set email = $2, name = $3, updated_at = now() where id = $1`, id, email, name)
		return err
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		duplicateEmail(w, email)
		return
	}
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if !created {
		sendError(w, http.StatusConflict, "This account cannot be updated here. Reload the page.")
		return
	}
	if role == "admin" {
		if err := a.setLocalAdministrator(ctx, act.OrgID, id, true); err != nil {
			a.fail(w, r, err)
			return
		}
	}
	out := map[string]any{"user": map[string]any{"id": id, "email": email, "name": name, "role": role}}
	if body["addToDirectory"] == true && !solo {
		payload, _ := json.Marshal(map[string]any{"email": email, "name": name})
		if msg := a.directoryUser(ctx, payload); msg != "" {
			out["directoryError"] = msg
		}
	}
	status := http.StatusCreated
	if solo {
		status = http.StatusOK
	}
	sendJSON(w, status, out)
}

// directoryUser asks the worker to create the user in the directory and
// returns the failure message, or "".
func (a *API) directoryUser(ctx context.Context, payload []byte) string {
	status, reply, err := a.workerDo(ctx, http.MethodPost, "/admin/directory/users", workerAdminTimeout, payload)
	if err != nil {
		return "worker is unavailable"
	}
	if status < 400 {
		return ""
	}
	var parsed struct {
		Error any `json:"error"`
	}
	if json.Unmarshal(reply, &parsed) != nil {
		return fmt.Sprintf("Worker returned HTTP %d", status)
	}
	if msg, ok := parsed.Error.(string); ok && msg != "" {
		return msg
	}
	return fmt.Sprintf("HTTP %d", status)
}

// setLocalAdministrator is the function of that name in packages/db/src/groups.ts.
func (a *API) setLocalAdministrator(ctx context.Context, orgID, userID string, administrator bool) error {
	var stillMember bool
	err := a.withAdministratorGuard(ctx, orgID, func(tx pgx.Tx) error {
		var groupID string
		err := tx.QueryRow(ctx, `select id::text from user_group where org_id = $1 and slug = 'administrators'`, orgID).Scan(&groupID)
		if errors.Is(err, pgx.ErrNoRows) {
			return groupErr("not_found", "built-in group administrators is missing")
		}
		if err != nil {
			return err
		}
		if administrator {
			var found string
			err := tx.QueryRow(ctx, `select id from app_user where org_id = $1 and id = $2`, orgID, userID).Scan(&found)
			if errors.Is(err, pgx.ErrNoRows) {
				return groupErr("not_found", "user not found")
			}
			if err != nil {
				return err
			}
			_, err = tx.Exec(ctx, `insert into user_group_membership (org_id, group_id, user_id, source)
				values ($1, $2, $3, 'local') on conflict do nothing`, orgID, groupID, userID)
			return err
		}
		if _, err := tx.Exec(ctx, `delete from user_group_membership
			where org_id = $1 and group_id = $2 and user_id = $3 and source = 'local'`, orgID, groupID, userID); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `select exists (select 1 from user_group_membership
			where org_id = $1 and group_id = $2 and user_id = $3)`, orgID, groupID, userID).Scan(&stillMember)
	})
	if err == nil && stillMember {
		return groupErr("conflict", "this user is an administrator through an IdP rule; change the rule instead")
	}
	return err
}

func (a *API) adminUserPatch(w http.ResponseWriter, r *http.Request) {
	act, ok := a.userAdmin(w, r)
	if !ok {
		return
	}
	body, ok := readUserBody(w, r)
	if !ok {
		return
	}
	rawRole, hasRole := body["role"]
	role, _ := rawRole.(string)
	if hasRole && role != "admin" && role != "member" {
		sendError(w, http.StatusBadRequest, "role must be admin or member")
		return
	}
	rawDisabled, hasDisabled := body["disabled"]
	disabled, isBool := rawDisabled.(bool)
	if hasDisabled && !isBool {
		sendError(w, http.StatusBadRequest, "disabled must be a boolean")
		return
	}
	if !hasRole && !hasDisabled {
		sendError(w, http.StatusBadRequest, "nothing to change — pass role and/or disabled")
		return
	}

	ctx, userID := r.Context(), r.PathValue("userId")
	var wasDisabled, wasAdmin bool
	err := a.DB.QueryRow(ctx, `
		select u.disabled_at is not null,
		  exists (select 1 from user_group_membership m join user_group g on g.id = m.group_id
		    where m.org_id = $1 and m.user_id = u.id and g.slug = 'administrators')
		from app_user u where u.org_id = $1 and u.id = $2`, act.OrgID, userID).Scan(&wasDisabled, &wasAdmin)
	if errors.Is(err, pgx.ErrNoRows) {
		sendError(w, http.StatusNotFound, "user not found")
		return
	}
	if err != nil {
		a.fail(w, r, err)
		return
	}
	targetRole := "member"
	if wasAdmin {
		targetRole = "admin"
	}
	if wasAdmin && !wasDisabled && (role == "member" || (hasDisabled && disabled)) {
		var others int
		err := a.DB.QueryRow(ctx, `
			select count(*)::int from user_group_membership m
			join user_group g on g.id = m.group_id
			join app_user u on u.id = m.user_id and u.disabled_at is null
			where m.org_id = $1 and g.slug = 'administrators' and m.user_id <> $2`, act.OrgID, userID).Scan(&others)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		if others == 0 {
			sendError(w, http.StatusConflict, "refusing to remove the last active administrator")
			return
		}
	}
	if hasRole && role != targetRole {
		if err := a.setLocalAdministrator(ctx, act.OrgID, userID, role == "admin"); err != nil {
			var ge *groupError
			if errors.As(err, &ge) {
				status := http.StatusConflict
				if ge.code == "not_found" {
					status = http.StatusNotFound
				}
				sendError(w, status, ge.message)
				return
			}
			a.fail(w, r, err)
			return
		}
	}
	if hasDisabled {
		if _, err := a.DB.Exec(ctx, `update app_user set disabled_at = case when $2 then now() end, updated_at = now() where id = $1`,
			userID, disabled); err != nil {
			a.fail(w, r, err)
			return
		}
	} else {
		disabled = wasDisabled
	}
	if !hasRole {
		role = targetRole
	}
	sendJSON(w, http.StatusOK, map[string]any{"user": map[string]any{"id": userID, "role": role, "disabled": disabled}})
}

type effectiveGroup struct {
	GroupID string `json:"groupId"`
	Name    string `json:"name"`
}

type effectiveItem struct {
	ItemType string           `json:"itemType"`
	ItemID   string           `json:"itemId"`
	Groups   []effectiveGroup `json:"groups"`
}

// adminUserAccess is effectiveAccess() and resolveUserGroups() in packages/db.
func (a *API) adminUserAccess(w http.ResponseWriter, r *http.Request) {
	act, ok := a.accessAdmin(w, r)
	if !ok {
		return
	}
	ctx, userID := r.Context(), r.PathValue("userId")
	rows, err := a.DB.Query(ctx, `
		select distinct g.id::text, g.slug from user_group g
		join app_user u on u.id = $2 and u.org_id = g.org_id and u.disabled_at is null
		where g.org_id = $1
		  and (g.slug = 'everyone'
		    or exists (select 1 from user_group_membership m where m.group_id = g.id and m.user_id = u.id))
		order by g.slug`, act.OrgID, userID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	type found struct{ id, slug string }
	groups, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (found, error) {
		var g found
		return g, row.Scan(&g.id, &g.slug)
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	ids, slugs := []string{}, []string{}
	administrator := false
	for _, g := range groups {
		ids = append(ids, g.id)
		slugs = append(slugs, g.slug)
		administrator = administrator || g.slug == "administrators"
	}
	items := []*effectiveItem{}
	if len(ids) > 0 {
		rows, err := a.DB.Query(ctx, `
			select ig.item_type, ig.item_id, g.id::text, g.name
			from item_grant ig join user_group g on g.id = ig.group_id
			where ig.org_id = $1 and ig.group_id = any($2::uuid[])
			order by ig.item_type, ig.item_id = '*' desc, ig.item_id, lower(g.name)`, act.OrgID, ids)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		byKey := map[string]*effectiveItem{}
		var itemType, itemID string
		var g effectiveGroup
		_, err = pgx.ForEachRow(rows, []any{&itemType, &itemID, &g.GroupID, &g.Name}, func() error {
			key := itemType + "\x00" + itemID
			item := byKey[key]
			if item == nil {
				item = &effectiveItem{ItemType: itemType, ItemID: itemID, Groups: []effectiveGroup{}}
				byKey[key] = item
				items = append(items, item)
			}
			item.Groups = append(item.Groups, g)
			return nil
		})
		if err != nil {
			a.fail(w, r, err)
			return
		}
	}
	sendJSON(w, http.StatusOK, map[string]any{"administrator": administrator, "items": items, "groupSlugs": slugs})
}

type deviceRow struct {
	ID          string  `json:"id"`
	UserID      string  `json:"userId"`
	UserEmail   string  `json:"userEmail"`
	UserName    *string `json:"userName"`
	Name        string  `json:"name"`
	TokenPrefix string  `json:"tokenPrefix"`
	CreatedAt   string  `json:"createdAt"`
	LastUsedAt  string  `json:"lastUsedAt"`
	RevokedAt   *string `json:"revokedAt"`
	Status      string  `json:"status"`
}

func (a *API) adminDevices(w http.ResponseWriter, r *http.Request) {
	act, ok := a.accessAdmin(w, r)
	if !ok {
		return
	}
	rows, err := a.DB.Query(r.Context(), `
		select d.id::text, d.user_id, u.email, u.name, d.name, d.token_prefix,
		       d.created_at, d.last_used_at, d.revoked_at,
		       d.last_used_at <= now() - make_interval(days => $2) as idle
		from app_device d join app_user u on u.id = d.user_id
		where d.org_id = $1
		order by d.revoked_at is not null, d.last_used_at desc`, act.OrgID, deviceIdleDays)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	devices, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (deviceRow, error) {
		var d deviceRow
		var created, used time.Time
		var revoked *time.Time
		var idle bool
		if err := row.Scan(&d.ID, &d.UserID, &d.UserEmail, &d.UserName, &d.Name, &d.TokenPrefix, &created, &used, &revoked, &idle); err != nil {
			return d, err
		}
		d.CreatedAt, d.LastUsedAt = jsTime(created), jsTime(used)
		switch {
		case revoked != nil:
			at := jsTime(*revoked)
			d.RevokedAt, d.Status = &at, "revoked"
		case idle:
			d.Status = "idle"
		default:
			d.Status = "active"
		}
		return d, nil
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	sendJSON(w, http.StatusOK, map[string]any{"devices": devices})
}

func (a *API) adminDeviceRevoke(w http.ResponseWriter, r *http.Request) {
	act, ok := a.accessAdmin(w, r)
	if !ok {
		return
	}
	ctx, deviceID := r.Context(), r.PathValue("deviceId")
	var id, userID, name string
	err := pgx.ErrNoRows
	if deviceIDPattern.MatchString(deviceID) {
		err = a.DB.QueryRow(ctx, `
			update app_device set revoked_at = now(), revoked_by_user_id = $3
			where org_id = $1 and id = $2::uuid and revoked_at is null
			returning id::text, user_id, name`, act.OrgID, deviceID, act.UserID).Scan(&id, &userID, &name)
	}
	var pgErr *pgconn.PgError
	if errors.Is(err, pgx.ErrNoRows) || errors.As(err, &pgErr) && pgErr.Code == "22P02" {
		sendError(w, http.StatusNotFound, "no active device with this id")
		return
	}
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.recordAudit(ctx, auditEvent{OrgID: act.OrgID, EntityKind: "device", EntityID: id, Event: "revoked",
		Payload: map[string]any{"byUserId": act.UserID, "userId": userID, "deviceName": name}})
	sendJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

const passwordMinLength = 8

var commonPasswords = map[string]bool{"secret": true, "password": true, "postgres": true}

// databaseURLConfigured is configuredDatabaseUrl() in packages/db.
func (a *API) databaseURLConfigured() (bool, error) {
	if file := strings.TrimSpace(os.Getenv("OPENNEKO_DATABASE_URL_FILE")); file != "" {
		raw, err := os.ReadFile(file)
		if err != nil {
			return false, err
		}
		if strings.TrimSpace(string(raw)) == "" {
			return false, fmt.Errorf("OPENNEKO_DATABASE_URL_FILE (%s) is empty", file)
		}
		return true, nil
	}
	local, _ := config.ReadLocal(a.SecretsDir)
	return strings.TrimSpace(local.DatabaseURL) != "", nil
}

func (a *API) passwordChanged(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.accessAdmin(w, r); !ok {
		return
	}
	changed := strings.TrimSpace(os.Getenv("OPENNEKO_DATABASE_URL_FILE")) != ""
	if !changed {
		local, _ := config.ReadLocal(a.SecretsDir)
		changed = local.DatabaseURL != "" || local.Pg != nil && local.Pg.Password != "" && local.Pg.Password != "secret"
	}
	sendJSON(w, http.StatusOK, map[string]bool{"changed": changed})
}

func (a *API) changePassword(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.accessAdmin(w, r); !ok {
		return
	}
	byURL, err := a.databaseURLConfigured()
	if err != nil {
		sendError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if byURL {
		sendError(w, http.StatusConflict, "This install connects with a database URL. Change the password where the database is managed, then update the URL.")
		return
	}
	body, _ := readBody(r)
	password, _ := field(body, "password")
	pw, _ := password.(string)
	pw = jsTrim(pw)
	switch {
	case jsLength(pw) < passwordMinLength:
		sendError(w, http.StatusBadRequest, fmt.Sprintf("Password must be at least %d characters.", passwordMinLength))
		return
	case commonPasswords[strings.ToLower(pw)]:
		sendError(w, http.StatusBadRequest, "That password is too common. Pick something else.")
		return
	case strings.ContainsAny(pw, "'\\\n\r\x00"):
		sendError(w, http.StatusBadRequest, "Password contains characters we can't safely apply (quotes, backslashes, newlines).")
		return
	}
	if err := a.rotateDatabaseRole(r.Context(), pw); err != nil {
		log.Printf("[change-password] %v", err)
		message := err.Error()
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			message = pgErr.Message
		}
		sendError(w, http.StatusInternalServerError, message)
		return
	}
	a.DB.Reset()
	status, _, err := a.workerDo(r.Context(), http.MethodPost, "/admin/reconnect", 5*time.Second, nil)
	notified := err == nil && status >= 200 && status < 300
	if !notified {
		log.Print("[change-password] worker reconnect notification failed; its job runner keeps the old credentials until it restarts")
	}
	sendJSON(w, http.StatusOK, map[string]bool{"ok": true, "workerNotified": notified})
	if os.Getenv("OPENNEKO_RESTART_WEB_ON_PASSWORD_CHANGE") == "1" && a.Restart != nil {
		time.AfterFunc(250*time.Millisecond, a.Restart)
	}
}

// rotateDatabaseRole holds a session lock across the role change and the
// config write, so two rotations cannot leave the file and the role apart.
func (a *API) rotateDatabaseRole(ctx context.Context, password string) error {
	conn, err := a.DB.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `select pg_advisory_lock(hashtext('openneko.password-rotation'))`); err != nil {
		return err
	}
	defer func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx), `select pg_advisory_unlock(hashtext('openneko.password-rotation'))`)
	}()
	role := a.DBRole
	if role == "" {
		role = "neko"
	}
	if _, err := conn.Exec(ctx, fmt.Sprintf(`alter role %s with password '%s'`, pgx.Identifier{role}.Sanitize(), password)); err != nil {
		return err
	}
	return config.WriteLocalPgPassword(a.SecretsDir, password)
}
