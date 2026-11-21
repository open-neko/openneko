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

// UnconfinedAllowCommand and UnconfinedDenyCommand change the override on
// the host. No route of the web app or the API changes it.
const (
	UnconfinedAllowCommand = `openneko confinement allow-unconfined --reason "<why this host cannot confine>"`
	UnconfinedDenyCommand  = "openneko confinement deny"
)

// unconfinedTurns is the operator's override that lets agent turns run
// without OS confinement. It holds only for the host and kernel it was
// given for, and only while that system cannot confine.
type unconfinedTurns struct {
	On       bool   `json:"on"`
	Host     string `json:"host,omitempty"`
	HostID   string `json:"hostId,omitempty"`
	Kernel   string `json:"kernel,omitempty"`
	Missing  string `json:"missing,omitempty"`
	Reason   string `json:"reason,omitempty"`
	HostUser string `json:"hostUser,omitempty"`
	At       string `json:"at,omitempty"`
}

func (a *API) unconfinedTurnsSetting(ctx context.Context) (unconfinedTurns, error) {
	var out unconfinedTurns
	var raw []byte
	var at time.Time
	err := a.DB.QueryRow(ctx, `select value, updated_at from install_setting where name = $1`, unconfinedSetting).Scan(&raw, &at)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return unconfinedTurns{}, fmt.Errorf("install setting %s: %w", unconfinedSetting, err)
	}
	out.At = at.UTC().Format(time.RFC3339)
	return out, nil
}

// appliesTo tells whether the override holds for the system serve reports.
func (u unconfinedTurns) appliesTo(st serveStates) bool {
	return u.On && u.HostID != "" && u.HostID == st.hostID && u.Kernel == st.kernel && st.turnsOff != ""
}

// notHere tells why an override that is on does not hold for st.
func (u unconfinedTurns) notHere(st serveStates) string {
	switch {
	case !u.On:
		return ""
	case st.turnsOff == "":
		return "this system confines agent turns, so the override does not apply"
	case u.HostID == "" || u.HostID != st.hostID:
		return fmt.Sprintf("the override was given for host %s, not for this host", u.Host)
	case u.Kernel != st.kernel:
		return fmt.Sprintf("the override was given for %s, and this host now runs %s", u.Kernel, st.kernel)
	}
	return ""
}

// line is what the status page, doctor and release-check show while the
// override applies.
func (u unconfinedTurns) line() string {
	return fmt.Sprintf("agent turns run WITHOUT OS confinement: host user %s allowed it on %s (%s, %s): %s. The agent can read the files and reach the network of the OpenNeko account",
		u.HostUser, u.Host, u.Kernel, u.At, u.Reason)
}

// unconfinedOffer tells whether this system cannot confine agent turns, so
// that the operator may allow them without confinement.
func (a *API) unconfinedOffer(ctx context.Context) (serveStates, map[string]any) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if a.serveSocket() == "" {
		return serveStates{}, map[string]any{"available": false, "blocked": "Agent turns are off: OPENNEKO_SERVE_SOCKET is not set."}
	}
	st, err := serveConfinement(ctx, a.serveSocket())
	switch {
	case err != nil:
		return st, map[string]any{"available": false, "blocked": err.Error()}
	case st.turnsOff == "":
		return st, map[string]any{"available": false, "blocked": "This system confines agent turns, so the override stays off.", "host": st.hostName, "kernel": st.kernel}
	case st.hostID == "":
		return st, map[string]any{"available": false, "blocked": "openneko serve did not report a host id; run serve from the same release as web."}
	}
	return st, map[string]any{"available": true, "host": st.hostName, "kernel": st.kernel, "reason": st.turnsOff}
}

// settingsAgentUnconfinedGet shows the override and the host commands that
// change it.
func (a *API) settingsAgentUnconfinedGet(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.settingsAdmin(w, r); !ok {
		return
	}
	a.sendUnconfined(w, r)
}

func (a *API) sendUnconfined(w http.ResponseWriter, r *http.Request) {
	setting, err := a.unconfinedTurnsSetting(r.Context())
	if err != nil {
		a.fail(w, r, err)
		return
	}
	st, offer := a.unconfinedOffer(r.Context())
	out := map[string]any{"applies": setting.appliesTo(st), "system": offer,
		"commands": map[string]string{"allow": UnconfinedAllowCommand, "deny": UnconfinedDenyCommand}}
	if why := setting.notHere(st); why != "" {
		out["notApplied"] = why
	}
	setting.HostID = ""
	out["setting"] = setting
	sendJSON(w, http.StatusOK, out)
}

// operatorUnconfined is the write of `openneko confinement` on the web
// admin socket. Only the install account and root reach that socket.
func (a *API) operatorUnconfined(w http.ResponseWriter, r *http.Request) {
	raw, _ := readBody(r)
	body := objectFields(raw)
	on, isBool := body["on"].(bool)
	hostUser, _ := body["hostUser"].(string)
	reason, _ := body["reason"].(string)
	hostUser, reason = strings.TrimSpace(hostUser), strings.TrimSpace(reason)
	switch {
	case !isBool:
		sendError(w, http.StatusBadRequest, "on must be true or false")
		return
	case hostUser == "":
		sendError(w, http.StatusBadRequest, "hostUser must name the host account that runs the command")
		return
	case on && reason == "":
		sendError(w, http.StatusBadRequest, "give the reason with --reason")
		return
	case len(reason) > 500:
		sendError(w, http.StatusBadRequest, "the reason is longer than 500 characters")
		return
	}
	ctx := r.Context()
	current, err := a.unconfinedTurnsSetting(ctx)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	next := unconfinedTurns{Host: current.Host, HostID: current.HostID, Kernel: current.Kernel, Missing: current.Missing, Reason: reason, HostUser: hostUser}
	if on {
		st, offer := a.unconfinedOffer(ctx)
		if offer["available"] != true {
			sendError(w, http.StatusConflict, fmt.Sprint(offer["blocked"]))
			return
		}
		next = unconfinedTurns{On: true, Host: st.hostName, HostID: st.hostID, Kernel: st.kernel, Missing: st.turnsOff, Reason: reason, HostUser: hostUser}
	}
	value, _ := json.Marshal(next)
	if _, err := a.DB.Exec(ctx, `insert into install_setting (name, value, updated_by_user_id, updated_at) values ($1, $2, null, now())
		on conflict (name) do update set value = excluded.value, updated_by_user_id = null, updated_at = now()`,
		unconfinedSetting, value); err != nil {
		a.fail(w, r, err)
		return
	}
	event := "agent.unconfined_turns.off"
	if on {
		event = "agent.unconfined_turns.on"
	}
	log.Printf("[agent] %s by host user %s on host %s (%s): %s", event, hostUser, next.Host, next.Kernel, reason)
	payload := map[string]any{"hostUser": hostUser, "host": next.Host, "hostId": next.HostID, "kernel": next.Kernel, "missing": next.Missing, "reason": reason}
	if err := a.auditEveryOrg(ctx, auditEvent{EntityKind: "install_setting", EntityID: unconfinedSetting, Event: event, Payload: payload}); err != nil {
		a.fail(w, r, err)
		return
	}
	servicesMu.Lock()
	clear(servicesCache)
	servicesMu.Unlock()
	a.sendUnconfined(w, r)
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

// unconfinedRecord names the row that keeps whether a turn ran unconfined.
type unconfinedRecord struct{ workRun, job string }

// turnRunsUnconfined tells whether serve must run this turn without OS
// confinement: the override holds for this host, kernel and state. The
// run or job record keeps the answer.
func (a *API) turnRunsUnconfined(ctx context.Context, socket string, rec unconfinedRecord) (bool, error) {
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
	if err != nil || !setting.appliesTo(st) {
		return false, nil
	}
	if rec.workRun != "" {
		if _, err := a.DB.Exec(ctx, `update work_run set ran_unconfined = true where id::text = $1`, rec.workRun); err != nil {
			return false, err
		}
		log.Printf("[agent] run %s runs without OS confinement: the host operator allowed it", rec.workRun)
	}
	if rec.job != "" {
		if _, err := a.DB.Exec(ctx, `update processing_job set ran_unconfined = true where id::text = $1`, rec.job); err != nil {
			return false, err
		}
	}
	return true, nil
}

// serveUnconfinedHealth gives doctor, release-check and `openneko
// confinement status` the override on the admin socket.
func (a *API) serveUnconfinedHealth(w http.ResponseWriter, r *http.Request) {
	setting, err := a.unconfinedTurnsSetting(r.Context())
	if err != nil {
		sendError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var st serveStates
	var statusErr error
	if socket := a.serveSocket(); socket != "" && setting.On {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		st, statusErr = serveConfinement(ctx, socket)
		cancel()
	}
	out := map[string]any{"on": setting.appliesTo(st), "stored": setting.On}
	switch {
	case setting.appliesTo(st):
		out["line"] = setting.line()
	case statusErr != nil:
		out["notApplied"] = statusErr.Error()
	case setting.notHere(st) != "":
		out["notApplied"] = setting.notHere(st)
	}
	sendJSON(w, http.StatusOK, out)
}
