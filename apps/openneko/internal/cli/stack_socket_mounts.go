package cli

import (
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

const stackSocketVolume = "openneko-run"

var stackSocketServices = []string{"web", "openneko-serve"}

// stackSocketMounts lists each service outside web and serve that mounts the
// volume of serve's sockets in one of the compose files.
func stackSocketMounts(files []string) ([]string, error) {
	var found []string
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		var doc struct {
			Services map[string]struct {
				Volumes []any `yaml:"volumes"`
			} `yaml:"services"`
		}
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			return nil, fmt.Errorf("read %s: %w", file, err)
		}
		for name, svc := range doc.Services {
			if slices.Contains(stackSocketServices, name) || slices.Contains(found, name) {
				continue
			}
			for _, v := range svc.Volumes {
				if mountSource(v) == stackSocketVolume {
					found = append(found, name)
					break
				}
			}
		}
	}
	slices.Sort(found)
	return found, nil
}

func mountSource(v any) string {
	switch m := v.(type) {
	case string:
		source, _, _ := strings.Cut(m, ":")
		return source
	case map[string]any:
		source, _ := m["source"].(string)
		return source
	}
	return ""
}

func warnStackSocketMounts(out io.Writer, files []string) error {
	services, err := stackSocketMounts(files)
	if err != nil || len(services) == 0 {
		return err
	}
	fmt.Fprintf(out, "\nWARNING: these services mount %s, the folder of serve's sockets: %s.\n", stackSocketVolume, strings.Join(services, ", "))
	fmt.Fprintln(out, "WARNING: a command in those services can reach serve.sock and config-check.sock.")
	fmt.Fprintln(out, "WARNING: give the sandbox the openneko-sandbox-socket volume instead (INSTALL.md, \"Sandbox\").")
	fmt.Fprintln(out)
	return nil
}
