package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/open-neko/neko/apps/openneko/internal/prompt"
	"github.com/open-neko/neko/apps/openneko/internal/secrets"
)

func newSecretsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secrets",
		Short: "Manage per-plugin env values stored at ~/.config/openneko/secrets.json",
	}
	cmd.AddCommand(newSecretsListCmd(), newSecretsSetCmd(), newSecretsUnsetCmd())
	return cmd
}

func newSecretsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list [<plugin>]",
		Short: "Show env keys stored for a plugin (or all plugins); values never echoed",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := secrets.Read("")
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(args) == 1 {
				plugin := args[0]
				keys := secrets.ListKeysForPlugin(store, plugin)
				if len(keys) == 0 {
					fmt.Fprintln(out, "no secrets stored")
					return nil
				}
				fmt.Fprintln(out, plugin)
				for _, k := range keys {
					fmt.Fprintf(out, "  %s\n", k)
				}
				return nil
			}
			if len(store) == 0 {
				fmt.Fprintln(out, "no secrets stored")
				return nil
			}
			plugins := sortedPluginNames(store)
			for _, p := range plugins {
				keys := secrets.ListKeysForPlugin(store, p)
				if len(keys) == 0 {
					continue
				}
				fmt.Fprintln(out, p)
				for _, k := range keys {
					fmt.Fprintf(out, "  %s\n", k)
				}
			}
			return nil
		},
	}
}

func sortedPluginNames(s secrets.Store) []string {
	names := make([]string, 0, len(s))
	for n := range s {
		names = append(names, n)
	}
	// reuse secrets.ListKeysForPlugin's behavior: alphabetical
	sortStrings(names)
	return names
}

func sortStrings(s []string) {
	// avoid pulling sort directly here to keep deps minimal in this file
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

func newSecretsSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set <plugin> <key> [<value>]",
		Short: "Set an env value for a plugin",
		Args:  cobra.RangeArgs(2, 3),
		RunE: func(cmd *cobra.Command, args []string) error {
			plugin := args[0]
			key := args[1]
			if plugin == "" || key == "" {
				return WithExit(2, errors.New("secrets set: plugin and key required"))
			}
			if !secrets.IsValidEnvKey(key) {
				return fmt.Errorf(`secrets set: key %q must be UPPER_SNAKE_CASE`, key)
			}
			var value string
			if len(args) == 3 {
				value = args[2]
			} else {
				if !prompt.IsInteractive() {
					return WithExit(2, fmt.Errorf("secrets set: value required when stdin is not a TTY (pass it as the third arg)"))
				}
				v, err := prompt.Hidden(key + " (hidden): ")
				if err != nil {
					return err
				}
				value = v
			}
			s, err := secrets.Read("")
			if err != nil {
				return err
			}
			existing := map[string]bool{}
			for _, k := range secrets.ListKeysForPlugin(s, plugin) {
				existing[k] = true
			}
			next, err := secrets.Set(s, plugin, key, value)
			if err != nil {
				return err
			}
			if err := secrets.Write(next, ""); err != nil {
				return err
			}
			verb := "updated"
			if !existing[key] {
				verb = "set"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s %s/%s\n", verb, plugin, key)
			return nil
		},
	}
}

func newSecretsUnsetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unset <plugin> <key>",
		Short: "Remove an env value",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			plugin := args[0]
			key := args[1]
			if plugin == "" || key == "" {
				return WithExit(2, errors.New("secrets unset: plugin and key required"))
			}
			s, err := secrets.Read("")
			if err != nil {
				return err
			}
			next, removed := secrets.Unset(s, plugin, key)
			out := cmd.OutOrStdout()
			if !removed {
				fmt.Fprintf(out, "%s/%s was not set\n", plugin, key)
				return nil
			}
			if err := secrets.Write(next, ""); err != nil {
				return err
			}
			fmt.Fprintf(out, "unset %s/%s\n", plugin, key)
			return nil
		},
	}
}
