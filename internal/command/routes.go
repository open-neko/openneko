package command

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strings"

	ax "github.com/ax-llm/ax/packages/go"
	"github.com/open-neko/harness/internal/agent"
	"github.com/open-neko/harness/internal/budgettriage"
)

type modelRoute struct {
	Key       string            `json:"key"`
	Model     string            `json:"model"`
	URL       string            `json:"url"`
	APIKeyEnv string            `json:"api_key_env"`
	Price     *agent.TokenPrice `json:"price,omitempty"`
}

type modelFallback struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type routeConfig struct {
	Context             string               `json:"context"`
	Executor            string               `json:"executor"`
	ExecutorEscalation  string               `json:"executor_escalation,omitempty"`
	ExecutorAfterErrors int                  `json:"executor_after_errors,omitempty"`
	Responder           string               `json:"responder"`
	Skill               string               `json:"skill,omitempty"`
	Triage              string               `json:"triage,omitempty"`
	BudgetPolicy        *budgettriage.Policy `json:"budget_policy,omitempty"`
	Fallbacks           []modelFallback      `json:"fallbacks,omitempty"`
	Routes              []modelRoute         `json:"routes"`
	PricingVersion      string               `json:"pricing_version,omitempty"`
	GraphJinPrice       *agent.TokenPrice    `json:"graphjin_price,omitempty"`
}

// loadModelClient reads a host-owned allowlist. The run JSON cannot add a
// provider, change a stage model, or name a credential. Keys remain outside the
// config and are never included in the checkpoint digest.
func loadModelClient(getenv func(string) string) (ax.AIClient, string, error) {
	raw := getenv("HARNESS_MODEL_ROUTES")
	if raw == "" {
		base, model, key := getenv("HARNESS_MODEL_URL"), getenv("HARNESS_MODEL"), getenv("HARNESS_MODEL_API_KEY")
		if err := validModelURL(base); err != nil || model == "" || key == "" {
			return nil, "", fmt.Errorf("configure HARNESS_MODEL_URL, HARNESS_MODEL and HARNESS_MODEL_API_KEY")
		}
		return ax.NewOpenAICompatibleClient(ax.Object("base_url", base, "api_key", key, "model", model)), "", nil
	}
	cfg, digest, err := parseRouteConfig(raw)
	if err != nil {
		return nil, "", err
	}
	entries := make([]ax.Value, 0, len(cfg.Routes))
	for _, route := range cfg.Routes {
		if getenv(route.APIKeyEnv) == "" {
			return nil, "", fmt.Errorf("HARNESS_MODEL_ROUTES route missing credential")
		}
		service := ax.NewOpenAICompatibleClient(ax.Object("base_url", route.URL, "api_key", getenv(route.APIKeyEnv), "model", route.Model))
		// A logical key can distinguish two accounts that expose the same model.
		// Ax's explicit router entry strips the key before the provider call, so
		// the provider uses its configured actual model.
		service.Name = route.Key
		entries = append(entries, ax.RouterServiceEntry{Key: route.Key, Description: route.Model, Service: service})
	}
	router, err := ax.NewMultiServiceRouter(entries)
	if err != nil {
		return nil, "", fmt.Errorf("invalid HARNESS_MODEL_ROUTES: %w", err)
	}
	prices := make(map[string]agent.TokenPrice, len(cfg.Routes))
	fallbacks := make(map[string]string, len(cfg.Fallbacks))
	for _, route := range cfg.Routes {
		if route.Price != nil {
			prices[route.Key] = *route.Price
		}
	}
	for _, fallback := range cfg.Fallbacks {
		fallbacks[fallback.From] = fallback.To
	}
	return &agent.RoutedClient{AIClient: router, Stages: agent.StageModels{
		Context: cfg.Context, Executor: cfg.Executor, Responder: cfg.Responder, Skill: cfg.Skill,
		ExecutorEscalation: cfg.ExecutorEscalation, ExecutorAfterErrors: cfg.ExecutorAfterErrors,
	}, Fallbacks: fallbacks, PricingVersion: cfg.PricingVersion, Prices: prices, GraphJinPrice: cfg.GraphJinPrice}, digest, nil
}

// RoutingDigest validates the host route manifest without resolving any keys.
// The inspector uses it to compare the same trusted profile as the runner.
func RoutingDigest(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	_, digest, err := parseRouteConfig(raw)
	return digest, err
}

// RouteHasSkill lets the product adapter load staged skill metadata only when
// the trusted routing profile enables the separate semantic selector.
func RouteHasSkill(raw string) (bool, error) {
	if raw == "" {
		return false, nil
	}
	cfg, _, err := parseRouteConfig(raw)
	return cfg.Skill != "", err
}

// loadTriageClient binds native Typesafe to one dedicated operator-approved
// OpenShell route. The run cannot choose its endpoint, model or credential.
func loadTriageClient(raw string, getenv func(string) string) (*agent.BudgetTriage, error) {
	if raw == "" {
		return nil, fmt.Errorf("budget triage requires trusted routed configuration")
	}
	cfg, _, err := parseRouteConfig(raw)
	if err != nil || cfg.Triage == "" {
		return nil, fmt.Errorf("budget triage route unavailable")
	}
	for _, route := range cfg.Routes {
		if route.Key != cfg.Triage {
			continue
		}
		key := getenv(route.APIKeyEnv)
		if key == "" {
			return nil, fmt.Errorf("budget triage route missing credential")
		}
		return &agent.BudgetTriage{Route: route.Key, Model: route.Model, Policy: *cfg.BudgetPolicy,
			Client: ax.Typesafe(ax.Object("base_url", route.URL, "api_key", key, "model", route.Model,
				"retry", ax.Object("maxRetries", 0)))}, nil
	}
	return nil, fmt.Errorf("budget triage route unavailable")
}

func parseRouteConfig(raw string) (routeConfig, string, error) {
	if len(raw) > 65536 {
		return routeConfig{}, "", fmt.Errorf("HARNESS_MODEL_ROUTES exceeds limit")
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	var cfg routeConfig
	if err := decoder.Decode(&cfg); err != nil {
		return routeConfig{}, "", fmt.Errorf("invalid HARNESS_MODEL_ROUTES")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return routeConfig{}, "", fmt.Errorf("invalid HARNESS_MODEL_ROUTES")
	}
	if len(cfg.Routes) == 0 || len(cfg.Routes) > 8 || cfg.Context == "" || cfg.Executor == "" || cfg.Responder == "" {
		return routeConfig{}, "", fmt.Errorf("invalid HARNESS_MODEL_ROUTES profile")
	}
	known := make(map[string]bool, len(cfg.Routes))
	priced := cfg.PricingVersion != "" || cfg.GraphJinPrice != nil
	if len(cfg.PricingVersion) > 128 || strings.TrimSpace(cfg.PricingVersion) != cfg.PricingVersion ||
		(cfg.PricingVersion == "") != (!priced) || cfg.GraphJinPrice != nil && !cfg.GraphJinPrice.Valid() {
		return routeConfig{}, "", fmt.Errorf("invalid HARNESS_MODEL_ROUTES pricing profile")
	}
	for _, route := range cfg.Routes {
		if !validRouteKey(route.Key) || route.Model == "" || len(route.Model) > 128 || known[route.Key] || validModelURL(route.URL) != nil ||
			!validEnvName(route.APIKeyEnv) || route.Price != nil && !route.Price.Valid() || priced && route.Price == nil || !priced && route.Price != nil {
			return routeConfig{}, "", fmt.Errorf("invalid HARNESS_MODEL_ROUTES route")
		}
		known[route.Key] = true
	}
	for _, model := range []string{cfg.Context, cfg.Executor, cfg.Responder, cfg.Skill, cfg.ExecutorEscalation, cfg.Triage} {
		if model == "" {
			continue
		}
		if !known[model] {
			return routeConfig{}, "", fmt.Errorf("HARNESS_MODEL_ROUTES stage has no approved route")
		}
	}
	if cfg.ExecutorEscalation == "" && cfg.ExecutorAfterErrors != 0 ||
		cfg.ExecutorEscalation != "" && (cfg.ExecutorAfterErrors < 1 || cfg.ExecutorAfterErrors > 8 ||
			cfg.ExecutorEscalation == cfg.Executor || cfg.Executor == cfg.Context || cfg.Executor == cfg.Responder || cfg.Executor == cfg.Skill) {
		return routeConfig{}, "", fmt.Errorf("invalid HARNESS_MODEL_ROUTES executor escalation")
	}
	if (cfg.Triage == "") != (cfg.BudgetPolicy == nil) || cfg.BudgetPolicy != nil && !cfg.BudgetPolicy.Valid() {
		return routeConfig{}, "", fmt.Errorf("invalid HARNESS_MODEL_ROUTES budget policy")
	}
	if cfg.Triage != "" && (cfg.PricingVersion == "" || cfg.Triage == cfg.Context || cfg.Triage == cfg.Executor ||
		cfg.Triage == cfg.Responder || cfg.Triage == cfg.Skill || cfg.Triage == cfg.ExecutorEscalation) {
		return routeConfig{}, "", fmt.Errorf("invalid HARNESS_MODEL_ROUTES triage stage")
	}
	if len(cfg.Fallbacks) > len(cfg.Routes) {
		return routeConfig{}, "", fmt.Errorf("invalid HARNESS_MODEL_ROUTES fallback profile")
	}
	active := map[string]bool{cfg.Context: true, cfg.Executor: true, cfg.Responder: true}
	if cfg.Skill != "" {
		active[cfg.Skill] = true
	}
	if cfg.ExecutorEscalation != "" {
		active[cfg.ExecutorEscalation] = true
	}
	fallbackSources := map[string]bool{}
	for _, fallback := range cfg.Fallbacks {
		if !active[fallback.From] || !known[fallback.To] || fallback.To == cfg.Triage || fallback.From == fallback.To || fallbackSources[fallback.From] {
			return routeConfig{}, "", fmt.Errorf("invalid HARNESS_MODEL_ROUTES fallback profile")
		}
		fallbackSources[fallback.From] = true
	}
	canonical, _ := json.Marshal(cfg)
	digest := sha256.Sum256(canonical)
	return cfg, hex.EncodeToString(digest[:]), nil
}

func validModelURL(base string) error {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("invalid model URL")
	}
	return nil
}

func validEnvName(name string) bool {
	if len(name) < 2 || len(name) > 128 || name[0] < 'A' || name[0] > 'Z' {
		return false
	}
	for _, c := range name[1:] {
		if !(c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_') {
			return false
		}
	}
	return true
}

func validRouteKey(key string) bool {
	if len(key) < 1 || len(key) > 64 || key[0] < 'a' || key[0] > 'z' {
		return false
	}
	for _, c := range key[1:] {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}
