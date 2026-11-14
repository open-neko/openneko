package webfront

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"gopkg.in/yaml.v3"
)

const queueWorkflowRunFire = "workflow_run_fire"

// CronSweep is the minute workflow_cron_sweep: the behavior thresholds, then
// the condition watchers. A failure in either half is logged and does not fail
// the job.
func (a *API) CronSweep(ctx context.Context) error {
	orgID, err := a.OrgID(ctx)
	if err != nil {
		return err
	}
	if _, err := a.behaviorSweep(ctx, orgID, profileBehaviorThresholds(), time.Now()); err != nil {
		log.Printf("[cron-sweep] behavior sweep failed: %v", err)
	}
	if _, err := a.sweepWatchers(ctx, orgID, time.Now()); err != nil {
		log.Printf("[cron-sweep] watcher sweep failed: %v", err)
	}
	return nil
}

// CronSweepResult is what one workflow_cron_sweep did for an org.
type CronSweepResult struct {
	OrgID          string           `json:"orgId"`
	At             string           `json:"at"`
	BehaviorAlerts []BehaviorAlert  `json:"behaviorAlerts"`
	Checked        int              `json:"checked"`
	Fired          []WatcherFireOut `json:"fired"`
}

type BehaviorAlert struct {
	Kind          string `json:"kind"`
	Subject       string `json:"subject"`
	Observed      int    `json:"observed"`
	Threshold     int    `json:"threshold"`
	WindowSeconds int    `json:"windowSeconds"`
}

type WatcherFireOut struct {
	WatcherID string          `json:"watcherId"`
	Name      string          `json:"name"`
	Value     json.RawMessage `json:"value"`
}

// CronSweepAt runs both halves of workflow_cron_sweep once for orgID with
// now as the clock. Firings go to the workflow_run_fire queue, as in the job.
func (a *API) CronSweepAt(ctx context.Context, orgID string, now time.Time) (CronSweepResult, error) {
	res := CronSweepResult{OrgID: orgID, At: jsTime(now.UTC()), BehaviorAlerts: []BehaviorAlert{}, Fired: []WatcherFireOut{}}
	var exists bool
	if err := a.DB.QueryRow(ctx, `select exists (select 1 from organization where id::text = $1)`, orgID).Scan(&exists); err != nil {
		return CronSweepResult{}, err
	}
	if !exists {
		return CronSweepResult{}, fmt.Errorf("no organization has the ID %s", orgID)
	}
	alerts, err := a.behaviorSweep(ctx, orgID, profileBehaviorThresholds(), now)
	if err != nil {
		return res, fmt.Errorf("behavior sweep: %w", err)
	}
	for _, x := range alerts {
		res.BehaviorAlerts = append(res.BehaviorAlerts, BehaviorAlert{x.Kind, x.Subject, x.Observed, x.Threshold, x.WindowSeconds})
	}
	checked, fired, err := a.sweepWatchersCounted(ctx, orgID, now)
	res.Checked = checked
	for _, f := range fired {
		value := f.Value
		if _, undef := value.(jsUndefined); undef {
			value = nil
		}
		res.Fired = append(res.Fired, WatcherFireOut{f.ID, f.Name, json.RawMessage(jsStringify(value, false))})
	}
	if err != nil {
		return res, fmt.Errorf("watcher sweep: %w", err)
	}
	return res, nil
}

type behaviorThresholds struct {
	ControlPlaneCallsPerRun int
	ActionRequestsPerHour   int
	MemoryWritesPerHour     int
}

type behaviorAlert struct {
	Kind          string
	Subject       string
	Observed      int
	Threshold     int
	WindowSeconds int
}

// behaviorLimit is envInt() of packages/llm/src/work/behavior-monitor.ts.
func behaviorLimit(name string, fallback int) int {
	v, ok := os.LookupEnv(name)
	if !ok {
		return fallback
	}
	n, ok := jsNumber(v)
	if !ok || math.IsInf(n, 0) || n <= 0 {
		return fallback
	}
	return int(math.Floor(n))
}

// profileBehaviorThresholds is profileBehaviorThresholds() of
// packages/llm/src/work/deployment-profile.ts: OPENNEKO_PROFILE "org" halves
// the envelopes and "hardened" quarters them.
func profileBehaviorThresholds() behaviorThresholds {
	scale := 1.0
	switch strings.ToLower(os.Getenv("OPENNEKO_PROFILE")) {
	case "org":
		scale = 0.5
	case "hardened":
		scale = 0.25
	}
	apply := func(n int) int { return max(1, int(math.Floor(float64(n)*scale))) }
	return behaviorThresholds{
		ControlPlaneCallsPerRun: apply(behaviorLimit("OPENNEKO_BEHAVIOR_MAX_CP_CALLS_PER_RUN_10M", 200)),
		ActionRequestsPerHour:   apply(behaviorLimit("OPENNEKO_BEHAVIOR_MAX_ACTIONS_PER_HOUR", 50)),
		MemoryWritesPerHour:     apply(behaviorLimit("OPENNEKO_BEHAVIOR_MAX_MEMORY_WRITES_PER_HOUR", 100)),
	}
}

const (
	controlPlaneWindow = 10 * 60
	hourWindow         = 60 * 60
)

// behaviorSweep raises a behavior_alert, once per kind and subject in each
// window, and sends the security.behavior_threshold event to the worker.
func (a *API) behaviorSweep(ctx context.Context, orgID string, limits behaviorThresholds, now time.Time) ([]behaviorAlert, error) {
	var raised []behaviorAlert
	rows, err := a.DB.Query(ctx, `select coalesce(run_id::text, ''), count(*)::int from control_plane_audit
		where org_id = $1 and created_at >= $2 group by run_id`, orgID, now.Add(-controlPlaneWindow*time.Second))
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var run string
		var calls int
		if err := rows.Scan(&run, &calls); err != nil {
			rows.Close()
			return nil, err
		}
		if calls > limits.ControlPlaneCallsPerRun {
			raised = append(raised, behaviorAlert{"control_plane_call_volume", run, calls, limits.ControlPlaneCallsPerRun, controlPlaneWindow})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	hourAgo := now.Add(-hourWindow * time.Second)
	var actions, writes int
	if err := a.DB.QueryRow(ctx, `select
		(select count(*)::int from action_request where org_id = $1 and created_at >= $2),
		(select count(*)::int from work_memory_event where org_id = $1 and action = 'remember' and created_at >= $2)`,
		orgID, hourAgo).Scan(&actions, &writes); err != nil {
		return nil, err
	}
	if actions > limits.ActionRequestsPerHour {
		raised = append(raised, behaviorAlert{"action_request_volume", orgID, actions, limits.ActionRequestsPerHour, hourWindow})
	}
	if writes > limits.MemoryWritesPerHour {
		raised = append(raised, behaviorAlert{"memory_write_volume", orgID, writes, limits.MemoryWritesPerHour, hourWindow})
	}

	var persisted []behaviorAlert
	for _, alert := range raised {
		var seen bool
		if err := a.DB.QueryRow(ctx, `select exists (select 1 from behavior_alert
			where org_id = $1 and kind = $2 and subject = $3 and created_at >= $4)`,
			orgID, alert.Kind, alert.Subject, now.Add(-time.Duration(alert.WindowSeconds)*time.Second)).Scan(&seen); err != nil {
			return persisted, err
		}
		if seen {
			continue
		}
		if _, err := a.DB.Exec(ctx, `insert into behavior_alert (org_id, kind, subject, observed, threshold, window_seconds, details)
			values ($1, $2, $3, $4, $5, $6, '{}'::jsonb)`,
			orgID, alert.Kind, alert.Subject, alert.Observed, alert.Threshold, alert.WindowSeconds); err != nil {
			return persisted, err
		}
		log.Printf("[behavior-monitor] ALERT org=%s %s subject=%s observed=%d > threshold=%d in %ds",
			orgID, alert.Kind, alert.Subject, alert.Observed, alert.Threshold, alert.WindowSeconds)
		payload := fmt.Sprintf(`{"kind":%s,"subject":%s,"observed":%d,"threshold":%d,"windowSeconds":%d}`,
			jsStringify(alert.Kind, false), jsStringify(alert.Subject, false), alert.Observed, alert.Threshold, alert.WindowSeconds)
		dedupe := fmt.Sprintf("%s:%s:%d", alert.Kind, alert.Subject, now.UnixMilli()/(int64(alert.WindowSeconds)*1000))
		body := fmt.Sprintf(`{"orgId":%s,"name":"security.behavior_threshold","source":"behavior-monitor","payload":%s,"dedupeKey":%s}`,
			jsStringify(orgID, false), payload, jsStringify(dedupe, false))
		status, reply, err := a.workerDo(ctx, http.MethodPost, "/admin/events/external", 10*time.Second, []byte(body))
		if err != nil || status != http.StatusOK {
			log.Printf("[behavior-monitor] external event dispatch failed: %d %v %s", status, err, reply)
		}
		persisted = append(persisted, alert)
	}
	return persisted, nil
}

// jsUndefined is JavaScript undefined: a missing value, as a watcher path
// that the result does not hold.
type jsUndefined struct{}

// jsonText is JSON.stringify(v); ok is false where JavaScript gives undefined.
func jsonText(v any) (string, bool) {
	if _, ok := v.(jsUndefined); ok {
		return "", false
	}
	return jsStringify(v, false), true
}

func sameJSON(x, y any) bool {
	a, aok := jsonText(x)
	b, bok := jsonText(y)
	return aok == bok && a == b
}

// jsToNumber is Number(v) for a parsed JSON value.
func jsToNumber(v any) float64 {
	switch t := v.(type) {
	case jsUndefined, *jsObj:
		return math.NaN()
	case []any:
		switch len(t) {
		case 0:
			return 0
		case 1:
			switch e := t[0].(type) {
			case nil:
				return 0
			case string, float64, []any:
				return jsToNumber(e)
			}
		}
		return math.NaN()
	}
	return jsValueNumber(v)
}

// extractValueAtPath walks a dotted path ("orders.0.count") through the data.
func extractValueAtPath(data any, path string) any {
	current := data
	for _, part := range strings.Split(path, ".") {
		if part == "" {
			continue
		}
		switch t := current.(type) {
		case nil, jsUndefined:
			return jsUndefined{}
		case []any:
			n, ok := jsNumber(part)
			if !ok || n != math.Trunc(n) || n < 0 || n >= float64(len(t)) {
				return jsUndefined{}
			}
			current = t[int(n)]
		case *jsObj:
			v, ok := t.vals[part]
			if !ok {
				return jsUndefined{}
			}
			current = v
		default:
			return jsUndefined{}
		}
	}
	return current
}

func watcherConditionMet(op string, value, threshold, last any) bool {
	switch op {
	case "changed":
		if _, undef := last.(jsUndefined); undef || last == nil {
			return false
		}
		return !sameJSON(value, last)
	case "eq":
		return sameJSON(value, threshold)
	case "ne":
		return !sameJSON(value, threshold)
	}
	x, y := jsToNumber(value), jsToNumber(threshold)
	if math.IsNaN(x) || math.IsInf(x, 0) || math.IsNaN(y) || math.IsInf(y, 0) {
		return false
	}
	switch op {
	case "gt":
		return x > y
	case "gte":
		return x >= y
	case "lt":
		return x < y
	case "lte":
		return x <= y
	}
	return false
}

func resolveWatcherVariables(definition any, now time.Time) (*jsObj, error) {
	out := &jsObj{vals: map[string]any{}}
	obj, _ := definition.(*jsObj)
	if obj == nil {
		return out, nil
	}
	for _, name := range obj.ordered() {
		d, ok := obj.vals[name].(*jsObj)
		if !ok {
			return nil, fmt.Errorf("watcher variable %s must be a descriptor", name)
		}
		kind := d.vals["kind"]
		switch kind {
		case "now":
			out.set(name, jsDate(now))
		case "seconds_ago":
			seconds := jsToNumber(orUndefined(d, "seconds"))
			if math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 0 {
				return nil, fmt.Errorf("watcher variable %s.seconds must be non-negative", name)
			}
			out.set(name, jsDate(now.Add(-time.Duration(seconds*float64(time.Second)))))
		case "literal":
			v, ok := d.vals["value"]
			if ok {
				out.set(name, v)
			}
		default:
			k := "undefined"
			if v, ok := d.vals["kind"]; ok {
				k = jsValueString(v)
			}
			return nil, fmt.Errorf("watcher variable %s uses unsupported kind %s", name, k)
		}
	}
	return out, nil
}

func orUndefined(o *jsObj, key string) any {
	if v, ok := o.vals[key]; ok {
		return v
	}
	return jsUndefined{}
}

type packSource struct{ ID, GraphqlURL, AuthMode string }

type watcherFire struct {
	ID, Name string
	Value    any
}

// sweepWatchers is sweepWatchers() of packages/llm/src/workflows/watchers.ts:
// each due watcher runs its GraphJin query and queues its workflow on
// workflow_run_fire when the condition holds outside the quiet period.
func (a *API) sweepWatchers(ctx context.Context, orgID string, now time.Time) ([]watcherFire, error) {
	_, fired, err := a.sweepWatchersCounted(ctx, orgID, now)
	return fired, err
}

// sweepWatchersCounted also gives the number of due watchers it checked.
func (a *API) sweepWatchersCounted(ctx context.Context, orgID string, now time.Time) (int, []watcherFire, error) {
	now = now.UTC().Truncate(time.Millisecond)
	type watcherRow struct {
		id, workflowID, name, query, path, op, severity string
		threshold, variables, last                      []byte
		cadence, debounce, cooldown                     int
		checked, fired                                  *time.Time
	}
	rows, err := a.DB.Query(ctx, `select id::text, workflow_id::text, name, query, value_path, op, severity, threshold, variables_json, last_value,
		cadence_seconds, debounce_seconds, cooldown_seconds, last_checked_at, last_fired_at
		from watcher where org_id = $1 and enabled
		and exists (select 1 from workflow_definition d where d.id = watcher.workflow_id and d.enabled)
		order by name, id`, orgID)
	if err != nil {
		return 0, nil, err
	}
	var due []watcherRow
	for rows.Next() {
		var w watcherRow
		if err := rows.Scan(&w.id, &w.workflowID, &w.name, &w.query, &w.path, &w.op, &w.severity, &w.threshold, &w.variables, &w.last,
			&w.cadence, &w.debounce, &w.cooldown, &w.checked, &w.fired); err != nil {
			rows.Close()
			return 0, nil, err
		}
		if w.checked == nil || !w.checked.Add(time.Duration(w.cadence)*time.Second).After(now) {
			due = append(due, w)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(due) == 0 {
		return 0, nil, err
	}
	var fallback *packSource
	var url, mode *string
	err = a.DB.QueryRow(ctx, `select graphql_url, auth_mode from data_source where org_id = $1 and enabled
		order by is_default desc, created_at limit 1`, orgID).Scan(&url, &mode)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return len(due), nil, err
	}
	if err == nil {
		fallback = &packSource{GraphqlURL: deref(url), AuthMode: deref(mode)}
	}

	parse := func(raw []byte) any {
		if raw == nil {
			return nil
		}
		v, err := jsParse(raw)
		if err != nil {
			return nil
		}
		return v
	}
	var fired []watcherFire
	for _, w := range due {
		threshold, last := parse(w.threshold), parse(w.last)
		var value any = jsUndefined{}
		var failure *string
		data, err := a.watcherQuery(ctx, orgID, w.id, w.query, parse(w.variables), fallback, now)
		if err != nil {
			msg := utf16Prefix(err.Error(), 500)
			failure = &msg
		} else {
			value = extractValueAtPath(data, w.path)
		}
		met := failure == nil && watcherConditionMet(w.op, value, threshold, last)
		quiet := max(w.debounce, w.cooldown)
		outside := w.fired == nil || !w.fired.Add(time.Duration(quiet)*time.Second).After(now)
		unchanged := w.op != "changed" && sameJSON(value, last) && watcherConditionMet(w.op, last, threshold, nil)
		fires := met && outside && !unchanged

		stored := value
		if _, undef := stored.(jsUndefined); undef {
			stored = nil
		}
		var lastValue *string
		if stored != nil {
			text := jsStringify(stored, false)
			lastValue = &text
		}
		err = pgx.BeginFunc(ctx, a.DB, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `update watcher set last_checked_at = $2, last_error = $3,
				last_value = case when $3::text is null then $4::jsonb else last_value end,
				last_fired_at = case when $5 then $2 else last_fired_at end, updated_at = $2
				where id = $1`, w.id, now, failure, lastValue, fires); err != nil {
				return err
			}
			if !fires {
				return nil
			}
			trigger := &jsObj{}
			trigger.set("watcherId", w.id)
			trigger.set("watcherName", w.name)
			trigger.set("value", stored)
			trigger.set("threshold", threshold)
			trigger.set("op", w.op)
			trigger.set("severity", w.severity)
			job := &jsObj{}
			job.set("orgId", orgID)
			job.set("workflowId", w.workflowID)
			job.set("triggerKind", "watcher")
			job.set("triggerPayload", trigger)
			if err := a.ensureQueue(ctx, queueWorkflowRunFire); err != nil {
				return err
			}
			_, err := a.enqueueSingleton(ctx, tx, queueWorkflowRunFire, json.RawMessage(jsStringify(job, false)),
				"watcher:"+w.id, max(w.cadence, quiet))
			return err
		})
		if err != nil {
			return len(due), fired, err
		}
		if fires {
			fired = append(fired, watcherFire{w.id, w.name, value})
			log.Printf("[watcher] FIRED org=%s %q (%s %s; value=%s)", orgID, w.name, w.op, jsStringify(threshold, false), jsStringify(stored, false))
		}
	}
	return len(due), fired, nil
}

func (a *API) watcherQuery(ctx context.Context, orgID, watcherID, query string, variables any, fallback *packSource, now time.Time) (any, error) {
	src, err := a.packArtifactSource(ctx, orgID, "watcher", watcherID)
	if err != nil {
		return nil, err
	}
	if src == nil {
		src = fallback
	}
	if src == nil || src.GraphqlURL == "" {
		return nil, errors.New("watcher has no enabled GraphJin source")
	}
	vars, err := resolveWatcherVariables(variables, now)
	if err != nil {
		return nil, err
	}
	endpoint := strings.TrimRight(src.GraphqlURL, "/")
	if !strings.HasSuffix(endpoint, "/api/v1/graphql") {
		endpoint += "/api/v1/graphql"
	}
	header := http.Header{"Content-Type": {"application/json"}, "X-Role": {"service"}}
	if src.AuthMode == "jwt" {
		token, err := a.graphjinToken(orgID, "service", 300*time.Second)
		if err != nil {
			return nil, err
		}
		if err := a.checkLegacyPackOAuth(ctx, orgID, endpoint); err != nil {
			return nil, err
		}
		header.Set("Authorization", "Bearer "+token)
	}
	return watcherGraphQL(ctx, endpoint, header, query, vars)
}

// checkLegacyPackOAuth is the one check of packUserConnectionHeaders() that
// holds for a service token: an OAuth pack installed before the account scope
// snapshot blocks queries to its source.
func (a *API) checkLegacyPackOAuth(ctx context.Context, orgID, endpoint string) error {
	var legacy bool
	err := a.DB.QueryRow(ctx, `select exists (select 1 from pack_install i join data_source d on d.org_id = i.org_id
		and d.id::text = i.config->'_runtime'->'source'->>'id'
		where i.org_id = $1 and i.status <> 'removed' and d.enabled and d.auth_mode = 'jwt'
		and d.graphql_url = i.config->'_runtime'->'source'->>'graphqlUrl' and d.auth_mode = i.config->'_runtime'->'source'->>'authMode'
		and (d.graphql_url = $2 or d.mcp_url = $2)
		and coalesce(jsonb_typeof(i.config->'_userOAuth'), '') <> 'array'
		and case jsonb_typeof(i.config->'_oauth')
			when 'object' then i.config->'_oauth' <> '{}'::jsonb
			when 'array' then jsonb_array_length(i.config->'_oauth') > 0
			when 'string' then i.config->>'_oauth' <> ''
			else false end)`, orgID, endpoint).Scan(&legacy)
	if err != nil {
		return err
	}
	if legacy {
		return errors.New("Ask an admin to upgrade the OAuth pack to confirm its account scope")
	}
	return nil
}

func watcherGraphQL(ctx context.Context, endpoint string, header http.Header, query string, variables *jsObj) (any, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	body := `{"query":` + jsStringify(query, false) + `,"variables":` + jsStringify(variables, false) + `}`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("Failed to parse URL from %s", endpoint)
	}
	req.Header = header
	res, err := graphjinClient.Do(req)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, errors.New("The operation was aborted due to timeout")
		}
		return nil, errors.New("fetch failed")
	}
	defer res.Body.Close()
	text, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, errors.New("fetch failed")
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return nil, fmt.Errorf("graphjin query failed: %d %s", res.StatusCode, utf16Prefix(string(text), 500))
	}
	parsed, err := jsParse(bytes.TrimSpace(text))
	if err != nil {
		return nil, fmt.Errorf("graphjin returned a body that is not JSON: %v", err)
	}
	out, _ := parsed.(*jsObj)
	if out == nil {
		return nil, nil
	}
	if list, ok := out.vals["errors"].([]any); ok && len(list) > 0 {
		messages := make([]string, 0, len(list))
		for _, item := range list {
			switch t := item.(type) {
			case string:
				messages = append(messages, t)
			case *jsObj:
				if m, ok := t.vals["message"].(string); ok {
					messages = append(messages, m)
				} else {
					messages = append(messages, jsStringify(t, false))
				}
			case []any:
				messages = append(messages, jsStringify(t, false))
			default:
				messages = append(messages, jsValueString(t))
			}
		}
		return nil, graphqlErrors(strings.Join(messages, "; "))
	}
	return out.vals["data"], nil
}

// graphqlErrors holds the messages of a response's errors array.
type graphqlErrors string

func (e graphqlErrors) Error() string { return string(e) }

// packArtifactSource is packArtifactSource() of
// packages/llm/src/graphjin/pack-source.ts. A pack artifact runs only on the
// reviewed source: a changed source, binding or table mapping fails it. Nil
// means the artifact is not from a pack.
func (a *API) packArtifactSource(ctx context.Context, orgID, kind, nativeID string) (*packSource, error) {
	table, locator, enabled := "watcher", "a.metadata->'locator'->>'name' = n.name", "n.enabled"
	if kind == "metric" {
		table, locator, enabled = "metric", "a.metadata->'locator'->>'role' = n.role and a.metadata->'locator'->>'slug' = n.slug", "n.active"
	}
	var selection, bindings, hashes, tables []byte
	var status string
	var on bool
	err := a.DB.QueryRow(ctx, `select i.config->'_runtime'->'source', i.config->'_runtime'->'bindings', i.config->'_runtime'->'bindingHashes',
		i.config->'_runtime'->'tables', i.status, `+enabled+`
		from pack_artifact a join pack_install i on i.id = a.pack_install_id and i.org_id = a.org_id
		join `+table+` n on n.org_id = a.org_id and `+locator+`
		where a.org_id = $1 and a.artifact_kind = $2 and n.id::text = $3 limit 1`, orgID, kind, nativeID).
		Scan(&selection, &bindings, &hashes, &tables, &status, &on)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var sel any
	if selection != nil {
		sel, _ = jsParse(selection)
	}
	if sel == nil || sel == false || sel == "" || sel == 0.0 {
		return nil, nil
	}
	selected, _ := sel.(*jsObj)
	if status != "installed" || !on {
		return nil, errors.New("Pack is not active")
	}
	var config *jsObj
	readConfig := func(missing string) (*jsObj, error) {
		if config != nil {
			return config, nil
		}
		file := os.Getenv("OPENNEKO_GRAPHJIN_CONFIG")
		if file == "" {
			return nil, errors.New(missing)
		}
		raw, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		v, err := yamlToJS(raw)
		if err != nil {
			return nil, err
		}
		config, _ = v.(*jsObj)
		if config == nil {
			config = &jsObj{vals: map[string]any{}}
		}
		return config, nil
	}
	if b, _ := jsParse(bindings); b != nil {
		if obj, ok := b.(*jsObj); ok && len(obj.keys) > 0 {
			cfg, err := readConfig("Pack source bindings cannot be verified")
			if err != nil {
				return nil, err
			}
			h, _ := jsParse(hashes)
			hashObj, _ := h.(*jsObj)
			for _, key := range obj.ordered() {
				src := findNamed(cfg, "sources", obj.vals[key])
				var want any
				if hashObj != nil {
					want = hashObj.vals[key]
				}
				if src == nil || want != graphjinConfigPatchHash(src) {
					return nil, errors.New("Pack source binding changed; reconfigure and revalidate the pack")
				}
			}
		}
	}
	if t, _ := jsParse(tables); t != nil {
		if list, ok := t.([]any); ok && len(list) > 0 {
			cfg, err := readConfig("Pack query routing cannot be verified")
			if err != nil {
				return nil, err
			}
			for _, expected := range list {
				var name any
				if e, ok := expected.(*jsObj); ok {
					name = e.vals["name"]
				}
				current := findNamed(cfg, "tables", name)
				if current == nil || graphjinConfigPatchHash(current) != graphjinConfigPatchHash(expected) {
					return nil, errors.New("Pack query routing changed; restore the reviewed table mapping")
				}
			}
		}
	}
	want := packSource{}
	if selected != nil {
		want.ID, _ = selected.vals["id"].(string)
		want.GraphqlURL, _ = selected.vals["graphqlUrl"].(string)
		want.AuthMode, _ = selected.vals["authMode"].(string)
	}
	var got packSource
	var url, mode *string
	err = a.DB.QueryRow(ctx, `select id::text, graphql_url, auth_mode from data_source where org_id = $1 and id::text = $2 and enabled`,
		orgID, want.ID).Scan(&got.ID, &url, &mode)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	got.GraphqlURL, got.AuthMode = deref(url), deref(mode)
	if err != nil || selected == nil || url == nil || mode == nil || got.GraphqlURL != want.GraphqlURL || got.AuthMode != want.AuthMode ||
		selected.vals["graphqlUrl"] == nil || selected.vals["authMode"] == nil {
		return nil, errors.New("Pack data source changed or is unavailable; reconfigure and revalidate the pack")
	}
	return &got, nil
}

func findNamed(config *jsObj, list string, name any) any {
	items, _ := config.vals[list].([]any)
	for _, item := range items {
		if obj, ok := item.(*jsObj); ok {
			if strictEqual(obj.vals["name"], name) {
				return obj
			}
		}
	}
	return nil
}

// strictEqual is === for parsed scalars.
func strictEqual(x, y any) bool {
	switch a := x.(type) {
	case string:
		b, ok := y.(string)
		return ok && a == b
	case float64:
		b, ok := y.(float64)
		return ok && a == b
	case bool:
		b, ok := y.(bool)
		return ok && a == b
	}
	return false
}

// graphjinConfigPatchHash is graphjinConfigPatchHash() of
// packages/llm/src/graphjin/config-change.ts: keys in localeCompare order.
func graphjinConfigPatchHash(v any) string {
	var b strings.Builder
	writeConfigValue(&b, v)
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

func writeConfigValue(b *strings.Builder, v any) {
	switch t := v.(type) {
	case []any:
		b.WriteByte('[')
		for i, item := range t {
			if i > 0 {
				b.WriteByte(',')
			}
			writeConfigValue(b, item)
		}
		b.WriteByte(']')
	case *jsObj:
		keys := t.ordered()
		slices.SortStableFunc(keys, localeCompare)
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(jsStringify(k, false))
			b.WriteByte(':')
			writeConfigValue(b, t.vals[k])
		}
		b.WriteByte('}')
	default:
		b.WriteString(jsStringify(v, false))
	}
}

// yamlToJS is parse() of the yaml package with its YAML 1.2 core schema:
// mappings become *jsObj in document order, numbers float64, and a
// timestamp stays text.
func yamlToJS(raw []byte) (any, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	if doc.Kind == 0 || len(doc.Content) == 0 {
		return nil, nil
	}
	return yamlNodeToJS(doc.Content[0]), nil
}

func yamlNodeToJS(n *yaml.Node) any {
	switch n.Kind {
	case yaml.AliasNode:
		return yamlNodeToJS(n.Alias)
	case yaml.SequenceNode:
		out := make([]any, 0, len(n.Content))
		for _, c := range n.Content {
			out = append(out, yamlNodeToJS(c))
		}
		return out
	case yaml.MappingNode:
		out := &jsObj{vals: map[string]any{}}
		for i := 0; i+1 < len(n.Content); i += 2 {
			out.set(n.Content[i].Value, yamlNodeToJS(n.Content[i+1]))
		}
		return out
	}
	switch n.ShortTag() {
	case "!!null":
		return nil
	case "!!bool":
		return strings.EqualFold(n.Value, "true")
	case "!!int":
		v := strings.ReplaceAll(n.Value, "_", "")
		if i, err := strconv.ParseInt(v, 0, 64); err == nil {
			return float64(i)
		}
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	case "!!float":
		switch strings.ToLower(strings.TrimPrefix(n.Value, "+")) {
		case ".inf":
			return math.Inf(1)
		case "-.inf":
			return math.Inf(-1)
		case ".nan":
			return math.NaN()
		}
		if f, err := strconv.ParseFloat(n.Value, 64); err == nil {
			return f
		}
	}
	return n.Value
}
