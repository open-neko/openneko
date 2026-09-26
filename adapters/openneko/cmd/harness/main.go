package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/open-neko/harness/adapters/openneko/broker"
	productmcp "github.com/open-neko/harness/adapters/openneko/mcp"
	"github.com/open-neko/harness/internal/agent"
	"github.com/open-neko/harness/internal/command"
	"github.com/open-neko/harness/internal/localtool"
)

func main() {
	lookup, err := broker.GraphJin(os.Getenv("OPENNEKO_BROKER_URL"), os.Getenv("OPENNEKO_BROKER_TOKEN"), os.Getenv("OPENNEKO_DATA_SOURCE_ID"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid GraphJin capability binding")
		os.Exit(2)
	}
	tools := agent.Tools{Lookup: lookup}
	if kinds := os.Getenv("OPENNEKO_HARNESS_ACTION_KINDS"); kinds != "" {
		propose, bindErr := broker.Propose(os.Getenv("OPENNEKO_BROKER_URL"), os.Getenv("OPENNEKO_BROKER_TOKEN"))
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
	var closeTools []func() error
	if flag := os.Getenv("OPENNEKO_HARNESS_MCP_MEMORY_READ"); flag != "" {
		if flag != "1" {
			fmt.Fprintln(os.Stderr, "invalid memory capability binding")
			os.Exit(2)
		}
		capabilities, closeSession, connectErr := productmcp.ConnectMemory(context.Background(), productmcp.MemoryConfig{
			BridgePath: os.Getenv("OPENNEKO_MCP_BRIDGE"),
			BrokerURL:  os.Getenv("OPENNEKO_BROKER_URL"), BrokerToken: os.Getenv("OPENNEKO_BROKER_TOKEN"),
			OrgID: os.Getenv("OPENNEKO_MCP_ORG_ID"), ThreadID: os.Getenv("OPENNEKO_MCP_THREAD_ID"),
			RunID: os.Getenv("OPENNEKO_MCP_RUN_ID"), SkillsRoot: os.Getenv("OPENNEKO_MCP_SKILLS_ROOT"),
		})
		if connectErr != nil {
			fmt.Fprintln(os.Stderr, "memory capability unavailable:", connectErr)
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
