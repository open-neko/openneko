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

type DB interface {
	Querier
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

type storedRow struct {
	Provider string
	Model    *string
	Label    *string
	Enabled  bool
	Config   map[string]any
	Secrets  map[string]string
}

func loadStored(ctx context.Context, db Querier, secretsDir, orgID, scope string) (*storedRow, error) {
	var (
		row             storedRow
		rawCfg, rawSecr []byte
	)
	err := db.QueryRow(ctx, `select provider, model, label, enabled, config, secrets from llm_provider_config
	  where org_id = $1 and scope = $2 limit 1`, orgID, scope).Scan(&row.Provider, &row.Model, &row.Label, &row.Enabled, &rawCfg, &rawSecr)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(rawCfg, &row.Config)
	if row.Config == nil {
		row.Config = map[string]any{}
	}
	var sealed map[string]any
	_ = json.Unmarshal(rawSecr, &sealed)
	if row.Secrets, err = DecryptSecrets(secretsDir, sealed); err != nil {
		return nil, err
	}
	return &row, nil
}

// PrimarySettings is resolvePrimaryProviderConfig(orgId) in llm.ts. A stored
// row that does not pass Validate is an error; no stored row gives the default.
func PrimarySettings(ctx context.Context, db Querier, secretsDir, orgID string) (Settings, error) {
	row, err := loadStored(ctx, db, secretsDir, orgID, "primary")
	if err != nil {
		return Settings{}, err
	}
	if row == nil || !IsPrimaryProvider(row.Provider) {
		return DefaultSettings(), nil
	}
	s := Settings{Source: "org", Provider: row.Provider, Model: DefaultModel(row.Provider), Enabled: row.Enabled, Label: row.Label, Config: row.Config, Secrets: row.Secrets}
	if row.Model != nil {
		s.Model = *row.Model
	}
	return s, s.Validate()
}
