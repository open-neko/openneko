package assets

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

var databaseURLConsumers = []string{"storage-reconcile", "neko-migrate", "openneko-serve-init", "web", "worker"}

func composeNode(t *testing.T, raw []byte) *yaml.Node {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Content[0]
}

func mappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

func TestProdComposeHasNoPostgres(t *testing.T) {
	root := repoRootForTest(t)
	sourceRaw, err := os.ReadFile(filepath.Join(root, "compose.yml"))
	if err != nil {
		t.Fatal(err)
	}
	for label, raw := range map[string][]byte{
		"source":   sourceRaw,
		"packaged": readEmbeddedCompose(t, "core.yml"),
	} {
		document := loadComposeParityDocument(t, raw)
		for name, service := range document.Services {
			image := strings.ToLower(service.Image)
			if name == "neko-db" || strings.Contains(image, "neko-db") || strings.Contains(image, "postgres") || strings.Contains(image, "pgvector") {
				t.Fatalf("%s prod compose runs a database: %s (%s)", label, name, service.Image)
			}
			if _, ok := service.DependsOn["neko-db"]; ok {
				t.Fatalf("%s %s waits for neko-db", label, name)
			}
			for key := range service.Environment {
				if strings.HasPrefix(key, "NEKO_PG_") {
					t.Fatalf("%s %s sets %s; prod reads the database URL only", label, name, key)
				}
			}
		}
		for volume := range document.Volumes {
			if strings.HasPrefix(volume, "neko-db") {
				t.Fatalf("%s prod compose keeps the database volume %s", label, volume)
			}
		}
		if strings.Contains(string(raw), "target: neko-db") {
			t.Fatalf("%s prod compose builds the neko-db image", label)
		}

		node := composeNode(t, raw)
		secret := mappingValue(mappingValue(mappingValue(node, "secrets"), "database-url"), "environment")
		if secret == nil || secret.Value != "OPENNEKO_DATABASE_URL" {
			t.Fatalf("%s: the database-url secret must come from OPENNEKO_DATABASE_URL", label)
		}
		services := mappingValue(node, "services")
		for _, name := range databaseURLConsumers {
			if document.Services[name].Environment["OPENNEKO_DATABASE_URL_FILE"] != "/run/secrets/database-url" {
				t.Fatalf("%s %s does not read /run/secrets/database-url", label, name)
			}
			if mappingValue(mappingValue(services, name), "secrets") == nil {
				t.Fatalf("%s %s does not mount the database-url secret", label, name)
			}
		}
		if mappingValue(mappingValue(services, "openneko-serve"), "secrets") != nil {
			t.Fatalf("%s openneko-serve mounts the main database URL", label)
		}
	}
}

func TestBundledDatabaseOverlaysAgree(t *testing.T) {
	sourceRaw, err := os.ReadFile(filepath.Join(repoRootForTest(t), "compose.db.yml"))
	if err != nil {
		t.Fatal(err)
	}
	packagedRaw := readEmbeddedCompose(t, "bundled-db.yml")
	source := loadComposeParityDocument(t, sourceRaw)
	packaged := loadComposeParityDocument(t, packagedRaw)

	for label, raw := range map[string][]byte{"source": sourceRaw, "packaged": packagedRaw} {
		services := mappingValue(composeNode(t, raw), "services")
		for _, name := range databaseURLConsumers {
			service := mappingValue(services, name)
			if secrets := mappingValue(service, "secrets"); secrets == nil || secrets.Tag != "!reset" {
				t.Fatalf("%s %s must reset the database-url secret", label, name)
			}
			if file := mappingValue(mappingValue(service, "environment"), "OPENNEKO_DATABASE_URL_FILE"); file == nil || file.Tag != "!reset" {
				t.Fatalf("%s %s must reset OPENNEKO_DATABASE_URL_FILE", label, name)
			}
		}
	}

	var names []string
	for name := range packaged.Services {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		want := packaged.Services[name]
		got, ok := source.Services[name]
		if !ok {
			t.Fatalf("compose.db.yml has no %s", name)
		}
		if name == "neko-db" {
			if !slices.Equal(got.Command, want.Command) || got.Environment["PGDATA"] != want.Environment["PGDATA"] || !slices.Equal(got.Volumes, want.Volumes) {
				t.Fatalf("neko-db differs between compose.db.yml and bundled-db.yml")
			}
			continue
		}
		for key, value := range want.Environment {
			if got.Environment[key] != value {
				t.Fatalf("%s %s = %v in compose.db.yml, %v in bundled-db.yml", name, key, got.Environment[key], value)
			}
		}
		for dep, condition := range want.DependsOn {
			if got.DependsOn[dep] != condition {
				t.Fatalf("%s waits for %s differently in compose.db.yml", name, dep)
			}
		}
	}
	if len(source.Services) != len(packaged.Services) {
		t.Fatalf("compose.db.yml has %d services, bundled-db.yml has %d", len(source.Services), len(packaged.Services))
	}
}
