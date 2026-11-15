package webfront

import (
	"fmt"
	"strings"
	"testing"
)

func TestReportDeploymentProfile(t *testing.T) {
	for _, c := range []struct{ profile, hosts, want string }{
		{"", "api.openai.com", "[deployment-profile] posture: solo"},
		{"bogus", "", "[deployment-profile] posture: solo"},
		{"ORG", "api.openai.com", "[deployment-profile] posture: org"},
		{"hardened", "llm.internal, 10.0.0.5", "[deployment-profile] posture: hardened"},
		{"hardened", "llm.internal, api.anthropic.com,generativelanguage.googleapis.com", "[deployment-profile] posture: hardened\n" +
			"[deployment-profile] hardened profile expects a client-provided on-prem LLM, but the model host is public: api.anthropic.com, generativelanguage.googleapis.com"},
	} {
		t.Setenv("OPENNEKO_PROFILE", c.profile)
		t.Setenv("OPENNEKO_AGENT_MODEL_HOST", c.hosts)
		var lines []string
		ReportDeploymentProfile(func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) })
		if got := strings.Join(lines, "\n"); got != c.want {
			t.Errorf("%q %q:\n%s\nwant\n%s", c.profile, c.hosts, got, c.want)
		}
	}
}
