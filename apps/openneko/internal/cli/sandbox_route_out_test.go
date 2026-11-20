package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSandboxRoutesOutReadsTheMergedNetworks(t *testing.T) {
	dir := t.TempDir()
	write := func(name, text string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	core, err := filepath.Abs(filepath.Join("..", "..", "assets", "compose", "core.yml"))
	if err != nil {
		t.Fatal(err)
	}
	recipe, err := filepath.Abs(filepath.Join("..", "..", "..", "..", "recipes", "sandbox", "compose.yml"))
	if err != nil {
		t.Fatal(err)
	}
	native, err := filepath.Abs(filepath.Join("..", "..", "..", "..", "recipes", "sandbox", "compose.native.yml"))
	if err != nil {
		t.Fatal(err)
	}
	desktop, err := filepath.Abs(filepath.Join("..", "..", "..", "..", "recipes", "sandbox", "compose.desktop.yml"))
	if err != nil {
		t.Fatal(err)
	}
	for name, files := range map[string][]string{
		"core only":           {core},
		"desktop recipe":      {desktop},
		"recipe":              {core, recipe},
		"native recipe":       {native},
		"no network":          {core, write("none.yml", "services:\n  openneko-sandbox:\n    network_mode: none\n")},
		"recipe, empty entry": {core, recipe, write("same.yml", "networks:\n  openneko-sandbox: {}\n")},
	} {
		if got, err := sandboxRoutesOut(files); err != nil || len(got) != 0 {
			t.Errorf("%s: %v %v", name, got, err)
		}
	}
	for name, c := range map[string]struct {
		files []string
		want  []string
	}{
		"pre-6.0 override": {[]string{core, write("old.yml", "services:\n  openneko-sandbox:\n    image: openneko-sandbox:5\n")},
			[]string{"network default is not internal"}},
		"internal turned off": {[]string{core, recipe, write("open.yml", "networks:\n  openneko-sandbox:\n    internal: false\n")},
			[]string{"network openneko-sandbox is not internal"}},
		"second network": {[]string{core, recipe, write("extra.yml", "services:\n  openneko-sandbox:\n    networks:\n      outside: {}\nnetworks:\n  outside: {}\n")},
			[]string{"network outside is not internal"}},
		"host network": {[]string{core, recipe, write("host.yml", "services:\n  openneko-sandbox:\n    network_mode: host\n")},
			[]string{"network_mode: host"}},
		"external network": {[]string{core, write("ext.yml", "services:\n  openneko-sandbox:\n    networks: [lan]\nnetworks:\n  lan:\n    external: true\n")},
			[]string{"network lan is external, so OpenNeko cannot see whether it is internal"}},
	} {
		got, err := sandboxRoutesOut(c.files)
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %v %v, want %v", name, got, err, c.want)
		}
	}
	var out bytes.Buffer
	if err := warnSandboxRoutesOut(&out, []string{core, write("old2.yml", "services:\n  openneko-sandbox:\n    image: x\n")}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "WARNING: the openneko-sandbox service has a route out: network default is not internal.") ||
		!strings.Contains(out.String(), "terminal stays off") {
		t.Fatalf("warning:\n%s", out.String())
	}
	out.Reset()
	if err := warnSandboxRoutesOut(&out, []string{core, recipe}); err != nil || out.Len() != 0 {
		t.Fatalf("a warning for the recipe: %q %v", out.String(), err)
	}
}
