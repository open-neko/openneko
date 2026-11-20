package cli

import (
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

const sandboxService = "openneko-sandbox"

// sandboxRoutesOut merges the compose files the way compose does for the
// sandbox service and names each network setting that gives it a route out.
func sandboxRoutesOut(files []string) ([]string, error) {
	var (
		present     bool
		networkMode string
		joined      []string
		internal    = map[string]bool{}
		external    = map[string]bool{}
	)
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		var doc struct {
			Services map[string]struct {
				NetworkMode *string `yaml:"network_mode"`
				Networks    any     `yaml:"networks"`
			} `yaml:"services"`
			Networks map[string]*struct {
				Internal *bool `yaml:"internal"`
				External any   `yaml:"external"`
			} `yaml:"networks"`
		}
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			return nil, fmt.Errorf("read %s: %w", file, err)
		}
		if svc, ok := doc.Services[sandboxService]; ok {
			present = true
			if svc.NetworkMode != nil {
				networkMode = *svc.NetworkMode
			}
			for _, n := range networkNames(svc.Networks) {
				if !slices.Contains(joined, n) {
					joined = append(joined, n)
				}
			}
		}
		for name, n := range doc.Networks {
			if n == nil {
				continue
			}
			if n.Internal != nil {
				internal[name] = *n.Internal
			}
			if n.External != nil {
				external[name] = n.External != false
			}
		}
	}
	if !present {
		return nil, nil
	}
	if networkMode != "" && networkMode != "none" {
		return []string{"network_mode: " + networkMode}, nil
	}
	if networkMode == "none" {
		return nil, nil
	}
	if len(joined) == 0 {
		joined = []string{"default"}
	}
	var out []string
	for _, n := range joined {
		switch {
		case external[n]:
			out = append(out, "network "+n+" is external, so OpenNeko cannot see whether it is internal")
		case !internal[n]:
			out = append(out, "network "+n+" is not internal")
		}
	}
	return out, nil
}

func networkNames(v any) []string {
	var out []string
	switch n := v.(type) {
	case []any:
		for _, item := range n {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
	case map[string]any:
		for name := range n {
			out = append(out, name)
		}
		slices.Sort(out)
	}
	return out
}

func warnSandboxRoutesOut(out io.Writer, files []string) error {
	reasons, err := sandboxRoutesOut(files)
	if err != nil || len(reasons) == 0 {
		return err
	}
	fmt.Fprintf(out, "\nWARNING: the %s service has a route out: %s.\n", sandboxService, strings.Join(reasons, "; "))
	fmt.Fprintln(out, "WARNING: the agent's terminal stays off until the sandbox reports a closed network. Remove the network settings of the sandbox from compose.override.yml and use recipes/sandbox/compose.yml (INSTALL.md, \"Sandbox\").")
	fmt.Fprintln(out)
	return nil
}
