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
)

const unconfinedSetting = "agent_turns_unconfined"

// unconfinedTurns is the install setting that lets agent turns run without
// OS confinement on a system that cannot confine them.
type unconfinedTurns struct {
	On     bool   `json:"on"`
	Host   string `json:"host,omitempty"`
	Kernel string `json:"kernel,omitempty"`
	Reason string `json:"reason,omitempty"`
	By     string `json:"by,omitempty"`
	At     string `json:"at,omitempty"`
}

func (a *API) unconfinedTurnsSetting(ctx context.Context) (unconfinedTurns, error) {
	var out unconfinedTurns
	var raw []byte
	var by *string
	var at time.Time
	err := a.DB.QueryRow(ctx, `select s.value, u.email, s.updated_at from install_setting s
		left join app_user u on u.id = s.updated_by_user_id where s.name = $1`, unconfinedSetting).Scan(&raw, &by, &at)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return unconfinedTurns{}, fmt.Errorf("install setting %s: %w", unconfinedSetting, err)
	}
	if by != nil {
		out.By = *by
	}
	out.At = at.UTC().Format(time.RFC3339)
	return out, nil
}

// line is what the status page, doctor and release-check show while the
// setting is on.
func (u unconfinedTurns) line() string {
	line := "agent turns run WITHOUT OS confinement: an admin turned this on"
	if u.By != "" {
		line += " (" + u.By + ", " + u.At + ")"
	}
	return line + ". The agent can read the files and reach the network of the OpenNeko account"
}

// unconfinedOffer tells the admin form whether the setting can be turned on
// here: only where serve reports that agent turns are off because the
// system cannot confine them.
func (a *API) unconfinedOffer(ctx context.Context) map[string]any {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	st, err := serveConfinement(ctx, a.serveSocket())
	switch {
	case a.serveSocket() == "":
		return map[string]any{"available": false, "blocked": "Agent turns are off: OPENNEKO_SERVE_SOCKET is not set."}
	case err != nil:
		return map[string]any{"available": false, "blocked": err.Error()}
	case st.turnsOff == "":
		return map[string]any{"available": false, "blocked": "This system confines agent turns, so this setting stays off."}
	}
	return map[string]any{"available": true, "host": st.hostName, "kernel": st.kernel, "reason": st.turnsOff}
}

func (a *API) settingsAgentUnconfinedGet(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.settingsAdmin(w, r); !ok {
		return
	}
	setting, err := a.unconfinedTurnsSetting(r.Context())
	if err != nil {
		a.fail(w, r, err)
		return
	}
	sendJSON(w, http.StatusOK, map[string]any{"setting": setting, "system": a.unconfinedOffer(r.Context())})
}

// settingsAgentUnconfinedPut turns the setting on or off. Turning it on
// needs a signed-in admin, a system that cannot confine agent turns, and the
// host name typed back.
func (a *API) settingsAgentUnconfinedPut(w http.ResponseWriter, r *http.Request) {
	act, ok := a.settingsAdmin(w, r)
	if !ok {
		return
	}
	if act.UserID == "" || act.PackID != "" {
		sendError(w, http.StatusForbidden, "Only a signed-in admin can change this setting.")
		return
	}
	raw, _ := readBody(r)
	body := objectFields(raw)
	on, isBool := body["on"].(bool)
	if !isBool {
		sendError(w, http.StatusBadRequest, "on must be true or false")
		return
	}
	ctx := r.Context()
	current, err := a.unconfinedTurnsSetting(ctx)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	next := unconfinedTurns{}
	if on {
		offer := a.unconfinedOffer(ctx)
		if offer["available"] != true {
			sendError(w, http.StatusConflict, fmt.Sprint(offer["blocked"]))
			return
		}
		host, _ := offer["host"].(string)
		confirm, _ := body["confirm"].(string)
		if host == "" || strings.TrimSpace(confirm) != host {
			sendError(w, http.StatusBadRequest, fmt.Sprintf("Type the host name %q to confirm.", host))
			return
		}
		next = unconfinedTurns{On: true, Host: host, Reason: fmt.Sprint(offer["reason"])}
		next.Kernel, _ = offer["kernel"].(string)
	} else {
		next.Host, next.Kernel, next.Reason = current.Host, current.Kernel, current.Reason
	}
	value, _ := json.Marshal(next)
	if _, err := a.DB.Exec(ctx, `insert into install_setting (name, value, updated_by_user_id, updated_at) values ($1, $2, $3, now())
		on conflict (name) do update set value = excluded.value, updated_by_user_id = excluded.updated_by_user_id, updated_at = now()`,
		unconfinedSetting, value, act.UserID); err != nil {
		a.fail(w, r, err)
		return
	}
	if on != current.On {
		event := "agent.unconfined_turns.off"
		if on {
			event = "agent.unconfined_turns.on"
		}
		log.Printf("[agent] %s by user %s on host %s: %s", event, act.UserID, next.Host, next.Reason)
		payload := map[string]any{"actorUserId": act.UserID, "host": next.Host, "kernel": next.Kernel, "reason": next.Reason}
		if err := a.auditEveryOrg(ctx, auditEvent{EntityKind: "install_setting", EntityID: unconfinedSetting, Event: event, Payload: payload}); err != nil {
			a.fail(w, r, err)
			return
		}
		servicesMu.Lock()
		clear(servicesCache)
		servicesMu.Unlock()
	}
	a.settingsAgentUnconfinedGet(w, r)
}

// auditEveryOrg appends an install event to the chain of each organization,
// since the setting covers them all.
func (a *API) auditEveryOrg(ctx context.Context, e auditEvent) error {
	rows, err := a.DB.Query(ctx, `select id from organization order by id`)
	if err != nil {
		return err
	}
	orgs, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, org := range orgs {
		e.OrgID = org
		if err := a.appendAudit(ctx, e); err != nil {
			return err
		}
	}
	return nil
}

// turnRunsUnconfined tells whether serve must run this turn without OS
// confinement: the install setting is on and serve reports that it cannot
// confine agent turns. The run record keeps the answer.
func (a *API) turnRunsUnconfined(ctx context.Context, socket, workRunID string) (bool, error) {
	if a.DB == nil || socket == "" {
		return false, nil
	}
	setting, err := a.unconfinedTurnsSetting(ctx)
	if err != nil || !setting.On {
		return false, err
	}
	statusCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	st, err := serveConfinement(statusCtx, socket)
	if err != nil || st.turnsOff == "" {
		return false, nil
	}
	if workRunID != "" {
		if _, err := a.DB.Exec(ctx, `update work_run set ran_unconfined = true where id::text = $1`, workRunID); err != nil {
			return false, err
		}
		log.Printf("[agent] run %s runs without OS confinement: an admin turned this on", workRunID)
	}
	return true, nil
}

// serveUnconfinedHealth gives doctor and release-check the setting on the
// admin socket.
func (a *API) serveUnconfinedHealth(w http.ResponseWriter, r *http.Request) {
	setting, err := a.unconfinedTurnsSetting(r.Context())
	if err != nil {
		sendError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := map[string]any{"on": setting.On}
	if setting.On {
		out["line"] = setting.line()
	}
	sendJSON(w, http.StatusOK, out)
}
