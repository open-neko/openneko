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
)

type modelRoute struct {
	Key       string `json:"key"`
	Model     string `json:"model"`
	URL       string `json:"url"`
	APIKeyEnv string `json:"api_key_env"`
}

type routeConfig struct {
	Context   string       `json:"context"`
	Executor  string       `json:"executor"`
	Responder string       `json:"responder"`
	Routes    []modelRoute `json:"routes"`
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
	if len(raw) > 65536 {
		return nil, "", fmt.Errorf("HARNESS_MODEL_ROUTES exceeds limit")
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	var cfg routeConfig
	if err := decoder.Decode(&cfg); err != nil {
		return nil, "", fmt.Errorf("invalid HARNESS_MODEL_ROUTES")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, "", fmt.Errorf("invalid HARNESS_MODEL_ROUTES")
	}
	if len(cfg.Routes) == 0 || len(cfg.Routes) > 8 || cfg.Context == "" || cfg.Executor == "" || cfg.Responder == "" {
		return nil, "", fmt.Errorf("invalid HARNESS_MODEL_ROUTES profile")
	}
	known := make(map[string]bool, len(cfg.Routes))
	entries := make([]ax.Value, 0, len(cfg.Routes))
	for _, route := range cfg.Routes {
		if !validRouteKey(route.Key) || route.Model == "" || len(route.Model) > 128 || known[route.Key] || validModelURL(route.URL) != nil ||
			!validEnvName(route.APIKeyEnv) || getenv(route.APIKeyEnv) == "" {
			return nil, "", fmt.Errorf("invalid HARNESS_MODEL_ROUTES route or missing credential")
		}
		known[route.Key] = true
		service := ax.NewOpenAICompatibleClient(ax.Object("base_url", route.URL, "api_key", getenv(route.APIKeyEnv), "model", route.Model))
		// A logical key can distinguish two accounts that expose the same model.
		// Ax's explicit router entry strips the key before the provider call, so
		// the provider uses its configured actual model.
		service.Name = route.Key
		entries = append(entries, ax.RouterServiceEntry{Key: route.Key, Description: route.Model, Service: service})
	}
	for _, model := range []string{cfg.Context, cfg.Executor, cfg.Responder} {
		if !known[model] {
			return nil, "", fmt.Errorf("HARNESS_MODEL_ROUTES stage has no approved route")
		}
	}
	router, err := ax.NewMultiServiceRouter(entries)
	if err != nil {
		return nil, "", fmt.Errorf("invalid HARNESS_MODEL_ROUTES: %w", err)
	}
	canonical, _ := json.Marshal(cfg)
	digest := sha256.Sum256(canonical)
	return &agent.RoutedClient{AIClient: router, Stages: agent.StageModels{
		Context: cfg.Context, Executor: cfg.Executor, Responder: cfg.Responder,
	}}, hex.EncodeToString(digest[:]), nil
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
