package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/open-neko/neko/apps/openneko/assets"
	"github.com/open-neko/neko/apps/openneko/internal/compose"
	"github.com/open-neko/neko/apps/openneko/internal/config"
	"github.com/open-neko/neko/apps/openneko/internal/instance"
)

func isolateStackDatabase(t *testing.T, volumes ...string) *compose.Supervisor {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("OPENNEKO_DATABASE_URL_FILE", "")
	t.Setenv("OPENNEKO_DATABASE_URL", "")
	t.Setenv(instance.EnvName, "")
	previous := volumeExists
	t.Cleanup(func() { volumeExists = previous })
	volumeExists = func(_ context.Context, _ *compose.Supervisor, name string) (bool, error) {
		for _, volume := range volumes {
			if volume == name {
				return true, nil
			}
		}
		return false, nil
	}
	sup := compose.New(assets.ComposeFS)
	sup.RuntimeDir = t.TempDir()
	return sup
}

func materializedNames(files []string) string {
	var names []string
	for _, file := range files {
		names = append(names, filepath.Base(file))
	}
	return strings.Join(names, ",")
}

func TestProdStartWithoutADatabaseURLStopsBeforeCompose(t *testing.T) {
	sup := isolateStackDatabase(t)
	_, err := stackFiles(context.Background(), sup, compose.ModeProd, "openneko-prod")
	if !errors.Is(err, errNoDatabaseURL) {
		t.Fatalf("err = %v, want errNoDatabaseURL", err)
	}
	for _, hint := range []string{"openneko setup --database-url", "OPENNEKO_DATABASE_URL_FILE", "--mode demo"} {
		if !strings.Contains(err.Error(), hint) {
			t.Fatalf("message does not name %q:\n%s", hint, err)
		}
	}
	if entries, _ := os.ReadDir(sup.RuntimeDir); len(entries) != 0 {
		t.Fatalf("compose files were written before the database check: %v", entries)
	}
}

func TestProdStartPassesTheConfiguredURLToCompose(t *testing.T) {
	sup := isolateStackDatabase(t, "openneko-prod_neko-db-data")
	const url = "postgres://neko:pw@db.internal:5432/neko?sslmode=verify-full"
	if err := config.WriteLocalDatabaseURL("", url); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(config.Dir(""), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "pw@") {
		t.Fatal("config.json holds the database URL in plain text")
	}
	files, err := stackFiles(context.Background(), sup, compose.ModeProd, "openneko-prod")
	if err != nil {
		t.Fatal(err)
	}
	if got := materializedNames(files); got != "core.yml" {
		t.Fatalf("files = %s; a configured URL wins over the old volume", got)
	}
	if os.Getenv("OPENNEKO_DATABASE_URL") != url {
		t.Fatal("compose does not get the database URL")
	}
}

func TestProdStartReadsTheURLFile(t *testing.T) {
	sup := isolateStackDatabase(t)
	file := filepath.Join(t.TempDir(), "database.url")
	if err := os.WriteFile(file, []byte("postgres://neko:pw@db.internal/neko\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENNEKO_DATABASE_URL_FILE", file)
	if _, err := stackFiles(context.Background(), sup, compose.ModeProd, "openneko-prod"); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("OPENNEKO_DATABASE_URL") != "postgres://neko:pw@db.internal/neko" {
		t.Fatal("compose does not get the URL from the file")
	}
}

func TestLegacyProdInstallKeepsTheBundledDatabase(t *testing.T) {
	sup := isolateStackDatabase(t, "openneko_neko-db-data")
	t.Setenv("OPENNEKO_DATABASE_URL", "postgres://stale@elsewhere/neko")
	files, err := stackFiles(context.Background(), sup, compose.ModeProd, "openneko")
	if err != nil {
		t.Fatal(err)
	}
	if got := materializedNames(files); got != "core.yml,bundled-db.yml" {
		t.Fatalf("files = %s", got)
	}
	if _, set := os.LookupEnv("OPENNEKO_DATABASE_URL"); set {
		t.Fatal("a bundled database must not pass a URL to compose")
	}
	if _, err := stackFiles(context.Background(), sup, compose.ModeProd, "openneko-acme-prod"); !errors.Is(err, errNoDatabaseURL) {
		t.Fatalf("another project's volume made this install legacy: %v", err)
	}
}

func TestDevAndDemoAlwaysRunTheBundledDatabase(t *testing.T) {
	sup := isolateStackDatabase(t)
	if err := config.WriteLocalDatabaseURL("", "postgres://neko:pw@db.internal/neko"); err != nil {
		t.Fatal(err)
	}
	for mode, want := range map[compose.Mode]string{
		compose.ModeDev:  "core.yml,dev.yml,bundled-db.yml",
		compose.ModeDemo: "core.yml,demo.yml,bundled-db.yml",
	} {
		files, err := stackFiles(context.Background(), sup, mode, "openneko-"+string(mode))
		if err != nil {
			t.Fatal(err)
		}
		if got := materializedNames(files); got != want {
			t.Fatalf("%s files = %s, want %s", mode, got, want)
		}
	}
}

func TestSetupStoresTheDatabaseURL(t *testing.T) {
	sup := isolateStackDatabase(t)
	asked := false
	ask := func(string) (string, error) {
		asked = true
		return " postgres://neko:pw@db.internal:5432/neko?sslmode=require \n", nil
	}
	if err := ensureSetupDatabaseURL(context.Background(), sup, compose.ModeProd, "", false, ask); !errors.Is(err, errNoDatabaseURL) {
		t.Fatalf("no TTY and no flag: err = %v", err)
	}
	if err := ensureSetupDatabaseURL(context.Background(), sup, compose.ModeProd, "", true, ask); err != nil {
		t.Fatal(err)
	}
	if !asked {
		t.Fatal("setup did not ask for the URL")
	}
	local, _ := config.ReadLocal("")
	if local.DatabaseURL != "postgres://neko:pw@db.internal:5432/neko?sslmode=require" {
		t.Fatalf("stored URL = %q", local.DatabaseURL)
	}
	asked = false
	if err := ensureSetupDatabaseURL(context.Background(), sup, compose.ModeProd, "", true, ask); err != nil || asked {
		t.Fatalf("setup asked again for a stored URL (err %v)", err)
	}
	if err := ensureSetupDatabaseURL(context.Background(), sup, compose.ModeDemo, "postgres://x@db/neko", false, ask); err == nil {
		t.Fatal("--database-url was accepted in demo mode")
	}
}

func TestSetupRefusesADatabaseURLTheContainersCannotUse(t *testing.T) {
	for _, raw := range []string{
		"",
		"db.internal:5432/neko",
		"mysql://neko@db.internal/neko",
		"postgres://neko@localhost:5432/neko",
		"postgres://neko@127.0.0.1/neko",
		"postgresql://neko@[::1]/neko",
	} {
		if checkDatabaseURLShape(raw) == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
	for _, raw := range []string{
		"postgres://neko:pw@db.internal:5432/neko?sslmode=verify-full",
		"postgresql://neko@host.docker.internal/neko",
	} {
		if err := checkDatabaseURLShape(raw); err != nil {
			t.Fatalf("refused %q: %v", raw, err)
		}
	}
}

func TestProdStartMountsTheTLSFilesTheURLNames(t *testing.T) {
	sup := isolateStackDatabase(t)
	dir := t.TempDir()
	write := func(name, body string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	ca, cert, key := write("ca.pem", "CA PEM"), write("client.pem", "CERT PEM"), write("client.key", "KEY PEM")
	for _, env := range []string{"OPENNEKO_DATABASE_CA", "OPENNEKO_DATABASE_CERT", "OPENNEKO_DATABASE_KEY"} {
		t.Setenv(env, "")
	}
	hostURL := "postgres://neko:pw@db.internal:5432/neko?sslmode=verify-full&sslrootcert=" + ca + "&sslcert=" + cert + "&sslkey=" + key
	if err := config.WriteLocalDatabaseURL("", hostURL); err != nil {
		t.Fatal(err)
	}
	files, err := stackFiles(context.Background(), sup, compose.ModeProd, "openneko-prod")
	if err != nil {
		t.Fatal(err)
	}
	if got := materializedNames(files); got != "core.yml,database-tls.yml" {
		t.Fatalf("files = %s", got)
	}
	want := "postgres://neko:pw@db.internal:5432/neko?sslcert=%2Frun%2Fsecrets%2Fdatabase-cert&sslkey=%2Frun%2Fsecrets%2Fdatabase-key&sslmode=verify-full&sslrootcert=%2Frun%2Fsecrets%2Fdatabase-ca"
	if got := os.Getenv("OPENNEKO_DATABASE_URL"); got != want {
		t.Fatalf("container URL = %s", got)
	}
	for env, body := range map[string]string{"OPENNEKO_DATABASE_CA": "CA PEM", "OPENNEKO_DATABASE_CERT": "CERT PEM", "OPENNEKO_DATABASE_KEY": "KEY PEM"} {
		if got := os.Getenv(env); got != body {
			t.Fatalf("%s = %q", env, got)
		}
	}

	if err := config.WriteLocalDatabaseURL("", "postgres://neko:pw@db.internal:5432/neko?sslmode=verify-full&sslrootcert=system"); err != nil {
		t.Fatal(err)
	}
	files, err = stackFiles(context.Background(), sup, compose.ModeProd, "openneko-prod")
	if err != nil {
		t.Fatal(err)
	}
	if got := materializedNames(files); got != "core.yml" || os.Getenv("OPENNEKO_DATABASE_CA") != "" {
		t.Fatalf("files = %s, CA env %q; sslrootcert=system names no file", got, os.Getenv("OPENNEKO_DATABASE_CA"))
	}

	if err := config.WriteLocalDatabaseURL("", "postgres://neko:pw@db.internal/neko?sslmode=verify-full&sslrootcert="+filepath.Join(dir, "missing.pem")); err != nil {
		t.Fatal(err)
	}
	if _, err := stackFiles(context.Background(), sup, compose.ModeProd, "openneko-prod"); err == nil || !strings.Contains(err.Error(), "sslrootcert=") {
		t.Fatalf("a missing CA file: err = %v", err)
	}
}

func TestDatabaseTLSOverlayCoversEveryDatabaseService(t *testing.T) {
	type service struct {
		Secrets []struct {
			Source string `yaml:"source"`
			UID    string `yaml:"uid"`
			Mode   any    `yaml:"mode"`
		} `yaml:"secrets"`
	}
	var core struct {
		Services map[string]service `yaml:"services"`
	}
	raw, err := assets.ComposeFS.ReadFile("compose/core.yml")
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(raw, &core); err != nil {
		t.Fatal(err)
	}
	for name, svc := range core.Services {
		for _, secret := range svc.Secrets {
			if secret.Source == "database-url" && !slices.Contains(databaseServices, name) {
				t.Fatalf("%s reads the database URL and gets no TLS files", name)
			}
		}
	}
	var overlay struct {
		Services map[string]service `yaml:"services"`
		Secrets  map[string]struct {
			Environment string `yaml:"environment"`
		} `yaml:"secrets"`
	}
	if err := yaml.Unmarshal(databaseTLSOverlay([]string{"database-ca", "database-key"}), &overlay); err != nil {
		t.Fatal(err)
	}
	for _, name := range databaseServices {
		if _, ok := core.Services[name]; !ok {
			t.Fatalf("%s is not a core.yml service", name)
		}
		svc := overlay.Services[name]
		if len(svc.Secrets) != 2 || svc.Secrets[0].Source != "database-ca" || svc.Secrets[1].Source != "database-key" || svc.Secrets[1].UID != "1001" {
			t.Fatalf("%s overlay secrets = %+v", name, svc.Secrets)
		}
	}
	if overlay.Secrets["database-ca"].Environment != "OPENNEKO_DATABASE_CA" || overlay.Secrets["database-key"].Environment != "OPENNEKO_DATABASE_KEY" || len(overlay.Secrets) != 2 {
		t.Fatalf("overlay secrets = %+v", overlay.Secrets)
	}
}
