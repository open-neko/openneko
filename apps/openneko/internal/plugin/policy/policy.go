// Install-policy fetcher for the CLI. Reads the deployment-wide policy
// from the worker's admin port (loopback HTTP) before allowing
// privileged install paths like --unverified or git-URL installs.
//
// Falls back to the most-restrictive default when the worker isn't
// reachable — operators running `openneko install` against a deployment
// that doesn't have a worker process yet shouldn't be able to flip on
// privileged install paths just by not running the worker.
package policy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"slices"
	"time"
)

// Policy mirrors the deployment-wide trust floor for plugin + skill
// installs. Keep this in sync with apps/web/src/lib/install-policy-
// settings.ts (the canonical write side) and packages/db/src/install-
// policy.ts (the canonical read side).
type Policy struct {
	AllowUnverified           bool     `json:"allowUnverified"`
	AllowGitURLInstalls       bool     `json:"allowGitUrlInstalls"`
	AllowedMarketplaces       []string `json:"allowedMarketplaces"`
	AllowSandboxedSkillEscape bool     `json:"allowSandboxedSkillEscape"`
}

// DefaultPolicy is the secure-by-default policy applied when the
// worker admin port is unreachable. Mirrors DEFAULT_POLICY on the
// TS side.
var DefaultPolicy = Policy{
	AllowUnverified:           false,
	AllowGitURLInstalls:       false,
	AllowedMarketplaces:       []string{"https://open-neko.github.io/plugins/marketplace.json"},
	AllowSandboxedSkillEscape: false,
}

// workerAdminBase returns the loopback URL of the worker admin port.
// WORKER_ADMIN_URL overrides for tests / non-default deployments.
func workerAdminBase() string {
	if v := os.Getenv("WORKER_ADMIN_URL"); v != "" {
		return v
	}
	return "http://127.0.0.1:4100"
}

// Fetch reads the policy from the worker. Returns (policy, source) where
// source is "org" (read from DB), "default" (worker has no row), or
// "unreachable" (worker didn't answer, default applied). The CLI uses
// `source` only for diagnostics; behavior is the same: enforce the
// returned policy.
func Fetch(ctx context.Context) (Policy, string, error) {
	endpoint, err := url.JoinPath(workerAdminBase(), "/admin/install-policy")
	if err != nil {
		return DefaultPolicy, "unreachable", err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return DefaultPolicy, "unreachable", err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return DefaultPolicy, "unreachable", nil
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return DefaultPolicy, "unreachable", fmt.Errorf("worker /admin/install-policy: HTTP %d", res.StatusCode)
	}
	var body struct {
		Policy Policy `json:"policy"`
		Source string `json:"source"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		return DefaultPolicy, "unreachable", err
	}
	source := body.Source
	if source == "" {
		source = "org"
	}
	return body.Policy, source, nil
}

// CheckSource is the predicate the CLI uses to decide whether to allow
// a given install attempt. Mirrors isInstallSourceAllowed on the TS
// side.
type InstallSource int

const (
	SourceUnverified InstallSource = iota
	SourceGitURL
)

func (p Policy) Allows(source InstallSource) bool {
	switch source {
	case SourceUnverified:
		return p.AllowUnverified
	case SourceGitURL:
		return p.AllowGitURLInstalls
	}
	return false
}

func (p Policy) MarketplaceAllowed(marketplaceURL string) bool {
	return slices.Contains(p.AllowedMarketplaces, marketplaceURL)
}
