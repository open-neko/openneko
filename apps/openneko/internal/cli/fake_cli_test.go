package cli

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// With OPENNEKO_TEST_FAKE_CLI set, a copy of the test binary stands in for a
// new openneko release on every system: it logs its arguments to that file
// and answers as version 9.9.9.
func TestMain(m *testing.M) {
	calls := os.Getenv("OPENNEKO_TEST_FAKE_CLI")
	if calls == "" {
		os.Exit(m.Run())
	}
	args := strings.Join(os.Args[1:], " ")
	if f, err := os.OpenFile(calls, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		fmt.Fprintln(f, args)
		_ = f.Close()
	}
	if os.Getenv("OPENNEKO_TEST_FAKE_CLI_FAIL") == "release-check" && strings.HasPrefix(args, "release-check") {
		fmt.Fprintln(os.Stderr, "no built-in packs")
		os.Exit(1)
	}
	fmt.Println("9.9.9")
	os.Exit(0)
}

func writeFakeCLI(t *testing.T, path string) []byte {
	t.Helper()
	self, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, self, 0o755); err != nil {
		t.Fatal(err)
	}
	return self
}
