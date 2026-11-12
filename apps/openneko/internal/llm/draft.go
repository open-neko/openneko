package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf16"
)

// Draft is an EditableProviderConfig from the provider settings form. Secrets
// hold only the fields the admin typed.
type Draft struct {
	Scope    string
	Provider string
	Model    string
	Enabled  bool
	Label    *string
	Config   map[string]any
	Secrets  map[string]string
}

func ParseDraft(body map[string]any) Draft {
	str := func(key string) string { s, _ := body[key].(string); return s }
	d := Draft{Scope: str("scope"), Provider: str("provider"), Model: str("model"), Config: map[string]any{}, Secrets: map[string]string{}}
	d.Enabled, _ = body["enabled"].(bool)
	if label, ok := body["label"].(string); ok {
		d.Label = &label
	}
	if cfg, ok := body["config"].(map[string]any); ok {
		d.Config = cfg
	}
	if secrets, ok := body["secrets"].(map[string]any); ok {
		for k, v := range secrets {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				d.Secrets[k] = s
			}
		}
	}
	return d
}

func merged(stored *storedRow, d Draft) (map[string]any, map[string]string) {
	cfg, secrets := map[string]any{}, map[string]string{}
	if stored != nil {
		for k, v := range stored.Config {
			cfg[k] = v
		}
		for k, v := range stored.Secrets {
			secrets[k] = v
		}
	}
	for k, v := range d.Config {
		cfg[k] = v
	}
	for k, v := range d.Secrets {
		secrets[k] = v
	}
	return cfg, secrets
}

// PrimaryFromDraft is resolvePrimaryProviderConfig(orgId, draft): the stored
// row's config and secrets fill what the draft leaves out when the provider
// is the same.
func PrimaryFromDraft(ctx context.Context, db Querier, secretsDir, orgID string, d Draft) (Settings, error) {
	if d.Scope != "primary" {
		return Settings{}, fmt.Errorf("Expected primary config draft, got %s", d.Scope)
	}
	var stored *storedRow
	if orgID != "" {
		row, err := loadStored(ctx, db, secretsDir, orgID, "primary")
		if err != nil {
			return Settings{}, err
		}
		if row != nil && IsPrimaryProvider(row.Provider) && row.Provider == d.Provider {
			stored = row
		}
	}
	cfg, secrets := merged(stored, d)
	s := Settings{Source: "draft", Provider: d.Provider, Model: d.Model, Enabled: d.Enabled, Label: d.Label, Config: cfg, Secrets: secrets}
	if s.Model == "" {
		s.Model = DefaultModel(d.Provider)
	}
	return s, s.Validate()
}

// SetupComplete is hasPrimaryProviderSetup() in apps/web/src/lib/provider-settings.ts.
func SetupComplete(ctx context.Context, db Querier, secretsDir, orgID string) (bool, error) {
	row, err := loadStored(ctx, db, secretsDir, orgID, "primary")
	if err != nil || row == nil || !IsPrimaryProvider(row.Provider) || !row.Enabled {
		return false, err
	}
	model := ""
	if row.Model != nil {
		model = *row.Model
	}
	if model == "" {
		return false, nil
	}
	for _, f := range Fields(row.Provider) {
		value := configString(row.Config, f.Key)
		if f.Kind == "secret" {
			value = row.Secrets[f.Key]
		}
		if f.IsRequired() && value == "" {
			return false, nil
		}
	}
	_, err = ResolveRuntime(row.Provider, model, row.Config)
	return err == nil, nil
}

type ResearchSettings struct {
	Source   string
	Provider string
	Model    string
	Enabled  bool
	Secrets  map[string]string
}

func (r ResearchSettings) Off() bool { return !r.Enabled || r.Provider == "disabled" }

func DefaultResearchModel(provider string) string {
	if provider == "perplexity" {
		return "sonar-deep-research"
	}
	return ""
}

// ResearchFromDraft is resolveResearchProviderConfig(orgId, draft) in
// packages/llm/src/industry-researcher.ts.
func ResearchFromDraft(ctx context.Context, db Querier, secretsDir, orgID string, d Draft) (ResearchSettings, error) {
	if d.Scope != "research" {
		return ResearchSettings{}, fmt.Errorf("Expected research config draft, got %s", d.Scope)
	}
	var stored *storedRow
	if orgID != "" {
		row, err := loadStored(ctx, db, secretsDir, orgID, "research")
		if err != nil {
			return ResearchSettings{}, err
		}
		if row != nil && (row.Provider == "disabled" || row.Provider == "perplexity") && row.Provider == d.Provider {
			stored = row
		}
	}
	_, secrets := merged(stored, d)
	r := ResearchSettings{Source: "draft", Provider: d.Provider, Model: d.Model, Enabled: d.Enabled, Secrets: secrets}
	if r.Model == "" {
		r.Model = DefaultResearchModel(d.Provider)
	}
	if !r.Off() && r.Provider == "perplexity" && r.Secrets["apiKey"] == "" {
		return r, fmt.Errorf("Perplexity research provider requires an API key")
	}
	return r, nil
}

const PerplexityURL = "https://api.perplexity.ai/chat/completions"

// TestResearch is testResearchProvider(): one chat call that must answer 2xx.
func TestResearch(ctx context.Context, client *http.Client, url string, r ResearchSettings) error {
	model := r.Model
	if model == "" {
		model = DefaultResearchModel("perplexity")
	}
	body, _ := json.Marshal(map[string]any{
		"model":    model,
		"messages": []map[string]string{{"role": "user", "content": "Reply with READY only."}},
	})
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+r.Secrets["apiKey"])
	req.Header.Set("Content-Type", "application/json")
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode > 299 {
		text, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
		return fmt.Errorf("perplexity %d %s: %s", res.StatusCode, strings.TrimPrefix(res.Status, fmt.Sprintf("%d ", res.StatusCode)), firstUnits(string(text), 300))
	}
	return nil
}

func firstUnits(s string, n int) string {
	units := utf16.Encode([]rune(s))
	if len(units) <= n {
		return s
	}
	return string(utf16.Decode(units[:n]))
}
