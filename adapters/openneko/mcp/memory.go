package mcp

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	protocol "github.com/modelcontextprotocol/go-sdk/mcp"
	shared "github.com/open-neko/harness/adapters/mcp"
	"github.com/open-neko/harness/internal/agent"
)

// ReadConfig is trusted launch context, never a model-selected tool argument.
type ReadConfig struct {
	BridgePath, BrokerURL, BrokerToken, OrgID, ThreadID, RunID, SkillsRoot string
	Interaction, Cards, Workflow                                           bool
	Management                                                             bool
}

//go:embed interaction_schemas.json
var interactionSchemas []byte

const searchSchema = `{"$schema":"http://json-schema.org/draft-07/schema#","properties":{"limit":{"maximum":20,"minimum":1,"type":"integer"},"query":{"maxLength":800,"minLength":2,"type":"string"}},"required":["query"],"type":"object"}`
const workflowListSchema = `{"$schema":"http://json-schema.org/draft-07/schema#","properties":{"limit":{"maximum":200,"minimum":1,"type":"integer"}},"type":"object"}`
const emptySchema = `{"$schema":"http://json-schema.org/draft-07/schema#","properties":{},"type":"object"}`
const recordsCatalogSchema = `{"$schema":"http://json-schema.org/draft-07/schema#","properties":{"app":{"maxLength":63,"minLength":1,"type":"string"}},"type":"object"}`
const recordsFindSchema = `{"$schema":"http://json-schema.org/draft-07/schema#","properties":{"after":{"maxLength":4096,"minLength":1,"type":"string"},"app":{"maxLength":63,"minLength":1,"type":"string"},"filters":{"items":{"properties":{"field":{"maxLength":63,"minLength":1,"type":"string"},"operator":{"enum":["eq","neq","in","contains","starts_with","is_null"],"type":"string"},"value":{}},"required":["field","operator"],"type":"object"},"maxItems":20,"type":"array"},"first":{"maximum":50,"minimum":1,"type":"integer"},"myRecords":{"type":"boolean"},"object":{"maxLength":63,"minLength":1,"type":"string"},"search":{"maxLength":200,"minLength":1,"type":"string"},"sort":{"properties":{"direction":{"enum":["asc","desc"],"type":"string"},"field":{"maxLength":63,"minLength":1,"type":"string"}},"required":["field","direction"],"type":"object"}},"required":["app","object"],"type":"object"}`
const recordsGetSchema = `{"$schema":"http://json-schema.org/draft-07/schema#","properties":{"allFields":{"type":"boolean"},"app":{"maxLength":63,"minLength":1,"type":"string"},"id":{"maxLength":512,"minLength":1,"type":"string"},"object":{"maxLength":63,"minLength":1,"type":"string"}},"required":["app","object","id"],"type":"object"}`
const recordsBlueprintSchema = `{"$schema":"http://json-schema.org/draft-07/schema#","properties":{"blueprint":{"description":"Exact id returned by the listing call. Omit only when listing available blueprints.","maxLength":63,"minLength":1,"type":"string"}},"type":"object"}`
const recordsRecycleFindSchema = `{"$schema":"http://json-schema.org/draft-07/schema#","properties":{"after":{"maxLength":4096,"minLength":1,"type":"string"},"app":{"maxLength":63,"minLength":1,"type":"string"},"first":{"maximum":50,"minimum":1,"type":"integer"},"object":{"maxLength":63,"minLength":1,"type":"string"},"search":{"maxLength":200,"minLength":1,"type":"string"}},"required":["app","object"],"type":"object"}`
const recordsRecycleGetSchema = `{"$schema":"http://json-schema.org/draft-07/schema#","properties":{"app":{"maxLength":63,"minLength":1,"type":"string"},"id":{"maxLength":512,"minLength":1,"type":"string"},"object":{"maxLength":63,"minLength":1,"type":"string"}},"required":["app","object","id"],"type":"object"}`

// ConnectReads admits pinned read-only tools from OpenNeko's stdio bridge.
// Discovery must match the pinned schemas; bridge content cannot grant tools.
func ConnectReads(ctx context.Context, cfg ReadConfig, memory, library, records bool) ([]agent.Capability, func() error, error) {
	u, err := url.Parse(cfg.BrokerURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || cfg.BrokerToken == "" || cfg.OrgID == "" || cfg.ThreadID == "" || cfg.RunID == "" || cfg.SkillsRoot == "" || !filepath.IsAbs(cfg.BridgePath) || (!memory && !library && !records && !cfg.Interaction && !cfg.Cards && !cfg.Workflow && !cfg.Management) || (library && !memory) {
		return nil, nil, fmt.Errorf("invalid OpenNeko read bridge binding")
	}
	if info, err := os.Stat(cfg.BridgePath); err != nil || !info.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("OpenNeko read bridge unavailable")
	}
	var servers []string
	var allowed []shared.Admission
	if memory {
		servers = append(servers, "neko_memory")
		allowed = append(allowed, shared.Admission{
			Name: "memory_search", Alias: "mcp_memory_search", Version: "1", Origin: "openneko",
			Effect: "read", Description: "Search the current run's saved memories.", Schema: json.RawMessage(searchSchema),
		})
	}
	if library {
		servers = append(servers, "neko_library")
		allowed = append(allowed, shared.Admission{
			Name: "library_search", Alias: "mcp_library_search", Version: "1", Origin: "openneko",
			Effect: "read", Description: "Search authorized document-library concepts.", Schema: json.RawMessage(searchSchema),
		})
	}
	if records {
		servers = append(servers, "neko_records")
		allowed = append(allowed,
			shared.Admission{Name: "records_browse_catalog", Alias: "mcp_neko_records_browse_catalog", Version: "1", Origin: "openneko", Effect: "read", Description: "Browse the actor's readable records apps, objects, fields and grants.", Schema: json.RawMessage(recordsCatalogSchema)},
			shared.Admission{Name: "records_browse_blueprints", Alias: "mcp_neko_records_browse_blueprints", Version: "1", Origin: "openneko", Effect: "read", Description: "List or load a shipped records app blueprint by exact id.", Schema: json.RawMessage(recordsBlueprintSchema)},
			shared.Admission{Name: "records_find_records", Alias: "mcp_neko_records_find_records", Version: "1", Origin: "openneko", Effect: "read", Description: "Find records through the registry under the current actor's permissions.", Schema: json.RawMessage(recordsFindSchema)},
			shared.Admission{Name: "records_get_record", Alias: "mcp_neko_records_get_record", Version: "1", Origin: "openneko", Effect: "read", Description: "Read one record by an exact id returned by find_records.", Schema: json.RawMessage(recordsGetSchema)},
			shared.Admission{Name: "records_find_recycled_records", Alias: "mcp_neko_records_find_recycled_records", Version: "1", Origin: "openneko", Effect: "read", Description: "Find soft-deleted record summaries under the current actor's permissions.", Schema: json.RawMessage(recordsRecycleFindSchema)},
			shared.Admission{Name: "records_get_recycled_record", Alias: "mcp_neko_records_get_recycled_record", Version: "1", Origin: "openneko", Effect: "read", Description: "Read one soft-deleted record summary by an exact id returned by find_recycled_records.", Schema: json.RawMessage(recordsRecycleGetSchema)},
		)
	}
	if cfg.Workflow {
		servers = append(servers, "neko_workflow_builder")
		allowed = append(allowed, shared.Admission{Name: "workflow_builder_list_workflows", Alias: "mcp_neko_workflow_builder_list_workflows", Version: "1", Origin: "openneko", Effect: "read", Description: "List workflows visible to the current Work-run actor, including their saved steps and triggers.", Schema: json.RawMessage(workflowListSchema)})
	}
	if cfg.Management {
		servers = append(servers, "neko_plugin_manager", "neko_user_manager", "neko_channel_manager", "neko_data_source_manager", "neko_rule_builder")
		allowed = append(allowed,
			shared.Admission{Name: "plugin_manager_list_plugins", Alias: "mcp_neko_plugin_manager_list_plugins", Version: "1", Origin: "openneko", Effect: "read", Description: "List installed plugins and available marketplace entries without changing them.", Schema: json.RawMessage(emptySchema)},
			shared.Admission{Name: "user_manager_list_users", Alias: "mcp_neko_user_manager_list_users", Version: "1", Origin: "openneko", Effect: "read", Description: "List organization users and synchronized groups for the bound Work run.", Schema: json.RawMessage(emptySchema)},
			shared.Admission{Name: "user_manager_list_groups", Alias: "mcp_neko_user_manager_list_groups", Version: "1", Origin: "openneko", Effect: "read", Description: "List organization groups, memberships and grants.", Schema: json.RawMessage(emptySchema)},
			shared.Admission{Name: "channel_manager_list_channels", Alias: "mcp_neko_channel_manager_list_channels", Version: "1", Origin: "openneko", Effect: "read", Description: "List organization channel identities and links.", Schema: json.RawMessage(emptySchema)},
			shared.Admission{Name: "data_source_manager_list_data_sources", Alias: "mcp_neko_data_source_manager_list_data_sources", Version: "1", Origin: "openneko", Effect: "read", Description: "List registered data sources without credentials.", Schema: json.RawMessage(emptySchema)},
			shared.Admission{Name: "rule_builder_list_rules", Alias: "mcp_neko_rule_builder_list_rules", Version: "1", Origin: "openneko", Effect: "read", Description: "List organization action rules without changing them.", Schema: json.RawMessage(workflowListSchema)},
		)
	}
	if cfg.Interaction || cfg.Cards {
		var schemas map[string]json.RawMessage
		if err := json.Unmarshal(interactionSchemas, &schemas); err != nil {
			return nil, nil, fmt.Errorf("invalid pinned interaction schemas: %w", err)
		}
		if cfg.Interaction {
			servers = append(servers, "neko_interaction")
			allowed = append(allowed, shared.Admission{Name: "interaction_ask_user_question", Alias: "mcp_neko_interaction_ask_user_question", Version: "1", Origin: "openneko", Effect: "pause", Description: "Ask the operator for missing information and end this turn. The next answer starts a new run.", Schema: schemas["ask_user_question"]})
		}
		if cfg.Cards {
			servers = append(servers, "neko_ui")
			allowed = append(allowed, shared.Admission{Name: "ui_render_cards", Alias: "mcp_neko_ui_render_cards", Version: "1", Origin: "openneko", Effect: "interaction", Description: "Render a validated Work card for this run.", Schema: schemas["render_cards"]})
		}
	}
	args := []string{cfg.BridgePath, strings.Join(servers, ",")}
	if strings.HasSuffix(cfg.BridgePath, ".ts") {
		args = append([]string{"--import", "tsx"}, args...)
	}
	cmd := exec.Command("node", args...)
	cmd.Dir = filepath.Dir(cfg.BridgePath)
	// Do not pass the model placeholder or other inherited credentials to Node.
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"OPENNEKO_BROKER_URL=" + cfg.BrokerURL,
		"OPENNEKO_BROKER_TOKEN=" + cfg.BrokerToken,
		"OPENNEKO_MCP_MODE=work",
		"OPENNEKO_MCP_ORG_ID=" + cfg.OrgID,
		"OPENNEKO_MCP_THREAD_ID=" + cfg.ThreadID,
		"OPENNEKO_MCP_RUN_ID=" + cfg.RunID,
		"OPENNEKO_MCP_SKILLS_ROOT=" + cfg.SkillsRoot,
		"OPENNEKO_MCP_WANTS_CARDS=" + map[bool]string{true: "1", false: "0"}[cfg.Cards],
		"OPENNEKO_MCP_PLUGIN_ACTIONS=[]",
		"OPENNEKO_MCP_PACK_ACTIONS=[]",
		"OPENNEKO_MCP_MEMORY_READ_ONLY=1",
	}
	for _, name := range []string{"ALL_PROXY", "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "no_proxy", "NODE_USE_ENV_PROXY"} {
		if value := os.Getenv(name); value != "" {
			cmd.Env = append(cmd.Env, name+"="+value)
		}
	}
	client := protocol.NewClient(&protocol.Implementation{Name: "openneko-harness", Version: "1"}, nil)
	session, err := client.Connect(ctx, &protocol.CommandTransport{Command: cmd}, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("OpenNeko read bridge connection failed: %w", err)
	}
	capabilities, err := shared.Admit(ctx, session, allowed)
	if err != nil {
		_ = session.Close()
		return nil, nil, fmt.Errorf("OpenNeko read admission failed: %w", err)
	}
	return capabilities, session.Close, nil
}
