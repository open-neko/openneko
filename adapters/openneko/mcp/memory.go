package mcp

import (
	"context"
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
}

const searchSchema = `{"$schema":"http://json-schema.org/draft-07/schema#","properties":{"limit":{"maximum":20,"minimum":1,"type":"integer"},"query":{"maxLength":800,"minLength":2,"type":"string"}},"required":["query"],"type":"object"}`

// ConnectReads admits pinned read-only tools from OpenNeko's stdio bridge.
// Discovery must match the pinned schemas; bridge content cannot grant tools.
func ConnectReads(ctx context.Context, cfg ReadConfig, library bool) ([]agent.Capability, func() error, error) {
	u, err := url.Parse(cfg.BrokerURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || cfg.BrokerToken == "" || cfg.OrgID == "" || cfg.ThreadID == "" || cfg.RunID == "" || cfg.SkillsRoot == "" || !filepath.IsAbs(cfg.BridgePath) {
		return nil, nil, fmt.Errorf("invalid OpenNeko read bridge binding")
	}
	if info, err := os.Stat(cfg.BridgePath); err != nil || !info.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("OpenNeko read bridge unavailable")
	}
	servers := "neko_memory"
	allowed := []shared.Admission{{
		Name: "memory_search", Alias: "mcp_memory_search", Version: "1", Origin: "openneko",
		Effect: "read", Description: "Search the current run's saved memories.", Schema: json.RawMessage(searchSchema),
	}}
	if library {
		servers += ",neko_library"
		allowed = append(allowed, shared.Admission{
			Name: "library_search", Alias: "mcp_library_search", Version: "1", Origin: "openneko",
			Effect: "read", Description: "Search authorized document-library concepts.", Schema: json.RawMessage(searchSchema),
		})
	}
	args := []string{cfg.BridgePath, servers}
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
