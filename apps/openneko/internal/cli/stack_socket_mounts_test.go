package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestStackSocketMountsFindsASandboxInServesSocketFolder(t *testing.T) {
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
	if got, err := stackSocketMounts([]string{core, recipe}); err != nil || len(got) != 0 {
		t.Fatalf("the stack and the sandbox recipe: %v %v", got, err)
	}
	oldOverride := write("old.yml", `services:
  openneko-sandbox:
    image: openneko-sandbox:latest
    volumes:
      - openneko-run:/run/openneko
  web:
    volumes: [openneko-run:/run/openneko]
`)
	longForm := write("long.yml", `services:
  sidecar:
    volumes:
      - type: volume
        source: openneko-run
        target: /run/openneko
        read_only: true
`)
	got, err := stackSocketMounts([]string{core, oldOverride, longForm})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"openneko-sandbox", "sidecar"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("services = %v, want %v", got, want)
	}
	var out bytes.Buffer
	if err := warnStackSocketMounts(&out, []string{core, oldOverride}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "WARNING: these services mount openneko-run, the folder of serve's sockets: openneko-sandbox.") {
		t.Fatalf("warning:\n%s", out.String())
	}
	out.Reset()
	if err := warnStackSocketMounts(&out, []string{core, recipe}); err != nil || out.Len() != 0 {
		t.Fatalf("a warning with no such mount: %q %v", out.String(), err)
	}
}
