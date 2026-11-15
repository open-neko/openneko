package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/open-neko/neko/apps/openneko/internal/instance"
)

func TestGraphjinImportUpdate(t *testing.T) {
	update, err := graphjinImportUpdate([]byte("sources:\n  - name: customer\n    type: postgres\ntables:\n  - name: orders\n"))
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string][]map[string]any
	if err := json.Unmarshal(update, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed["update_sources"][0]["name"] != "customer" || parsed["tables"][0]["name"] != "orders" {
		t.Fatalf("unexpected import update: %s", update)
	}
	for _, input := range []string{
		"auth:\n  type: none\n",
		"secrets:\n  keystore: {}\n",
		"sources: {}\n",
		"sources: []\nsources: []\n",
	} {
		if _, err := graphjinImportUpdate([]byte(input)); err == nil {
			t.Errorf("accepted unsafe or invalid import %q", strings.TrimSpace(input))
		}
	}
}

func TestGraphjinImportRefusesUnselectedInstallation(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv(instance.EnvName, "")
	file := filepath.Join(t.TempDir(), "customer.yml")
	if err := os.WriteFile(file, []byte("sources: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := NewRoot()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"graphjin", "import", file})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "selected production installation") {
		t.Fatalf("expected production installation guard, got %v", err)
	}
}

func TestGraphjinImportCallsWebAdminSocket(t *testing.T) {
	dir, err := os.MkdirTemp("", "wa")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "web-admin.sock")
	stop, err := serveWebAdmin(socket, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/admin/graphjin/import" {
			t.Fatalf("%s %s", r.Method, r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		sources, _ := body["update_sources"].([]any)
		if len(sources) != 1 || sources[0].(map[string]any)["name"] != "customer" {
			t.Fatalf("unexpected body: %#v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	t.Setenv(webAdminSocketEnv, socket)
	t.Setenv("OPENNEKO_PROXIED", "1")
	var out bytes.Buffer
	cmd := NewRoot()
	cmd.SetIn(strings.NewReader("sources:\n  - name: customer\n    type: postgres\n"))
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"graphjin", "import", "-"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != "GraphJin configuration imported" {
		t.Fatalf("unexpected output: %q", out.String())
	}
}
