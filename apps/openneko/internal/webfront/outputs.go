package webfront

import (
	"encoding/json"
	"net/http"
	"time"
)

// outputs is GET /api/v1/outputs: the recent outputs of the workflows that
// the caller can see, each with the rows it is about. A view such as the
// world builder shows an output on those objects.
func (a *API) outputs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	act, err := a.currentActor(ctx, r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	type outputOut struct {
		ID         string          `json:"id"`
		WorkflowID *string         `json:"workflowId"`
		Kind       string          `json:"kind"`
		Title      string          `json:"title"`
		Body       string          `json:"body"`
		Scope      *string         `json:"scope"`
		Topic      *string         `json:"topic"`
		Mood       *string         `json:"mood"`
		Subjects   []outputSubject `json:"subjects"`
		SeenCount  int             `json:"seenCount"`
		LastSeenAt string          `json:"lastSeenAt"`
		CreatedAt  string          `json:"createdAt"`
	}
	q := r.URL.Query()
	mood := q.Get("mood")
	var since *time.Time
	if text := q.Get("since"); text != "" {
		at, err := time.Parse(time.RFC3339, text)
		if err != nil {
			sendError(w, http.StatusBadRequest, "since must be a time such as 2026-11-25T10:00:00Z")
			return
		}
		since = &at
	}
	rows, err := a.DB.Query(ctx, `
		select o.id::text, r.workflow_id::text, o.kind, o.title, o.body, o.scope, o.topic, o.mood,
		  coalesce(o.payload->'subjects', '[]'::jsonb), o.seen_count, o.last_seen_at, o.created_at
		from workflow_output o
		left join workflow_run r on r.id = o.workflow_run_id
		where o.org_id = $1
		  and ($2 = '' or o.mood = $2)
		  and ($3::timestamptz is null or o.last_seen_at > $3)
		order by o.last_seen_at desc
		limit $4`, act.OrgID, mood, since, queryLimit(r, 50))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	out := []outputOut{}
	for rows.Next() {
		var o outputOut
		var subjects []byte
		var last, created time.Time
		if err := rows.Scan(&o.ID, &o.WorkflowID, &o.Kind, &o.Title, &o.Body, &o.Scope, &o.Topic, &o.Mood, &subjects, &o.SeenCount, &last, &created); err != nil {
			rows.Close()
			a.fail(w, r, err)
			return
		}
		o.Subjects = []outputSubject{}
		var list []outputSubject
		if json.Unmarshal(subjects, &list) == nil {
			for _, subject := range list {
				if subject.Table != "" && subject.Key != "" {
					o.Subjects = append(o.Subjects, subject)
				}
			}
		}
		o.LastSeenAt, o.CreatedAt = jsTime(last), jsTime(created)
		out = append(out, o)
	}
	if err := rows.Err(); err != nil {
		a.fail(w, r, err)
		return
	}
	visible, err := a.workflowVisibility(ctx, act)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	owners, err := a.workflowOwners(ctx, act.OrgID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	shown := out[:0]
	for _, o := range out {
		if o.WorkflowID != nil && visible(*o.WorkflowID, owners[*o.WorkflowID]) {
			shown = append(shown, o)
		}
	}
	sendJSON(w, http.StatusOK, map[string]any{"outputs": shown})
}
