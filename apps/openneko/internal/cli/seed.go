package cli

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/open-neko/neko/apps/openneko/assets"
	"github.com/open-neko/neko/apps/openneko/internal/compose"
	"github.com/open-neko/neko/apps/openneko/internal/instance"
)

func newSeedCmd() *cobra.Command {
	var (
		loadOnly bool
		orgOnly  bool
	)
	cmd := &cobra.Command{
		Use:   "seed [adventureworks]",
		Short: "Load demo data (currently only adventureworks)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := "adventureworks"
			if len(args) == 1 {
				target = args[0]
			}
			if target != "adventureworks" {
				return WithExit(2, fmt.Errorf("seed: unknown target %q (want: adventureworks)", target))
			}

			if err := requireDemoInstance(loadOnly || orgOnly); err != nil {
				return err
			}

			if loadOnly || orgOnly {
				ctx := cmd.Context()
				out := cmd.OutOrStdout()
				if loadOnly {
					cfg, err := adventureWorksLoadConfigFromEnv(out)
					if err != nil {
						return err
					}
					if err := runAdventureWorksLoad(ctx, cfg); err != nil {
						return err
					}
				}
				if orgOnly {
					cfg, err := adventureWorksOrgSeedConfigFromEnv(out)
					if err != nil {
						return err
					}
					if err := runAdventureWorksOrgSeed(ctx, cfg); err != nil {
						return err
					}
				}
				return nil
			}

			sup := compose.New(assets.ComposeFS)
			project, err := sup.ProjectName(compose.ModeDemo)
			if err != nil {
				return err
			}
			files, err := allStackFiles(sup)
			if err != nil {
				return err
			}
			// Seed always lives under the demo project, regardless of what
			// other mode the operator may be running in this dir.
			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()

			// One-shot: load the AdventureWorks CSVs.
			if code, err := sup.Run(ctx, project, files, []string{"run", "--rm", "adventureworks-init"}, os.Stdout, os.Stderr); err != nil {
				return err
			} else if code != 0 {
				return WithExit(code, errors.New("adventureworks-init failed"))
			}
			// One-shot: seed neko-db workflows that reference adventureworks data.
			if code, err := sup.Run(ctx, project, files, []string{"run", "--rm", "neko-adventureworks-seed"}, os.Stdout, os.Stderr); err != nil {
				return err
			} else if code != 0 {
				return WithExit(code, errors.New("neko-adventureworks-seed failed"))
			}
			fmt.Fprintln(cmd.OutOrStdout(), "demo seed complete — bring the simulators up with `openneko start --mode demo`")
			return nil
		},
	}
	cmd.Flags().BoolVar(&loadOnly, "load", false, "Load the AdventureWorks dataset into the customer database")
	cmd.Flags().BoolVar(&orgOnly, "org", false, "Seed the AdventureWorks organization, data source, workflows, and policy into neko-db")
	return cmd
}

const seedTargetEnv = "OPENNEKO_SEED_TARGET"

// requireDemoInstance refuses a seed of an installation that is not in demo
// mode. With no installation settings here, the step flags write to the
// database that the environment names, so the caller must mark that target as
// a demo with OPENNEKO_SEED_TARGET=demo.
func requireDemoInstance(steps bool) error {
	settings, ok, err := requireConfiguredNamedInstallation()
	if err != nil {
		return err
	}
	name := instance.Current()
	switch {
	case ok && settings.Mode != string(compose.ModeDemo) && name != "":
		return fmt.Errorf("instance %q is installed in %s mode; AdventureWorks seed requires a demo instance", name, settings.Mode)
	case ok && settings.Mode != string(compose.ModeDemo):
		return fmt.Errorf("this installation is in %s mode; AdventureWorks seed requires a demo instance", settings.Mode)
	case ok:
		return nil
	case name != "":
		return fmt.Errorf("instance %q has not been configured; run `openneko --instance %s setup --mode demo` first", name, name)
	case steps && os.Getenv(seedTargetEnv) != string(compose.ModeDemo):
		return fmt.Errorf("no demo installation here, so the seed would write to the database that the environment names; AdventureWorks seed requires a demo instance. Run `openneko setup --mode demo`, or set %s=demo to seed a development database of your own", seedTargetEnv)
	}
	return nil
}
