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

func TestSandboxNetworkHasNoRouteOut(t *testing.T) {
	type service struct {
		Networks    yaml.Node `yaml:"networks"`
		NetworkMode string    `yaml:"network_mode"`
	}
	type network struct {
		Internal bool   `yaml:"internal"`
		Driver   string `yaml:"driver"`
	}
	for _, set := range [][]string{
		{"compose.yml", "compose.db.yml", "recipes/sandbox/compose.yml"},
		{"recipes/sandbox/compose.native.yml"},
	} {
		joins := map[string][]string{}
		networks := map[string]network{}
		modes := map[string]string{}
		for _, file := range set {
			var doc struct {
				Services map[string]service `yaml:"services"`
				Networks map[string]network `yaml:"networks"`
			}
			if err := yaml.Unmarshal(readRootCompose(t, file), &doc); err != nil {
				t.Fatalf("%s: %v", file, err)
			}
			for name, svc := range doc.Services {
				if svc.NetworkMode != "" {
					modes[name] = svc.NetworkMode
				}
				switch svc.Networks.Kind {
				case yaml.SequenceNode:
					for _, n := range svc.Networks.Content {
						joins[name] = append(joins[name], n.Value)
					}
				case yaml.MappingNode:
					for i := 0; i < len(svc.Networks.Content); i += 2 {
						joins[name] = append(joins[name], svc.Networks.Content[i].Value)
					}
				}
			}
			for name, n := range doc.Networks {
				networks[name] = n
			}
		}
		if mode, ok := modes["openneko-sandbox"]; ok {
			t.Errorf("%v: the sandbox sets network_mode %q", set, mode)
		}
		own := joins["openneko-sandbox"]
		if len(own) == 0 {
			t.Errorf("%v: the sandbox names no network, so it joins the default network with a route out", set)
		}
		for _, name := range own {
			if n := networks[name]; !n.Internal || (n.Driver != "" && n.Driver != "bridge") {
				t.Errorf("%v: the sandbox joins network %s, which is not internal: %+v", set, name, n)
			}
			for other, list := range joins {
				if other != "openneko-sandbox" && slices.Contains(list, name) {
					t.Errorf("%v: %s joins the sandbox network %s", set, other, name)
				}
			}
		}
	}
}
