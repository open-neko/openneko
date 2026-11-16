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

func TestSeedHasNoLocalAlias(t *testing.T) {
	if newSeedCmd().Flags().Lookup("local") != nil {
		t.Fatal("seed still defines --local")
	}
}
