package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/open-neko/neko/apps/openneko/internal/plugin/marketplace"
	"github.com/open-neko/neko/apps/openneko/internal/plugin/store"
)

func newMarketplaceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "marketplace",
		Short: "Manage trusted plugin marketplaces",
	}
	cmd.AddCommand(newMarketplaceListCmd(), newMarketplaceAddCmd(), newMarketplaceRemoveCmd())
	return cmd
}

func newMarketplaceListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "Show trusted marketplaces",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := store.Read("")
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(s.Marketplaces) == 0 {
				fmt.Fprintln(out, "no marketplaces trusted")
				return nil
			}
			for _, m := range s.Marketplaces {
				tag := ""
				if m.Official {
					tag = "  [official]"
				}
				fmt.Fprintf(out, "%s  %s%s\n", m.Name, m.URL, tag)
			}
			return nil
		},
	}
}

func newMarketplaceAddCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "add <url>",
		Short: "Trust a third-party marketplace URL",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			url := args[0]
			if url == "" {
				return WithExit(2, errors.New("marketplace add: URL required"))
			}
			client := marketplace.NewClient()
			mp, err := client.Fetch(context.Background(), url)
			if err != nil {
				return err
			}
			slug := store.Slugify(mp.Name)
			if slug == "" {
				return fmt.Errorf("marketplace at %s has an empty or unparseable name", url)
			}
			s, err := store.Read("")
			if err != nil {
				return err
			}
			entry := store.TrustedMarketplace{
				Name:    slug,
				URL:     url,
				AddedAt: time.Now().UTC().Format("2006-01-02"),
			}
			next, err := store.Add(s, entry)
			if err != nil {
				return err
			}
			if err := store.Write(next, ""); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "trusted %s as %q — %d plugin(s) listed\n", mp.Name, slug, len(mp.Plugins))
			return nil
		},
	}
}

func newMarketplaceRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <name-or-url>",
		Short: "Stop trusting a marketplace",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := args[0]
			if target == "" {
				return WithExit(2, errors.New("marketplace remove: name or URL required"))
			}
			s, err := store.Read("")
			if err != nil {
				return err
			}
			next, removed, err := store.Remove(s, target)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if removed == nil {
				fmt.Fprintf(out, "no trusted marketplace matched %s\n", target)
				return nil
			}
			if err := store.Write(next, ""); err != nil {
				return err
			}
			fmt.Fprintf(out, "removed marketplace %s\n", removed.Name)
			return nil
		},
	}
}
