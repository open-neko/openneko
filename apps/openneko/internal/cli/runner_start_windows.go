package cli

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/open-neko/neko/apps/openneko/internal/confine"
	"github.com/open-neko/openneko/apps/ax-harness/serve"
)

// runnerStarter starts each agent runner in an app container of its own.
// The turn's folders must lie inside root, the folder of agent runs, as the
// Linux turn part keeps them.
func runnerStarter(out io.Writer, root string) func(serve.RunnerStart) (serve.RunnerProcess, error) {
	if n, err := confine.RemoveStaleRunners(); err != nil {
		fmt.Fprintf(out, "warning: openneko serve could not read the app containers of earlier runners: %v\n", err)
	} else if n > 0 {
		fmt.Fprintf(out, "openneko serve deleted %d app containers of runners whose serve had ended\n", n)
	}
	return func(r serve.RunnerStart) (serve.RunnerProcess, error) {
		if len(r.Folders) == 0 {
			return nil, fmt.Errorf("the turn names no workspace folder")
		}
		for _, f := range r.Folders {
			if !inside(root, f) {
				return nil, fmt.Errorf("the turn's folder %s is not inside the agent runs folder %s", f, root)
			}
		}
		spec := confine.Spec{ReadWrite: r.Folders, Sockets: r.Links, Exec: []string{r.Args[0]}}
		return confine.StartRunner(spec, r.Args, r.Env, r.Stderr, confine.Limits{CPUSeconds: r.Limits.CPUSeconds, Memory: r.Limits.AddressSpace})
	}
}

func inside(root, p string) bool {
	if root == "" || !filepath.IsAbs(p) {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(p))
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
