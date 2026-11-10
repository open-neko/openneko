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
	database, err := resolveStackDatabase(ctx, sup, mode, project)
	if err != nil {
		return nil, err
	}
	if database.bundled {
		_ = os.Unsetenv("OPENNEKO_DATABASE_URL")
	} else if err := os.Setenv("OPENNEKO_DATABASE_URL", database.url); err != nil {
		return nil, fmt.Errorf("pass the database URL to compose: %w", err)
	}
	return sup.MaterializeStack(mode, database.bundled)
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
