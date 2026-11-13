package webfront

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/open-neko/neko/apps/openneko/internal/config"
)

type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

var (
	memoryKinds  = []string{"preference", "business_rule", "metric_definition", "thread_note", "correction", "company_context", "other"}
	memoryScopes = []string{"global", "thread", "database"}
)

const memoryColumns = `id::text, org_id, kind, scope, scope_id, text, pinned, confidence,
	case when jsonb_typeof(metadata) = 'object' then metadata else '{}'::jsonb end,
	source_run_id::text, source_thread_id::text, use_count, last_used_at, user_id, origin_id::text,
	overrides_origin_id::text, suppressed, promoted_from_id::text, promoted_by, promoted_at,
	integrity_hmac, expires_at, archived_at, created_at, updated_at`

// memoryOut is rowToMemory() in packages/llm/src/work/memory.ts, in its key order.
type memoryOut struct {
	IntegrityOK       bool       `json:"integrityOk"`
	ExpiresAt         *string    `json:"expiresAt"`
	ID                string     `json:"id"`
	OrgID             string     `json:"orgId"`
	Kind              string     `json:"kind"`
	Scope             string     `json:"scope"`
	ScopeID           *string    `json:"scopeId"`
	Text              string     `json:"text"`
	Pinned            bool       `json:"pinned"`
	Confidence        float32    `json:"confidence"`
	Metadata          storedJSON `json:"metadata"`
	SourceRunID       *string    `json:"sourceRunId"`
	SourceThreadID    *string    `json:"sourceThreadId"`
	UseCount          int        `json:"useCount"`
	LastUsedAt        *string    `json:"lastUsedAt"`
	UserID            *string    `json:"userId"`
	OriginID          *string    `json:"originId"`
	OverridesOriginID *string    `json:"overridesOriginId"`
	Suppressed        bool       `json:"suppressed"`
	PromotedFromID    *string    `json:"promotedFromId"`
	PromotedBy        *string    `json:"promotedBy"`
	PromotedAt        *string    `json:"promotedAt"`
	ArchivedAt        *string    `json:"archivedAt"`
	CreatedAt         string     `json:"createdAt"`
	UpdatedAt         string     `json:"updatedAt"`

	updatedAt time.Time
}

func (a *API) scanMemory(row pgx.Row) (memoryOut, error) {
	var m memoryOut
	var hash *string
	var lastUsed, promoted, expires, archived *time.Time
	var created time.Time
	var meta []byte
	if err := row.Scan(&m.ID, &m.OrgID, &m.Kind, &m.Scope, &m.ScopeID, &m.Text, &m.Pinned, &m.Confidence, &meta,
		&m.SourceRunID, &m.SourceThreadID, &m.UseCount, &lastUsed, &m.UserID, &m.OriginID,
		&m.OverridesOriginID, &m.Suppressed, &m.PromotedFromID, &m.PromotedBy, &promoted,
		&hash, &expires, &archived, &created, &m.updatedAt); err != nil {
		return m, err
	}
	m.Metadata = meta
	m.LastUsedAt, m.PromotedAt, m.ExpiresAt, m.ArchivedAt = jsTimePtr(lastUsed), jsTimePtr(promoted), jsTimePtr(expires), jsTimePtr(archived)
	m.CreatedAt, m.UpdatedAt = jsTime(created), jsTime(m.updatedAt)
	m.IntegrityOK = true
	if hash != nil {
		if want := a.memoryHMAC(m.OrgID, deref(m.UserID), m.Kind, m.Scope, deref(m.ScopeID), m.Text); want != nil {
			m.IntegrityOK = *want == *hash
		}
	}
	return m, nil
}

func (a *API) collectMemories(ctx context.Context, q querier, sql string, args ...any) ([]memoryOut, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []memoryOut{}
	for rows.Next() {
		m, err := a.scanMemory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (a *API) getMemory(ctx context.Context, q querier, orgID, id string) (*memoryOut, error) {
	if !isUUID(id) {
		return nil, nil
	}
	m, err := a.scanMemory(q.QueryRow(ctx, `select `+memoryColumns+` from work_memory where org_id = $1 and id = $2`, orgID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &m, err
}

// memoryHMAC is memoryIntegrityHmac(): nil when the install key is missing.
func (a *API) memoryHMAC(orgID, userID, kind, scope, scopeID, text string) *string {
	key, err := config.SigningSecret(a.SecretsDir, "work-memory:"+orgID)
	if err != nil {
		return nil
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(strings.Join([]string{userID, kind, scope, scopeID, text}, "\x00")))
	s := hex.EncodeToString(mac.Sum(nil))
	return &s
}

func memoryEvent(ctx context.Context, q querier, orgID string, memoryID, runID, threadID *string, action string, payload map[string]any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = q.Exec(ctx, `insert into work_memory_event (org_id, memory_id, run_id, thread_id, action, payload)
		values ($1, $2::uuid, $3::uuid, $4::uuid, $5, $6::jsonb)`, orgID, memoryID, runID, threadID, action, raw)
	return err
}

func jsNow() time.Time { return time.Now().UTC().Truncate(time.Millisecond) }

// memoryLayer is effectiveMemoryLayer(): a member's own layer, or "" for the team layer.
func memoryLayer(act actor) string {
	if act.Admin {
		return ""
	}
	return act.UserID
}

func teamMemoryID(m *memoryOut) string {
	if m.Scope == "database" {
		return "database:" + deref(m.ScopeID)
	}
	return "global"
}

// teamVisible is the team_memory part of the route checks in Node: a
// member sees a team row only when the row is a thread note or a group of
// theirs holds its scope.
func (a *API) teamVisible(ctx context.Context, act actor, m *memoryOut) (bool, error) {
	if act.Admin || m.UserID != nil || m.Scope == "thread" {
		return true, nil
	}
	held, err := a.heldItems(ctx, act, "team_memory")
	return held.has(teamMemoryID(m)), err
}

func (a *API) workMemories(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	act, err := a.currentActor(ctx, r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	held, err := a.heldItems(ctx, act, "team_memory")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	ids := make([]string, 0, len(held.IDs))
	for id := range held.IDs {
		ids = append(ids, id)
	}
	team := `($4 or m.scope = 'thread'
		or (m.scope = 'global' and 'global' = any($5::text[]))
		or (m.scope = 'database' and ('database:' || coalesce(m.scope_id, '')) = any($5::text[])))`
	memories, err := a.collectMemories(ctx, a.DB, `select `+memoryColumns+` from work_memory m
		where m.org_id = $1 and ($3 or m.archived_at is null) and case when $2 = ''
		  then m.user_id is null and `+team+`
		  else (m.user_id = $2 and m.suppressed = false)
		    or (m.user_id is null and `+team+` and not exists (
		      select 1 from work_memory p where p.org_id = m.org_id and p.user_id = $2
		        and p.overrides_origin_id = m.origin_id and p.archived_at is null))
		  end
		order by m.pinned desc, m.updated_at desc
		limit 200`,
		act.OrgID, memoryLayer(act), r.URL.Query().Get("includeArchived") == "true", held.All, ids)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	sendJSON(w, http.StatusOK, map[string]any{"memories": memories})
}

func (a *API) workMemory(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	act, err := a.currentActor(ctx, r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	m, err := a.getMemory(ctx, a.DB, act.OrgID, r.PathValue("id"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if m == nil || (!act.Admin && m.UserID != nil && *m.UserID != act.UserID) {
		sendError(w, http.StatusNotFound, "not found")
		return
	}
	if ok, err := a.teamVisible(ctx, act, m); err != nil || !ok {
		if err != nil {
			a.fail(w, r, err)
		} else {
			sendError(w, http.StatusNotFound, "not found")
		}
		return
	}
	sendJSON(w, http.StatusOK, map[string]any{"memory": m})
}

func (a *API) personalizeMemory(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	act, err := a.currentActor(ctx, r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if act.Admin || act.UserID == "" {
		sendError(w, http.StatusForbidden, "personal overrides are for members; admins edit the team layer")
		return
	}
	body, _ := readBody(r)
	textValue, _ := field(body, "text")
	text, _ := textValue.(string)
	suppressValue, _ := field(body, "suppress")
	suppress := suppressValue == true
	if text == "" && !suppress {
		sendError(w, http.StatusBadRequest, "provide text or suppress")
		return
	}
	id := r.PathValue("id")
	existing, err := a.getMemory(ctx, a.DB, act.OrgID, id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if existing != nil {
		if ok, err := a.teamVisible(ctx, act, existing); err != nil || !ok {
			if err != nil {
				a.fail(w, r, err)
			} else {
				sendError(w, http.StatusNotFound, "not found")
			}
			return
		}
	}
	var m *memoryOut
	err = pgx.BeginFunc(ctx, a.DB, func(tx pgx.Tx) error {
		m, err = a.overrideMemory(ctx, tx, act.OrgID, act.UserID, id, text, suppress, nil, nil)
		return err
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if m == nil {
		sendError(w, http.StatusNotFound, "not found")
		return
	}
	sendJSON(w, http.StatusOK, map[string]any{"memory": m})
}

// overrideMemory is overrideWorkMemoryForUser(). A nil memory means the
// target is gone, archived or in another member's layer.
func (a *API) overrideMemory(ctx context.Context, tx pgx.Tx, orgID, userID, id, text string, suppress bool, runID, threadID *string) (*memoryOut, error) {
	target, err := a.getMemory(ctx, tx, orgID, id)
	if err != nil || target == nil || target.ArchivedAt != nil || (target.UserID != nil && *target.UserID != userID) {
		return nil, err
	}
	newText := strings.TrimSpace(text)
	if newText == "" {
		newText = target.Text
	}
	hash := a.memoryHMAC(orgID, userID, target.Kind, target.Scope, deref(target.ScopeID), newText)
	now := jsNow()
	action := "override"
	if suppress {
		action = "suppress"
	}
	if target.UserID != nil {
		m, err := a.scanMemory(tx.QueryRow(ctx, `update work_memory set text = $3, suppressed = $4, integrity_hmac = $5,
			embedding = case when $6 then null else embedding end, updated_at = $7
			where org_id = $1 and id = $2 returning `+memoryColumns,
			orgID, target.ID, newText, suppress, hash, text != "", now))
		if err != nil {
			return nil, err
		}
		return &m, memoryEvent(ctx, tx, orgID, &m.ID, runID, threadID, action, map[string]any{"userId": userID, "originId": m.OriginID})
	}
	if _, err := tx.Exec(ctx, `insert into memory_fork (org_id, user_id, baseline_at) values ($1, $2, $3)
		on conflict do nothing`, orgID, userID, now); err != nil {
		return nil, err
	}
	origin := target.ID
	if target.OriginID != nil {
		origin = *target.OriginID
	}
	var existingID string
	err = tx.QueryRow(ctx, `select id::text from work_memory where org_id = $1 and user_id = $2
		and overrides_origin_id = $3 and archived_at is null limit 1`, orgID, userID, origin).Scan(&existingID)
	var m memoryOut
	switch {
	case err == nil:
		m, err = a.scanMemory(tx.QueryRow(ctx, `update work_memory set text = $2, suppressed = $3, integrity_hmac = $4,
			embedding = case when $5 then null else embedding end, updated_at = $6
			where id = $1 returning `+memoryColumns, existingID, newText, suppress, hash, text != "", now))
	case errors.Is(err, pgx.ErrNoRows):
		m, err = a.scanMemory(tx.QueryRow(ctx, `insert into work_memory (id, org_id, kind, scope, scope_id, text, pinned,
			  confidence, metadata, user_id, origin_id, overrides_origin_id, suppressed, integrity_hmac, created_at, updated_at)
			select g.id, s.org_id, s.kind, s.scope, s.scope_id, $3, s.pinned, s.confidence,
			  case when jsonb_typeof(s.metadata) = 'object' then s.metadata else '{}'::jsonb end || '{"origin":"user_override"}',
			  $4, g.id, $5, $6, $7, $8, $8
			from work_memory s, (select gen_random_uuid() as id) g
			where s.org_id = $1 and s.id = $2
			returning `+memoryColumns, orgID, target.ID, newText, userID, origin, suppress, hash, now))
	}
	if err != nil {
		return nil, err
	}
	return &m, memoryEvent(ctx, tx, orgID, &m.ID, runID, threadID, action, map[string]any{"userId": userID, "overridesOriginId": m.OverridesOriginID})
}

func (a *API) forgetMemory(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	body, _ := readBody(r)
	var reason any
	if v, _ := field(body, "reason"); v != nil {
		if s, ok := v.(string); ok {
			reason = s
		}
	}
	act, err := a.currentActor(ctx, r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	id := r.PathValue("id")
	if !isUUID(id) || (!act.Admin && act.UserID == "") {
		sendError(w, http.StatusNotFound, "not found or already archived")
		return
	}
	target, err := a.getMemory(ctx, a.DB, act.OrgID, id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if !act.Admin && target != nil && target.UserID != nil && *target.UserID != act.UserID {
		sendError(w, http.StatusNotFound, "not found")
		return
	}
	if !act.Admin {
		if target == nil || target.ArchivedAt != nil {
			sendError(w, http.StatusNotFound, "not found or already archived")
			return
		}
		if target.UserID == nil {
			var m *memoryOut
			err := pgx.BeginFunc(ctx, a.DB, func(tx pgx.Tx) error {
				m, err = a.overrideMemory(ctx, tx, act.OrgID, act.UserID, id, "", true, nil, nil)
				return err
			})
			if err != nil {
				a.fail(w, r, err)
			} else if m == nil {
				sendError(w, http.StatusNotFound, "not found")
			} else {
				sendJSON(w, http.StatusOK, struct {
					OK         bool `json:"ok"`
					Suppressed bool `json:"suppressed"`
				}{true, true})
			}
			return
		}
	}
	archived := false
	err = pgx.BeginFunc(ctx, a.DB, func(tx pgx.Tx) error {
		now := jsNow()
		tag, err := tx.Exec(ctx, `update work_memory set archived_at = $3, updated_at = $3
			where org_id = $1 and id = $2::uuid and archived_at is null and ($4 = '' or user_id = $4)`,
			act.OrgID, id, now, memoryLayer(act))
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		archived = true
		return memoryEvent(ctx, tx, act.OrgID, &id, nil, nil, "forget", map[string]any{"reason": reason})
	})
	switch {
	case err == nil && !archived:
		sendError(w, http.StatusNotFound, "not found or already archived")
	case err != nil:
		a.fail(w, r, err)
	default:
		sendJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

type promoteError struct{ message string }

func (e *promoteError) Error() string { return e.message }

func (a *API) promoteMemory(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	act, err := a.currentActor(ctx, r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if !act.Admin {
		sendError(w, http.StatusForbidden, "admin only")
		return
	}
	id := r.PathValue("id")
	promotedBy := act.UserID
	if promotedBy == "" {
		promotedBy = "admin"
	}
	var m memoryOut
	var sourceUser string
	err = pgx.BeginFunc(ctx, a.DB, func(tx pgx.Tx) error {
		source, err := a.getMemory(ctx, tx, act.OrgID, id)
		if err != nil {
			return err
		}
		if source == nil {
			return &promoteError{"not found"}
		}
		if source.ArchivedAt != nil || source.UserID == nil {
			return &promoteError{"Personal memory not found: " + id}
		}
		if source.Suppressed {
			return &promoteError{"Cannot promote a suppressed memory: " + id}
		}
		sourceUser = *source.UserID
		now := jsNow()
		origin := source.ID
		if source.OverridesOriginID != nil {
			origin = *source.OverridesOriginID
			if _, err := tx.Exec(ctx, `update work_memory set archived_at = $3, updated_at = $3
				where org_id = $1 and origin_id = $2 and user_id is null and archived_at is null`, act.OrgID, origin, now); err != nil {
				return err
			}
		} else if source.OriginID != nil {
			origin = *source.OriginID
		}
		hash := a.memoryHMAC(act.OrgID, "", source.Kind, source.Scope, deref(source.ScopeID), source.Text)
		m, err = a.scanMemory(tx.QueryRow(ctx, `insert into work_memory (org_id, kind, scope, scope_id, text, pinned,
			  confidence, metadata, origin_id, promoted_from_id, promoted_by, promoted_at, integrity_hmac, created_at, updated_at)
			select org_id, kind, scope, scope_id, text, pinned, confidence,
			  case when jsonb_typeof(metadata) = 'object' then metadata else '{}'::jsonb end || '{"origin":"promoted"}',
			  $3, id, $4, $5, $6, $5, $5
			from work_memory where org_id = $1 and id = $2
			returning `+memoryColumns, act.OrgID, source.ID, origin, promotedBy, now, hash))
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update work_memory set archived_at = $3, updated_at = $3 where org_id = $1 and id = $2`,
			act.OrgID, source.ID, now); err != nil {
			return err
		}
		return memoryEvent(ctx, tx, act.OrgID, &m.ID, nil, nil, "promote",
			map[string]any{"promotedFromId": source.ID, "promotedBy": promotedBy, "originId": origin})
	})
	var refused *promoteError
	if errors.As(err, &refused) {
		sendError(w, http.StatusNotFound, refused.message)
		return
	}
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if _, err := a.DB.Exec(ctx, `insert into config_change (org_id, artifact_kind, artifact_ref, scope, user_id, actor_user_id, summary, status)
		values ($1, 'memory', $2, 'team', $3, $4, 'Promoted a personal memory to the team', 'promoted')`,
		act.OrgID, m.ID, sourceUser, promotedBy); err != nil {
		log.Printf("[config-vcs] config_change insert failed: %v", err)
	}
	sendJSON(w, http.StatusOK, map[string]any{"memory": m})
}

// workThreadOwned is getAuthorizedWorkThread(): web Ask history is personal
// for every role.
func (a *API) workThreadOwned(ctx context.Context, act actor, threadID string) (bool, error) {
	if act.UserID == "" || !isUUID(threadID) {
		return false, nil
	}
	var owned bool
	err := a.DB.QueryRow(ctx, `select coalesce(created_by_user_id = $3, false) from work_thread where org_id = $1 and id = $2`,
		act.OrgID, threadID, act.UserID).Scan(&owned)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return owned, err
}

func (a *API) workRunOwned(ctx context.Context, act actor, runID string) (bool, error) {
	if !isUUID(runID) {
		return false, nil
	}
	var threadID string
	err := a.DB.QueryRow(ctx, `select thread_id::text from work_run where org_id = $1 and id = $2`, act.OrgID, runID).Scan(&threadID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return a.workThreadOwned(ctx, act, threadID)
}

const pendingColumns = `id::text, org_id, thread_id::text, run_id::text, status, draft_text, draft_kind, draft_scope,
	draft_scope_id, confidence, reasoning, case when jsonb_typeof(conflict) = 'array' then conflict else '[]'::jsonb end,
	decision_text, decided_at, memory_id::text, created_at, updated_at`

// pendingOut is rowToPending() in packages/llm/src/work/memory.ts.
type pendingOut struct {
	ID           string     `json:"id"`
	OrgID        string     `json:"orgId"`
	ThreadID     *string    `json:"threadId"`
	RunID        *string    `json:"runId"`
	Status       string     `json:"status"`
	DraftText    string     `json:"draftText"`
	DraftKind    string     `json:"draftKind"`
	DraftScope   string     `json:"draftScope"`
	DraftScopeID *string    `json:"draftScopeId"`
	Confidence   float32    `json:"confidence"`
	Reasoning    *string    `json:"reasoning"`
	Conflicts    storedJSON `json:"conflicts"`
	DecisionText *string    `json:"decisionText"`
	DecidedAt    *string    `json:"decidedAt"`
	MemoryID     *string    `json:"memoryId"`
	CreatedAt    string     `json:"createdAt"`
	UpdatedAt    string     `json:"updatedAt"`
}

func scanPending(row pgx.Row) (pendingOut, error) {
	var p pendingOut
	var conflicts []byte
	var decided *time.Time
	var created, updated time.Time
	err := row.Scan(&p.ID, &p.OrgID, &p.ThreadID, &p.RunID, &p.Status, &p.DraftText, &p.DraftKind, &p.DraftScope,
		&p.DraftScopeID, &p.Confidence, &p.Reasoning, &conflicts, &p.DecisionText, &decided, &p.MemoryID, &created, &updated)
	p.Conflicts, p.DecidedAt, p.CreatedAt, p.UpdatedAt = conflicts, jsTimePtr(decided), jsTime(created), jsTime(updated)
	return p, err
}

func getPending(ctx context.Context, q querier, orgID, id string, lock bool) (*pendingOut, error) {
	if !isUUID(id) {
		return nil, nil
	}
	sql := `select ` + pendingColumns + ` from work_pending_memory where org_id = $1 and id = $2`
	if lock {
		sql += ` for update`
	}
	p, err := scanPending(q.QueryRow(ctx, sql, orgID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &p, err
}

func (a *API) pendingMemories(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()
	threadID, runID := q.Get("threadId"), q.Get("runId")
	act, err := a.currentActor(ctx, r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	for _, check := range []struct {
		id    string
		owned func(context.Context, actor, string) (bool, error)
	}{{threadID, a.workThreadOwned}, {runID, a.workRunOwned}} {
		if check.id == "" {
			continue
		}
		if ok, err := check.owned(ctx, act, check.id); err != nil || !ok {
			if err != nil {
				a.fail(w, r, err)
			} else {
				sendError(w, http.StatusNotFound, "not found")
			}
			return
		}
	}
	var filter string
	var arg any
	switch {
	case runID != "":
		filter, arg = `p.run_id = $2::uuid`, runID
	case threadID != "":
		filter, arg = `p.thread_id = $2::uuid`, threadID
	default:
		filter, arg = `p.thread_id in (select t.id from work_thread t
			where t.org_id = p.org_id and t.channel = 'web' and t.created_by_user_id is not distinct from nullif($2, '')
			  and not exists (select 1 from workflow_run wr where wr.thread_id = t.id))`, act.UserID
	}
	from := `work_pending_memory p where p.org_id = $1 and p.status = 'proposed' and ` + filter
	if runID == "" && threadID == "" {
		from = `(select * from work_pending_memory where org_id = $1 and status = 'proposed' order by created_at asc limit 200) p
			where ` + filter
	}
	rows, err := a.DB.Query(ctx, `select `+pendingColumns+` from `+from+` order by p.created_at asc limit 200`, act.OrgID, arg)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	out := []pendingOut{}
	for rows.Next() {
		p, err := scanPending(rows)
		if err != nil {
			rows.Close()
			a.fail(w, r, err)
			return
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		a.fail(w, r, err)
		return
	}
	sendJSON(w, http.StatusOK, map[string]any{"pending": out})
}

type decideOut struct {
	OK      bool        `json:"ok"`
	Pending *pendingOut `json:"pending"`
	Memory  *memoryOut  `json:"memory,omitempty"`
}

type pendingError struct{ message string }

func (e *pendingError) Error() string { return e.message }

func (a *API) decidePendingMemory(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	body, _ := readBody(r)
	idValue, _ := field(body, "id")
	id, _ := idValue.(string)
	if id == "" {
		sendError(w, http.StatusBadRequest, "id is required")
		return
	}
	act, err := a.currentActor(ctx, r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	pending, err := getPending(ctx, a.DB, act.OrgID, id, false)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	authorized := false
	switch {
	case pending == nil:
	case pending.ThreadID != nil:
		authorized, err = a.workThreadOwned(ctx, act, *pending.ThreadID)
	case pending.RunID != nil:
		authorized, err = a.workRunOwned(ctx, act, *pending.RunID)
	}
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if !authorized {
		sendError(w, http.StatusNotFound, "not found")
		return
	}
	action, _ := field(body, "action")
	if action != "accept" && action != "decline" {
		sendError(w, http.StatusBadRequest, "action must be accept or decline")
		return
	}
	str := func(name string) *string {
		v, _ := field(body, name)
		if s, ok := v.(string); ok {
			return &s
		}
		return nil
	}
	var out decideOut
	err = pgx.BeginFunc(ctx, a.DB, func(tx pgx.Tx) error {
		p, err := getPending(ctx, tx, act.OrgID, id, true)
		if err != nil {
			return err
		}
		if p == nil {
			return &pendingError{"Pending memory not found: " + id}
		}
		if p.Status != "proposed" {
			return &pendingError{"Pending memory " + id + " already " + p.Status}
		}
		now := jsNow()
		if action == "decline" {
			if _, err := tx.Exec(ctx, `update work_pending_memory set status = 'declined', decision_text = $3, decided_at = $4, updated_at = $4
				where org_id = $1 and id = $2`, act.OrgID, id, str("reason"), now); err != nil {
				return err
			}
			out.Pending, err = getPending(ctx, tx, act.OrgID, id, false)
			return err
		}
		var pinned *bool
		if v, _ := field(body, "pinned"); v != nil {
			if b, ok := v.(bool); ok {
				pinned = &b
			}
		}
		scope := p.DraftScope
		if s := str("scope"); s != nil && slices.Contains(memoryScopes, *s) {
			scope = *s
		}
		var decision *string
		text := p.DraftText
		if t := str("text"); t != nil {
			trimmed := strings.TrimSpace(*t)
			decision = &trimmed
			if trimmed != "" {
				text = trimmed
			}
		}
		scopeID := p.DraftScopeID
		if s := str("scopeId"); s != nil {
			scopeID = s
		}
		m, err := a.rememberMemory(ctx, tx, act.OrgID, p, text, scope, scopeID, pinned, now)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update work_pending_memory set status = 'accepted', decision_text = $3, decided_at = $4,
			memory_id = $5, updated_at = $4 where org_id = $1 and id = $2`, act.OrgID, id, decision, now, m.ID); err != nil {
			return err
		}
		out.Memory = m
		out.Pending, err = getPending(ctx, tx, act.OrgID, id, false)
		return err
	})
	var refused *pendingError
	if errors.As(err, &refused) {
		sendError(w, http.StatusBadRequest, refused.message)
		return
	}
	if err != nil {
		a.fail(w, r, err)
		return
	}
	out.OK = true
	sendJSON(w, http.StatusOK, out)
}

// rememberMemory is rememberWorkMemory() as acceptPendingWorkMemory() calls it.
func (a *API) rememberMemory(ctx context.Context, tx pgx.Tx, orgID string, p *pendingOut, text, scope string, scopeID *string, pinned *bool, now time.Time) (*memoryOut, error) {
	if !slices.Contains(memoryKinds, p.DraftKind) {
		return nil, &pendingError{"Invalid work memory kind: " + p.DraftKind}
	}
	if scope != "thread" || p.ThreadID == nil {
		scope = "global"
	}
	var resolvedScope *string
	switch {
	case scope == "global":
	case scopeID != nil && *scopeID != "":
		resolvedScope = scopeID
	default:
		resolvedScope = p.ThreadID
	}
	pin := scope != "thread" && (p.DraftKind == "preference" || p.DraftKind == "business_rule" || p.DraftKind == "metric_definition")
	if pinned != nil {
		pin = *pinned
	}
	confidence := min(max(p.Confidence, 0), 1)
	var layer *string
	if p.RunID != nil {
		if err := tx.QueryRow(ctx, `select case when actor_role = 'member' and coalesce(actor_user_id, '') <> '' then actor_user_id end
			from work_run where id = $1`, *p.RunID).Scan(&layer); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
	}
	meta, _ := json.Marshal(map[string]string{"origin": "auto_memory_accepted", "pendingId": p.ID})
	return a.insertMemory(ctx, tx, orgID, memoryDraft{Kind: p.DraftKind, Text: text, Scope: scope, ScopeID: resolvedScope,
		Pinned: pin, Confidence: confidence, Metadata: meta, RunID: p.RunID, ThreadID: p.ThreadID, Layer: layer}, now)
}

type memoryDraft struct {
	Kind, Text, Scope      string
	ScopeID                *string
	Pinned                 bool
	Confidence             float32
	Metadata               []byte
	RunID, ThreadID, Layer *string
}

// insertMemory is the insert and the "remember" event of rememberWorkMemory().
func (a *API) insertMemory(ctx context.Context, q querier, orgID string, d memoryDraft, now time.Time) (*memoryOut, error) {
	var expires *time.Time
	if d.Kind == "thread_note" {
		e := now.Add(30 * 24 * time.Hour)
		expires = &e
	}
	text := strings.TrimSpace(d.Text)
	hash := a.memoryHMAC(orgID, deref(d.Layer), d.Kind, d.Scope, deref(d.ScopeID), text)
	m, err := a.scanMemory(q.QueryRow(ctx, `insert into work_memory (id, org_id, kind, scope, scope_id, text, pinned, confidence,
		  metadata, source_run_id, source_thread_id, user_id, origin_id, integrity_hmac, expires_at, created_at, updated_at)
		select g.id, $1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9::uuid, $10::uuid, $11, g.id, $12, $13, $14, $14
		from (select gen_random_uuid() as id) g
		returning `+memoryColumns,
		orgID, d.Kind, d.Scope, d.ScopeID, text, d.Pinned, d.Confidence, d.Metadata, d.RunID, d.ThreadID, d.Layer, hash, expires, now))
	if err != nil {
		return nil, err
	}
	return &m, memoryEvent(ctx, q, orgID, &m.ID, d.RunID, d.ThreadID, "remember", map[string]any{
		"kind": d.Kind, "scope": d.Scope, "scopeId": d.ScopeID, "pinned": d.Pinned, "userId": d.Layer, "text": text,
	})
}

func (a *API) holdsGlobalTeamMemory(ctx context.Context, act actor) (bool, error) {
	if act.Admin || act.UserID == "" {
		return false, nil
	}
	held, err := a.heldItems(ctx, act, "team_memory")
	return held.has("global"), err
}

type pullUpdate struct {
	OriginID    string     `json:"originId"`
	Override    memoryOut  `json:"override"`
	TeamMemory  *memoryOut `json:"teamMemory"`
	TeamRemoved bool       `json:"teamRemoved"`
}

func (a *API) memoryPullUpdates(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	act, err := a.currentActor(ctx, r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	updates := []pullUpdate{}
	if ok, err := a.holdsGlobalTeamMemory(ctx, act); err != nil || !ok {
		if err != nil {
			a.fail(w, r, err)
		} else {
			sendJSON(w, http.StatusOK, map[string]any{"updates": updates})
		}
		return
	}
	var baseline time.Time
	err = a.DB.QueryRow(ctx, `select baseline_at from memory_fork where org_id = $1 and user_id = $2`, act.OrgID, act.UserID).Scan(&baseline)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		a.fail(w, r, err)
		return
	}
	baseline = baseline.Truncate(time.Millisecond)
	overrides, err := a.collectMemories(ctx, a.DB, `select `+memoryColumns+` from work_memory
		where org_id = $1 and user_id = $2 and overrides_origin_id is not null and archived_at is null`, act.OrgID, act.UserID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	for _, o := range overrides {
		team, err := a.collectMemories(ctx, a.DB, `select `+memoryColumns+` from work_memory
			where org_id = $1 and origin_id = $2 and user_id is null and archived_at is null limit 1`, act.OrgID, *o.OverridesOriginID)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		switch {
		case len(team) == 0:
			updates = append(updates, pullUpdate{OriginID: *o.OverridesOriginID, Override: o, TeamRemoved: true})
		case team[0].updatedAt.Truncate(time.Millisecond).After(baseline):
			updates = append(updates, pullUpdate{OriginID: *o.OverridesOriginID, Override: o, TeamMemory: &team[0]})
		}
	}
	sendJSON(w, http.StatusOK, map[string]any{"updates": updates})
}

func (a *API) applyMemoryPull(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	act, err := a.currentActor(ctx, r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if ok, err := a.holdsGlobalTeamMemory(ctx, act); err != nil || !ok {
		if err != nil {
			a.fail(w, r, err)
		} else {
			sendError(w, http.StatusForbidden, "members only")
		}
		return
	}
	body, _ := readBody(r)
	list, _ := field(body, "decisions")
	decisions, _ := list.([]any)
	applied := 0
	err = pgx.BeginFunc(ctx, a.DB, func(tx pgx.Tx) error {
		now := jsNow()
		for _, d := range decisions {
			origin, _ := field(d, "originId")
			choice, _ := field(d, "choice")
			originID, ok := origin.(string)
			if !ok || choice != "take_theirs" || !isUUID(originID) {
				continue
			}
			rows, err := tx.Query(ctx, `update work_memory set archived_at = $4, updated_at = $4
				where org_id = $1 and user_id = $2 and overrides_origin_id = $3 and archived_at is null returning id::text`,
				act.OrgID, act.UserID, originID, now)
			if err != nil {
				return err
			}
			ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
			if err != nil {
				return err
			}
			for _, id := range ids {
				applied++
				if err := memoryEvent(ctx, tx, act.OrgID, &id, nil, nil, "pull_take_theirs",
					map[string]any{"originId": originID, "userId": act.UserID}); err != nil {
					return err
				}
			}
		}
		_, err := tx.Exec(ctx, `insert into memory_fork (org_id, user_id, baseline_at) values ($1, $2, $3)
			on conflict (org_id, user_id) do update set baseline_at = excluded.baseline_at, updated_at = $3`,
			act.OrgID, act.UserID, now)
		return err
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	sendJSON(w, http.StatusOK, struct {
		OK      bool `json:"ok"`
		Applied int  `json:"applied"`
	}{true, applied})
}
