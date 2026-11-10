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

// GraphJin reads its auth key only at start, and web and worker must not
// race the org-row bootstrap, so all three wait for openneko-serve-init.
func TestServeInitGatesTheGraphjinSecretConsumers(t *testing.T) {
	for _, file := range []string{"core.yml", "demo.yml"} {
		raws := [][]byte{readEmbeddedCompose(t, "core.yml")}
		if file != "core.yml" {
			raws = append(raws, readEmbeddedCompose(t, file))
		}
		graph := mergedDependencyGraph(t, raws...)
		for _, consumer := range []string{"openneko-serve", "worker", "web"} {
			if !slices.Contains(graph[consumer], "openneko-serve-init") {
				t.Fatalf("%s: %s does not depend on openneko-serve-init (deps: %v)", file, consumer, graph[consumer])
			}
		}
	}
}
