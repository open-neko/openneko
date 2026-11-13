package webfront

import (
	"context"
	"log"
	"os"
	"strings"

	"github.com/open-neko/neko/apps/openneko/internal/llm"
)

// agentCoreInput is RunAgentBackendInput in packages/llm/src/work/agent-core.ts,
// with what the runner process held: the model keys, the serve socket and
// whether the org's terminal reaches a sandbox.
type agentCoreInput struct {
	Backend                *llm.AxBackendConfig
	Prompt, UserMessage    string
	OrgID, ThreadID, RunID string
	Workspace              agentWorkspace
	BackendState           any
	PackActions            []packActionDescriptor
	SourceConfigEnabled    bool
	GraphjinToolPolicy     string
	NativeDelegationOff    bool
	CoreCards              bool
	CardsOff               bool
	Shell                  bool
	Keys                   map[string]string
	Socket                 string
	Emit                   func(ctx context.Context, event any) error
	Workflow               *workflowCoreInput
	FilesEnabled           func() bool
	toolBackend            func(turnBinding) toolBackend
	startData              func(turnBinding, toolBackend) (*dataSocket, error)
	installData            func(binRoot, socket string) error
	afterTools             func(*toolServer)
	withoutDataSocket      bool
}

// workflowCoreInput is what a workflow turn adds to the core: its run, its
// servers and its limits.
type workflowCoreInput struct {
	WorkflowRunID, ObservationID string
	Servers                      []string
	NetworkHosts                 []string
	TimeoutMs, MaxToolIterations int
	ReasoningEffort              string
}

func (in agentCoreInput) binding() turnBinding {
	if w := in.Workflow; w != nil {
		return turnBinding{OrgID: in.OrgID, ThreadID: in.ThreadID, RunID: in.RunID, Kind: "workflow",
			CardSchema: pick(!in.CardsOff, "rich", ""), SkillsRoot: in.Workspace.SkillsRoot,
			WorkflowRunID: w.WorkflowRunID, ObservationID: w.ObservationID}
	}
	schema := "rich"
	if in.CoreCards {
		schema = "core"
	}
	return turnBinding{OrgID: in.OrgID, ThreadID: in.ThreadID, RunID: in.RunID, Kind: "work",
		GraphjinToolPolicy: in.GraphjinToolPolicy, WantsCards: !in.CardsOff, CardSchema: schema,
		SkillsRoot: in.Workspace.SkillsRoot, PackActions: in.PackActions}
}

func filesEnabled() bool { return strings.TrimSpace(os.Getenv("OPENNEKO_SERVE_URL")) != "" }

// agentCoreServers is the logical servers runAgentBackend() mounts for a
// work turn, in its order. The card server ships only to a turn that renders
// cards; the pack action server only when one action is not denied.
func agentCoreServers(in agentCoreInput, packTools int) []string {
	servers := []string{"neko_interaction", "neko_graphjin"}
	if !in.CardsOff {
		servers = append(servers, "neko_ui")
	}
	servers = append(servers, "neko_skills", "neko_memory", "neko_guides")
	files := filesEnabled
	if in.FilesEnabled != nil {
		files = in.FilesEnabled
	}
	if files() {
		servers = append(servers, "neko_files")
	}
	servers = append(servers, "neko_workflow_builder", "neko_rule_builder", "neko_user_manager",
		"neko_channel_manager", "neko_data_source_manager")
	if in.SourceConfigEnabled {
		servers = append(servers, "neko_source_config_manager")
	}
	servers = append(servers, "neko_audit")
	if packTools > 0 {
		servers = append(servers, "neko_pack_actions")
	}
	return servers
}

// runAgentCore is runAgentBackend() with the runner's part of entry.ts: the
// run's tool server answers serve's bridge stream in this process, and skill
// scripts reach the run's neko_graphjin tools on the data socket.
func (a *API) runAgentCore(ctx context.Context, in agentCoreInput) (axRunResult, error) {
	b := in.binding()
	var be toolBackend
	if in.toolBackend != nil {
		be = in.toolBackend(b)
	} else {
		rt := a.runTools(b)
		rt.events = in.Emit
		be = rt
	}
	var servers []string
	if in.Workflow != nil {
		servers = in.Workflow.Servers
	} else {
		servers = agentCoreServers(in, len(packActionTools(b, be)))
	}
	tools, err := turnTools(b, be, servers)
	if err != nil {
		return axRunResult{}, err
	}
	if in.afterTools != nil {
		in.afterTools(tools)
	}
	pythonPath := ""
	if !in.withoutDataSocket {
		start := in.startData
		if start == nil {
			start = func(b turnBinding, be toolBackend) (*dataSocket, error) { return startDataSocket("", b, be) }
		}
		install := in.installData
		if install == nil {
			install = installDataClient
		}
		socket, err := start(b, be)
		if err != nil {
			return axRunResult{}, err
		}
		defer func() {
			if err := socket.close(); err != nil {
				log.Printf("[work-run] data socket close failed: %v", err)
			}
		}()
		if err := install(in.Workspace.BinRoot, socket.path); err != nil {
			return axRunResult{}, err
		}
		pythonPath = in.Workspace.BinRoot
		if p := os.Getenv("PYTHONPATH"); p != "" {
			pythonPath += ":" + p
		}
	}
	ws := in.Workspace
	o := axTurnOptions{
		Prompt: in.Prompt, UserMessage: in.UserMessage, OrgID: in.OrgID, ThreadID: in.ThreadID, Tag: "work " + in.RunID,
		Workspace: &ws, BackendState: in.BackendState, WantsCards: !in.CardsOff, RichCards: !in.CoreCards,
		Servers: servers, Tools: tools, NativeDelegationOff: in.NativeDelegationOff, Shell: in.Shell,
		PythonPath: pythonPath, Keys: in.Keys, Socket: in.Socket, Emit: in.Emit,
	}
	if w := in.Workflow; w != nil {
		o.Tag, o.NetworkHosts = "workflow "+in.RunID, w.NetworkHosts
		o.TimeoutMs, o.MaxToolIterations, o.ReasoningEffort = w.TimeoutMs, w.MaxToolIterations, w.ReasoningEffort
	}
	return runAx(ctx, in.Backend, o)
}
