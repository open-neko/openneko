package webfront

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/open-neko/neko/apps/openneko/internal/gjconfig"
)

const legacyDemoGraphjinConfig = "/graphjin-config/agentic.yml"

var (
	inStackGraphjin     = regexp.MustCompile(`^https?://(graphjin|neko-graphjin)[:/]`)
	provisionRetryDelay = 3 * time.Second
)

// provisionHostConfig is provisionHostConfig() in packages/llm/src/host-provision.ts.
// A failed step is logged and does not fail the save that called it.
func (a *API) provisionHostConfig(ctx context.Context, orgID string) {
	ctx = context.WithoutCancel(ctx)
	if err := a.writeGraphjinClient(ctx, orgID); err != nil {
		log.Printf("[host-provision] graphjin write failed: %v", err)
	}
	if err := a.provisionSourcesMode(ctx, orgID); err != nil {
		log.Printf("[host-provision] sources-mode provision failed: %v", err)
	}
}

func (a *API) writeGraphjinClient(ctx context.Context, orgID string) error {
	var graphqlURL string
	err := a.DB.QueryRow(ctx, `select graphql_url from data_source where org_id = $1
		order by is_default desc, created_at limit 1`, orgID).Scan(&graphqlURL)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && graphqlURL == "") {
		return nil
	}
	if err != nil {
		return err
	}
	server, ok := graphjinServerBase(graphqlURL)
	if !ok {
		return nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	path := filepath.Join(dir, "graphjin", "client.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o777); err != nil {
		return err
	}
	body, _ := json.MarshalIndent(map[string]string{"server": server, "token": "", "expires_at": "0001-01-01T00:00:00Z"}, "", "  ")
	return atomicWrite(path, body, 0o664)
}

func graphjinServerBase(graphqlURL string) (string, bool) {
	u, err := url.Parse(graphqlURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", false
	}
	host := u.Host
	if (u.Scheme == "http" && u.Port() == "80") || (u.Scheme == "https" && u.Port() == "443") {
		host = strings.TrimSuffix(host, ":"+u.Port())
	}
	origin := strings.ToLower(u.Scheme) + "://" + host
	if base, ok := strings.CutSuffix(u.Path, "/api/v1/graphql"); ok {
		return origin + base, true
	}
	return origin, true
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func (a *API) provisionSourcesMode(ctx context.Context, orgID string) error {
	cfgPath := strings.TrimSpace(os.Getenv("OPENNEKO_GRAPHJIN_CONFIG"))
	wroteConfig, demo := false, false
	if cfgPath != "" {
		secret, err := gjconfig.SigningSecretB64(a.SecretsDir, orgID)
		var r gjconfig.Result
		if err == nil {
			r, err = gjconfig.ReconcileFile(cfgPath, secret)
		}
		if err != nil {
			log.Printf("[host-provision] GraphJin agentic config reconciliation failed: %v", err)
		} else {
			demo = (os.Getenv("OPENNEKO_STACK_MODE") == "demo" || cfgPath == legacyDemoGraphjinConfig) && gjconfig.HasJWTSecret(r.Content)
			if r.Changed() {
				wroteConfig = true
				log.Printf("[host-provision] reconciled GraphJin agentic config in %s", cfgPath)
			}
		}
	}

	where := "org_id = $1"
	if demo {
		where += ` and (label = 'AdventureWorks' or graphql_url like 'http://graphjin:8080/%')`
	}
	var id, authMode, graphqlURL string
	err := a.DB.QueryRow(ctx, `select id::text, auth_mode, graphql_url from data_source where `+where+`
		order by is_default desc, created_at limit 1`, orgID).Scan(&id, &authMode, &graphqlURL)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if graphqlURL == "" {
		return nil
	}
	if demo && authMode != "jwt" {
		if err := a.setAuthModeJWT(ctx, id); err != nil {
			return err
		}
		authMode = "jwt"
		log.Printf("[host-provision] reconciled packaged demo data source auth_mode=jwt")
	}
	// A gj_catalog answer does not prove GraphJin verified the token, so an
	// explicit development source keeps its mode.
	if authMode == "development" || (authMode == "jwt" && !wroteConfig) {
		return nil
	}

	token, err := a.graphjinToken(orgID, "service", 5*time.Minute)
	if err != nil {
		return err
	}
	attempts := 1
	if wroteConfig {
		attempts = 6
	} else if inStackGraphjin.MatchString(graphqlURL) {
		attempts = 4
	}
	for attempt := 1; attempt <= attempts; attempt++ {
		if answersCatalog(ctx, graphqlURL, token) {
			if authMode != "jwt" {
				if err := a.setAuthModeJWT(ctx, id); err != nil {
					return err
				}
				log.Printf("[host-provision] data source answers gj_catalog with an actor token: auth_mode=jwt (agentic mode on)")
			}
			return nil
		}
		if attempt < attempts {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(provisionRetryDelay):
			}
		}
	}
	return nil
}

func (a *API) setAuthModeJWT(ctx context.Context, id string) error {
	_, err := a.DB.Exec(ctx, `update data_source set auth_mode = 'jwt', updated_at = now() where id = $1`, id)
	return err
}

func answersCatalog(ctx context.Context, graphqlURL, token string) bool {
	ctx, cancel := context.WithTimeout(ctx, 2500*time.Millisecond)
	defer cancel()
	body := []byte(`{"query":"query { gj_catalog(id: \"help:discovery\") { id } }"}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, graphqlURL, bytes.NewReader(body))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return false
	}
	var out struct {
		Data struct {
			Catalog any `json:"gj_catalog"`
		} `json:"data"`
		Errors []any `json:"errors"`
	}
	if json.NewDecoder(res.Body).Decode(&out) != nil || len(out.Errors) > 0 {
		return false
	}
	switch v := out.Data.Catalog.(type) {
	case []any:
		return len(v) > 0
	case nil:
		return false
	case bool:
		return v
	case string:
		return v != ""
	case float64:
		return v != 0
	}
	return true
}
