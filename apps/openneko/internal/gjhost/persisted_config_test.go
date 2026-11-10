package gjhost

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dosco/graphjin/serv/v3"
)

func TestPinnedLoaderReadsTheConfigTheApprovalWorkerWrites(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "packages", "llm", "test", "fixtures", "graphjin-persisted-config.yml"))
	if err != nil {
		t.Fatal(err)
	}
	conf := strings.NewReplacer(
		"REPLACE_WITH_BASE64_32_BYTE_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
		"REPLACE_WITH_PER_ORG_SECRET_B64", "c2VjcmV0LXNlY3JldC1zZWNyZXQtc2VjcmV0LXNlY3JldA==",
	).Replace(string(raw))
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "agentic.yml"), []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := serv.ReadInConfig(filepath.Join(dir, "agentic"))
	if err != nil {
		t.Fatal(err)
	}
	var role *string
	for _, r := range loaded.Roles {
		if r.Name == "support" {
			role = &r.Match
		}
	}
	if role == nil || *role != "role = 'support'" {
		t.Fatalf("role support = %v", role)
	}
	var found bool
	for _, s := range loaded.Sources {
		if s.Name != "shop" {
			continue
		}
		found = true
		if s.Type != "mysql" || !s.ReadOnly || s.Port != 3306 {
			t.Fatalf("shop source = %+v", s)
		}
		if !slices.Equal(s.Access.BlockedTables, []string{"admin_user", "oauth_token"}) ||
			!slices.Equal(s.Access.AdminTables, []string{"sales_invoice"}) {
			t.Fatalf("shop access = %+v", s.Access)
		}
	}
	if !found {
		t.Fatal("source shop is missing")
	}
}
