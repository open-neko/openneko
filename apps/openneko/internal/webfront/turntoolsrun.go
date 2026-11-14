package webfront

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/open-neko/neko/apps/openneko/internal/config"
	"github.com/open-neko/neko/apps/openneko/internal/files"
)

// turnBinding is the run a tool server acts for. The model never names the
// org, the run or the user: each tool takes them from here.
type turnBinding struct {
	OrgID, ThreadID, RunID, Kind string
	MemoryReadOnly               bool
	GraphjinToolPolicy           string
	WantsCards                   bool
	CardSchema, SkillsRoot       string
	WorkflowRunID, ObservationID string
	PackActions                  []packActionDescriptor
}

func (b turnBinding) trace() traceBinding {
	return traceBinding{RunID: b.RunID, OrgID: b.OrgID, Kind: b.Kind}
}

func (b turnBinding) runID() *string {
	if b.RunID == "" {
		return nil
	}
	return &b.RunID
}

func (b turnBinding) threadID() *string {
	if b.ThreadID == "" {
		return nil
	}
	return &b.ThreadID
}

type memorySearchIn struct {
	OrgID string  `json:"orgId"`
	Query string  `json:"query"`
	Limit int     `json:"limit"`
	RunID *string `json:"runId"`
}

type memoryRememberIn struct {
	OrgID    string  `json:"orgId"`
	ThreadID *string `json:"threadId"`
	RunID    *string `json:"runId"`
	Text     string  `json:"text"`
	Kind     string  `json:"kind"`
	Scope    string  `json:"scope"`
	Pinned   bool    `json:"pinned"`
}

type memoryUpdateIn struct {
	OrgID    string  `json:"orgId"`
	RunID    *string `json:"runId"`
	ThreadID *string `json:"threadId"`
	MemoryID string  `json:"memoryId"`
	Text     string  `json:"text"`
}

type skillSearchIn struct {
	OrgID string `json:"orgId"`
	RunID string `json:"runId"`
	Query string `json:"query"`
	Limit *int   `json:"limit,omitempty"`
}

type fileSearchIn struct {
	OrgID string  `json:"orgId"`
	RunID *string `json:"runId"`
	Query string  `json:"query"`
	Limit *int    `json:"limit,omitempty"`
}

type fileReadIn struct {
	OrgID    string  `json:"orgId"`
	RunID    *string `json:"runId"`
	Path     string  `json:"path"`
	Offset   *int    `json:"offset,omitempty"`
	MaxChars *int    `json:"maxChars,omitempty"`
}

type fileSQLIn struct {
	OrgID string  `json:"orgId"`
	RunID *string `json:"runId"`
	SQL   string  `json:"sql"`
}

// toolBackend is the part of AgentControlPlane that these tools call. Results
// are JSON.parse values.
type toolBackend interface {
	audit(ctx context.Context, path string)
	searchMemory(ctx context.Context, in memorySearchIn) (any, error)
	rememberMemory(ctx context.Context, in memoryRememberIn) (string, error)
	updateMemory(ctx context.Context, in memoryUpdateIn) (string, error)
	searchSkills(ctx context.Context, in skillSearchIn) (any, error)
	searchFiles(ctx context.Context, in fileSearchIn) (any, error)
	readFile(ctx context.Context, in fileReadIn) (any, error)
	queryFiles(ctx context.Context, in fileSQLIn) (any, error)
	listGraphjinTools(ctx context.Context, in graphjinCaller) (any, error)
	callGraphjinTool(ctx context.Context, in graphjinToolCall) (any, error)
	emitEvent(ctx context.Context, event any) error
	evaluatePolicy(ctx context.Context, in policyIn) (any, error)
	createActionRequest(ctx context.Context, in *jsObj) (any, error)
	enqueueAction(ctx context.Context, in actionRefIn) error
	waitForAction(ctx context.Context, in actionWaitIn) (any, error)
	emitOutput(ctx context.Context, in outputIn) (any, error)
	saveWorkflow(ctx context.Context, in *jsObj) (any, error)
	listWorkflows(ctx context.Context, in workflowListIn) (any, error)
	deleteWorkflow(ctx context.Context, in workflowDeleteIn) (any, error)
	upsertPolicy(ctx context.Context, in *jsObj) (any, error)
	listPolicies(ctx context.Context, in policyListIn) (any, error)
	listUsers(ctx context.Context, in orgIn) (any, error)
	listGroups(ctx context.Context, in orgIn) (any, error)
	listChannels(ctx context.Context, in orgIn) (any, error)
	listDataSources(ctx context.Context, in orgIn) (any, error)
	listAuditTrail(ctx context.Context, in auditTrailIn) (any, error)
	describeSourceGraph(ctx context.Context, in sourceConfigIn) (any, error)
	listSourceSecretNames(ctx context.Context, in sourceConfigIn) (any, error)
	describeSourceConfig(ctx context.Context, in sourceConfigDescribeIn) (any, error)
	checkSource(ctx context.Context, in sourceCheckIn) (any, error)
	importOpenAPISpec(ctx context.Context, in openAPIImportIn) (any, error)
	listOpenAPISpecs(ctx context.Context, in openAPIListIn) (any, error)
	previewSourceConfigChange(ctx context.Context, in sourceConfigPreviewIn) (any, error)
}

var memoryKindEnum = []string{"preference", "business_rule", "metric_definition", "thread_note", "correction", "company_context", "other"}

func joinWords(parts ...string) string { return strings.Join(parts, " ") }

// turnTools builds the tool server for servers, in that order. A server that
// is not ported yet is an error, so a run never loses a tool without notice.
func turnTools(b turnBinding, be toolBackend, servers []string) (*toolServer, error) {
	if _, err := parseGraphjinToolPolicy(b.GraphjinToolPolicy); err != nil {
		return nil, err
	}
	var groups []toolGroup
	for _, name := range servers {
		switch name {
		case "neko_memory":
			groups = append(groups, toolGroup{static: memoryTools(b, be)})
		case "neko_guides":
			groups = append(groups, toolGroup{static: guideTools(b, be)})
		case "neko_files":
			groups = append(groups, toolGroup{static: fileTools(b, be)})
		case "neko_graphjin":
			groups = append(groups, graphjinTools(b, be))
		case "neko_interaction":
			groups = append(groups, toolGroup{static: interactionTools(b, be)})
		case "neko_ui":
			groups = append(groups, toolGroup{dynamic: cardTools(b, be)})
		case "neko_skills":
			groups = append(groups, toolGroup{static: skillTools(b)})
		case "neko_action", "neko_workflow_output":
			if b.WorkflowRunID == "" {
				return nil, fmt.Errorf("mcp-bridge: %s requires workflowRunId", name)
			}
			if name == "neko_action" {
				groups = append(groups, toolGroup{static: actionTools(b, be)})
			} else {
				groups = append(groups, toolGroup{static: outputTools(b, be)})
			}
		case "neko_workflow_builder":
			groups = append(groups, toolGroup{static: workflowBuilderTools(b, be)})
		case "neko_rule_builder":
			groups = append(groups, toolGroup{static: ruleBuilderTools(b, be)})
		case "neko_user_manager":
			groups = append(groups, toolGroup{static: userManagerTools(b, be)})
		case "neko_channel_manager":
			groups = append(groups, toolGroup{static: channelManagerTools(b, be)})
		case "neko_data_source_manager":
			groups = append(groups, toolGroup{static: dataSourceManagerTools(b, be)})
		case "neko_audit":
			groups = append(groups, toolGroup{static: auditTools(b, be)})
		case "neko_source_config_manager":
			groups = append(groups, toolGroup{static: sourceConfigTools(b, be)})
		case "neko_pack_actions":
			tools := packActionTools(b, be)
			if len(tools) == 0 {
				return nil, errors.New("mcp-bridge: no pack actions in this run")
			}
			groups = append(groups, toolGroup{static: tools})
		default:
			return nil, fmt.Errorf("mcp-bridge: unknown server %s", name)
		}
	}
	return newToolServer(groups...), nil
}

func memoryTools(b turnBinding, be toolBackend) []toolDef {
	search := toolDef{server: "neko_memory", name: "search",
		description: joinWords("Semantic search over OpenNeko's saved memories. Returns top-N",
			"memories ranked by cosine similarity to the query. Use when the",
			"loaded memories above don't seem to cover what the operator is",
			"asking about."),
		props: []toolProp{strProp("query", 2, 800, true), intProp("limit", 1, 20, false)},
		call: func(ctx context.Context, args *jsObj) (toolResult, error) {
			limit := 5
			if n := optionalInt(args, "limit"); n != nil {
				limit = *n
			}
			be.audit(ctx, "/v1/memory/search")
			query := stringArg(args, "query")
			runID := b.runID()
			if b.Kind == "agent-job" {
				runID = nil
			}
			results, err := traceMemorySearch(b.trace(), query, &limit, func() (any, error) {
				return be.searchMemory(ctx, memorySearchIn{OrgID: b.OrgID, Query: query, Limit: limit, RunID: runID})
			})
			if err != nil {
				return toolResult{}, err
			}
			return toolText(jsObjOf("ok", true, "results", results)), nil
		}}
	save := toolDef{server: "neko_memory", name: "save",
		description: joinWords("Save a durable memory the operator stated explicitly. Use only when",
			"the operator says to remember/save something, corrects a recurring",
			"assumption, defines a metric/business rule, or states a stable",
			"preference. Never speculatively. Default scope=global, kind=business_rule,",
			"pinned=true."),
		props: []toolProp{strProp("text", 5, 2000, true), {name: "kind", typ: "string", enum: memoryKindEnum},
			{name: "scope", typ: "string", enum: []string{"global", "thread"}}, {name: "pinned", typ: "boolean"}},
		call: func(ctx context.Context, args *jsObj) (toolResult, error) {
			in := memoryRememberIn{OrgID: b.OrgID, ThreadID: b.threadID(), RunID: b.runID(), Text: stringArg(args, "text"),
				Kind: orDefault(stringArg(args, "kind"), "business_rule"), Scope: orDefault(stringArg(args, "scope"), "global"), Pinned: true}
			if pinned, ok := args.vals["pinned"].(bool); ok {
				in.Pinned = pinned
			}
			be.audit(ctx, "/v1/memory/remember")
			id, err := be.rememberMemory(ctx, in)
			if err != nil {
				return toolResult{}, err
			}
			return toolText(jsObjOf("ok", true, "memoryId", id)), nil
		}}
	update := toolDef{server: "neko_memory", name: "update",
		description: joinWords("Replace the text of a saved memory. Use when the operator corrects,",
			"extends or merges a memory that is already saved, instead of saving",
			"a second memory. Pass the memory id from the loaded list or a search",
			"result, and the whole new text."),
		props: []toolProp{{name: "memoryId", typ: "string", uuid: true, required: true}, strProp("text", 5, 2000, true)},
		call: func(ctx context.Context, args *jsObj) (toolResult, error) {
			memoryID := stringArg(args, "memoryId")
			be.audit(ctx, "/v1/memory/update")
			id, err := be.updateMemory(ctx, memoryUpdateIn{OrgID: b.OrgID, RunID: b.runID(), ThreadID: b.threadID(),
				MemoryID: memoryID, Text: stringArg(args, "text")})
			if err != nil {
				return toolResult{isError: true, text: jsStringify(jsObjOf("ok", false,
					"error", "No saved memory "+memoryID+" is visible to this run."), false)}, nil
			}
			return toolText(jsObjOf("ok", true, "memoryId", id)), nil
		}}
	if b.MemoryReadOnly {
		return []toolDef{search}
	}
	return []toolDef{search, save, update}
}

func guideTools(b turnBinding, be toolBackend) []toolDef {
	return []toolDef{{server: "neko_guides", name: "find",
		description: "Find the installed skill guides closest in meaning to the work described.",
		props:       []toolProp{strProp("query", 2, 800, true), intProp("limit", 1, 10, false)},
		call: func(ctx context.Context, args *jsObj) (toolResult, error) {
			be.audit(ctx, "/v1/skills/search")
			skills, err := be.searchSkills(ctx, skillSearchIn{OrgID: b.OrgID, RunID: b.RunID, Query: stringArg(args, "query"),
				Limit: optionalInt(args, "limit")})
			if err != nil {
				return toolResult{}, err
			}
			return toolText(jsObjOf("skills", skills)), nil
		}}}
}

func fileTools(b turnBinding, be toolBackend) []toolDef {
	reply := func(v any, err error) (toolResult, error) {
		if err != nil {
			return toolResult{isError: true, text: err.Error()}, nil
		}
		return toolText(v), nil
	}
	return []toolDef{
		{server: "neko_files", name: "search",
			description: joinWords("Keyword search over the files the user uploaded: PDF, Word, PowerPoint,",
				"Excel, CSV, Markdown and text. Returns the best files with matching",
				"passages and where they are (page, slide, sheet or section). For a",
				"spreadsheet or CSV file it also lists its SQL tables and columns."),
			props: []toolProp{strProp("query", 1, 800, true), intProp("limit", 1, 20, false)},
			call: func(ctx context.Context, args *jsObj) (toolResult, error) {
				query, limit := stringArg(args, "query"), optionalInt(args, "limit")
				be.audit(ctx, "/v1/files/search")
				return reply(traceFileSearch(b.trace(), query, limit, func() (any, error) {
					return be.searchFiles(ctx, fileSearchIn{OrgID: b.OrgID, RunID: b.runID(), Query: query, Limit: limit})
				}))
			}},
		{server: "neko_files", name: "read_text",
			description: joinWords("Read the text of one of the user's files, with page, slide and sheet",
				"markers. Use the path from search. A long file comes back in parts:",
				"when truncated is true, call again with offset set to offset plus the",
				"length of the text already read."),
			props: []toolProp{strProp("path", 1, 1024, true), intProp("offset", 0, maxSafeInt, false), intProp("maxChars", 1000, 100000, false)},
			call: func(ctx context.Context, args *jsObj) (toolResult, error) {
				be.audit(ctx, "/v1/files/read")
				return reply(be.readFile(ctx, fileReadIn{OrgID: b.OrgID, RunID: b.runID(), Path: stringArg(args, "path"),
					Offset: optionalInt(args, "offset"), MaxChars: optionalInt(args, "maxChars")}))
			}},
		{server: "neko_files", name: "query_tables",
			description: joinWords("Run one read-only DuckDB SELECT or WITH query over the sheet tables of",
				"the user's spreadsheet and CSV files. Use the table names that search",
				"or read_text list. Each sheet has a raw table (name ends in __raw) with",
				"every cell as text by column letter. A sheet with a header row also has",
				"a typed table with named columns. Use it for sums, counts, filters and",
				"joins across sheets."),
			props: []toolProp{strProp("sql", 1, 20000, true)},
			call: func(ctx context.Context, args *jsObj) (toolResult, error) {
				be.audit(ctx, "/v1/files/sql")
				return reply(be.queryFiles(ctx, fileSQLIn{OrgID: b.OrgID, RunID: b.runID(), SQL: stringArg(args, "sql")}))
			}},
	}
}

// runTools is the database backend of the tools, the InProcessControlPlane
// methods behind the broker.
type runTools struct {
	api     *API
	binding turnBinding
	events  func(ctx context.Context, event any) error

	once     sync.Once
	identity [3]*string
}

func (a *API) runTools(b turnBinding) *runTools { return &runTools{api: a, binding: b} }

// emitEvent is the broker's /v1/events: an audit row, then the run's event
// sink. The turn sets the sink; with none, the event is dropped and logged.
func (r *runTools) emitEvent(ctx context.Context, event any) error {
	r.audit(ctx, "/v1/events")
	if r.events == nil {
		log.Printf("[agent-broker] dropped 1 event(s) for %s: no event sink", r.binding.RunID)
		return nil
	}
	return r.events(ctx, event)
}

// audit is auditControlPlaneCall(): one control_plane_audit row per call,
// best effort.
func (r *runTools) audit(ctx context.Context, path string) {
	r.once.Do(func() {
		err := r.api.DB.QueryRow(ctx, `select actor_user_id, actor_role, backend from work_run where id::text = $1`,
			r.binding.RunID).Scan(&r.identity[0], &r.identity[1], &r.identity[2])
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			log.Printf("[agent-broker] audit insert failed (call proceeded): %v", err)
		}
	})
	if _, err := r.api.DB.Exec(context.WithoutCancel(ctx), `insert into control_plane_audit (org_id, run_id, path, actor_user_id, actor_role, backend)
		values ($1, $2::uuid, $3, $4, $5, $6)`, r.binding.OrgID, r.binding.runID(), path, r.identity[0], r.identity[1], r.identity[2]); err != nil {
		log.Printf("[agent-broker] audit insert failed (call proceeded): %v", err)
	}
}

// entitlementActorForRun is entitlementActorForRun() in
// packages/llm/src/work/entitlement-scope.ts; nil for an unknown run.
func (a *API) entitlementActorForRun(ctx context.Context, orgID, runID string) (*actor, error) {
	var userID, role *string
	err := a.DB.QueryRow(ctx, `select actor_user_id, actor_role from work_run where id::text = $1 and org_id = $2`,
		runID, orgID).Scan(&userID, &role)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if deref(userID) != "" {
		r, err := a.role(ctx, orgID, *userID)
		return &actor{OrgID: orgID, UserID: *userID, Admin: r == "admin"}, err
	}
	if r := deref(role); r != "" && r != "service" && r != "admin" {
		return &actor{OrgID: orgID}, nil
	}
	var packID string
	err = a.DB.QueryRow(ctx, `select i.pack_id from workflow_run r
		join workflow_definition w on w.id = r.workflow_id
		join pack_artifact a on a.org_id = w.org_id and a.artifact_kind = 'workflow' and a.target_ref = w.name and a.ownership <> 'detached'
		join pack_install i on i.id = a.pack_install_id and i.status <> 'removed'
		where r.work_run_id::text = $1 and r.org_id = $2
		order by i.pack_id limit 1`, runID, orgID).Scan(&packID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if packID != "" {
		return &actor{OrgID: orgID, PackID: packID}, nil
	}
	return &actor{OrgID: orgID, Admin: true}, nil
}

func (a *API) runMemoryScope(ctx context.Context, orgID, runID string) (memoryScope, error) {
	s := memoryScope{OrgID: orgID, RunID: runID, Team: []string{}}
	if runID == "" {
		s.Team = nil
		return s, nil
	}
	var userID, role *string
	err := a.DB.QueryRow(ctx, `select actor_user_id, actor_role from work_run where id::text = $1`, runID).Scan(&userID, &role)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return s, err
	}
	if deref(role) == "member" && deref(userID) != "" {
		s.Layer = *userID
	}
	act, err := a.entitlementActorForRun(ctx, orgID, runID)
	if err != nil || act == nil {
		return s, err
	}
	held, err := a.heldItems(ctx, *act, "team_memory")
	s.Team = heldList(held)
	return s, err
}

func (r *runTools) searchMemory(ctx context.Context, in memorySearchIn) (any, error) {
	query := jsTrim(in.Query)
	if query == "" {
		return []any{}, nil
	}
	s, err := r.api.runMemoryScope(ctx, in.OrgID, deref(in.RunID))
	if err != nil {
		return nil, err
	}
	found, err := r.api.memorySearch(ctx, s, query, min(max(in.Limit, 1), 20))
	if err != nil {
		return nil, err
	}
	results := make([]any, len(found))
	for i, m := range found {
		results[i] = jsObjOf("source", "saved_memory", "memory", jsFromGo(m.memoryOut), "score", m.score)
	}
	return results, nil
}

func (r *runTools) rememberMemory(ctx context.Context, in memoryRememberIn) (string, error) {
	if !containsString(memoryKindEnum, in.Kind) {
		return "", errors.New("Invalid work memory kind: " + in.Kind)
	}
	scope := "global"
	var scopeID *string
	if in.Scope == "thread" && deref(in.ThreadID) != "" {
		scope, scopeID = "thread", in.ThreadID
	}
	var layer *string
	if in.RunID != nil {
		var userID, role *string
		err := r.api.DB.QueryRow(ctx, `select actor_user_id, actor_role from work_run where id::text = $1`, *in.RunID).Scan(&userID, &role)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return "", err
		}
		if deref(role) == "member" && deref(userID) != "" {
			layer = userID
		}
	}
	m, err := r.api.insertMemory(ctx, r.api.DB, in.OrgID, memoryDraft{Kind: in.Kind, Text: in.Text, Scope: scope, ScopeID: scopeID,
		Pinned: in.Pinned, Confidence: 0.8, Metadata: []byte("{}"), RunID: in.RunID, ThreadID: in.ThreadID, Layer: layer}, jsNow())
	if err != nil {
		return "", err
	}
	return m.ID, nil
}

// updateMemory is updateWorkMemoryForRun(): a member's run edits through the
// member's own layer; a team run rewrites the team row in place.
func (r *runTools) updateMemory(ctx context.Context, in memoryUpdateIn) (string, error) {
	text := strings.TrimSpace(in.Text)
	if text == "" {
		return "", errors.New("Memory text is empty")
	}
	s, err := r.api.runMemoryScope(ctx, in.OrgID, deref(in.RunID))
	if err != nil {
		return "", err
	}
	tx, err := r.api.DB.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck
	found, err := r.api.collectMemories(ctx, tx, `select `+memoryColumns+` from work_memory where `+memoryVisible+` and id::text = $4 limit 1`,
		append(s.args(), in.MemoryID)...)
	if err != nil {
		return "", err
	}
	if len(found) == 0 || !found[0].IntegrityOK {
		return "", errors.New("Memory not found: " + in.MemoryID)
	}
	target := found[0]
	var m *memoryOut
	if s.Layer != "" {
		m, err = r.api.overrideMemory(ctx, tx, in.OrgID, s.Layer, target.ID, text, false, in.RunID, in.ThreadID)
		if err == nil && m == nil {
			err = errors.New("Memory not found: " + in.MemoryID)
		}
	} else {
		var updated memoryOut
		updated, err = r.api.scanMemory(tx.QueryRow(ctx, `update work_memory set text = $3, integrity_hmac = $4, embedding = null, updated_at = $5
			where org_id = $1 and id = $2 returning `+memoryColumns,
			in.OrgID, target.ID, text, r.api.memoryHMAC(in.OrgID, "", target.Kind, target.Scope, deref(target.ScopeID), text), jsNow()))
		m = &updated
		if err == nil {
			err = memoryEvent(ctx, tx, in.OrgID, &target.ID, in.RunID, in.ThreadID, "update",
				map[string]any{"previousText": target.Text, "text": text})
		}
	}
	if err != nil {
		return "", err
	}
	return m.ID, tx.Commit(ctx)
}

// runSourceConfigAdmin is requireSourceConfigAccess() for a run: true only
// for an admin run while the org allows the source-config tools.
func (a *API) runSourceConfigAdmin(ctx context.Context, orgID, runID string) (bool, error) {
	if enabled, err := a.sourceConfigEnabled(ctx, orgID); err != nil || !enabled {
		return false, err
	}
	if runID == "" {
		return false, nil
	}
	var runOrg string
	var userID, role *string
	err := a.DB.QueryRow(ctx, `select org_id, actor_user_id, actor_role from work_run where id::text = $1`, runID).Scan(&runOrg, &userID, &role)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && runOrg != orgID {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if deref(userID) != "" {
		r, err := a.role(ctx, orgID, *userID)
		return r == "admin", err
	}
	var anyUser bool
	if err := a.DB.QueryRow(ctx, `select exists (select 1 from app_user where org_id = $1)`, orgID).Scan(&anyUser); err != nil {
		return false, err
	}
	return !anyUser && deref(role) == "admin", nil
}

const skillKeywordQuery = `nullif(replace(plainto_tsquery('english', $2)::text, ' & ', ' | '), '')::tsquery`

// searchSkills is searchSkillsForRun() with the admin-only skills left out
// of a run that is not an admin run.
func (r *runTools) searchSkills(ctx context.Context, in skillSearchIn) (any, error) {
	a := r.api
	adminRun, err := a.runSourceConfigAdmin(ctx, in.OrgID, in.RunID)
	if err != nil {
		return nil, err
	}
	query := jsTrim(in.Query)
	results := []any{}
	if query == "" {
		return results, nil
	}
	act, err := a.entitlementActorForRun(ctx, in.OrgID, in.RunID)
	if err != nil || act == nil {
		return results, err
	}
	held, err := a.heldItems(ctx, *act, "skill")
	if err != nil {
		return nil, err
	}
	heldIDs := heldList(held)
	limit := 3
	if in.Limit != nil {
		limit = *in.Limit
	}
	limit = min(max(limit, 1), 10)
	terminal, err := a.orgTerminal(ctx, in.OrgID)
	if err != nil {
		return nil, err
	}
	own, _, ownNames := runSkillsWithOwn(nil, nil, ownRunSkills(orgAgentRoot(in.OrgID), act.UserID), adminRun, terminal)
	for _, s := range matchOwnSkills(own, query) {
		if len(results) < limit {
			results = append(results, jsObjOf("name", s.Name, "score", 1.0))
		}
	}
	if heldIDs != nil && len(heldIDs) == 0 || len(results) == limit {
		return results, nil
	}
	var hidden []string
	if !terminal {
		for name := range sandboxSkills {
			hidden = append(hidden, name)
		}
	}
	if !adminRun {
		hidden = append(hidden, "graphjin-config")
	}
	hidden = append(hidden, ownNames...)
	limit -= len(results)
	type hit struct {
		name  string
		score float64
	}
	keyword := func(unindexed string) ([]hit, error) {
		rows, err := a.DB.Query(ctx, `select skill_name, ts_rank_cd(to_tsvector('english', document), `+skillKeywordQuery+`)::text
			from skill_embedding
			where org_id = $1 and ($3::text[] is null or skill_name = any($3::text[])) and not (skill_name = any($4::text[]))
			  and ($5 = '' or embedding is null or embedding_model is distinct from $5)
			  and to_tsvector('english', document) @@ `+skillKeywordQuery+`
			order by ts_rank_cd(to_tsvector('english', document), `+skillKeywordQuery+`) desc, skill_name limit $6`,
			in.OrgID, query, heldIDs, append([]string{}, hidden...), unindexed, limit)
		if err != nil {
			return nil, err
		}
		return pgx.CollectRows(rows, func(row pgx.CollectableRow) (hit, error) {
			var h hit
			var score string
			err := row.Scan(&h.name, &score)
			h.score = pgNumber(score)
			return h, err
		})
	}
	var hits []hit
	model := a.EmbeddingModel()
	var vector string
	if model != "" {
		if v, err := a.EmbedText(ctx, query); err != nil {
			log.Printf("[skill-index] embedding unavailable; searching by keyword: %v", err)
		} else {
			vector = vectorText(v)
		}
	}
	if vector == "" {
		if hits, err = keyword(""); err != nil {
			return nil, err
		}
	} else {
		rows, err := a.DB.Query(ctx, `select skill_name, (embedding <=> $2::vector)::text from skill_embedding
			where org_id = $1 and embedding_model = $3 and embedding is not null
			  and ($4::text[] is null or skill_name = any($4::text[])) and not (skill_name = any($5::text[]))
			order by embedding <=> $2::vector limit $6`, in.OrgID, vector, model, heldIDs, append([]string{}, hidden...), limit)
		if err != nil {
			return nil, err
		}
		semantic, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (hit, error) {
			var h hit
			var distance string
			err := row.Scan(&h.name, &distance)
			h.score = 1 - pgNumber(distance)
			return h, err
		})
		if err != nil {
			return nil, err
		}
		unindexed, err := keyword(model)
		if err != nil {
			return nil, err
		}
		if len(unindexed) == 0 {
			hits = semantic[:min(len(semantic), limit)]
		} else {
			seen := map[string]bool{}
			push := func(list []hit, i int) {
				if i < len(list) && len(hits) < limit && !seen[list[i].name] {
					seen[list[i].name] = true
					hits = append(hits, list[i])
				}
			}
			for i := 0; i < max(len(semantic), len(unindexed)) && len(hits) < limit; i++ {
				push(semantic, i)
				push(unindexed, i)
			}
		}
	}
	for _, h := range hits {
		results = append(results, jsObjOf("name", h.name, "score", h.score))
	}
	return results, nil
}

// pgNumber is Number(text) || 0 for a number that Postgres gave as text.
func pgNumber(text string) float64 {
	v, err := jsParse([]byte(strings.TrimSpace(text)))
	f, ok := v.(float64)
	if err != nil || !ok || math.IsNaN(f) {
		return 0
	}
	return f
}

// runOwner is resolveRunOwnerUserId(): the thread's creator, or the owner of
// the routine that a run without a thread creator belongs to.
func (a *API) runOwner(ctx context.Context, orgID, runID string) (string, error) {
	var owner *string
	err := a.DB.QueryRow(ctx, `select t.created_by_user_id from work_run r join work_thread t on t.id = r.thread_id
		where r.org_id = $1 and r.id::text = $2 limit 1`, orgID, runID).Scan(&owner)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	if deref(owner) != "" {
		return *owner, nil
	}
	err = a.DB.QueryRow(ctx, `select w.owner_user_id from workflow_run r join workflow_definition w on w.id = r.workflow_id
		where r.org_id = $1 and r.work_run_id::text = $2 limit 1`, orgID, runID).Scan(&owner)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	return deref(owner), nil
}

// filesCall is request() in packages/llm/src/files/client.ts, as the run's
// owner.
func (a *API) filesCall(ctx context.Context, orgID string, runID *string, path string, body any) (any, error) {
	userID := ""
	if runID != nil {
		var err error
		if userID, err = a.runOwner(ctx, orgID, *runID); err != nil {
			return nil, err
		}
	}
	if userID == "" {
		return nil, errors.New("This run has no owner, so it has no private files to read.")
	}
	base := serveURL()
	if base == "" {
		return nil, errors.New("File search is off: OPENNEKO_SERVE_URL is not set.")
	}
	key, err := config.SigningSecret(a.SecretsDir, files.TokenLabel)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	payload, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+files.MintToken(key, orgID, userID, time.Now().Add(time.Minute)))
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	parsed, perr := jsParse(raw)
	if perr != nil {
		parsed = nil
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		if msg, ok := jsGet(parsed, "error").(string); ok {
			return nil, errors.New(msg)
		}
		return nil, fmt.Errorf("openneko serve answered %d", res.StatusCode)
	}
	return parsed, nil
}

func (r *runTools) searchFiles(ctx context.Context, in fileSearchIn) (any, error) {
	body := map[string]any{"query": in.Query}
	if in.Limit != nil && *in.Limit != 0 {
		body["limit"] = *in.Limit
	}
	return r.api.filesCall(ctx, in.OrgID, in.RunID, "/v1/files/search", body)
}

func (r *runTools) readFile(ctx context.Context, in fileReadIn) (any, error) {
	body := map[string]any{"path": in.Path}
	if in.Offset != nil && *in.Offset != 0 {
		body["offset"] = *in.Offset
	}
	if in.MaxChars != nil && *in.MaxChars != 0 {
		body["max_chars"] = *in.MaxChars
	}
	return r.api.filesCall(ctx, in.OrgID, in.RunID, "/v1/files/read", body)
}

func (r *runTools) queryFiles(ctx context.Context, in fileSQLIn) (any, error) {
	return r.api.filesCall(ctx, in.OrgID, in.RunID, "/v1/files/sql", map[string]any{"sql": in.SQL})
}
