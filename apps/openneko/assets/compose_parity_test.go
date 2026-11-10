package assets

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type composeParityDocument struct {
	Services map[string]composeParityService `yaml:"services"`
	Volumes  map[string]any                  `yaml:"volumes"`
}

type composeParityService struct {
	Image       string                             `yaml:"image"`
	User        string                             `yaml:"user"`
	Entrypoint  []string                           `yaml:"entrypoint"`
	Restart     string                             `yaml:"restart"`
	ReadOnly    bool                               `yaml:"read_only"`
	CPUs        string                             `yaml:"cpus"`
	MemLimit    string                             `yaml:"mem_limit"`
	PidsLimit   int                                `yaml:"pids_limit"`
	NetworkMode string                             `yaml:"network_mode"`
	CapDrop     []string                           `yaml:"cap_drop"`
	Tmpfs       []string                           `yaml:"tmpfs"`
	Profiles    []string                           `yaml:"profiles"`
	DependsOn   map[string]composeParityDependency `yaml:"depends_on"`
	Environment map[string]any                     `yaml:"environment"`
	Volumes     []string                           `yaml:"volumes"`
	Ports       []string                           `yaml:"ports"`
	Command     []string                           `yaml:"command"`
	Healthcheck map[string]any                     `yaml:"healthcheck"`
	Networks    any                                `yaml:"networks"`
}

func composeNetworkAliases(service composeParityService, network string) []string {
	networks, _ := service.Networks.(map[string]any)
	entry, _ := networks[network].(map[string]any)
	raw, _ := entry["aliases"].([]any)
	var aliases []string
	for _, alias := range raw {
		if s, ok := alias.(string); ok {
			aliases = append(aliases, s)
		}
	}
	return aliases
}

func TestEmbeddingIsBoundedAndLibrarianIsGone(t *testing.T) {
	root := repoRootForTest(t)
	sourceRaw, err := os.ReadFile(filepath.Join(root, "compose.yml"))
	if err != nil {
		t.Fatal(err)
	}
	packagedRaw, err := ComposeFS.ReadFile("compose/core.yml")
	if err != nil {
		t.Fatal(err)
	}

	for label, document := range map[string]composeParityDocument{
		"source":   loadComposeParityDocument(t, sourceRaw),
		"packaged": loadComposeParityDocument(t, packagedRaw),
	} {
		if _, ok := document.Services["librarian"]; ok {
			t.Fatalf("%s compose still runs the retired librarian", label)
		}
		if _, ok := document.Services["worker"].DependsOn["librarian"]; ok {
			t.Fatalf("%s worker still waits for the retired librarian", label)
		}
		if _, ok := document.Services["embedding"]; ok {
			t.Fatalf("%s embedding is an external service, not a core service", label)
		}
		for _, consumer := range []string{"web", "worker"} {
			if document.Services[consumer].Environment["NEKO_EMBEDDING_URL"] != "${NEKO_EMBEDDING_URL:-}" {
				t.Fatalf("%s %s embedding endpoint must come from the operator", label, consumer)
			}
		}
	}

}

func TestCustomerGraphJinRunsInServe(t *testing.T) {
	root := repoRootForTest(t)
	read := func(path string) composeParityDocument {
		var raw []byte
		var err error
		if strings.HasPrefix(path, "compose/") {
			raw, err = ComposeFS.ReadFile(path)
		} else {
			raw, err = os.ReadFile(filepath.Join(root, path))
		}
		if err != nil {
			t.Fatal(err)
		}
		return loadComposeParityDocument(t, raw)
	}
	for _, path := range []string{"compose.yml", "compose.adventureworks.yml", "compose.dev.yml", "compose/core.yml", "compose/demo.yml"} {
		doc := read(path)
		for _, gone := range []string{"graphjin", "graphjin-config-init", "graphjin-secret-init"} {
			if _, ok := doc.Services[gone]; ok {
				t.Errorf("%s still defines the %s service; the customer GraphJin runs in openneko-serve", path, gone)
			}
		}
		for name, service := range doc.Services {
			for _, gone := range []string{"graphjin", "graphjin-secret-init"} {
				if _, ok := service.DependsOn[gone]; ok {
					t.Errorf("%s: %s waits for the removed %s service", path, name, gone)
				}
			}
		}
	}

	for _, path := range []string{"compose.yml", "compose/core.yml"} {
		doc := read(path)
		serve := doc.Services["openneko-serve"]
		if got := serve.Environment["OPENNEKO_GRAPHJIN_CONFIG"]; got != "/config/graphjin/agentic.yml" {
			t.Errorf("%s serve GraphJin config = %#v", path, got)
		}
		if got := serve.Environment["OPENNEKO_GRAPHJIN_ADDR"]; got != "0.0.0.0:8080" {
			t.Errorf("%s serve GraphJin address = %#v", path, got)
		}
		if got := composeNetworkAliases(serve, "default"); !reflect.DeepEqual(got, []string{"graphjin"}) {
			t.Errorf("%s serve must answer as graphjin for existing configs, aliases = %v", path, got)
		}
		for _, name := range []string{"openneko-serve", "web", "worker"} {
			if got := doc.Services[name].DependsOn["openneko-serve-init"].Condition; got != "service_completed_successfully" {
				t.Errorf("%s: %s must start after serve-init writes the per-org JWT secret, got %q", path, name, got)
			}
		}
		volume := composeMountSourceAt(serve.Volumes, "/config/graphjin")
		for _, name := range []string{"openneko-serve-init", "web", "worker"} {
			if got := composeMountSourceAt(doc.Services[name].Volumes, "/config/graphjin"); got != volume || got == "" {
				t.Errorf("%s: %s mounts %q at /config/graphjin, serve mounts %q", path, name, got, volume)
			}
		}
		if got := doc.Services["openneko-serve-init"].Environment["OPENNEKO_GRAPHJIN_CONFIG"]; got != "/config/graphjin/agentic.yml" {
			t.Errorf("%s serve-init seeds %#v", path, got)
		}
	}

	demo := read("compose/demo.yml")
	for _, name := range []string{"openneko-serve-init", "openneko-serve"} {
		if got := composeMountSourceAt(demo.Services[name].Volumes, "/config/graphjin"); got != "graphjin-config" {
			t.Errorf("demo %s mounts %q at /config/graphjin, want graphjin-config", name, got)
		}
	}
	if got := demo.Services["openneko-serve-init"].Environment["OPENNEKO_GRAPHJIN_SEED"]; got != "/seed/dev.sources.yml" {
		t.Errorf("demo seed = %#v", got)
	}
	if got := demo.Services["openneko-serve-init"].DependsOn["neko-adventureworks-seed"].Condition; got != "service_completed_successfully" {
		t.Errorf("demo serve-init must run after the seed creates the demo org, got %q", got)
	}

	dockerfile, err := os.ReadFile(filepath.Join(root, "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	for _, seed := range []string{
		"COPY db/graphjin/customer.sources.example.yml /seed/customer.sources.yml",
		"COPY db/graphjin/dev.sources.example.yml /seed/dev.sources.yml",
	} {
		if !strings.Contains(string(dockerfile), seed) {
			t.Errorf("the serve image lacks %q", seed)
		}
	}
	if strings.Contains(string(dockerfile), "graphjin-supervisor.sh") {
		t.Error("the Dockerfile still ships the shell GraphJin supervisor")
	}
}

func TestSubscriptionsGraphJinRunsInServe(t *testing.T) {
	root := repoRootForTest(t)
	read := func(path string) composeParityDocument {
		var raw []byte
		var err error
		if strings.HasPrefix(path, "compose/") {
			raw, err = ComposeFS.ReadFile(path)
		} else {
			raw, err = os.ReadFile(filepath.Join(root, path))
		}
		if err != nil {
			t.Fatal(err)
		}
		return loadComposeParityDocument(t, raw)
	}
	for _, path := range []string{"compose.yml", "compose.adventureworks.yml", "compose.dev.yml", "compose/core.yml", "compose/demo.yml"} {
		doc := read(path)
		if _, ok := doc.Services["neko-graphjin"]; ok {
			t.Errorf("%s still defines neko-graphjin; serve runs the subscriptions GraphJin", path)
		}
		for name, service := range doc.Services {
			if _, ok := service.DependsOn["neko-graphjin"]; ok {
				t.Errorf("%s: %s waits for the removed neko-graphjin service", path, name)
			}
		}
	}
	for _, path := range []string{"compose.yml", "compose/core.yml"} {
		doc := read(path)
		if got := doc.Services["openneko-serve"].Environment["OPENNEKO_SUBSCRIPTIONS_ADDR"]; got != "0.0.0.0:8089" {
			t.Errorf("%s serve subscriptions address = %#v", path, got)
		}
		for _, name := range []string{"web", "worker"} {
			if got := doc.Services[name].Environment["OPENNEKO_GRAPHJIN_URL"]; got != "http://openneko-serve:8089" {
				t.Errorf("%s: %s subscribes at %#v, want http://openneko-serve:8089", path, name, got)
			}
		}
	}
	for _, gone := range []string{"scripts/neko-graphjin-entrypoint.sh", "db/graphjin/neko.yml", "apps/openneko/cmd/graphjin-config"} {
		if _, err := os.Stat(filepath.Join(root, gone)); err == nil {
			t.Errorf("%s is still in the repo", gone)
		}
	}
	dockerfile, err := os.ReadFile(filepath.Join(root, "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(dockerfile), "AS neko-graphjin") {
		t.Error("the Dockerfile still builds the neko-graphjin image")
	}
}

func composeMountSourceAt(mounts []string, target string) string {
	for _, mount := range mounts {
		parts := strings.Split(mount, ":")
		if len(parts) >= 2 && parts[1] == target {
			return parts[0]
		}
	}
	return ""
}

func TestPostReleaseSmokeGatesRetainedDemoUpgrades(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRootForTest(t), ".github", "workflows", "post-release-smoke.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var parsedWorkflow any
	if err := yaml.Unmarshal(raw, &parsedWorkflow); err != nil {
		t.Fatalf("post-release smoke is not valid YAML: %v", err)
	}
	workflow := string(raw)
	for _, required := range []string{
		"openneko start --mode demo --detach --pull never",
		"openneko upgrade --stack-only --mode demo",
		"test ! -e /config/.openneko-graphjin-supervisor.sh",
		"com.docker.compose.service=openneko-serve",
		"{{.State.Status}}",
		"authenticated_datasource_query=ok",
	} {
		if !strings.Contains(workflow, required) {
			t.Fatalf("post-release smoke no longer gates retained demo upgrades: missing %q", required)
		}
	}
}

func TestProductionDeployRequiresAuthenticatedDatasourceQuery(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRootForTest(t), ".github", "workflows", "deploy.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var parsedWorkflow any
	if err := yaml.Unmarshal(raw, &parsedWorkflow); err != nil {
		t.Fatalf("production deploy is not valid YAML: %v", err)
	}
	workflow := string(raw)
	for _, required := range []string{
		"openneko upgrade --mode demo",
		"OPENNEKO_PORT=80 openneko status",
		"callGraphjinMcpTool",
		"authenticated_datasource_query=ok",
	} {
		if !strings.Contains(workflow, required) {
			t.Fatalf("production deploy no longer verifies the datasource upgrade: missing %q", required)
		}
	}
	if strings.Contains(workflow, "OPENNEKO_PORT=80 openneko status || true") {
		t.Fatal("production deploy ignores an unhealthy upgraded stack")
	}
}

func TestVendoredAgentBinaryCannotBeOverridden(t *testing.T) {
	rootRaw, err := os.ReadFile("../../../compose.yml")
	if err != nil {
		t.Fatal(err)
	}
	packagedRaw, err := ComposeFS.ReadFile("compose/core.yml")
	if err != nil {
		t.Fatal(err)
	}
	for label, document := range map[string]composeParityDocument{
		"source":   loadComposeParityDocument(t, rootRaw),
		"packaged": loadComposeParityDocument(t, packagedRaw),
	} {
		for _, serviceName := range []string{"web", "worker"} {
			if _, exists := document.Services[serviceName].Environment["OPENNEKO_AGENT_MODEL_BINARY"]; exists {
				t.Fatalf("%s %s exposes an override for the vendored agent executable", label, serviceName)
			}
		}
	}
}

func TestStorageReconciliationGatesEveryDatabaseConsumer(t *testing.T) {
	rootRaw, err := os.ReadFile("../../../compose.yml")
	if err != nil {
		t.Fatal(err)
	}
	packagedRaw, err := ComposeFS.ReadFile("compose/core.yml")
	if err != nil {
		t.Fatal(err)
	}
	for label, document := range map[string]composeParityDocument{
		"source":   loadComposeParityDocument(t, rootRaw),
		"packaged": loadComposeParityDocument(t, packagedRaw),
	} {
		reconcile, ok := document.Services["storage-reconcile"]
		if !ok {
			t.Fatalf("%s compose is missing storage-reconcile", label)
		}
		if want := []string{"storage", "reconcile"}; !reflect.DeepEqual(reconcile.Command, want) {
			t.Fatalf("%s storage-reconcile command = %v, want %v", label, reconcile.Command, want)
		}
		if reconcile.DependsOn["neko-db"].Condition != "service_healthy" {
			t.Fatalf("%s storage-reconcile must wait for healthy neko-db", label)
		}
		for _, key := range []string{"NEKO_PG_HOST", "NEKO_PG_DATABASE"} {
			if _, ok := reconcile.Environment[key]; !ok {
				t.Fatalf("%s storage-reconcile is missing %s", label, key)
			}
		}
		for _, consumer := range []string{"neko-migrate"} {
			if document.Services[consumer].DependsOn["storage-reconcile"].Condition != "service_completed_successfully" {
				t.Fatalf("%s %s can start before storage reconciliation", label, consumer)
			}
		}
	}
}

func TestDatabaseImagesEnforcePinnedGlibcStorageABI(t *testing.T) {
	root := repoRootForTest(t)
	dockerfile, err := os.ReadFile(root + "/Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	raw := string(dockerfile)
	for _, required := range []string{
		"FROM pgvector/pgvector:0.8.6-pg16-bookworm@sha256:ccc6e83d6e35e931dc7c5def2022729d5a6c370318d099181995567ff1fb4d6b AS neko-db",
		`LABEL org.openneko.storage-contract="1"`,
		`test "$(getconf GNU_LIBC_VERSION)" = "glibc 2.36"`,
	} {
		if !strings.Contains(raw, required) {
			t.Fatalf("Dockerfile does not enforce storage ABI line %q", required)
		}
	}
	if strings.Contains(raw, "postgres:16-alpine") {
		t.Fatal("stateful database runtime regressed to Alpine/musl")
	}
	// neko-db is a plain pgvector Postgres: logical `openneko backup` dumps
	// replaced the pgBackRest archiver and its custom entrypoint.
	for _, retired := range []string{"pgbackrest", "pgBackRest", "AS records-db", "AS neko-backup", "AS records-graphjin", "packages/records"} {
		if strings.Contains(raw, retired) {
			t.Fatalf("Dockerfile still references retired %q", retired)
		}
	}
}

type composeParityDependency struct {
	Condition string `yaml:"condition"`
}

type composeParityInventory struct {
	Restart        string
	Profiles       []string
	Dependencies   []string
	Environment    []string
	Mounts         []string
	Command        []string
	HasHealthcheck bool
}

func loadComposeParityDocument(t *testing.T, raw []byte) composeParityDocument {
	t.Helper()
	var document composeParityDocument
	if err := yaml.Unmarshal(raw, &document); err != nil {
		t.Fatalf("parse compose YAML: %v", err)
	}
	return document
}

func sortedKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func normalizeComposeMount(mount string) string {
	separator := strings.Index(mount, ":")
	if strings.HasPrefix(mount, "${") {
		if end := strings.Index(mount, "}"); end >= 0 {
			if offset := strings.Index(mount[end+1:], ":"); offset >= 0 {
				separator = end + 1 + offset
			}
		}
	}
	if separator < 0 {
		return mount
	}
	source := mount[:separator]
	if strings.HasPrefix(source, "${") && strings.HasSuffix(source, "}") {
		variable := strings.TrimSuffix(strings.TrimPrefix(source, "${"), "}")
		name, modifier, _ := strings.Cut(variable, ":")
		if name == "OPENNEKO_CONFIG_VOLUME" && strings.HasPrefix(modifier, "-") {
			source = strings.TrimPrefix(modifier, "-")
		} else {
			source = "$" + name
		}
	}
	return source + mount[separator:]
}

func composeParityInventoryFor(service composeParityService) composeParityInventory {
	dependencies := make([]string, 0, len(service.DependsOn))
	for name, dependency := range service.DependsOn {
		dependencies = append(dependencies, fmt.Sprintf("%s:%s", name, dependency.Condition))
	}
	sort.Strings(dependencies)
	mounts := make([]string, len(service.Volumes))
	for index, mount := range service.Volumes {
		mounts[index] = normalizeComposeMount(mount)
	}
	sort.Strings(mounts)
	profiles := append([]string(nil), service.Profiles...)
	sort.Strings(profiles)
	return composeParityInventory{
		Restart:        service.Restart,
		Profiles:       profiles,
		Dependencies:   dependencies,
		Environment:    sortedKeys(service.Environment),
		Mounts:         mounts,
		Command:        service.Command,
		HasHealthcheck: len(service.Healthcheck) > 0,
	}
}

func composeHealthcheckTest(t *testing.T, label, serviceName string, service composeParityService) []string {
	t.Helper()
	raw, ok := service.Healthcheck["test"].([]any)
	if !ok {
		t.Fatalf("%s %s healthcheck test is not a command: %#v", label, serviceName, service.Healthcheck["test"])
	}
	command := make([]string, len(raw))
	for index, value := range raw {
		argument, ok := value.(string)
		if !ok {
			t.Fatalf("%s %s healthcheck argument %d is not a string: %#v", label, serviceName, index, value)
		}
		command[index] = argument
	}
	return command
}

func TestSourceAndPackagedComposeStateInventoryMatch(t *testing.T) {
	rootRaw, err := os.ReadFile("../../../compose.yml")
	if err != nil {
		t.Fatal(err)
	}
	packagedRaw, err := ComposeFS.ReadFile("compose/core.yml")
	if err != nil {
		t.Fatal(err)
	}
	root := loadComposeParityDocument(t, rootRaw)
	packaged := loadComposeParityDocument(t, packagedRaw)

	if got, want := sortedKeys(root.Volumes), sortedKeys(packaged.Volumes); !reflect.DeepEqual(got, want) {
		t.Fatalf("named-volume inventory drifted:\nsource:   %v\npackaged: %v", got, want)
	}
	if got, want := sortedKeys(root.Services), sortedKeys(packaged.Services); !reflect.DeepEqual(got, want) {
		t.Fatalf("service inventory drifted:\nsource:   %v\npackaged: %v", got, want)
	}
	for _, name := range sortedKeys(root.Services) {
		got := composeParityInventoryFor(root.Services[name])
		want := composeParityInventoryFor(packaged.Services[name])
		if !reflect.DeepEqual(got, want) {
			t.Errorf("service %s state/recovery inventory drifted:\nsource:   %#v\npackaged: %#v", name, got, want)
		}
	}
}

func TestPackagedDemoBootstrapsJWTBeforeServingWeb(t *testing.T) {
	raw, err := ComposeFS.ReadFile("compose/demo.yml")
	if err != nil {
		t.Fatal(err)
	}
	demo := loadComposeParityDocument(t, raw)

	seed := demo.Services["neko-adventureworks-seed"]
	if got := seed.Environment["ADVENTUREWORKS_AUTH_MODE"]; got != "jwt" {
		t.Fatalf("demo seed auth mode = %#v, want jwt", got)
	}
	if got := seed.Environment["ADVENTUREWORKS_RECONCILE_AUTH_MODE"]; got != "1" {
		t.Fatalf("demo auth reconciliation = %#v, want 1", got)
	}

	worker := demo.Services["worker"]
	if got := worker.DependsOn["neko-adventureworks-seed"].Condition; got != "service_completed_successfully" {
		t.Fatalf("worker demo-seed dependency = %q, want service_completed_successfully", got)
	}
	web := demo.Services["web"]
	if got := web.DependsOn["worker"].Condition; got != "service_healthy" {
		t.Fatalf("web worker dependency = %q, want service_healthy", got)
	}
	for _, serviceName := range []string{"web", "worker"} {
		if got := demo.Services[serviceName].Environment["OPENNEKO_STACK_MODE"]; got != "demo" {
			t.Fatalf("%s stack mode = %#v, want demo", serviceName, got)
		}
	}

	coreRaw, err := ComposeFS.ReadFile("compose/core.yml")
	if err != nil {
		t.Fatal(err)
	}
	prod := loadComposeParityDocument(t, coreRaw)
	for _, serviceName := range []string{"web", "worker"} {
		if got, exists := prod.Services[serviceName].Environment["OPENNEKO_STACK_MODE"]; exists {
			t.Fatalf("production %s unexpectedly sets OPENNEKO_STACK_MODE=%#v", serviceName, got)
		}
	}
}

func TestOpenShellIsGone(t *testing.T) {
	root := repoRootForTest(t)
	for _, name := range []string{
		"Dockerfile",
		"compose.yml",
		"compose.dev.yml",
		"apps/openneko/assets/compose/core.yml",
		"apps/openneko/assets/compose/demo.yml",
		"apps/openneko/assets/compose/dev.yml",
		".github/workflows/release-binaries.yml",
	} {
		raw, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(raw), "\n") {
			code := strings.TrimSpace(line)
			if strings.HasPrefix(code, "#") {
				continue
			}
			if strings.Contains(strings.ToLower(code), "openshell") || strings.Contains(code, "neko-reaper") {
				t.Fatalf("%s still runs part of OpenShell: %s", name, code)
			}
		}
	}
	for _, name := range []string{"compose.openshell.yml", "compose.openshell-host.yml", "apps/openneko/assets/compose/openshell.yml"} {
		if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Fatalf("%s must not exist: %v", name, err)
		}
	}
}
