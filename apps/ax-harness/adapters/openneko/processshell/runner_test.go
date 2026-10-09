package processshell

import (
	"context"
	"fmt"
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

func TestInventoryUsesCurrentFlagAndFallsBackOnlyForLegacyCLI(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "current", true: "legacy"}[legacy], func(t *testing.T) {
			opts, marker := fixture(t)
			cli := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + marker + "'\n"
			if legacy {
				cli += "case \" $* \" in *' --page-size '*) echo \"error: unexpected argument '--page-size'\" >&2; exit 2;; esac\n"
			}
			if legacy {
				cli += "printf '[]'\n"
			} else {
				cli += "printf '{\"sandboxes\":[],\"next_page_token\":\"\"}'\n"
			}
			if err := os.WriteFile(opts.CLI, []byte(cli), 0700); err != nil {
				t.Fatal(err)
			}
			runner, err := New(opts)
			if err != nil {
				t.Fatal(err)
			}
			defer runner.Close()
			if err := runner.reap(context.Background()); err != nil {
				t.Fatal(err)
			}
			calls, err := os.ReadFile(marker)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(calls), "--page-size 500") {
				t.Fatalf("current inventory flag missing: %s", calls)
			}
			if strings.Contains(string(calls), "--limit 500") != legacy {
				t.Fatalf("unexpected legacy inventory fallback: %s", calls)
			}
		})
	}
}

func TestRejectsIncompleteCurrentInventory(t *testing.T) {
	opts, _ := fixture(t)
	if err := os.WriteFile(opts.CLI, []byte("#!/bin/sh\nprintf '{\"sandboxes\":[],\"next_page_token\":\"more\"}'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	runner, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer runner.Close()
	if err := runner.reap(context.Background()); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("accepted incomplete inventory: %v", err)
	}
}

func TestCurrentInventoryReapsOnlyOwnedSandbox(t *testing.T) {
	for _, owned := range []bool{false, true} {
		t.Run(map[bool]string{false: "foreign", true: "owned"}[owned], func(t *testing.T) {
			opts, marker := fixture(t)
			runner, err := New(opts)
			if err != nil {
				t.Fatal(err)
			}
			defer runner.Close()
			runLabel := "another-run"
			if owned {
				runLabel = opts.RunID
			}
			cli := fmt.Sprintf(`#!/bin/sh
case " $* " in
  *" sandbox list "*) printf '{"sandboxes":[{"name":"%s","labels":{"openneko.process_run":"%s","openneko.process_operation":"1"}}],"next_page_token":""}' ;;
  *" sandbox delete "*) printf deleted > '%s' ;;
esac
`, runner.name, runLabel, marker)
			if err := os.WriteFile(opts.CLI, []byte(cli), 0700); err != nil {
				t.Fatal(err)
			}
			err = runner.reap(context.Background())
			if owned && err != nil {
				t.Fatal(err)
			}
			if !owned && (err == nil || !strings.Contains(err.Error(), "ownership mismatch")) {
				t.Fatalf("foreign sandbox reaped or accepted: %v", err)
			}
			_, markerErr := os.Stat(marker)
			if owned && markerErr != nil || !owned && !os.IsNotExist(markerErr) {
				t.Fatalf("unexpected sandbox deletion state: %v", markerErr)
			}
		})
	}
}

func TestTeardownFailureDoesNotPublishDownloadedOutput(t *testing.T) {
	opts, _ := fixture(t)
	cli := `#!/bin/sh
case " $* " in
  *" sandbox list "*) printf '[]' ;;
  *" sandbox exec "*)
    case " $* " in *"stat -c%s"*) printf '16' ;; esac ;;
  *" sandbox download "*)
    for destination do :; done
    printf 'lead_id\nLEAD-42\n' > "$destination" ;;
  *" sandbox delete "*) exit 42 ;;
esac
`
	if err := os.WriteFile(opts.CLI, []byte(cli), 0700); err != nil {
		t.Fatal(err)
	}
	runner, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(context.Background(), Request{Argv: []string{"python3", "script.py"}, Outputs: []string{"result.csv"}})
	if err == nil || !strings.Contains(err.Error(), "teardown") {
		t.Fatalf("expected mandatory teardown failure, got %v", err)
	}
	if _, err := os.Stat(opts.OutputRoot); !os.IsNotExist(err) {
		t.Fatalf("failed teardown exposed output: %v", err)
	}
	if staged, err := filepath.Glob(filepath.Join(filepath.Dir(opts.OutputRoot), ".harness-process-*")); err != nil || len(staged) != 0 {
		t.Fatalf("failed teardown retained publication staging: %v, %v", staged, err)
	}
}
