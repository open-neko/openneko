package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/open-neko/harness/adapters/openneko/broker"
	productmcp "github.com/open-neko/harness/adapters/openneko/mcp"
	"github.com/open-neko/harness/internal/agent"
	"github.com/open-neko/harness/internal/command"
	"github.com/open-neko/harness/internal/localtool"
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
	var lookup func(context.Context, string) (json.RawMessage, error)
	if recordsOnly != "1" && lookupRead != "0" {
		var err error
		lookup, err = broker.GraphJin(os.Getenv("OPENNEKO_BROKER_URL"), os.Getenv("OPENNEKO_BROKER_TOKEN"), os.Getenv("OPENNEKO_DATA_SOURCE_ID"))
		if err != nil {
			fmt.Fprintln(os.Stderr, "invalid GraphJin capability binding")
			os.Exit(2)
		}
	}
	tools := agent.Tools{Lookup: lookup}
	if child := os.Getenv("OPENNEKO_HARNESS_CHILD_READS"); child != "" {
		tools.ChildReads = strings.Split(child, ",")
	}
	if recordsOnly == "1" {
		tools.Scope = "records-only"
	}
	if kinds := os.Getenv("OPENNEKO_HARNESS_ACTION_KINDS"); kinds != "" {
		if recordsOnly == "1" {
			fmt.Fprintln(os.Stderr, "records-only run cannot admit pack actions")
			os.Exit(2)
		}
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
		tools.Scope = kinds
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
		tools.Scope += "\nworkflow:" + workflowRunID
	}
	var closeTools []func() error
	memoryRead := os.Getenv("OPENNEKO_HARNESS_MCP_MEMORY_READ")
	libraryRead := os.Getenv("OPENNEKO_HARNESS_MCP_LIBRARY_READ")
	recordsRead := os.Getenv("OPENNEKO_HARNESS_MCP_RECORDS_READ")
	workflowRead := os.Getenv("OPENNEKO_HARNESS_MCP_WORKFLOW_READ")
	interaction := os.Getenv("OPENNEKO_HARNESS_MCP_INTERACTION")
	cards := os.Getenv("OPENNEKO_HARNESS_MCP_CARDS")
	if (memoryRead != "" && memoryRead != "1") || (libraryRead != "" && libraryRead != "1") || (recordsRead != "" && recordsRead != "1") || (workflowRead != "" && workflowRead != "1") || (interaction != "" && interaction != "1") || (cards != "" && cards != "1") || (libraryRead == "1" && memoryRead != "1") || (recordsOnly == "1" && (memoryRead != "" || libraryRead != "" || workflowRead != "" || recordsRead != "1")) {
		fmt.Fprintln(os.Stderr, "invalid read capability binding")
		os.Exit(2)
	}
	if memoryRead == "1" || recordsRead == "1" || workflowRead == "1" || interaction == "1" || cards == "1" {
		capabilities, closeSession, connectErr := productmcp.ConnectReads(context.Background(), productmcp.ReadConfig{
			BridgePath: os.Getenv("OPENNEKO_MCP_BRIDGE"),
			BrokerURL:  os.Getenv("OPENNEKO_BROKER_URL"), BrokerToken: os.Getenv("OPENNEKO_BROKER_TOKEN"),
			OrgID: os.Getenv("OPENNEKO_MCP_ORG_ID"), ThreadID: os.Getenv("OPENNEKO_MCP_THREAD_ID"),
			RunID: os.Getenv("OPENNEKO_MCP_RUN_ID"), SkillsRoot: os.Getenv("OPENNEKO_MCP_SKILLS_ROOT"),
			Interaction: interaction == "1", Cards: cards == "1", Workflow: workflowRead == "1",
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
		tools.Scope += "\nskills:" + os.Getenv("OPENNEKO_MCP_SKILLS_ROOT")
		closeTools = append(closeTools, skills.Close)
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
