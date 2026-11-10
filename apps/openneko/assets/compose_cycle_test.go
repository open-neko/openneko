package assets

import (
	"fmt"
	"os"
	"slices"
	"sort"
	"testing"
)

// The dependency graph is only real once the mode overlays are merged:
// Compose unions depends_on maps across files, so an edge that is harmless
// in one file can close a cycle against an edge in another (exactly how
// `graphjin -> worker` in core met `worker -> graphjin` in the demo overlay
// and broke every demo and dev bring-up). These tests merge each shipped
// file combination the way Compose does and reject cycles.

func mergedDependencyGraph(t *testing.T, rawFiles ...[]byte) map[string][]string {
	t.Helper()
	graph := map[string][]string{}
	for _, raw := range rawFiles {
		document := loadComposeParityDocument(t, raw)
		for name, service := range document.Services {
			if _, ok := graph[name]; !ok {
				graph[name] = nil
			}
			for dep := range service.DependsOn {
				graph[name] = append(graph[name], dep)
			}
		}
	}
	for name := range graph {
		sort.Strings(graph[name])
	}
	return graph
}

func findDependencyCycle(graph map[string][]string) []string {
	const (
		unvisited = 0
		visiting  = 1
		done      = 2
	)
	state := map[string]int{}
	var stack []string

	var visit func(node string) []string
	visit = func(node string) []string {
		switch state[node] {
		case visiting:
			// Trim the stack to the cycle itself for a readable failure.
			for i, n := range stack {
				if n == node {
					return append(append([]string{}, stack[i:]...), node)
				}
			}
			return []string{node, node}
		case done:
			return nil
		}
		state[node] = visiting
		stack = append(stack, node)
		for _, dep := range graph[node] {
			if cycle := visit(dep); cycle != nil {
				return cycle
			}
		}
		stack = stack[:len(stack)-1]
		state[node] = done
		return nil
	}

	for _, node := range sortedKeys(graph) {
		if cycle := visit(node); cycle != nil {
			return cycle
		}
	}
	return nil
}

func readEmbeddedCompose(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := ComposeFS.ReadFile("compose/" + name)
	if err != nil {
		t.Fatalf("read embedded %s: %v", name, err)
	}
	return raw
}

func readRootCompose(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile("../../../" + name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return raw
}

func TestPackagedComposeModesAreAcyclic(t *testing.T) {
	// Mirrors internal/compose.Materialize: core + mode overlay.
	combos := map[string][]string{
		"prod": {"core.yml"},
		"dev":  {"core.yml", "dev.yml"},
		"demo": {"core.yml", "demo.yml"},
	}
	for mode, files := range combos {
		t.Run(mode, func(t *testing.T) {
			var raws [][]byte
			for _, file := range files {
				raws = append(raws, readEmbeddedCompose(t, file))
			}
			if cycle := findDependencyCycle(mergedDependencyGraph(t, raws...)); cycle != nil {
				t.Fatalf("mode %s: dependency cycle in merged compose graph: %s",
					mode, fmt.Sprint(cycle))
			}
		})
	}
}

func TestSourceComposeOverlaysAreAcyclic(t *testing.T) {
	combos := map[string][]string{
		"base":           {"compose.yml"},
		"adventureworks": {"compose.yml", "compose.adventureworks.yml"},
		"dev":            {"compose.yml", "compose.adventureworks.yml", "compose.dev.yml"},
		"sandbox":        {"compose.yml", "recipes/sandbox/compose.yml"},
	}
	for name, files := range combos {
		t.Run(name, func(t *testing.T) {
			var raws [][]byte
			for _, file := range files {
				raws = append(raws, readRootCompose(t, file))
			}
			if cycle := findDependencyCycle(mergedDependencyGraph(t, raws...)); cycle != nil {
				t.Fatalf("%s: dependency cycle in merged compose graph: %s",
					name, fmt.Sprint(cycle))
			}
		})
	}
}

func stepServices(t *testing.T, rawFiles ...[]byte) map[string]bool {
	t.Helper()
	steps := map[string]bool{}
	for _, raw := range rawFiles {
		for name, service := range loadComposeParityDocument(t, raw).Services {
			if slices.Contains(service.Profiles, "steps") {
				steps[name] = true
			}
		}
	}
	return steps
}

// `openneko start` runs the steps before `up`, so a running service that
// depends on one would make `up` run it again or refuse the project. The
// chain inside the profile lets `compose run --rm openneko-serve-init` run
// every step in order from a source checkout.
func TestStartStepsStayOutOfTheUpGraph(t *testing.T) {
	chain := map[string]string{
		"neko-migrate":         "storage-reconcile",
		"openneko-config-init": "neko-migrate",
		"openneko-serve-init":  "openneko-config-init",
	}
	combos := map[string][][]byte{
		"prod":                  {readEmbeddedCompose(t, "core.yml")},
		"dev":                   {readEmbeddedCompose(t, "core.yml"), readEmbeddedCompose(t, "dev.yml")},
		"demo":                  {readEmbeddedCompose(t, "core.yml"), readEmbeddedCompose(t, "demo.yml")},
		"source":                {readRootCompose(t, "compose.yml")},
		"source adventureworks": {readRootCompose(t, "compose.yml"), readRootCompose(t, "compose.adventureworks.yml")},
		"source dev":            {readRootCompose(t, "compose.yml"), readRootCompose(t, "compose.adventureworks.yml"), readRootCompose(t, "compose.dev.yml")},
	}
	for name, raws := range combos {
		graph := mergedDependencyGraph(t, raws...)
		steps := stepServices(t, raws...)
		for step, previous := range chain {
			if !steps[step] || !steps[previous] {
				t.Fatalf("%s: %s and %s must be in the steps profile (steps: %v)", name, step, previous, steps)
			}
			if !slices.Contains(graph[step], previous) {
				t.Fatalf("%s: %s does not depend on %s", name, step, previous)
			}
		}
		for service, deps := range graph {
			if steps[service] {
				continue
			}
			for _, dep := range deps {
				if steps[dep] {
					t.Fatalf("%s: %s depends on the start step %s", name, service, dep)
				}
			}
		}
	}

	var running []string
	for service := range mergedDependencyGraph(t, readEmbeddedCompose(t, "core.yml")) {
		if !stepServices(t, readEmbeddedCompose(t, "core.yml"))[service] {
			running = append(running, service)
		}
	}
	sort.Strings(running)
	if want := []string{"neko-db", "openneko-serve", "web", "worker"}; !slices.Equal(running, want) {
		t.Fatalf("prod-mode compose runs %v, want %v", running, want)
	}

	demo := stepServices(t, readEmbeddedCompose(t, "core.yml"), readEmbeddedCompose(t, "demo.yml"))
	if !demo["neko-adventureworks-seed"] {
		t.Fatal("demo: the AdventureWorks seed must be a start step")
	}
}
