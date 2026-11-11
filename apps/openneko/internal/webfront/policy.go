package webfront

import (
	"sort"
	"strings"
)

type Rule string

const (
	Public   Rule = "public"
	Token    Rule = "token"
	SignedIn Rule = "signed-in"
	Admin    Rule = "admin"
)

type Policy struct {
	Prefix string
	Rule   Rule
}

// Policies must equal ACCESS_POLICIES in apps/web/src/lib/access-policy.ts;
// TestPoliciesMatchWeb compares them.
var Policies = []Policy{
	{"/signin", Public},
	{"/api/v1/auth", Public},
	{"/api/v1/sso", Public},
	{"/admin/settings/sso", Public},
	{"/api/v1/version", Public},
	{"/api/v1/health", Public},
	{"/api/v1/workflows/*/runs", Token},
	{"/api/v1/hooks", Token},
	{"/api/v1/channels", Token},
	{"/api/v1/integrations/connect", Token},
	{"/api/v1/pack-accounts", Token},

	{"/admin", Admin},
	{"/dev", Admin},
	{"/api/v1/admin", Admin},
	{"/api/v1/settings", Admin},
	{"/api/v1/policies", Admin},
	{"/api/v1/onboarding", Admin},
	{"/onboarding", Admin},
	{"/business-profile", Admin},
	{"/settings", Admin},

	{"/", SignedIn},
	{"/actions", SignedIn},
	{"/approvals", SignedIn},
	{"/integrations", SignedIn},
	{"/library", SignedIn},
	{"/memory", SignedIn},
	{"/runs", SignedIn},
	{"/skills", SignedIn},
	{"/chat", SignedIn},
	{"/device", SignedIn},
	{"/work", SignedIn},
	{"/workflows", SignedIn},
	{"/api/v1/action-requests", SignedIn},
	{"/api/v1/approvals", SignedIn},
	{"/api/v1/insights", SignedIn},
	{"/api/v1/integrations", SignedIn},
	{"/api/v1/library", SignedIn},
	{"/api/v1/my", SignedIn},
	{"/api/v1/observations", SignedIn},
	{"/api/v1/profile", SignedIn},
	{"/api/v1/settings/persona", SignedIn},
	{"/profile", SignedIn},
	{"/api/v1/subscriptions", SignedIn},
	{"/api/v1/bots", SignedIn},
	{"/api/v1/work", SignedIn},
	{"/api/v1/workflow-runs", SignedIn},
	{"/api/v1/workflows", SignedIn},
}

var byLength = func() []Policy {
	sorted := append([]Policy(nil), Policies...)
	sort.SliceStable(sorted, func(i, j int) bool { return len(sorted[i].Prefix) > len(sorted[j].Prefix) })
	return sorted
}()

// PolicyFor returns false for a path that no entry covers.
func PolicyFor(pathname string) (Policy, bool) {
	if len(pathname) > 1 && strings.HasSuffix(pathname, "/") {
		pathname = pathname[:len(pathname)-1]
	}
	path := APIV1Path(pathname)
	segments := strings.Split(path, "/")
	for _, p := range byLength {
		if p.Prefix == "/" {
			if path == "/" {
				return p, true
			}
			continue
		}
		prefix := strings.Split(p.Prefix, "/")
		if len(prefix) > len(segments) {
			continue
		}
		match := true
		for i, part := range prefix {
			if part != "*" && part != segments[i] {
				match = false
				break
			}
		}
		if match {
			return p, true
		}
	}
	return Policy{}, false
}

// APIV1Path maps an older /api/<path> alias to the /api/v1 path it reaches.
func APIV1Path(pathname string) string {
	if pathname == "/api" || !strings.HasPrefix(pathname, "/api/") {
		return pathname
	}
	if pathname == "/api/v1" || strings.HasPrefix(pathname, "/api/v1/") {
		return pathname
	}
	return "/api/v1" + pathname[4:]
}
