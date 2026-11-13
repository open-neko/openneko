package llm

import (
	"context"
	"encoding/json"
	"math"
	"strings"
)

// AxModelRoute is AxModelRoute in packages/llm/src/provider-runtime.ts.
type AxModelRoute struct {
	Provider string
	Model    string
	URL      string
	KeyEnv   string
	Options  map[string]string
	Price    *Price
}

// AxBackendConfig is AxBackendConfig in packages/llm/src/agent-backends/ax.ts.
type AxBackendConfig struct {
	Route               AxModelRoute
	Fallback            *AxModelRoute
	ContextWindowTokens int
	MaxOutputTokens     int
	ReasoningEffort     string
}

// ResolveAxModelRoute is resolveAxModelRoute().
func ResolveAxModelRoute(provider, model string, config map[string]any) (AxModelRoute, error) {
	route, err := AxRoute(Settings{Provider: provider, Model: model, Config: config})
	if err != nil {
		return AxModelRoute{}, err
	}
	rt, err := ResolveRuntime(provider, model, config)
	if err != nil {
		return AxModelRoute{}, err
	}
	return AxModelRoute{Provider: route.Provider, Model: route.Model, URL: route.URL, KeyEnv: rt.KeyEnv, Options: route.Options}, nil
}

func positiveInt(v any) int {
	f, ok := v.(float64)
	if !ok || f <= 0 || f != math.Trunc(f) || f > math.MaxInt32 {
		return 0
	}
	return int(f)
}

// IsReasoningEffort is isReasoningEffort() in packages/llm/src/agent-limits.ts.
func IsReasoningEffort(v any) bool {
	s, _ := v.(string)
	return s == "low" || s == "medium" || s == "high"
}

func axBackendConfig(provider string, model *string, enabled bool, config map[string]any) (*AxBackendConfig, error) {
	if model == nil || strings.TrimSpace(*model) == "" || !enabled || !IsPrimaryProvider(provider) {
		return nil, nil
	}
	if config == nil {
		config = map[string]any{}
	}
	route, err := ResolveAxModelRoute(provider, strings.TrimSpace(*model), config)
	if err != nil {
		return nil, err
	}
	effort := "medium"
	if IsReasoningEffort(config["reasoning_effort"]) {
		effort = config["reasoning_effort"].(string)
	}
	return &AxBackendConfig{Route: route, ContextWindowTokens: positiveInt(config["context_window_tokens"]),
		MaxOutputTokens: positiveInt(config["max_output_tokens"]), ReasoningEffort: effort}, nil
}

// ResolveAgentBackend is resolveAgentBackend() in
// packages/llm/src/agent-backend-resolver.ts: the "turn" task's model record
// wins over the primary provider row, and its fallback record joins it. Nil
// means no model is configured. Keys never travel with it.
func ResolveAgentBackend(ctx context.Context, db DB, orgID string) (*AxBackendConfig, error) {
	var (
		provider string
		model    *string
		enabled  bool
		raw      []byte
	)
	err := db.QueryRow(ctx, `select provider, model, enabled, config from llm_provider_config
	  where org_id = $1 and scope = 'primary' limit 1`, orgID).Scan(&provider, &model, &enabled, &raw)
	found := err == nil
	err = nil
	var config map[string]any
	_ = json.Unmarshal(raw, &config)
	_, primary, fallback, routeErr := TaskRouteRecords(ctx, db, orgID, "turn")
	if routeErr != nil {
		primary, fallback = nil, nil
	}
	var ax *AxBackendConfig
	switch {
	case primary != nil:
		m := primary.Model
		ax, err = axBackendConfig(primary.Provider, &m, primary.Enabled, primary.Config)
	case found:
		ax, err = axBackendConfig(provider, model, enabled, config)
	}
	if err != nil || ax == nil {
		return nil, err
	}
	if primary != nil && primary.Price != nil {
		price := *primary.Price
		ax.Route.Price = &price
	}
	if fallback != nil && IsPrimaryProvider(fallback.Provider) {
		cfg := fallback.Config
		if cfg == nil {
			cfg = map[string]any{}
		}
		if r, err := ResolveAxModelRoute(fallback.Provider, fallback.Model, cfg); err == nil {
			r.Price = fallback.Price
			ax.Fallback = &r
		}
	}
	return ax, nil
}
