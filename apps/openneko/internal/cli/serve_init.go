package cli

import (
	"context"
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
	var dir string
	cmd := &cobra.Command{
		Use:    "serve-init",
		Short:  "Write the narrow credentials that openneko serve reads",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if dir == "" {
				return fmt.Errorf("--dir is required")
			}
			if err := writeServeCredentials(cmd.Context(), defaultConn(), "", dir); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "openneko serve credentials written to %s\n", dir)
			return nil
		},
	}
	cmd.Flags().StringVar(&dir, "dir", os.Getenv(serveCredentialsEnv), "Folder for the serve credentials")
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
	role := admin
	role.User = filesRole
	role.Password = password
	if err := writeFileAtomic(filepath.Join(dir, serveDatabaseFile), []byte(databaseURL(role)+"\n")); err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(dir, serveTokenKeyFile), []byte(hex.EncodeToString(tokenKey)+"\n"))
}

func databaseURL(c db.ConnConfig) string {
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
		User:     url.UserPassword(c.User, c.Password),
		Host:     host + ":" + strconv.Itoa(port),
		Path:     "/" + database,
		RawQuery: url.Values{"sslmode": {ssl}}.Encode(),
	}
	return u.String()
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
