package cli

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/spf13/cobra"

	"github.com/open-neko/neko/apps/openneko/internal/config"
	"github.com/open-neko/neko/apps/openneko/internal/db"
	"github.com/open-neko/neko/apps/openneko/internal/files"
)

const (
	filesRole           = "neko_files"
	filesRoleLabel      = "openneko-files-role:v1"
	serveTokenKeyFile   = "token.key"
	serveDatabaseFile   = "database.url"
	serveCredentialsEnv = "OPENNEKO_SERVE_CREDENTIALS"
)

func newServeInitCmd() *cobra.Command {
	var dir, gjConfig, gjSeed string
	cmd := &cobra.Command{
		Use:    "serve-init",
		Short:  "Write the narrow credentials that openneko serve reads",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if dir == "" {
				return fmt.Errorf("--dir is required")
			}
			admin, err := defaultConn()
			if err != nil {
				return err
			}
			if err := writeServeCredentials(cmd.Context(), admin, "", dir); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "openneko serve credentials written to %s\n", dir)
			if gjConfig == "" {
				return nil
			}
			created, err := seedGraphjinConfig(gjConfig, gjSeed)
			if err != nil {
				return err
			}
			if created {
				fmt.Fprintf(cmd.OutOrStdout(), "customer GraphJin config written to %s\n", gjConfig)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&dir, "dir", os.Getenv(serveCredentialsEnv), "Folder for the serve credentials")
	cmd.Flags().StringVar(&gjConfig, "graphjin-config", os.Getenv("OPENNEKO_GRAPHJIN_CONFIG"), "Customer GraphJin config file to create when it is missing; empty skips this step")
	cmd.Flags().StringVar(&gjSeed, "graphjin-seed", os.Getenv("OPENNEKO_GRAPHJIN_SEED"), "Template for a new customer GraphJin config")
	return cmd
}

func writeServeCredentials(ctx context.Context, admin db.ConnConfig, configDir, dir string) error {
	if err := config.EnsureSecretKey(configDir); err != nil {
		return fmt.Errorf("read the secret-key: %w", err)
	}
	tokenKey, err := config.SigningSecret(configDir, files.TokenLabel)
	if err != nil {
		return err
	}
	pw, err := config.SigningSecret(configDir, filesRoleLabel)
	if err != nil {
		return err
	}
	password := hex.EncodeToString(pw)

	conn, err := pgx.Connect(ctx, admin.DSN())
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, fmt.Sprintf("alter role %s with login password '%s'", filesRole, password)); err != nil {
		return fmt.Errorf("set the %s password (the role comes from migration 0127; the database login needs the right to alter it): %w", filesRole, err)
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	roleURL, err := roleDatabaseURL(admin, filesRole, password)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(filepath.Join(dir, serveDatabaseFile), []byte(roleURL+"\n")); err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(dir, serveTokenKeyFile), []byte(hex.EncodeToString(tokenKey)+"\n"))
}

const graphjinKeystorePlaceholder = "REPLACE_WITH_BASE64_32_BYTE_KEY"

func seedGraphjinConfig(file, seed string) (bool, error) {
	dir := filepath.Dir(file)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false, err
	}
	_ = os.Remove(filepath.Join(dir, ".openneko-graphjin-supervisor.sh"))
	if _, err := os.Stat(file); err == nil {
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, err
	}
	if seed == "" {
		return false, fmt.Errorf("%s is missing and no --graphjin-seed is set", file)
	}
	raw, err := os.ReadFile(seed)
	if err != nil {
		return false, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return false, err
	}
	conf := strings.ReplaceAll(string(raw), graphjinKeystorePlaceholder, base64.StdEncoding.EncodeToString(key))
	return true, writeFileAtomic(file, []byte(conf))
}

func roleDatabaseURL(c db.ConnConfig, user, password string) (string, error) {
	if c.URL != "" {
		u, err := url.Parse(c.URL)
		if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
			return "", fmt.Errorf("the database URL must start with postgres:// or postgresql://")
		}
		u.User = url.UserPassword(user, password)
		return u.String(), nil
	}
	host := c.Host
	if host == "" {
		host = "localhost"
	}
	port := c.Port
	if port == 0 {
		port = 5432
	}
	ssl := c.SSLMode
	if ssl == "" {
		ssl = "disable"
	}
	database := c.Database
	if database == "" {
		database = "neko"
	}
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(user, password),
		Host:     host + ":" + strconv.Itoa(port),
		Path:     "/" + database,
		RawQuery: url.Values{"sslmode": {ssl}}.Encode(),
	}
	return u.String(), nil
}

func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func readServeCredentials(dir string) (tokenKey []byte, dsn string, err error) {
	raw, err := os.ReadFile(filepath.Join(dir, serveTokenKeyFile))
	if err != nil {
		return nil, "", err
	}
	if tokenKey, err = hex.DecodeString(strings.TrimSpace(string(raw))); err != nil || len(tokenKey) == 0 {
		return nil, "", fmt.Errorf("%s holds no key", filepath.Join(dir, serveTokenKeyFile))
	}
	raw, err = os.ReadFile(filepath.Join(dir, serveDatabaseFile))
	if err != nil {
		return nil, "", err
	}
	return tokenKey, strings.TrimSpace(string(raw)), nil
}
