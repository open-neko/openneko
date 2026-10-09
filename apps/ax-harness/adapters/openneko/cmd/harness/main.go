package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/open-neko/openneko/apps/ax-harness/adapters/openneko/broker"
	productmcp "github.com/open-neko/openneko/apps/ax-harness/adapters/openneko/mcp"
	"github.com/open-neko/openneko/apps/ax-harness/internal/agent"
	"github.com/open-neko/openneko/apps/ax-harness/internal/command"
	"github.com/open-neko/openneko/apps/ax-harness/internal/localtool"
)

func main() {
	recordsOnly := os.Getenv("OPENNEKO_HARNESS_RECORDS_ONLY")
	lookupRead := os.Getenv("OPENNEKO_HARNESS_LOOKUP_READ")
	if recordsOnly != "" && recordsOnly != "1" {
		fmt.Fprintln(os.Stderr, "invalid records-only binding")
		os.Exit(2)
	}
	if lookupRead != "" && lookupRead != "0" && lookupRead != "1" {
		fmt.Fprintln(os.Stderr, "invalid lookup binding")
		os.Exit(2)
	}
	scope, err := admissionScope(os.Getenv("OPENNEKO_MCP_ORG_ID"), os.Getenv("OPENNEKO_MCP_THREAD_ID"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	var lookup func(context.Context, string) (json.RawMessage, error)
	if recordsOnly != "1" && lookupRead != "0" {
		var err error
		lookup, err = broker.GraphJin(os.Getenv("OPENNEKO_BROKER_URL"), os.Getenv("OPENNEKO_BROKER_TOKEN"), os.Getenv("OPENNEKO_DATA_SOURCE_ID"))
		if err != nil {
			fmt.Fprintln(os.Stderr, "invalid GraphJin capability binding")
			os.Exit(2)
		}
	}
	tools := agent.Tools{Lookup: lookup, Scope: scope}
	if child := os.Getenv("OPENNEKO_HARNESS_CHILD_READS"); child != "" {
		tools.ChildReads = strings.Split(child, ",")
	}
	if recordsOnly == "1" {
		tools.Scope += "\nrecords-only"
	}
	if kinds := os.Getenv("OPENNEKO_HARNESS_ACTION_KINDS"); kinds != "" {
		propose, bindErr := broker.Propose(os.Getenv("OPENNEKO_BROKER_URL"), os.Getenv("OPENNEKO_BROKER_TOKEN"))
		var err error
		if bindErr != nil {
			err = bindErr
		} else {
			tools.Propose, err = admitActions(kinds, propose)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "invalid proposal capability binding")
			os.Exit(2)
		}
		tools.Scope += "\nactions:" + kinds
	}
	if enabled := os.Getenv("OPENNEKO_HARNESS_MEMORY_SAVE"); enabled != "" {
		if enabled != "1" || recordsOnly == "1" {
			fmt.Fprintln(os.Stderr, "invalid memory save binding")
			os.Exit(2)
		}
		save, err := broker.MemorySave(os.Getenv("OPENNEKO_BROKER_URL"), os.Getenv("OPENNEKO_BROKER_TOKEN"))
		if err != nil {
			fmt.Fprintln(os.Stderr, "memory save broker unavailable:", err)
			os.Exit(2)
		}
		var binding string
		tools.Capabilities = append(tools.Capabilities, agent.Capability{
			Name: "memory_save", Version: "1", Origin: "openneko", Effect: "durable",
			Description: "Save a durable memory only when the operator explicitly asks to remember something or states a stable correction or rule.",
			InputSchema: json.RawMessage(`{"type":"object","required":["text"],"properties":{"text":{"type":"string","minLength":5,"maxLength":2000},"kind":{"type":"string","enum":["preference","business_rule","metric_definition","thread_note","correction","company_context","other"]},"scope":{"type":"string","enum":["global","thread"]},"pinned":{"type":"boolean"}},"additionalProperties":false}`),
			Call: func(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
				return save(ctx, raw, binding)
			},
		})
		binding, err = tools.Binding("memory_save")
		if err != nil {
			fmt.Fprintln(os.Stderr, "invalid memory save capability:", err)
			os.Exit(2)
		}
	}
	if enabled := os.Getenv("OPENNEKO_HARNESS_SKILL_CREATE"); enabled != "" {
		if enabled != "1" || recordsOnly == "1" || os.Getenv("OPENNEKO_MCP_MODE") != "work" {
			fmt.Fprintln(os.Stderr, "invalid skill create binding")
			os.Exit(2)
		}
		create, err := broker.SkillCreate(os.Getenv("OPENNEKO_BROKER_URL"), os.Getenv("OPENNEKO_BROKER_TOKEN"))
		if err != nil {
			fmt.Fprintln(os.Stderr, "skill create broker unavailable:", err)
			os.Exit(2)
		}
		var binding string
		tools.Capabilities = append(tools.Capabilities, agent.Capability{
			Name: "skill_create", Version: "1", Origin: "openneko", Effect: "durable",
			Description: "Create a new shared OpenNeko skill only when the operator asks. Use a new lowercase hyphenated name. Supporting files are staged with SKILL.md and published together. Skill files contain instructions and scripts; they must not make direct model calls. Existing skill names cannot be replaced by this tool.",
			InputSchema: json.RawMessage(`{"type":"object","required":["name","description","body"],"properties":{"name":{"type":"string","pattern":"^[a-z0-9]+(-[a-z0-9]+)*$","maxLength":64},"description":{"type":"string","minLength":1,"maxLength":1024},"body":{"type":"string","minLength":1,"maxLength":60000},"license":{"type":"string","maxLength":200},"compatibility":{"type":"string","maxLength":500},"metadata":{"type":"object","additionalProperties":{"type":"string","maxLength":2048}},"allowedTools":{"type":"string","maxLength":1000},"files":{"type":"array","maxItems":10,"items":{"type":"object","required":["path","content"],"properties":{"path":{"type":"string","minLength":1,"maxLength":240},"content":{"type":"string","maxLength":32000}},"additionalProperties":false}}},"additionalProperties":false}`),
			Call: func(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
				return create(ctx, raw, binding)
			},
		})
		binding, err = tools.Binding("skill_create")
		if err != nil {
			fmt.Fprintln(os.Stderr, "invalid skill create capability:", err)
			os.Exit(2)
		}
		inspect, err := broker.SkillInspect(os.Getenv("OPENNEKO_BROKER_URL"), os.Getenv("OPENNEKO_BROKER_TOKEN"))
		if err != nil {
			fmt.Fprintln(os.Stderr, "skill inspect broker unavailable:", err)
			os.Exit(2)
		}
		tools.Capabilities = append(tools.Capabilities, agent.Capability{
			Name: "skill_inspect", Version: "1", Origin: "openneko", Effect: "read",
			Description: "Read the current whole-tree version of an installed org skill before updating it. Read SKILL.md and supporting files with skill_read as needed; use the returned version as expectedVersion in skill_update.",
			InputSchema: json.RawMessage(`{"type":"object","required":["name"],"properties":{"name":{"type":"string","pattern":"^[a-z0-9]+(-[a-z0-9]+)*$","maxLength":64}},"additionalProperties":false}`),
			Call:        inspect,
		})
		update, err := broker.SkillUpdate(os.Getenv("OPENNEKO_BROKER_URL"), os.Getenv("OPENNEKO_BROKER_TOKEN"))
		if err != nil {
			fmt.Fprintln(os.Stderr, "skill update broker unavailable:", err)
			os.Exit(2)
		}
		var updateBinding string
		tools.Capabilities = append(tools.Capabilities, agent.Capability{
			Name: "skill_update", Version: "1", Origin: "openneko", Effect: "durable",
			Description: "Replace an existing org skill only when the admin operator asks. First call skill_inspect for a whole-tree version, then submit the complete new SKILL.md content and all supporting files with that exact expectedVersion. A concurrent change is rejected. Skill files must not make direct model calls.",
			InputSchema: json.RawMessage(`{"type":"object","required":["name","description","body","expectedVersion"],"properties":{"name":{"type":"string","pattern":"^[a-z0-9]+(-[a-z0-9]+)*$","maxLength":64},"description":{"type":"string","minLength":1,"maxLength":1024},"body":{"type":"string","minLength":1,"maxLength":60000},"expectedVersion":{"type":"string","pattern":"^[a-f0-9]{64}$"},"license":{"type":"string","maxLength":200},"compatibility":{"type":"string","maxLength":500},"metadata":{"type":"object","additionalProperties":{"type":"string","maxLength":2048}},"allowedTools":{"type":"string","maxLength":1000},"files":{"type":"array","maxItems":10,"items":{"type":"object","required":["path","content"],"properties":{"path":{"type":"string","minLength":1,"maxLength":240},"content":{"type":"string","maxLength":32000}},"additionalProperties":false}}},"additionalProperties":false}`),
			Call: func(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
				return update(ctx, raw, updateBinding)
			},
		})
		updateBinding, err = tools.Binding("skill_update")
		if err != nil {
			fmt.Fprintln(os.Stderr, "invalid skill update capability:", err)
			os.Exit(2)
		}
	}
	if enabled := os.Getenv("OPENNEKO_HARNESS_WORKFLOW_SAVE"); enabled != "" {
		if enabled != "1" || recordsOnly == "1" || os.Getenv("OPENNEKO_MCP_MODE") != "work" {
			fmt.Fprintln(os.Stderr, "invalid workflow save binding")
			os.Exit(2)
		}
		save, err := broker.WorkflowSave(os.Getenv("OPENNEKO_BROKER_URL"), os.Getenv("OPENNEKO_BROKER_TOKEN"))
		if err != nil {
			fmt.Fprintln(os.Stderr, "workflow save broker unavailable:", err)
			os.Exit(2)
		}
		var binding string
		tools.Capabilities = append(tools.Capabilities, agent.Capability{
			Name: "workflow_save", Version: "1", Origin: "openneko", Effect: "durable",
			Description: "Create or update an OpenNeko workflow only when the operator asks. For a new name use expectedVersion='absent'; for an edit first list workflows and use its exact versionToken. A changed definition is rejected. Supports steps, batch output, cron, source-change triggers and condition watches. Confirm source columns before a source-change trigger; a watch needs an exact GraphJin query and value path. Definition and requested triggers commit together. The host persists confirmation cards with the save receipt.",
			InputSchema: json.RawMessage(workflowSaveSchema),
			Call: func(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
				return save(ctx, raw, binding)
			},
		})
		binding, err = tools.Binding("workflow_save")
		if err != nil {
			fmt.Fprintln(os.Stderr, "invalid workflow save capability:", err)
			os.Exit(2)
		}
		deleteWorkflow, err := broker.WorkflowDelete(os.Getenv("OPENNEKO_BROKER_URL"), os.Getenv("OPENNEKO_BROKER_TOKEN"))
		if err != nil {
			fmt.Fprintln(os.Stderr, "workflow delete broker unavailable:", err)
			os.Exit(2)
		}
		var deleteBinding string
		tools.Capabilities = append(tools.Capabilities, agent.Capability{
			Name: "workflow_delete", Version: "1", Origin: "openneko", Effect: "durable",
			Description: "Permanently delete a workflow and its dependent triggers, runs, outputs and actions only after the operator sends the exact current-run message DELETE WORKFLOW <JSON-quoted workflow name> PERMANENTLY. First list workflows; pass its id, exact name and versionToken. If confirmation is missing, tell the operator the required phrase and stop. A changed workflow is rejected. The host independently verifies confirmation and persists a deletion card.",
			InputSchema: json.RawMessage(`{"type":"object","required":["workflowId","name","expectedVersion"],"properties":{"workflowId":{"type":"string","format":"uuid"},"name":{"type":"string","minLength":1,"maxLength":120},"expectedVersion":{"type":"string","pattern":"^[0-9]{1,12}$"}},"additionalProperties":false}`),
			Call: func(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
				return deleteWorkflow(ctx, raw, deleteBinding)
			},
		})
		deleteBinding, err = tools.Binding("workflow_delete")
		if err != nil {
			fmt.Fprintln(os.Stderr, "invalid workflow delete capability:", err)
			os.Exit(2)
		}
	}
	if enabled := os.Getenv("OPENNEKO_HARNESS_RULE_SAVE"); enabled != "" {
		if enabled != "1" || recordsOnly == "1" || os.Getenv("OPENNEKO_MCP_MODE") != "work" {
			fmt.Fprintln(os.Stderr, "invalid rule save binding")
			os.Exit(2)
		}
		save, err := broker.RuleSave(os.Getenv("OPENNEKO_BROKER_URL"), os.Getenv("OPENNEKO_BROKER_TOKEN"))
		if err != nil {
			fmt.Fprintln(os.Stderr, "rule save broker unavailable:", err)
			os.Exit(2)
		}
		var binding string
		tools.Capabilities = append(tools.Capabilities, agent.Capability{
			Name: "rule_save", Version: "1", Origin: "openneko", Effect: "durable",
			Description: "Create or update an approval rule only when the admin operator asks. Use expectedVersion='absent' for a new name; to edit, list rules first and use its exact versionToken. Rules can change action approval behavior, including auto-approval. The host checks current admin authority, journals the write and persists a confirmation card.",
			InputSchema: json.RawMessage(`{"type":"object","required":["name","applies_to_kinds","mode","expectedVersion"],"properties":{"name":{"type":"string","minLength":1,"maxLength":120},"description":{"type":"string","maxLength":2000},"applies_to_kinds":{"type":"array","maxItems":40,"items":{"type":"string","minLength":1,"maxLength":120}},"applies_to_scopes":{"type":"array","maxItems":8,"items":{"type":"string","enum":["internal","external"]}},"mode":{"type":"string","enum":["observe_only","draft_only","auto_approve","approval_required","never"]},"risk_threshold_auto_approve":{"type":"string","enum":["low","medium","high","critical"]},"allowed_targets":{"type":"object"},"denied_targets":{"type":"object"},"limits":{"type":"object"},"approver_role":{"type":"string","enum":["admin"]},"priority":{"type":"integer","minimum":0,"maximum":10000},"enabled":{"type":"boolean"},"expectedVersion":{"type":"string","pattern":"^(absent|[0-9]{1,12})$"}},"additionalProperties":false}`),
			Call: func(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
				return save(ctx, raw, binding)
			},
		})
		binding, err = tools.Binding("rule_save")
		if err != nil {
			fmt.Fprintln(os.Stderr, "invalid rule save capability:", err)
			os.Exit(2)
		}
	}
	if workflowRunID := os.Getenv("OPENNEKO_HARNESS_WORKFLOW_RUN_ID"); workflowRunID != "" {
		if recordsOnly == "1" {
			fmt.Fprintln(os.Stderr, "records-only run cannot emit workflow output")
			os.Exit(2)
		}
		emit, err := broker.WorkflowOutput(os.Getenv("OPENNEKO_BROKER_URL"), os.Getenv("OPENNEKO_BROKER_TOKEN"))
		if err != nil {
			fmt.Fprintln(os.Stderr, "workflow output broker unavailable:", err)
			os.Exit(2)
		}
		var binding string
		tools.Capabilities = append(tools.Capabilities, agent.Capability{
			Name: "workflow_output_emit", Version: "1", Origin: "openneko", Effect: "durable",
			Description: "Persist an output for this queued workflow run. Give the evidence, type, title, and honest mood; a final answer alone does not create an output.",
			InputSchema: json.RawMessage(`{"type":"object","required":["kind"],"properties":{"kind":{"type":"string","enum":["report","summary","briefing_card_proposal","chart","table","file","message_draft","finding","observation","recommendation"]},"title":{"type":"string","maxLength":240},"body":{"type":"string","maxLength":64000},"payload":{"type":"object"},"artifactPath":{"type":"string","maxLength":1024},"scope":{"type":"string","maxLength":120},"topic":{"type":"string","maxLength":120},"mood":{"type":"string","enum":["good","watch","act"]},"timeWindowStart":{"type":"string","format":"date-time"},"timeWindowEnd":{"type":"string","format":"date-time"},"freshnessTtlSeconds":{"type":"integer","minimum":1,"maximum":31536000}},"additionalProperties":false}`),
			Call: func(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
				return emit(ctx, raw, binding)
			},
		})
		binding, err = tools.Binding("workflow_output_emit")
		if err != nil {
			fmt.Fprintln(os.Stderr, "invalid workflow output capability:", err)
			os.Exit(2)
		}
		bindWorkflowOutputVerification(&tools, binding)
		tools.Scope += "\nworkflow:" + workflowRunID
	}
	var closeTools []func() error
	memoryRead := os.Getenv("OPENNEKO_HARNESS_MCP_MEMORY_READ")
	libraryRead := os.Getenv("OPENNEKO_HARNESS_MCP_LIBRARY_READ")
	recordsRead := os.Getenv("OPENNEKO_HARNESS_MCP_RECORDS_READ")
	workflowRead := os.Getenv("OPENNEKO_HARNESS_MCP_WORKFLOW_READ")
	managementRead := os.Getenv("OPENNEKO_HARNESS_MCP_MANAGEMENT_READ")
	auditRead := os.Getenv("OPENNEKO_HARNESS_MCP_AUDIT_READ")
	sourceConfigRead := os.Getenv("OPENNEKO_HARNESS_MCP_SOURCE_CONFIG_READ")
	interaction := os.Getenv("OPENNEKO_HARNESS_MCP_INTERACTION")
	cards := os.Getenv("OPENNEKO_HARNESS_MCP_CARDS")
	if (memoryRead != "" && memoryRead != "1") || (libraryRead != "" && libraryRead != "1") || (recordsRead != "" && recordsRead != "1") || (workflowRead != "" && workflowRead != "1") || (managementRead != "" && managementRead != "1") || (auditRead != "" && auditRead != "1") || (sourceConfigRead != "" && sourceConfigRead != "1") || (interaction != "" && interaction != "1") || (cards != "" && cards != "1") || (libraryRead == "1" && memoryRead != "1") || (recordsOnly == "1" && (memoryRead != "" || libraryRead != "" || workflowRead != "" || managementRead != "" || auditRead != "" || sourceConfigRead != "" || recordsRead != "1")) || ((managementRead == "1" || auditRead == "1" || sourceConfigRead == "1") && os.Getenv("OPENNEKO_MCP_MODE") != "work") {
		fmt.Fprintln(os.Stderr, "invalid read capability binding")
		os.Exit(2)
	}
	if memoryRead == "1" || recordsRead == "1" || workflowRead == "1" || managementRead == "1" || auditRead == "1" || sourceConfigRead == "1" || interaction == "1" || cards == "1" {
		capabilities, closeSession, connectErr := productmcp.ConnectReads(context.Background(), productmcp.ReadConfig{
			BridgePath: os.Getenv("OPENNEKO_MCP_BRIDGE"),
			BrokerURL:  os.Getenv("OPENNEKO_BROKER_URL"), BrokerToken: os.Getenv("OPENNEKO_BROKER_TOKEN"),
			OrgID: os.Getenv("OPENNEKO_MCP_ORG_ID"), ThreadID: os.Getenv("OPENNEKO_MCP_THREAD_ID"),
			RunID: os.Getenv("OPENNEKO_MCP_RUN_ID"), SkillsRoot: os.Getenv("OPENNEKO_MCP_SKILLS_ROOT"),
			Interaction: interaction == "1", Cards: cards == "1", Workflow: workflowRead == "1", Management: managementRead == "1", Audit: auditRead == "1", SourceConfig: sourceConfigRead == "1",
		}, memoryRead == "1", libraryRead == "1", recordsRead == "1")
		if connectErr != nil {
			fmt.Fprintln(os.Stderr, "read capabilities unavailable:", connectErr)
			os.Exit(2)
		}
		tools.Capabilities = append(tools.Capabilities, capabilities...)
		closeTools = append(closeTools, closeSession)
	}
	if dir := os.Getenv("OPENNEKO_HARNESS_WORKSPACE_DIR"); dir != "" {
		files, openErr := localtool.OpenFiles(dir)
		if openErr != nil {
			fmt.Fprintln(os.Stderr, "file workspace unavailable:", openErr)
			os.Exit(2)
		}
		tools.Capabilities = append(tools.Capabilities, files.Capabilities()...)
		tools.OnResume = files.Restore
		tools.Scope += "\nworkspace:" + dir
		closeTools = append(closeTools, files.Close)
	}
	if dir := os.Getenv("OPENNEKO_HARNESS_UPLOADS_DIR"); dir != "" {
		uploads, openErr := localtool.OpenFiles(dir)
		if openErr != nil {
			fmt.Fprintln(os.Stderr, "upload workspace unavailable:", openErr)
			os.Exit(2)
		}
		tools.Capabilities = append(tools.Capabilities, uploads.UploadCapabilities()...)
		tools.Scope += "\nuploads:" + dir
		closeTools = append(closeTools, uploads.Close)
	}
	if enabled := os.Getenv("OPENNEKO_HARNESS_SKILLS_READ"); enabled != "" {
		if enabled != "1" || recordsOnly == "1" {
			fmt.Fprintln(os.Stderr, "invalid skill capability binding")
			os.Exit(2)
		}
		skills, openErr := localtool.OpenFiles(os.Getenv("OPENNEKO_MCP_SKILLS_ROOT"))
		if openErr != nil {
			fmt.Fprintln(os.Stderr, "skill workspace unavailable:", openErr)
			os.Exit(2)
		}
		tools.Capabilities = append(tools.Capabilities, skills.SkillCapabilities()...)
		skillRoute, routeErr := command.RouteHasSkill(os.Getenv("HARNESS_MODEL_ROUTES"))
		if routeErr != nil {
			fmt.Fprintln(os.Stderr, "invalid Harness skill route:", routeErr)
			os.Exit(2)
		}
		if skillRoute {
			tools.SkillCatalog, openErr = skills.SkillCatalog()
			if openErr != nil {
				fmt.Fprintln(os.Stderr, "staged skill catalog unavailable:", openErr)
				os.Exit(2)
			}
		}
		tools.Scope += "\nskills:" + os.Getenv("OPENNEKO_MCP_SKILLS_ROOT")
		closeTools = append(closeTools, skills.Close)
	}
	if enabled := os.Getenv("OPENNEKO_HARNESS_PROCESS_RUN"); enabled != "" {
		mode := os.Getenv("OPENNEKO_MCP_MODE")
		if enabled != "1" || recordsOnly == "1" || (mode != "work" && mode != "workflow") {
			fmt.Fprintln(os.Stderr, "invalid isolated process capability binding")
			os.Exit(2)
		}
		run, bindErr := broker.ProcessRun(os.Getenv("OPENNEKO_BROKER_URL"), os.Getenv("OPENNEKO_BROKER_TOKEN"))
		if bindErr != nil {
			fmt.Fprintln(os.Stderr, "isolated process broker unavailable")
			os.Exit(2)
		}
		var binding string
		tools.Capabilities = append(tools.Capabilities, agent.Capability{
			Name: "process_run", Version: "1", Origin: "openneko", Effect: "durable",
			Description: "Run a bounded Python or shell script in a separate credential-free OpenShell sandbox. Select only named uploads as inputs and declare output files before execution. Successful files become run artifacts after the process completes.",
			InputSchema: json.RawMessage(`{"type":"object","required":["language","script","outputs"],"properties":{"language":{"type":"string","enum":["python","shell"]},"script":{"type":"string","minLength":1,"maxLength":65536},"uploads":{"type":"array","maxItems":16,"uniqueItems":true,"items":{"type":"string","minLength":1,"maxLength":128}},"outputs":{"type":"array","minItems":1,"maxItems":16,"uniqueItems":true,"items":{"type":"string","minLength":1,"maxLength":128}}},"additionalProperties":false}`),
			Call: func(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
				return run(ctx, raw, binding)
			},
		})
		var err error
		binding, err = tools.Binding("process_run")
		if err != nil {
			fmt.Fprintln(os.Stderr, "invalid isolated process capability:", err)
			os.Exit(2)
		}
		tools.Scope += "\nprocess:isolated-v1"
	}
	cleanup := func() error {
		var first error
		for _, closeTool := range closeTools {
			if err := closeTool(); err != nil && first == nil {
				first = err
			}
		}
		return first
	}
	command.MainWithToolsAndCleanup(tools, cleanup)
}

func admissionScope(orgID, threadID string) (string, error) {
	valid := func(id string) bool {
		return id != "" && len(id) <= 128 && strings.TrimSpace(id) == id &&
			!strings.ContainsAny(id, "\r\n\x00")
	}
	if !valid(orgID) || !valid(threadID) {
		return "", fmt.Errorf("missing or invalid OpenNeko admission scope")
	}
	return "org:" + orgID + "\nthread:" + threadID, nil
}

// This narrows the model-visible proposal tool to the host's run-scoped
// entitlements. The broker remains authoritative after roles or definitions change.
func admitActions(raw string, propose func(context.Context, agent.Proposal) (agent.ProposalReceipt, error)) (func(context.Context, agent.Proposal) (agent.ProposalReceipt, error), error) {
	var kinds []string
	if len(raw) > 8192 || json.Unmarshal([]byte(raw), &kinds) != nil || len(kinds) == 0 || len(kinds) > 64 || propose == nil {
		return nil, fmt.Errorf("invalid action admission")
	}
	allowed := make(map[string]bool, len(kinds))
	for _, kind := range kinds {
		if kind == "" || len(kind) > 128 || allowed[kind] {
			return nil, fmt.Errorf("invalid action admission")
		}
		allowed[kind] = true
	}
	return func(ctx context.Context, proposal agent.Proposal) (agent.ProposalReceipt, error) {
		if !allowed[proposal.Action] {
			return agent.ProposalReceipt{Status: "denied", Reason: "Action was not admitted for this run"}, nil
		}
		return propose(ctx, proposal)
	}, nil
}
