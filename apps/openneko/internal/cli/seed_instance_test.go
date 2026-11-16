package cli

import (
	"io"
	"strings"
	"testing"

	"github.com/open-neko/neko/apps/openneko/internal/config"
	"github.com/open-neko/neko/apps/openneko/internal/installation"
)

func TestSeedStepFlagsRefuseANonDemoInstance(t *testing.T) {
	for _, flag := range []string{"--org", "--load"} {
		t.Run(flag, func(t *testing.T) {
			isolateNamedInstallation(t, "acme")
			if err := installation.Save(config.Dir(""), installation.Settings{
				Version:        installation.CurrentVersion,
				Instance:       "acme",
				Mode:           "prod",
				WebPort:        3100,
				WebBindAddress: "127.0.0.1",
			}); err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{"NEKO_PG_HOST", "PGHOST"} {
				t.Setenv(key, "127.0.0.1")
			}
			for _, key := range []string{"NEKO_PG_PORT", "PGPORT"} {
				t.Setenv(key, "1")
			}
			t.Setenv("DATABASE_URL", "postgres://x:y@127.0.0.1:1/neko?connect_timeout=1")

			cmd := newSeedCmd()
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cmd.SetArgs([]string{"adventureworks", flag})
			err := cmd.Execute()
			if err == nil || !strings.Contains(err.Error(), "requires a demo instance") {
				t.Fatalf("seed %s on a prod instance: err = %v, want the demo-mode refusal", flag, err)
			}
		})
	}
}

func runSeedStep(t *testing.T, flag string) error {
	t.Helper()
	for _, key := range []string{"NEKO_PG_HOST", "PGHOST"} {
		t.Setenv(key, "127.0.0.1")
	}
	for _, key := range []string{"NEKO_PG_PORT", "PGPORT"} {
		t.Setenv(key, "1")
	}
	t.Setenv("DATABASE_URL", "postgres://x:y@127.0.0.1:1/neko?connect_timeout=1")
	t.Setenv("ADVENTUREWORKS_CACHE_DIR", t.TempDir())
	cmd := newSeedCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"adventureworks", flag})
	return cmd.Execute()
}

func TestSeedStepFlagsReadTheModeOfTheUnnamedInstallation(t *testing.T) {
	for _, flag := range []string{"--org", "--load"} {
		for _, tc := range []struct {
			name, mode, target string
			refused            bool
		}{
			{name: "prod settings", mode: "prod", refused: true},
			{name: "prod settings with the demo mark", mode: "prod", target: "demo", refused: true},
			{name: "no settings", refused: true},
			{name: "no settings with another mark", target: "prod", refused: true},
			{name: "no settings with the demo mark", target: "demo"},
			{name: "demo settings", mode: "demo"},
		} {
			t.Run(flag+" "+tc.name, func(t *testing.T) {
				isolateNamedInstallation(t, "")
				t.Setenv("OPENNEKO_SEED_TARGET", tc.target)
				if tc.mode != "" {
					if err := installation.Save(config.Dir(""), installation.Settings{
						Version:        installation.CurrentVersion,
						Mode:           tc.mode,
						WebPort:        3100,
						WebBindAddress: "127.0.0.1",
					}); err != nil {
						t.Fatal(err)
					}
				}
				err := runSeedStep(t, flag)
				if refused := err != nil && strings.Contains(err.Error(), "requires a demo instance"); refused != tc.refused {
					t.Fatalf("seed %s: err = %v, want refused %v", flag, err, tc.refused)
				}
				if !tc.refused && err == nil {
					t.Fatalf("seed %s reached no database and still passed", flag)
				}
			})
		}
	}
}

func TestSeedHasNoLocalAlias(t *testing.T) {
	if newSeedCmd().Flags().Lookup("local") != nil {
		t.Fatal("seed still defines --local")
	}
}
