package skill

import (
	"context"
	"net/url"
	"testing"

	"github.com/open-neko/neko/apps/openneko/internal/config"
	"github.com/open-neko/neko/apps/openneko/internal/testpg"
)

func TestDefaultPolicyIsSecureByDefault(t *testing.T) {
	if DefaultPolicy.AllowGitURLInstalls {
		t.Errorf("default policy should not allow git-URL installs")
	}
}

func TestFetchPolicyReadsFromDatabase(t *testing.T) {
	pool := testpg.Pool(t, "0001_init.sql")
	ctx := context.Background()
	var schema string
	if err := pool.QueryRow(ctx, `select current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	u, err := url.Parse(testpg.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	if err := config.WriteLocalDatabaseURL("", u.String()); err != nil {
		t.Fatal(err)
	}

	pol, source, err := FetchPolicy(ctx)
	if err != nil || source != "default" || pol.AllowGitURLInstalls {
		t.Fatalf("with no org row: got %+v %q %v", pol, source, err)
	}

	if _, err := pool.Exec(ctx, `insert into organization (id, name) values ('org', 'Org')`); err != nil {
		t.Fatal(err)
	}
	pol, source, err = FetchPolicy(ctx)
	if err != nil || source != "org" || pol.AllowGitURLInstalls {
		t.Fatalf("with org and no policy row: got %+v %q %v", pol, source, err)
	}

	if _, err := pool.Exec(ctx, `insert into llm_provider_config (org_id, scope, provider, config) values ('org', 'install-policy', 'system', '{"allowGitUrlInstalls": true}')`); err != nil {
		t.Fatal(err)
	}
	pol, source, err = FetchPolicy(ctx)
	if err != nil || source != "org" || !pol.AllowGitURLInstalls {
		t.Fatalf("with allowGitUrlInstalls=true: got %+v %q %v", pol, source, err)
	}

	for _, value := range []string{`"yes"`, `"true"`, `1`, `"on"`, `"t"`, `"maybe"`, `null`, `false`, `{}`} {
		if _, err := pool.Exec(ctx, `update llm_provider_config set config = jsonb_build_object('allowGitUrlInstalls', $1::jsonb) where org_id = 'org' and scope = 'install-policy'`, value); err != nil {
			t.Fatal(err)
		}
		pol, source, err = FetchPolicy(ctx)
		if err != nil || source != "org" || pol.AllowGitURLInstalls {
			t.Errorf("with allowGitUrlInstalls=%s: got %+v %q %v", value, pol, source, err)
		}
	}
}

func TestFetchPolicyReturnsDefaultsWhenDatabaseUnreachable(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("NEKO_PG_HOST", "127.0.0.1")
	t.Setenv("NEKO_PG_PORT", "1")
	pol, source, _ := FetchPolicy(context.Background())
	if source != "unreachable" {
		t.Errorf("expected source=unreachable, got %q", source)
	}
	if pol.AllowGitURLInstalls {
		t.Errorf("unreachable database should not grant git-URL installs — got %+v", pol)
	}
}
