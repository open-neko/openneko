package cli

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/open-neko/neko/apps/openneko/internal/compose"
	"github.com/open-neko/neko/apps/openneko/internal/config"
	"github.com/open-neko/neko/apps/openneko/internal/instance"
)

func TestAgentImageRef(t *testing.T) {
	if got := agentImageRef("", "v1.2.3"); got != "ghcr.io/open-neko/agent:v1.2.3" {
		t.Fatalf("default: got %q", got)
	}
	if got := agentImageRef("my.registry/agent:custom", "v1.2.3"); got != "my.registry/agent:custom" {
		t.Fatalf("override should win: got %q", got)
	}
}

func TestOpenShellStateDirOverride(t *testing.T) {
	// macOS with nothing set → under $HOME (OrbStack maps only $HOME into its VM).
	if got := openShellStateDirOverride("darwin", "/Users/x", ""); got != "/Users/x/.openneko/openshell" {
		t.Fatalf("darwin: got %q", got)
	}
	// Linux → keep the compose default (empty = no override).
	if got := openShellStateDirOverride("linux", "/home/x", ""); got != "" {
		t.Fatalf("linux should not override: got %q", got)
	}
	// An explicit existing value is always respected (no override).
	if got := openShellStateDirOverride("darwin", "/Users/x", "/custom/state"); got != "" {
		t.Fatalf("existing value must win: got %q", got)
	}
}

func TestConfigureOpenShellStateDirScopesNamedInstance(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)
	t.Setenv(instance.EnvName, "acme")
	t.Setenv("OPENSHELL_STATE_DIR", filepath.Join(t.TempDir(), "another-customer"))
	if err := configureOpenShellStateDir(); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(stateHome, "openneko", "instances", "acme", "openshell")
	if got := os.Getenv("OPENSHELL_STATE_DIR"); got != want {
		t.Fatalf("OPENSHELL_STATE_DIR = %q, want %q", got, want)
	}
}

func TestConfigureOpenShellDBURLScopesNamedInstance(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv(instance.EnvName, "acme")
	t.Setenv(openShellDBPasswordEnv, "another-customer-password")
	t.Setenv("OPENSHELL_DB_URL", "postgres://other:secret@other-db:5432/other")

	configureOpenShellDBURL()
	wantPassword, err := config.OpenShellDBPassword("")
	if err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv(openShellDBPasswordEnv); got != wantPassword {
		t.Fatal("named OpenShell password was not derived from selected config")
	}
	if got := os.Getenv("OPENSHELL_DB_URL"); !strings.Contains(got, "@neko-db:5432/") || strings.Contains(got, "other-db") {
		t.Fatalf("named OPENSHELL_DB_URL = %q", got)
	}
}

func TestConfigureOpenShellNetworkUsesDistinctModeDefaults(t *testing.T) {
	for _, tc := range []struct {
		mode        compose.Mode
		wantSubnet  string
		wantGateway string
		wantIP      string
	}{
		{compose.ModeProd, "172.29.0.0/24", "172.29.0.1", "172.29.0.2"},
		{compose.ModeDev, "172.29.1.0/24", "172.29.1.1", "172.29.1.2"},
		{compose.ModeDemo, "172.29.2.0/24", "172.29.2.1", "172.29.2.2"},
	} {
		t.Run(string(tc.mode), func(t *testing.T) {
			t.Setenv(openShellNetworkSubnetEnv, "")
			t.Setenv(openShellNetworkGatewayEnv, "")
			t.Setenv(openShellNetworkIPRangeEnv, "")
			t.Setenv(openShellGatewayIPEnv, "")
			if err := configureOpenShellNetwork(tc.mode); err != nil {
				t.Fatal(err)
			}
			if got := os.Getenv(openShellNetworkSubnetEnv); got != tc.wantSubnet {
				t.Fatalf("subnet = %q, want %q", got, tc.wantSubnet)
			}
			if got := os.Getenv(openShellNetworkGatewayEnv); got != tc.wantGateway {
				t.Fatalf("network gateway = %q, want %q", got, tc.wantGateway)
			}
			if got := os.Getenv(openShellGatewayIPEnv); got != tc.wantIP {
				t.Fatalf("gateway IP = %q, want %q", got, tc.wantIP)
			}
			wantRange := strings.Replace(tc.wantSubnet, ".0/24", ".128/25", 1)
			if got := os.Getenv(openShellNetworkIPRangeEnv); got != wantRange {
				t.Fatalf("dynamic IP range = %q, want %q", got, wantRange)
			}
		})
	}
}

func TestConfigureOpenShellNetworkDerivesCustomGatewayIP(t *testing.T) {
	t.Setenv(openShellNetworkSubnetEnv, "10.77.8.0/24")
	t.Setenv(openShellNetworkGatewayEnv, "")
	t.Setenv(openShellNetworkIPRangeEnv, "")
	t.Setenv(openShellGatewayIPEnv, "")
	if err := configureOpenShellNetwork(compose.ModeProd); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv(openShellGatewayIPEnv); got != "10.77.8.2" {
		t.Fatalf("gateway IP = %q, want 10.77.8.2", got)
	}
	if got := os.Getenv(openShellNetworkGatewayEnv); got != "10.77.8.1" {
		t.Fatalf("network gateway = %q, want 10.77.8.1", got)
	}
	if got := os.Getenv(openShellNetworkIPRangeEnv); got != "10.77.8.128/25" {
		t.Fatalf("dynamic IP range = %q, want 10.77.8.128/25", got)
	}
}

func TestConfigureOpenShellNetworkRejectsGatewayOutsideSubnet(t *testing.T) {
	t.Setenv(openShellNetworkSubnetEnv, "10.77.8.0/24")
	t.Setenv(openShellNetworkGatewayEnv, "")
	t.Setenv(openShellNetworkIPRangeEnv, "")
	t.Setenv(openShellGatewayIPEnv, "10.88.0.2")
	err := configureOpenShellNetwork(compose.ModeProd)
	if err == nil || !strings.Contains(err.Error(), openShellGatewayIPEnv) {
		t.Fatalf("error = %v, want %s validation", err, openShellGatewayIPEnv)
	}
}

func TestConfigureOpenShellNetworkRejectsBridgeGatewayOutsideSubnet(t *testing.T) {
	t.Setenv(openShellNetworkSubnetEnv, "10.77.8.0/24")
	t.Setenv(openShellNetworkGatewayEnv, "10.88.0.1")
	t.Setenv(openShellNetworkIPRangeEnv, "")
	t.Setenv(openShellGatewayIPEnv, "")
	err := configureOpenShellNetwork(compose.ModeProd)
	if err == nil || !strings.Contains(err.Error(), openShellNetworkGatewayEnv) {
		t.Fatalf("error = %v, want %s validation", err, openShellNetworkGatewayEnv)
	}
}

func TestConfigureOpenShellDBURL(t *testing.T) {
	// Operator-set URL always wins.
	t.Setenv(instance.EnvName, "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv(openShellDBPasswordEnv, "")
	t.Setenv("OPENSHELL_DB_URL", "postgres://op:set@elsewhere:5432/db")
	configureOpenShellDBURL()
	if got := os.Getenv("OPENSHELL_DB_URL"); got != "postgres://op:set@elsewhere:5432/db" {
		t.Fatalf("operator value must win: got %q", got)
	}

	// No operator override -> URL derived for the dedicated `openshell` role
	// with the per-install secret-key password (NOT the neko password), host
	// pinned to the compose network, database defaulting to neko.
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv(openShellDBPasswordEnv, "")
	t.Setenv("OPENSHELL_DB_URL", "")
	configureOpenShellDBURL()
	got := os.Getenv("OPENSHELL_DB_URL")
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("derived URL not parseable: %q (%v)", got, err)
	}
	if u.User.Username() != "openshell" {
		t.Fatalf("role should be openshell, got %q (url %q)", u.User.Username(), got)
	}
	if u.Host != "neko-db:5432" || u.Path != "/neko" {
		t.Fatalf("host/db wrong: %q", got)
	}
	pw, _ := u.User.Password()
	want, _ := config.OpenShellDBPassword("")
	if pw != want || len(pw) != 64 { // sha256 hex
		t.Fatalf("password should be the derived secret-key value (64 hex): got %d chars", len(pw))
	}
	if propagated := os.Getenv(openShellDBPasswordEnv); propagated != pw {
		t.Fatal("gateway URL and managed-migration credential diverged")
	}

	// A rotated neko password in config.json must NOT leak into the gateway URL
	// (the role is decoupled), and a custom database name is honored.
	dir2 := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir2)
	t.Setenv(openShellDBPasswordEnv, "")
	if err := os.MkdirAll(filepath.Join(dir2, "openneko"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := `{"pg":{"user":"neko","password":"p@ss:w/rd","database":"customdb"}}`
	if err := os.WriteFile(filepath.Join(dir2, "openneko", "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENSHELL_DB_URL", "")
	configureOpenShellDBURL()
	got = os.Getenv("OPENSHELL_DB_URL")
	if !strings.Contains(got, "@neko-db:5432/customdb") {
		t.Fatalf("custom database not honored: %q", got)
	}
	if strings.Contains(got, "p%40ss") || strings.Contains(got, "neko:") {
		t.Fatalf("neko password leaked into the gateway URL: %q", got)
	}
	if u2, _ := url.Parse(got); u2.User.Username() != "openshell" {
		t.Fatalf("role should be openshell, got url %q", got)
	}
}
