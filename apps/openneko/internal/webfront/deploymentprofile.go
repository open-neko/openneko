package webfront

import (
	"os"
	"regexp"
	"slices"
	"strings"
)

var publicModelHost = regexp.MustCompile(`(?i)(googleapis\.com|anthropic\.com|openai\.com|openrouter\.ai|x\.ai|mistral\.ai)$`)

// DeploymentProfile is resolveDeploymentProfile() of
// packages/llm/src/work/deployment-profile.ts.
func DeploymentProfile() string {
	profile := strings.ToLower(os.Getenv("OPENNEKO_PROFILE"))
	if slices.Contains([]string{"solo", "team", "org", "hardened"}, profile) {
		return profile
	}
	return "solo"
}

// ReportDeploymentProfile is reportDeploymentProfile(): the posture line at
// boot, and a warning when a hardened install names a public model host.
func ReportDeploymentProfile(logf func(format string, args ...any)) {
	profile := DeploymentProfile()
	logf("[deployment-profile] posture: %s", profile)
	if profile != "hardened" {
		return
	}
	var public []string
	for _, h := range strings.Split(os.Getenv("OPENNEKO_AGENT_MODEL_HOST"), ",") {
		if h = strings.TrimSpace(h); h != "" && publicModelHost.MatchString(h) {
			public = append(public, h)
		}
	}
	if len(public) > 0 {
		logf("[deployment-profile] hardened profile expects a client-provided on-prem LLM, but the model host is public: %s", strings.Join(public, ", "))
	}
}
