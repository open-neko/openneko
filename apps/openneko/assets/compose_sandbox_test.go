package assets

import (
	"path"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type sandboxComposeService struct {
	Command     []string          `yaml:"command"`
	Environment map[string]string `yaml:"environment"`
	Volumes     []string          `yaml:"volumes"`
	GroupAdd    []string          `yaml:"group_add"`
}

func readSandboxComposeSet(t *testing.T) map[string]sandboxComposeService {
	t.Helper()
	merged := map[string]sandboxComposeService{}
	for _, file := range []string{"compose.yml", "compose.db.yml", "recipes/sandbox/compose.yml"} {
		var doc struct {
			Services map[string]sandboxComposeService `yaml:"services"`
		}
		if err := yaml.Unmarshal(readRootCompose(t, file), &doc); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		for name, svc := range doc.Services {
			got := merged[name]
			if svc.Command != nil {
				got.Command = svc.Command
			}
			if got.Environment == nil {
				got.Environment = map[string]string{}
			}
			for k, v := range svc.Environment {
				got.Environment[k] = v
			}
			got.Volumes = append(got.Volumes, svc.Volumes...)
			got.GroupAdd = append(got.GroupAdd, svc.GroupAdd...)
			merged[name] = got
		}
	}
	return merged
}

func namedVolume(mount string) (string, string, bool) {
	source, rest, ok := strings.Cut(mount, ":")
	if !ok || strings.HasPrefix(source, "/") || strings.HasPrefix(source, ".") || strings.HasPrefix(source, "$") {
		return "", "", false
	}
	target, _, _ := strings.Cut(rest, ":")
	return source, target, true
}

func TestSandboxOverlaySharesOnlyItsSocketVolume(t *testing.T) {
	services := readSandboxComposeSet(t)
	box, ok := services["openneko-sandbox"]
	if !ok {
		t.Fatal("the overlay defines no openneko-sandbox service")
	}
	listen := ""
	for i, arg := range box.Command {
		if arg == "--listen" && i+1 < len(box.Command) {
			listen = strings.TrimPrefix(box.Command[i+1], "unix://")
		}
	}
	if listen == "" {
		t.Fatalf("the sandbox listens on no Unix socket: %v", box.Command)
	}
	socketDir := path.Dir(listen)
	socketVolume := ""
	for _, mount := range box.Volumes {
		source, target, named := namedVolume(mount)
		if !named {
			t.Errorf("the sandbox mounts a host path: %s", mount)
			continue
		}
		if target == socketDir {
			socketVolume = source
			continue
		}
		for name, svc := range services {
			if name == "openneko-sandbox" {
				continue
			}
			for _, other := range svc.Volumes {
				if s, _, _ := namedVolume(other); s == source {
					t.Errorf("the sandbox shares volume %s with %s", source, name)
				}
			}
		}
	}
	if socketVolume == "" {
		t.Fatalf("no sandbox volume holds the socket folder %s", socketDir)
	}
	gid := box.Environment["OPENNEKO_SANDBOX_GID"]
	for _, name := range []string{"web", "openneko-serve"} {
		svc := services[name]
		if url := svc.Environment["OPENNEKO_SANDBOX_URL"]; url != "unix://"+listen {
			t.Errorf("%s: OPENNEKO_SANDBOX_URL = %q, want unix://%s", name, url, listen)
		}
		if !slices.Contains(svc.Volumes, socketVolume+":"+socketDir) {
			t.Errorf("%s does not mount %s at %s", name, socketVolume, socketDir)
		}
	}
	if !slices.Contains(services["web"].GroupAdd, gid) {
		t.Errorf("web is not in the socket's group %s (OPENNEKO_SANDBOX_GID); the web image's own group is 999", gid)
	}
	for name, svc := range services {
		for key, value := range svc.Environment {
			if key == "OPENNEKO_SANDBOX_URL" {
				continue
			}
			if strings.HasPrefix(strings.TrimPrefix(value, "unix://"), socketDir+"/") {
				t.Errorf("%s puts %s in the sandbox socket folder: %s", name, key, value)
			}
		}
		for _, mount := range svc.Volumes {
			if _, target, named := namedVolume(mount); named && target == socketDir && !strings.HasPrefix(mount, socketVolume+":") {
				t.Errorf("%s mounts another volume at the sandbox socket folder: %s", name, mount)
			}
		}
	}
}
