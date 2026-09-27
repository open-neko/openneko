package processshell

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixture(t *testing.T) (Options, string) {
	t.Helper()
	root := t.TempDir()
	input := filepath.Join(root, "input")
	if err := os.Mkdir(input, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(input, "script.py"), []byte("print('ok')\n"), 0600); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "cli-called")
	cli := filepath.Join(root, "openshell")
	if err := os.WriteFile(cli, []byte("#!/bin/sh\nprintf called > '"+marker+"'\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	return Options{CLI: cli, Gateway: "fixture", Image: "fixture:local", InputRoot: input,
		OutputRoot: filepath.Join(root, "output"), RunID: "fixture-run", OperationID: 1}, marker
}

func TestRejectsUnsafeInputAndOutputBeforeDispatch(t *testing.T) {
	opts, marker := fixture(t)
	if err := os.Symlink("script.py", filepath.Join(opts.InputRoot, "alias.py")); err != nil {
		t.Fatal(err)
	}
	if _, err := New(opts); err == nil || !strings.Contains(err.Error(), "input") {
		t.Fatalf("symlink input accepted: %v", err)
	}
	if err := os.Remove(filepath.Join(opts.InputRoot, "alias.py")); err != nil {
		t.Fatal(err)
	}
	for _, output := range []string{"../escape", "/absolute", "script.py", "result.csv"} {
		runner, err := New(opts)
		if err != nil {
			t.Fatal(err)
		}
		request := Request{Argv: []string{"python3", "script.py"}, Outputs: []string{output}}
		if output == "result.csv" {
			request.Argv = []string{"python3", "bad\x00argument"}
		}
		if _, err := runner.Run(context.Background(), request); err == nil {
			t.Fatalf("unsafe request accepted: %+v", request)
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatalf("unsafe request dispatched to CLI: %v", err)
		}
	}
}

func TestOutputRootMustBeFresh(t *testing.T) {
	opts, _ := fixture(t)
	if err := os.Mkdir(opts.OutputRoot, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := New(opts); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("existing output root accepted: %v", err)
	}
}

func TestOutputCaptureBounded(t *testing.T) {
	buffer := &limitedBuffer{limit: 4}
	_, _ = buffer.Write([]byte("ab"))
	_, _ = buffer.Write([]byte("cdef"))
	if buffer.String() != "abcd" || !buffer.truncated {
		t.Fatalf("unexpected bounded output: %q truncated=%v", buffer.String(), buffer.truncated)
	}
}
