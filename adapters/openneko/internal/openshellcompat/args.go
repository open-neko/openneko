// Package openshellcompat adapts the legacy host launch contract to OpenShell 0.0.116.
package openshellcompat

import (
	"fmt"
	"slices"
	"strings"
)

type Plan struct {
	Commands [][]string
	// Cleanup is valid only after this plan's create command succeeds.
	Cleanup []string
}

// Prepare preserves authority flags and translates only OpenNeko's exact cold
// no-op. v0.0.116 requires a live canonical process and forbids upload+detach,
// so uploads follow successful creation. Other invocations pass through unchanged.
func Prepare(args []string) (Plan, error) {
	unchanged := Plan{Commands: [][]string{append([]string(nil), args...)}}
	separator := slices.Index(args, "--")
	if separator < 0 || !slices.Equal(args[separator+1:], []string{"/bin/sh", "-lc", "true"}) {
		return unchanged, nil
	}
	i := 0
	for i < len(args) {
		switch args[i] {
		case "--gateway", "-g", "--gateway-endpoint", "--workspace":
			i += 2
		case "--gateway-insecure", "-v", "-vv", "-vvv":
			i++
		default:
			goto command
		}
	}
command:
	if i+1 >= separator || args[i] != "sandbox" || args[i+1] != "create" {
		return unchanged, nil
	}
	prefix := append([]string(nil), args[:i]...)
	create := append([]string(nil), args[:i+2]...)
	var name string
	var uploads []string
	noIgnore := false
	for j := i + 2; j < separator; j++ {
		switch args[j] {
		case "--no-keep":
			return Plan{}, fmt.Errorf("cannot detach with --no-keep; host must own deletion")
		case "--detach":
			continue
		case "--no-git-ignore":
			noIgnore = true
			continue
		case "--upload":
			j++
			if j >= separator {
				return Plan{}, fmt.Errorf("missing upload argument")
			}
			uploads = append(uploads, args[j])
			continue
		case "--name":
			if j+1 >= separator {
				return Plan{}, fmt.Errorf("missing sandbox name")
			}
			name = args[j+1]
		}
		create = append(create, args[j])
	}
	if name == "" {
		return Plan{}, fmt.Errorf("legacy cold creation requires an explicit sandbox name")
	}
	plan := Plan{Commands: [][]string{append(create, "--detach", "--", "/bin/sleep", "infinity")}, Cleanup: append(append([]string(nil), prefix...), "sandbox", "delete", name)}
	for _, upload := range uploads {
		source, destination, hasDestination := strings.Cut(upload, ":")
		if source == "" || !hasDestination || destination == "" {
			return Plan{}, fmt.Errorf("legacy upload requires explicit source:destination")
		}
		command := append(append([]string(nil), prefix...), "sandbox", "upload", name, source, destination)
		if noIgnore {
			command = append(command, "--no-git-ignore")
		}
		plan.Commands = append(plan.Commands, command)
	}
	return plan, nil
}
