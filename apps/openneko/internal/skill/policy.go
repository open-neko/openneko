package skill

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"time"
)

// Policy mirrors packages/db/src/install-policy.ts; keep the two in sync.
type Policy struct {
	AllowGitURLInstalls bool `json:"allowGitUrlInstalls"`
}

// DefaultPolicy applies when the worker is unreachable, so stopping the
// worker cannot turn on git-URL installs.
var DefaultPolicy = Policy{AllowGitURLInstalls: false}

func workerAdminBase() string {
	if v := os.Getenv("WORKER_ADMIN_URL"); v != "" {
		return v
	}
	return "http://127.0.0.1:4100"
}

// FetchPolicy's source is "org", "default" or "unreachable", for diagnostics only.
func FetchPolicy(ctx context.Context) (Policy, string, error) {
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
