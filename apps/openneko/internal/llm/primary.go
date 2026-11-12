package llm

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/open-neko/neko/apps/openneko/internal/config"
)

type Querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// DecryptSecrets is decryptSecrets() in packages/llm/src/llm.ts: a value that
// is not a string, or is empty, is left out.
func DecryptSecrets(secretsDir string, raw map[string]any) (map[string]string, error) {
	out := map[string]string{}
	for k, v := range raw {
		s, _ := v.(string)
		if s == "" {
			continue
		}
		plain, err := config.MaybeDecryptValue(secretsDir, s)
		if err != nil {
			return nil, err
		}
		if plain != "" {
			out[k] = plain
		}
	}
	return out, nil
}

func DefaultSettings() Settings {
	return Settings{Source: "default", Provider: "google-gemini", Model: DefaultModel("google-gemini"), Enabled: true, Config: map[string]any{}, Secrets: map[string]string{}}
}

// PrimarySettings is resolvePrimaryProviderConfig(orgId) in llm.ts. A stored
// row that does not pass Validate is an error; no stored row gives the default.
func PrimarySettings(ctx context.Context, db Querier, secretsDir, orgID string) (Settings, error) {
	var (
		provider        string
		model, label    *string
		enabled         bool
		rawCfg, rawSecr []byte
	)
	err := db.QueryRow(ctx, `select provider, model, label, enabled, config, secrets from llm_provider_config
	  where org_id = $1 and scope = 'primary' limit 1`, orgID).Scan(&provider, &model, &label, &enabled, &rawCfg, &rawSecr)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !IsPrimaryProvider(provider)) {
		return DefaultSettings(), nil
	}
	if err != nil {
		return Settings{}, err
	}
	cfg := map[string]any{}
	_ = json.Unmarshal(rawCfg, &cfg)
	if cfg == nil {
		cfg = map[string]any{}
	}
	var sealed map[string]any
	_ = json.Unmarshal(rawSecr, &sealed)
	secrets, err := DecryptSecrets(secretsDir, sealed)
	if err != nil {
		return Settings{}, err
	}
	s := Settings{Source: "org", Provider: provider, Model: DefaultModel(provider), Enabled: enabled, Label: label, Config: cfg, Secrets: secrets}
	if model != nil {
		s.Model = *model
	}
	return s, s.Validate()
}
