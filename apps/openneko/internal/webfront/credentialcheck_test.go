package webfront

import (
	"context"
	"strings"
	"testing"

	"github.com/open-neko/neko/apps/openneko/internal/config"
)

func TestProviderCredentialWarningAtStart(t *testing.T) {
	f, db := modelSettingsFront(t)
	a := f.API
	ctx := context.Background()
	sealed := func(dir, value string) string {
		t.Helper()
		if err := config.EnsureSecretKey(dir); err != nil {
			t.Fatal(err)
		}
		v, err := config.EncryptValue(dir, value)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}

	if got := a.ProviderCredentialWarning(ctx); !strings.Contains(got, "no model provider is set") {
		t.Fatalf("no provider: %q", got)
	}
	seedPrimary(t, db, "openai", "gpt-5", `{}`, `{"apiKey":"`+sealed(a.SecretsDir, "sk-good-0001")+`"}`)
	seed(t, db, `update llm_provider_config set enabled = false where scope = 'primary'`)
	if got := a.ProviderCredentialWarning(ctx); !strings.Contains(got, "no model provider is set") {
		t.Fatalf("a disabled provider only: %q", got)
	}
	seed(t, db, `update llm_provider_config set enabled = true where scope = 'primary'`)
	if got := a.ProviderCredentialWarning(ctx); got != "" {
		t.Fatalf("a readable credential: %q", got)
	}

	foreign := sealed(t.TempDir(), "sk-other-key-0002")
	seed(t, db, `update llm_provider_config set secrets = jsonb_build_object('apiKey', '`+foreign+`') where scope = 'primary'`)
	got := a.ProviderCredentialWarning(ctx)
	if !strings.Contains(got, "credential cannot be read for org org (openai)") || strings.Count(got, "\n") != 0 {
		t.Fatalf("a credential sealed with another key: %q", got)
	}
	if strings.Contains(got, foreign) || strings.Contains(got, strings.TrimPrefix(foreign, "enc:v1:")[:12]) || strings.Contains(got, "sk-other") {
		t.Fatalf("the warning holds the credential: %q", got)
	}

	seed(t, db, `update llm_provider_config set secrets = '{}' where scope = 'primary'`)
	if got := a.ProviderCredentialWarning(ctx); !strings.Contains(got, "openai requires an API key") {
		t.Fatalf("no credential: %q", got)
	}
}

func TestProviderCredentialWarningCountsATurnModelRoute(t *testing.T) {
	f, db := modelSettingsFront(t)
	a := f.API
	ctx := context.Background()
	if err := config.EnsureSecretKey(a.SecretsDir); err != nil {
		t.Fatal(err)
	}
	good, err := config.EncryptValue(a.SecretsDir, "sk-good-0003")
	if err != nil {
		t.Fatal(err)
	}

	t.Setenv("OPENAI_API_KEY", "sk-env-only-0004")
	t.Setenv("GEMINI_API_KEY", "sk-env-only-0005")
	if got := a.ProviderCredentialWarning(ctx); !strings.Contains(got, "no model provider is set") {
		t.Fatalf("a key in the environment only, which no turn uses: %q", got)
	}

	seed(t, db, `insert into llm_model (org_id, name, provider, model, secrets)
		values ('org', 'fast', 'openai', 'gpt-5', jsonb_build_object('apiKey', '`+good+`'))`)
	seed(t, db, `insert into llm_task_route (org_id, task, model_id) select 'org', 'turn', id from llm_model where name = 'fast'`)
	if got := a.ProviderCredentialWarning(ctx); got != "" {
		t.Fatalf("a turn route to a model with a readable key: %q", got)
	}

	foreign, err := config.EncryptValue(t.TempDir(), "sk-other-key-0006")
	if err != nil {
		t.Fatal(err)
	}
	seed(t, db, `update llm_model set secrets = jsonb_build_object('apiKey', '`+foreign+`') where name = 'fast'`)
	if got := a.ProviderCredentialWarning(ctx); !strings.Contains(got, "credential cannot be read for org org (turn model)") || strings.Contains(got, "sk-other") {
		t.Fatalf("a turn model sealed with another key: %q", got)
	}

	seed(t, db, `update llm_model set secrets = '{}' where name = 'fast'`)
	if got := a.ProviderCredentialWarning(ctx); !strings.Contains(got, "org org (turn model): openai requires an API key") {
		t.Fatalf("a turn model with no key: %q", got)
	}

	if _, err := db.Exec(ctx, `insert into llm_task_route (org_id, task, model_id) select 'org', 'default', id from llm_model where name = 'fast'`); err == nil {
		t.Fatal("a default route took a model; the check counts turn routes only")
	}

	seed(t, db, `update llm_model set enabled = false where name = 'fast'`)
	if got := a.ProviderCredentialWarning(ctx); !strings.Contains(got, "no model provider is set") {
		t.Fatalf("a route to a disabled model only: %q", got)
	}
}
