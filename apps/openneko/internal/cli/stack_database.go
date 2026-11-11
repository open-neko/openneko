package cli

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/open-neko/neko/apps/openneko/internal/compose"
	"github.com/open-neko/neko/apps/openneko/internal/config"
	"github.com/open-neko/neko/apps/openneko/internal/version"
)

const bundledDatabaseVolume = "neko-db-data"

var errNoDatabaseURL = errors.New(`prod mode needs OpenNeko's database URL, and none is set.
Give OpenNeko a Postgres database that you run, then do one of these:
  openneko setup --database-url 'postgres://USER:PASSWORD@HOST:5432/DATABASE?sslmode=verify-full'
  set OPENNEKO_DATABASE_URL_FILE to a file that holds the URL
To try OpenNeko with a bundled database, use --mode demo or --mode dev`)

var volumeExists = func(ctx context.Context, sup *compose.Supervisor, name string) (bool, error) {
	return sup.VolumeExists(ctx, name)
}

type stackDatabase struct {
	url     string
	bundled bool
}

func resolveStackDatabase(ctx context.Context, sup *compose.Supervisor, mode compose.Mode, project string) (stackDatabase, error) {
	if mode == compose.ModeDev || mode == compose.ModeDemo {
		return stackDatabase{bundled: true}, nil
	}
	local, _ := config.ReadLocal("")
	configured, err := databaseURL(local)
	if err != nil {
		return stackDatabase{}, err
	}
	if configured != "" {
		return stackDatabase{url: configured}, nil
	}
	legacy, err := volumeExists(ctx, sup, project+"_"+bundledDatabaseVolume)
	if err != nil {
		return stackDatabase{}, err
	}
	if legacy {
		return stackDatabase{bundled: true}, nil
	}
	return stackDatabase{}, errNoDatabaseURL
}

// stackFiles materializes the compose files for mode and puts the database
// URL into this process's environment, where compose reads the
// database-url secret.
func stackFiles(ctx context.Context, sup *compose.Supervisor, mode compose.Mode, project string) ([]string, error) {
	if err := pinImageVersion(sup); err != nil {
		return nil, err
	}
	database, err := resolveStackDatabase(ctx, sup, mode, project)
	if err != nil {
		return nil, err
	}
	for _, file := range databaseTLSFiles {
		_ = os.Unsetenv(file.env)
	}
	if database.bundled {
		_ = os.Unsetenv("OPENNEKO_DATABASE_URL")
		return sup.MaterializeStack(mode, true)
	}
	containerURL, contents, err := containerDatabaseURL(database.url)
	if err != nil {
		return nil, err
	}
	if err := os.Setenv("OPENNEKO_DATABASE_URL", containerURL); err != nil {
		return nil, fmt.Errorf("pass the database URL to compose: %w", err)
	}
	if len(contents) == 0 {
		return sup.MaterializeStack(mode, false)
	}
	var secrets []string
	for _, file := range databaseTLSFiles {
		content, ok := contents[file.secret]
		if !ok {
			continue
		}
		if err := os.Setenv(file.env, content); err != nil {
			return nil, fmt.Errorf("pass %s to compose: %w", file.param, err)
		}
		secrets = append(secrets, file.secret)
	}
	return sup.MaterializeStack(mode, false, compose.Overlay{Name: "database-tls.yml", Data: databaseTLSOverlay(secrets)})
}

// openneko-serve gets no URL secret, but its role URLs keep the main URL's
// TLS parameters, so it needs the files.
var databaseServices = []string{"database-check", "storage-reconcile", "neko-migrate", "openneko-serve-init", "openneko-serve", "web", "worker"}

var databaseTLSFiles = []struct{ param, secret, env string }{
	{"sslrootcert", "database-ca", "OPENNEKO_DATABASE_CA"},
	{"sslcert", "database-cert", "OPENNEKO_DATABASE_CERT"},
	{"sslkey", "database-key", "OPENNEKO_DATABASE_KEY"},
}

// containerDatabaseURL reads the TLS files that raw names on this host and
// points raw at the copies that compose puts in /run/secrets.
func containerDatabaseURL(raw string) (string, map[string]string, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		return raw, nil, nil
	}
	query := u.Query()
	contents := map[string]string{}
	for _, file := range databaseTLSFiles {
		path := query.Get(file.param)
		if path == "" || (file.param == "sslrootcert" && path == "system") {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", nil, fmt.Errorf("the database URL names %s=%s, and openneko cannot read it: %w", file.param, path, err)
		}
		if len(strings.TrimSpace(string(data))) == 0 {
			return "", nil, fmt.Errorf("the database URL names %s=%s, and the file is empty", file.param, path)
		}
		contents[file.secret] = string(data)
		query.Set(file.param, "/run/secrets/"+file.secret)
	}
	if len(contents) == 0 {
		return raw, nil, nil
	}
	u.RawQuery = query.Encode()
	return u.String(), contents, nil
}

func databaseTLSOverlay(secrets []string) []byte {
	var b strings.Builder
	b.WriteString("# Written by openneko start: the TLS files that the database URL names.\nservices:\n")
	for _, service := range databaseServices {
		fmt.Fprintf(&b, "  %s:\n    secrets:\n", service)
		for _, secret := range secrets {
			fmt.Fprintf(&b, "      - source: %s\n        uid: \"1001\"\n        gid: \"1001\"\n        mode: 0400\n", secret)
		}
	}
	b.WriteString("secrets:\n")
	for _, file := range databaseTLSFiles {
		for _, secret := range secrets {
			if secret == file.secret {
				fmt.Fprintf(&b, "  %s:\n    environment: %s\n", file.secret, file.env)
			}
		}
	}
	return []byte(b.String())
}

// ensureSetupDatabaseURL gives a prod install its database URL before the
// first bring-up: from --database-url, else from a prompt.
func ensureSetupDatabaseURL(ctx context.Context, sup *compose.Supervisor, mode compose.Mode, flagURL string, interactive bool, ask func(string) (string, error)) error {
	flagURL = strings.TrimSpace(flagURL)
	if mode != compose.ModeProd {
		if flagURL != "" {
			return fmt.Errorf("--database-url is for prod mode; %s mode runs its own database", mode)
		}
		return nil
	}
	if flagURL == "" {
		project, err := sup.ProjectName(mode)
		if err != nil {
			return err
		}
		_, err = resolveStackDatabase(ctx, sup, mode, project)
		if !errors.Is(err, errNoDatabaseURL) {
			return err
		}
		if !interactive {
			return err
		}
		flagURL, err = ask("OpenNeko's database URL (postgres://USER:PASSWORD@HOST:5432/DATABASE?sslmode=verify-full): ")
		if err != nil {
			return err
		}
		flagURL = strings.TrimSpace(flagURL)
	}
	if err := checkDatabaseURLShape(flagURL); err != nil {
		return err
	}
	return config.WriteLocalDatabaseURL("", flagURL)
}

func checkDatabaseURLShape(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Hostname() == "" {
		return errors.New("the database URL must look like postgres://USER:PASSWORD@HOST:5432/DATABASE")
	}
	switch strings.ToLower(u.Hostname()) {
	case "localhost", "127.0.0.1", "::1":
		return fmt.Errorf("the database host %q is the container itself; give an address that the OpenNeko containers can reach, such as host.docker.internal or the server's name", u.Hostname())
	}
	return nil
}

func usesBundledDatabase(files []string) bool {
	for _, file := range files {
		if filepath.Base(file) == "bundled-db.yml" {
			return true
		}
	}
	return false
}

// pinImageVersion pins compose's image tags for every command that runs the
// stack: OPENNEKO_VERSION when the caller set it, else the tag that
// `openneko upgrade` stored, else this binary's version.
func pinImageVersion(sup *compose.Supervisor) error {
	if os.Getenv("OPENNEKO_VERSION") != "" {
		return nil
	}
	imageVersion, err := sup.ImageVersion()
	if err != nil {
		return err
	}
	if imageVersion == "" {
		imageVersion = "v" + version.Version
	}
	return os.Setenv("OPENNEKO_VERSION", imageVersion)
}
