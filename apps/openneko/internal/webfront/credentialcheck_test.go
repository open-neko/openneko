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
