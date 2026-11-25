package webfront

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/open-neko/neko/apps/openneko/internal/llm"
	"github.com/open-neko/openneko/apps/ax-harness/inference"
)

// modelDecide answers one closed question on the route of the Decisions task.
// The world builder and other programs outside OpenNeko call it, so the
// model, the key and the spend stay in OpenNeko.
func (a *API) modelDecide(w http.ResponseWriter, r *http.Request) {
	body, _ := readBody(r)
	act, err := a.currentActor(r.Context(), r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	signature, _ := fieldString(body, "signature")
	instruction, _ := fieldString(body, "instruction")
	values, _ := field(body, "values")
	inputs, ok := values.(map[string]any)
	if signature == "" || !ok {
		sendError(w, http.StatusBadRequest, "Send a signature and a values object.")
		return
	}
	if err := inference.CheckDecisionSignature(signature); err != nil {
		sendError(w, http.StatusBadRequest, err.Error())
		return
	}
	res, err := a.hostCalls().Decide(r.Context(), act.OrgID, llm.Program{Signature: signature, Instruction: instruction}, inputs)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	sendJSON(w, http.StatusOK, map[string]any{"outputs": res.Outputs, "provider": res.Provider, "model": res.Model, "tokens": res.Usage.Total})
}

var worldRecordName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// worldRecordOwner gives the user_id of the record that the request names:
// the caller's own record with ?scope=mine, else the organization's.
func worldRecordOwner(r *http.Request, act actor) string {
	if r.URL.Query().Get("scope") == "mine" {
		return act.UserID
	}
	return ""
}

func (a *API) worldRecord(w http.ResponseWriter, r *http.Request) {
	act, err := a.currentActor(r.Context(), r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	name := r.PathValue("name")
	if !worldRecordName.MatchString(name) {
		sendError(w, http.StatusNotFound, "No such record.")
		return
	}
	var value json.RawMessage
	var updated time.Time
	err = a.DB.QueryRow(r.Context(), `select value, updated_at from world_record where org_id = $1 and user_id = $2 and name = $3`,
		act.OrgID, worldRecordOwner(r, act), name).Scan(&value, &updated)
	if errors.Is(err, pgx.ErrNoRows) {
		sendError(w, http.StatusNotFound, "No such record.")
		return
	}
	if err != nil {
		a.fail(w, r, err)
		return
	}
	sendJSON(w, http.StatusOK, map[string]any{"name": name, "value": value, "updatedAt": updated.UTC().Format(time.RFC3339)})
}

func (a *API) worldRecordSave(w http.ResponseWriter, r *http.Request) {
	body, _ := readBody(r)
	act, err := a.currentActor(r.Context(), r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	name := r.PathValue("name")
	value, ok := field(body, "value")
	if !worldRecordName.MatchString(name) || !ok || value == nil {
		sendError(w, http.StatusBadRequest, "Send a value for a record with a short lower-case name.")
		return
	}
	owner := worldRecordOwner(r, act)
	if owner == "" && !act.Admin {
		sendError(w, http.StatusForbidden, "Only an admin can change a record of the organization.")
		return
	}
	raw, err := json.Marshal(value)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if _, err := a.DB.Exec(r.Context(), `insert into world_record (org_id, user_id, name, value) values ($1, $2, $3, $4::jsonb)
		on conflict (org_id, user_id, name) do update set value = excluded.value, updated_at = now()`, act.OrgID, owner, name, string(raw)); err != nil {
		a.fail(w, r, err)
		return
	}
	sendJSON(w, http.StatusOK, map[string]any{"ok": true})
}
