package sandbox

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
)

type seen struct {
	method, path, auth string
	raw                []byte
	body               execBody
}

type fakeSandbox struct {
	got     []seen
	answers []any
	status  []int
}

func (f *fakeSandbox) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	entry := seen{method: r.Method, path: r.URL.Path, auth: r.Header.Get("Authorization")}
	entry.raw, _ = io.ReadAll(r.Body)
	_ = json.Unmarshal(entry.raw, &entry.body)
	f.got = append(f.got, entry)
	if r.Method == http.MethodDelete {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if len(f.status) > 0 {
		status := f.status[0]
		f.status = f.status[1:]
		if status != http.StatusOK {
			http.Error(w, "refused", status)
			return
		}
	}
	answer := any(map[string]any{"exit_code": 0, "output": "hi\n"})
	if len(f.answers) > 0 {
		answer, f.answers = f.answers[0], f.answers[1:]
	}
	_ = json.NewEncoder(w).Encode(answer)
}

func putPaths(body execBody) []string {
	var out []string
	for _, file := range body.Put {
		out = append(out, file.Path)
	}
	sort.Strings(out)
	return out
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestClientSendsTheKeyAndTheRunOverHTTP(t *testing.T) {
	fake := &fakeSandbox{}
	server := httptest.NewServer(fake)
	defer server.Close()
	workspace := t.TempDir()
	client, err := NewClient(Config{URL: server.URL + "/", Key: "k1"}, workspace)
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"RUN_DIR": filepath.Join(workspace, "runs", "r1"), "PYTHONPATH": filepath.Join(workspace, "lib") + string(filepath.ListSeparator) + workspace}
	result, err := client.Exec(context.Background(), "echo hi", filepath.Join(workspace, "skills", "xlsx"), env, 30, 2000)
	if err != nil || result.Output != "hi\n" {
		t.Fatalf("exec = %+v, %v", result, err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	got := fake.got
	if len(got) != 2 || got[0].method != http.MethodPost || got[0].path != "/v2/runs/"+client.run+"/exec" || got[1].method != http.MethodDelete || got[1].path != "/v2/runs/"+client.run {
		t.Fatalf("requests = %+v", got)
	}
	for _, request := range got {
		if request.auth != "Bearer k1" {
			t.Fatalf("auth = %q", request.auth)
		}
	}
	body := got[0].body
	if !body.Fresh || body.Root != workspace || body.Command != "echo hi" || body.SkillDir != "skills/xlsx" || body.TimeoutSeconds != 30 ||
		body.Env["RUN_DIR"] != "runs/r1" || body.Env["PYTHONPATH"] != "lib:." {
		t.Fatalf("body = %+v", body)
	}
	if _, err := client.Exec(context.Background(), "true", "/etc", nil, 5, 1000); err == nil {
		t.Fatal("a skill folder outside the workspace was sent")
	}
	if _, err := client.Exec(context.Background(), "true", "", map[string]string{"RUN_DIR": "/etc"}, 5, 1000); err == nil {
		t.Fatal("a run path outside the workspace was sent")
	}
}

func TestClientSendsTheWorkingFolder(t *testing.T) {
	fake := &fakeSandbox{}
	server := httptest.NewServer(fake)
	defer server.Close()
	workspace := t.TempDir()
	client, err := NewClient(Config{URL: server.URL}, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.ExecIn(context.Background(), filepath.Join(workspace, "runs", "r1"), "pwd", "", nil, 5, 1000); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Exec(context.Background(), "pwd", "", nil, 5, 1000); err != nil {
		t.Fatal(err)
	}
	if got := fake.got; len(got) != 2 || got[0].body.Dir != "runs/r1" || got[1].body.Dir != "" || strings.Contains(string(got[1].raw), `"dir"`) {
		t.Fatalf("requests = %+v", got)
	}
	if _, err := client.ExecIn(context.Background(), "/etc", "pwd", "", nil, 5, 1000); err == nil || !strings.Contains(err.Error(), "working folder") {
		t.Fatalf("a working folder outside the workspace was sent: %v", err)
	}
}

func TestClientCarriesNoSecretsToTheSandbox(t *testing.T) {
	fake := &fakeSandbox{}
	server := httptest.NewServer(fake)
	defer server.Close()
	workspace := t.TempDir()
	outside := t.TempDir()
	write(t, filepath.Join(workspace, "runs", "r1", "input.csv"), "a,b\n")
	write(t, filepath.Join(workspace, "runs", "r1", ".openneko", "job.json"), `{"token":"planted-job-secret"}`)
	write(t, filepath.Join(outside, "key.txt"), "planted-outside-secret")
	if err := os.Symlink(filepath.Join(outside, "key.txt"), filepath.Join(workspace, "runs", "r1", "key.txt")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENNEKO_BROKER_TOKEN", "planted-env-secret")
	client, _ := NewClient(Config{URL: server.URL}, workspace)
	if _, err := client.Exec(context.Background(), "ls", "", nil, 5, 1000); err != nil {
		t.Fatal(err)
	}
	raw := string(fake.got[0].raw)
	for _, secret := range []string{"planted-job-secret", "planted-outside-secret", "planted-env-secret", ".openneko"} {
		if strings.Contains(raw, secret) {
			t.Fatalf("the request carried %q", secret)
		}
	}
	if got := putPaths(fake.got[0].body); !slices.Equal(got, []string{"runs/r1/input.csv"}) {
		t.Fatalf("put = %v", got)
	}
	if got := fake.got[0].body.Dirs; !slices.Equal(got, []string{"runs", "runs/r1"}) {
		t.Fatalf("dirs = %v", got)
	}
}

func TestClientKeepsTheWorkspaceInStepWithTheSandbox(t *testing.T) {
	fake := &fakeSandbox{answers: []any{
		map[string]any{"exit_code": 0, "output": "", "dirs": []string{"runs", "runs/r1", "runs/r1/empty"}, "changed": []map[string]any{
			{"path": "runs/r1/artifacts/report.xlsx", "mode": 0o644, "data": []byte("xlsx bytes")},
			{"path": "bin/tool", "mode": 0o755, "data": []byte("#!/bin/sh\n")},
		}, "deleted": []string{"old.txt"}, "skipped": []map[string]any{{"path": "venv/bin/python", "reason": "not a regular file"}}},
		map[string]any{"exit_code": 0, "output": "ok"},
	}}
	server := httptest.NewServer(fake)
	defer server.Close()
	workspace := t.TempDir()
	write(t, filepath.Join(workspace, "old.txt"), "old")
	write(t, filepath.Join(workspace, "keep.txt"), "keep")
	client, _ := NewClient(Config{URL: server.URL}, workspace)
	ctx := context.Background()

	result, err := client.Exec(ctx, "make", "", nil, 5, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Output, "venv/bin/python (not a regular file)") {
		t.Fatalf("output = %q", result.Output)
	}
	if data, _ := os.ReadFile(filepath.Join(workspace, "runs", "r1", "artifacts", "report.xlsx")); string(data) != "xlsx bytes" {
		t.Fatalf("the artifact was not written back: %q", data)
	}
	if info, err := os.Stat(filepath.Join(workspace, "bin", "tool")); err != nil || info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("the tool lost its execute bit: %v %v", info, err)
	}
	if info, err := os.Stat(filepath.Join(workspace, "runs", "r1", "empty")); err != nil || !info.IsDir() {
		t.Fatalf("an empty folder the command made is missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "old.txt")); !os.IsNotExist(err) {
		t.Fatal("a file the command removed is still here")
	}

	write(t, filepath.Join(workspace, "keep.txt"), "edited by the file tool")
	if err := os.Remove(filepath.Join(workspace, "runs", "r1", "artifacts", "report.xlsx")); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Exec(ctx, "cat keep.txt", "", nil, 5, 1000); err != nil {
		t.Fatal(err)
	}
	second := fake.got[1].body
	if second.Fresh || len(second.Dirs) != 0 || !slices.Equal(putPaths(second), []string{"keep.txt"}) || !slices.Equal(second.Delete, []string{"runs/r1/artifacts/report.xlsx"}) {
		t.Fatalf("second command sent fresh=%v put=%v delete=%v", second.Fresh, putPaths(second), second.Delete)
	}
}

func TestClientRefusesFilesOutsideTheWorkspace(t *testing.T) {
	for _, name := range []string{"../escape.txt", "/tmp/escape.txt", `a\..\..\escape.txt`, "runs/r1/.openneko/job.json", "."} {
		fake := &fakeSandbox{answers: []any{map[string]any{"exit_code": 0, "changed": []map[string]any{{"path": name, "mode": 0o644, "data": []byte("x")}}}}}
		server := httptest.NewServer(fake)
		parent := t.TempDir()
		workspace := filepath.Join(parent, "w")
		write(t, filepath.Join(workspace, "runs", "r1", ".openneko", "job.json"), "{}")
		client, _ := NewClient(Config{URL: server.URL}, workspace)
		if _, err := client.Exec(context.Background(), "true", "", nil, 5, 1000); err == nil {
			t.Fatalf("%q was taken", name)
		}
		if data, _ := os.ReadFile(filepath.Join(workspace, "runs", "r1", ".openneko", "job.json")); string(data) != "{}" {
			t.Fatalf("%q overwrote the job file", name)
		}
		if _, err := os.Stat(filepath.Join(parent, "escape.txt")); !os.IsNotExist(err) {
			t.Fatalf("%q landed outside the workspace", name)
		}
		server.Close()
	}
}

func TestClientSendsEverythingAgainWhenTheSandboxLostTheRun(t *testing.T) {
	fake := &fakeSandbox{status: []int{http.StatusOK, http.StatusConflict, http.StatusOK}}
	server := httptest.NewServer(fake)
	defer server.Close()
	workspace := t.TempDir()
	write(t, filepath.Join(workspace, "a.txt"), "a")
	client, _ := NewClient(Config{URL: server.URL}, workspace)
	for i := 0; i < 2; i++ {
		if _, err := client.Exec(context.Background(), "true", "", nil, 5, 1000); err != nil {
			t.Fatal(err)
		}
	}
	if len(fake.got) != 3 {
		t.Fatalf("requests = %d", len(fake.got))
	}
	lost, again := fake.got[1].body, fake.got[2].body
	if lost.Fresh || len(lost.Put) != 0 || !again.Fresh || !slices.Equal(putPaths(again), []string{"a.txt"}) {
		t.Fatalf("lost=%+v again=%+v", lost, again)
	}
}

func TestClientReachesAUnixSocket(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "sandbox.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeSandbox{}
	server := &http.Server{Handler: fake}
	go server.Serve(listener)
	defer server.Close()
	client, err := NewClient(Config{URL: "unix://" + socket}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Exec(context.Background(), "true", "", nil, 5, 1000); err != nil {
		t.Fatal(err)
	}
	if fake.got[0].auth != "" {
		t.Fatalf("a client without a key sent %q", fake.got[0].auth)
	}
}

func TestClientReportsAnUnreachableOrRefusingSandbox(t *testing.T) {
	client, _ := NewClient(Config{URL: "unix://" + filepath.Join(t.TempDir(), "none.sock")}, t.TempDir())
	if _, err := client.Exec(context.Background(), "true", "", nil, 5, 1000); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("err = %v", err)
	}
	for status, want := range map[int]string{http.StatusUnauthorized: "refused", http.StatusNotFound: "older than this release"} {
		server := httptest.NewServer(&fakeSandbox{status: []int{status}})
		client, _ = NewClient(Config{URL: server.URL}, t.TempDir())
		if _, err := client.Exec(context.Background(), "true", "", nil, 5, 1000); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("status %d: err = %v", status, err)
		}
		server.Close()
	}
}

func TestClientRefusesBadURLs(t *testing.T) {
	if (Config{}).Enabled() || (Config{}).Check() != nil {
		t.Fatal("an empty URL must mean off")
	}
	for _, raw := range []string{"ftp://x", "unix://", "http://", "sandbox:8080"} {
		if _, err := NewClient(Config{URL: raw}, "/w"); err == nil || (Config{URL: raw}).Check() == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
}
