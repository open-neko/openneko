package cli

import (
	"regexp"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

var workerWord = regexp.MustCompile(`(?i)\bworkers?\b`)

func TestHelpTextNamesNoWorker(t *testing.T) {
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		for _, text := range []string{c.Short, c.Long} {
			if workerWord.MatchString(text) {
				t.Errorf("%s: %q", c.CommandPath(), text)
			}
		}
		check := func(f *pflag.Flag) {
			if workerWord.MatchString(f.Usage) {
				t.Errorf("%s --%s: %q", c.CommandPath(), f.Name, f.Usage)
			}
		}
		c.LocalFlags().VisitAll(check)
		c.PersistentFlags().VisitAll(check)
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(NewRoot())
}
