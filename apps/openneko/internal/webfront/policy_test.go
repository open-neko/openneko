package webfront

import (
	"os"
	"regexp"
	"testing"
)

func readWeb(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile("../../../web/src/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestPoliciesMatchWeb(t *testing.T) {
	entries := regexp.MustCompile(`\{ prefix: "([^"]+)", rule: "([^"]+)"`).FindAllStringSubmatch(readWeb(t, "lib/access-policy.ts"), -1)
	if len(entries) != len(Policies) {
		t.Fatalf("access-policy.ts has %d entries, Go has %d", len(entries), len(Policies))
	}
	for i, e := range entries {
		if Policies[i].Prefix != e[1] || string(Policies[i].Rule) != e[2] {
			t.Errorf("entry %d: web %s %s, Go %s %s", i, e[1], e[2], Policies[i].Prefix, Policies[i].Rule)
		}
	}
}

func TestServerPagesMatchWeb(t *testing.T) {
	entries := regexp.MustCompile(`(?m)^\s+"(/[^"]*)",$`).FindAllStringSubmatch(readWeb(t, "spa/server-pages.ts"), -1)
	if len(entries) != len(ServerPages) {
		t.Fatalf("server-pages.ts has %d pages, Go has %d", len(entries), len(ServerPages))
	}
	for i, e := range entries {
		if ServerPages[i] != e[1] {
			t.Errorf("page %d: web %s, Go %s", i, e[1], ServerPages[i])
		}
	}
}

func TestPolicyFor(t *testing.T) {
	cases := map[string]Rule{
		"/":                              SignedIn,
		"/signin":                        Public,
		"/chat/":                         SignedIn,
		"/chat/t1":                       SignedIn,
		"/admin/settings/sso":            Public,
		"/admin/settings/models":         Admin,
		"/api/v1/workflows/wf1/runs":     Token,
		"/api/v1/workflows/wf1":          SignedIn,
		"/api/workflows/wf1/runs":        Token,
		"/api/settings/persona":          SignedIn,
		"/api/v1/settings/models":        Admin,
		"/api/hooks/x":                   Token,
		"/api/v1/version":                Public,
		"/business-profile":              Admin,
		"/api/v1/workflow-runs/r1/steps": SignedIn,
	}
	for path, want := range cases {
		got, ok := PolicyFor(path)
		if !ok || got.Rule != want {
			t.Errorf("%s: got %v %v, want %s", path, got.Rule, ok, want)
		}
	}
	for _, path := range []string{"/nope", "/api/briefing", "/api/v1/briefing/x", "/api", "/chatter", "/administrators"} {
		if p, ok := PolicyFor(path); ok {
			t.Errorf("%s: got %v, want no entry", path, p)
		}
	}
}

func TestServerPagePattern(t *testing.T) {
	for _, p := range []string{"/admin/settings/graphjin", "/admin/settings/graphjin/", "/device", "/chat/connectors"} {
		if !serverPage.MatchString(p) {
			t.Errorf("%s should go to Node", p)
		}
	}
	for _, p := range []string{"/chat", "/admin", "/admin/access", "/admin/settings", "/admin/settings/workflows", "/admin/users", "/admin/users/groups/g1", "/admin/rules/p1", "/admin/users/groups/g1/x", "/profile", "/adminx", "/work", "/workflows/w1"} {
		if serverPage.MatchString(p) {
			t.Errorf("%s should be the static app", p)
		}
	}
}
