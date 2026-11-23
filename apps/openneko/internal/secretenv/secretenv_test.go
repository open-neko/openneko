package secretenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGetReadsTheVariableThenItsFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "k")
	if err := os.WriteFile(file, []byte(" from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SECRETENV_T_A", "")
	t.Setenv("SECRETENV_T_A_FILE", file)
	if got := Get("SECRETENV_T_A"); got != "from-file" {
		t.Errorf("from the file: %q", got)
	}
	t.Setenv("SECRETENV_T_A", "from-env")
	if got := Get("SECRETENV_T_A"); got != "from-env" {
		t.Errorf("the variable wins over the file: %q", got)
	}
	t.Setenv("SECRETENV_T_B_FILE", filepath.Join(dir, "absent"))
	if got := Get("SECRETENV_T_B"); got != "" {
		t.Errorf("a missing file reads as unset: %q", got)
	}
}

func TestReadHandedNeedsTheFlag(t *testing.T) {
	t.Setenv(StdinEnv, "")
	if err := ReadHanded(strings.NewReader(`{"SECRETENV_T_C":"handed"}` + "\n")); err != nil || Get("SECRETENV_T_C") != "" {
		t.Fatalf("read stdin with no flag: %v", err)
	}
	t.Setenv(StdinEnv, "1")
	t.Setenv("SECRETENV_T_C", "from-env")
	if err := ReadHanded(strings.NewReader(`{"SECRETENV_T_C":"handed"}` + "\n")); err != nil || Get("SECRETENV_T_C") != "handed" {
		t.Fatalf("handed value: %q %v", Get("SECRETENV_T_C"), err)
	}
	if err := ReadHanded(strings.NewReader("")); err == nil {
		t.Error("an empty stdin with the flag set must fail")
	}
}

func TestSecretURLAndWebhookNames(t *testing.T) {
	for name, want := range map[string]bool{"OPENNEKO_AUDIT_FAILURE_WEBHOOK_URL": true, "OPENNEKO_PUBLIC_URL": false} {
		if SecretName(name) != want {
			t.Errorf("SecretName(%s) = %v", name, !want)
		}
	}
	for value, want := range map[string]bool{
		"postgres://u:pw@db/x":                         true,
		"https://user@host.example/path":               true,
		"https://host.example/hook?token=abc":          true,
		"https://host.example/v1?api-key=abc":          true,
		"https://bucket.example/o?X-Amz-Signature=abc": true,
		"https://host.example/x?Password=abc":          true,
		"https://host.example:8443/v1":                 false,
		"unix:///run/openneko-sandbox/sandbox.sock":    false,
		"https://host.example/search?q=report":         false,
		"not a url":                                    false,
		"127.0.0.1:8090":                               false,
		"":                                             false,
	} {
		if SecretURL(value) != want {
			t.Errorf("SecretURL(%q) = %v, want %v", value, !want, want)
		}
	}
	clean, secrets := Split([]string{"PLAIN_URL=https://host.example/a?sig=1", "OPENNEKO_PUBLIC_URL=https://openneko.example"})
	if len(clean) != 1 || secrets["PLAIN_URL"] == "" {
		t.Errorf("Split: clean %v, secrets %v", clean, secrets)
	}
}

func TestReadHandedSetsTheURLs(t *testing.T) {
	t.Setenv(StdinEnv, "1")
	t.Setenv("SECRETENV_T_URL", "")
	t.Setenv("SECRETENV_T_KEY", "")
	if err := ReadHanded(strings.NewReader(`{"SECRETENV_T_URL":"https://u:p@host.example","SECRETENV_T_KEY":"k"}` + "\n")); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("SECRETENV_T_URL") != "https://u:p@host.example" {
		t.Error("a handed URL is not where its readers look")
	}
	if os.Getenv("SECRETENV_T_KEY") != "" || Get("SECRETENV_T_KEY") != "k" {
		t.Error("a handed key went into the process environment")
	}
}
