package skill

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/open-neko/neko/apps/openneko/internal/config"
	"github.com/open-neko/neko/apps/openneko/internal/db"
)

// Policy mirrors packages/db/src/install-policy.ts; keep the two in sync.
type Policy struct {
	AllowGitURLInstalls bool `json:"allowGitUrlInstalls"`
}

// DefaultPolicy applies when the database is unreachable or no policy row is
// stored, so stopping the database cannot turn on git-URL installs.
var DefaultPolicy = Policy{AllowGitURLInstalls: false}

// FetchPolicy's source is "org", "default" or "unreachable", for diagnostics only.
func FetchPolicy(ctx context.Context) (Policy, string, error) {
	target, err := resolvePolicyConn()
	if err != nil {
		return DefaultPolicy, "unreachable", err
	}
	return ReadPolicy(ctx, target)
}

// ReadPolicy reads the deployment install policy from llm_provider_config.
func ReadPolicy(ctx context.Context, target db.ConnConfig) (Policy, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	conn, err := db.Connect(ctx, target, "openneko install-policy")
	if err != nil {
		return DefaultPolicy, "unreachable", nil
	}
	defer conn.Close(context.Background())

	var orgID string
	err = conn.QueryRow(ctx, `select id from organization order by created_at asc limit 1`).Scan(&orgID)
	if errors.Is(err, pgx.ErrNoRows) {
		return DefaultPolicy, "default", nil
	}
	if err != nil {
		return DefaultPolicy, "unreachable", err
	}

	var allow bool
	err = conn.QueryRow(ctx, `select coalesce(config->'allowGitUrlInstalls' = 'true'::jsonb, false)
		from llm_provider_config where org_id = $1 and scope = 'install-policy' limit 1`, orgID).Scan(&allow)
	if errors.Is(err, pgx.ErrNoRows) {
		return DefaultPolicy, "org", nil
	}
	if err != nil {
		return DefaultPolicy, "unreachable", err
	}
	return Policy{AllowGitURLInstalls: allow}, "org", nil
}

func resolvePolicyConn() (db.ConnConfig, error) {
	conn := db.ConnConfig{
		Host:     envOr("NEKO_PG_HOST", "127.0.0.1"),
		Port:     envInt("NEKO_PG_PORT", envInt("OPENNEKO_DB_PORT", 5432)),
		User:     envOr("NEKO_PG_USER", "neko"),
		Password: envOr("NEKO_PG_PASSWORD", "secret"),
		Database: envOr("NEKO_PG_DATABASE", "neko"),
		SSLMode:  envOr("NEKO_PG_SSLMODE", "disable"),
	}
	local, _ := config.ReadLocal("")
	if local.Pg != nil {
		if local.Pg.Host != "" {
			conn.Host = local.Pg.Host
		}
		if local.Pg.Port != 0 {
			conn.Port = local.Pg.Port
		}
		if local.Pg.User != "" {
			conn.User = local.Pg.User
		}
		if local.Pg.Password != "" {
			conn.Password = local.Pg.Password
		}
		if local.Pg.Database != "" {
			conn.Database = local.Pg.Database
		}
		if local.Pg.SSLMode != "" {
			conn.SSLMode = local.Pg.SSLMode
		}
	}
	if os.Getenv("OPENNEKO_PG_ENV_OVERRIDE") == "1" {
		conn.Host = envOr("NEKO_PG_HOST", conn.Host)
		conn.Port = envInt("NEKO_PG_PORT", conn.Port)
		conn.User = envOr("NEKO_PG_USER", conn.User)
		conn.Password = envOr("NEKO_PG_PASSWORD", conn.Password)
		conn.Database = envOr("NEKO_PG_DATABASE", conn.Database)
		conn.SSLMode = envOr("NEKO_PG_SSLMODE", conn.SSLMode)
	}
	file := strings.TrimSpace(os.Getenv("OPENNEKO_DATABASE_URL_FILE"))
	if file != "" {
		raw, err := os.ReadFile(file)
		if err != nil {
			return db.ConnConfig{}, fmt.Errorf("read OPENNEKO_DATABASE_URL_FILE: %w", err)
		}
		url := strings.TrimSpace(string(raw))
		if url == "" {
			return db.ConnConfig{}, fmt.Errorf("OPENNEKO_DATABASE_URL_FILE (%s) is empty", file)
		}
		conn.URL = url
		return conn, nil
	}
	conn.URL = strings.TrimSpace(local.DatabaseURL)
	return conn, nil
}

func envOr(name, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return fallback
}

func envInt(name string, fallback int) int {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return fallback
}
