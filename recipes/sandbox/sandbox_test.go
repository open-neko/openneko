package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func startSandbox(t *testing.T, switchUsers bool) (*Server, string) {
	t.Helper()
	root := t.TempDir()
	_ = os.Chmod(root, 0o755)
	_ = os.Chmod(filepath.Dir(root), 0o755)
	server := &Server{Workspaces: filepath.Join(root, "runs"), Homes: filepath.Join(root, "homes"), BaseUID: 23000, Users: 4, GID: os.Getgid(), SwitchUsers: switchUsers, PATH: "/usr/bin:/bin"}
	socket := filepath.Join(root, "sandbox.sock")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe(ctx, "unix://"+socket) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(socket); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	return server, socket
}

type testClient struct {
	http    *http.Client
	base    string
	key     string
	run     string
	started bool
}

func newClient(socket string) *testClient {
	var id [12]byte
	_, _ = rand.Read(id[:])
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", socket)
	}}
	return &testClient{http: &http.Client{Transport: transport}, base: "http://sandbox", run: "run-" + hex.EncodeToString(id[:])}
}

func (c *testClient) do(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, reader)
	if err != nil {
		return nil, err
	}
	if c.key != "" {
		req.Header.Set("Authorization", "Bearer "+c.key)
	}
	return c.http.Do(req)
}

func (c *testClient) send(ctx context.Context, body execBody) (execAnswer, error) {
	body.Fresh = !c.started
	if body.TimeoutSeconds == 0 {
		body.TimeoutSeconds = 10
	}
	if body.MaxOutput == 0 {
		body.MaxOutput = 10_000
	}
	resp, err := c.do(ctx, http.MethodPost, "/v2/runs/"+c.run+"/exec", body)
	if err != nil {
		return execAnswer{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(resp.Body)
		return execAnswer{}, fmt.Errorf("%d %s", resp.StatusCode, bytes.TrimSpace(msg))
	}
	c.started = true
	var answer execAnswer
	return answer, json.NewDecoder(resp.Body).Decode(&answer)
}

func (c *testClient) Exec(ctx context.Context, cmd string) (execAnswer, error) {
	return c.send(ctx, execBody{Command: cmd})
}

func (c *testClient) Close() error {
	resp, err := c.do(context.Background(), http.MethodDelete, "/v2/runs/"+c.run, nil)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

func paths(files []fileData) []string {
	var out []string
	for _, file := range files {
		out = append(out, file.Path)
	}
	return out
}

func TestSandboxRunsCommandsWithoutSecretsAsTheRunUser(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "must-not-leak")
	switchUsers := os.Geteuid() == 0
	server, socket := startSandbox(t, switchUsers)
	client := newClient(socket)
	ctx := context.Background()

	result, err := client.Exec(ctx, "id -u; env; pwd")
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("exec = %+v, %v", result, err)
	}
	if strings.Contains(result.Output, "must-not-leak") || strings.Contains(result.Output, "OPENAI_API_KEY") {
		t.Fatalf("the sandbox passed a secret to the command:\n%s", result.Output)
	}
	if !strings.Contains(result.Output, "HOME="+filepath.Join(server.Homes, client.run)) {
		t.Fatalf("the run has no home of its own:\n%s", result.Output)
	}
	if !strings.Contains(result.Output, "\n"+filepath.Join(server.Workspaces, client.run)+"\n") {
		t.Fatalf("the command did not start in the run's folder:\n%s", result.Output)
	}
	if switchUsers {
		if first := strings.SplitN(result.Output, "\n", 2)[0]; first != strconv.Itoa(server.BaseUID) {
			t.Fatalf("command ran as uid %s, want %d", first, server.BaseUID)
		}
	}

	other := newClient(socket)
	if result, _ := other.Exec(ctx, "id -u"); switchUsers && strings.TrimSpace(result.Output) == strconv.Itoa(server.BaseUID) {
		t.Fatal("two runs share a user")
	}

	if _, err := client.Exec(ctx, "setsid sleep 60 >/dev/null 2>&1 &"); err != nil {
		t.Fatal(err)
	}
	if switchUsers && len(processesOf(server.BaseUID)) > 0 {
		t.Fatal("a command left a background process running")
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{filepath.Join(server.Homes, client.run), filepath.Join(server.Workspaces, client.run)} {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("ending the run left %s: %v", dir, err)
		}
	}
	if _, err := client.Exec(ctx, "true"); err == nil || !strings.HasPrefix(err.Error(), "409") {
		t.Fatalf("an ended run took a command: %v", err)
	}
	if _, err := other.Exec(ctx, "true"); err != nil {
		t.Fatal(err)
	}
	third := newClient(socket)
	if _, err := third.Exec(ctx, "true"); err != nil {
		t.Fatalf("an ended run's user was not freed: %v", err)
	}
}

func TestSandboxCarriesFilesBothWays(t *testing.T) {
	_, socket := startSandbox(t, os.Geteuid() == 0)
	client := newClient(socket)
	ctx := context.Background()

	answer, err := client.send(ctx, execBody{
		Command: "cat in/a.txt; ./tool.sh; echo changed > in/a.txt; mkdir -p out; echo new > out/b.txt; rm gone.txt; ln -s in/a.txt link",
		Put: []fileData{
			{Path: "in/a.txt", Mode: 0o644, Data: []byte("from the core\n")},
			{Path: "tool.sh", Mode: 0o755, Data: []byte("#!/bin/sh\necho tool ran\n")},
			{Path: "gone.txt", Mode: 0o644, Data: []byte("x")},
		},
	})
	if err != nil || answer.ExitCode != 0 {
		t.Fatalf("exec = %+v, %v", answer, err)
	}
	if answer.Output != "from the core\ntool ran\n" {
		t.Fatalf("output = %q", answer.Output)
	}
	if got := paths(answer.Changed); !slices.Equal(got, []string{"in/a.txt", "out/b.txt"}) {
		t.Fatalf("changed = %v", got)
	}
	if string(answer.Changed[0].Data) != "changed\n" || string(answer.Changed[1].Data) != "new\n" {
		t.Fatalf("changed data = %q, %q", answer.Changed[0].Data, answer.Changed[1].Data)
	}
	if !slices.Equal(answer.Dirs, []string{"out"}) {
		t.Fatalf("dirs = %v", answer.Dirs)
	}
	if !slices.Equal(answer.Deleted, []string{"gone.txt"}) {
		t.Fatalf("deleted = %v", answer.Deleted)
	}
	if len(answer.Skipped) != 1 || answer.Skipped[0].Path != "link" {
		t.Fatalf("skipped = %+v", answer.Skipped)
	}

	answer, err = client.send(ctx, execBody{Command: "test ! -e out && echo removed", Delete: []string{"out"}})
	if err != nil || strings.TrimSpace(answer.Output) != "removed" {
		t.Fatalf("a folder the core removed is still there: %+v %v", answer, err)
	}
	if len(answer.Changed) != 0 || len(answer.Deleted) != 0 || len(answer.Skipped) != 0 {
		t.Fatalf("an idle command reported files: %+v", answer)
	}

	answer, err = client.send(ctx, execBody{Command: "test -d runs/r2/artifacts && echo there", Dirs: []string{"runs/r2/artifacts"}})
	if err != nil || strings.TrimSpace(answer.Output) != "there" || len(answer.Dirs) != 0 {
		t.Fatalf("an empty folder the core sent is missing: %+v %v", answer, err)
	}

	answer, err = client.send(ctx, execBody{Command: "mkfifo pipe.txt"})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := client.send(ctx, execBody{Command: "cat pipe.txt", Put: []fileData{{Path: "pipe.txt", Data: []byte("plain")}}})
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a file sent over a pipe the run made blocked the sandbox")
	}
}

func TestSandboxHoldsBackFilesOverTheLimit(t *testing.T) {
	_, socket := startSandbox(t, os.Geteuid() == 0)
	old := maxTransfer
	maxTransfer = 10
	t.Cleanup(func() { maxTransfer = old })
	client := newClient(socket)
	ctx := context.Background()
	answer, err := client.Exec(ctx, "printf 123456 > a; printf 123456 > b")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(paths(answer.Changed), []string{"a"}) || len(answer.Skipped) != 1 || answer.Skipped[0].Path != "b" {
		t.Fatalf("answer = %+v", answer)
	}
	answer, err = client.Exec(ctx, "true")
	if err != nil || !slices.Equal(paths(answer.Changed), []string{"b"}) {
		t.Fatalf("a held-back file was not sent later: %+v %v", answer, err)
	}
	if _, err := client.send(ctx, execBody{Command: "true", Put: []fileData{{Path: "big", Data: []byte("12345678901")}}}); err == nil || !strings.HasPrefix(err.Error(), "400") {
		t.Fatalf("an upload over the limit was taken: %v", err)
	}
}

func TestSandboxRewritesTheCallersFolder(t *testing.T) {
	server, socket := startSandbox(t, os.Geteuid() == 0)
	client := newClient(socket)
	host := `C:\Users\neko\runs\org-1`
	answer, err := client.send(context.Background(), execBody{
		Root:    host,
		Command: "cat '" + host + "/a.txt'; pwd",
		Put:     []fileData{{Path: "a.txt", Data: []byte("ok\n")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if answer.Output != "ok\n"+host+"\n" {
		t.Fatalf("output = %q", answer.Output)
	}
	if strings.Contains(answer.Output, server.Workspaces) {
		t.Fatal("the output shows the sandbox's own path")
	}
}

func TestSandboxAsksForTheFilesAgainWhenItLostTheRun(t *testing.T) {
	_, socket := startSandbox(t, os.Geteuid() == 0)
	client := newClient(socket)
	client.started = true
	if _, err := client.Exec(context.Background(), "true"); err == nil || !strings.HasPrefix(err.Error(), "409") {
		t.Fatalf("an unknown run took a command: %v", err)
	}
	client.started = false
	if _, err := client.Exec(context.Background(), "true"); err != nil {
		t.Fatal(err)
	}
	client.started = false
	if _, err := client.Exec(context.Background(), "true"); err == nil || !strings.HasPrefix(err.Error(), "409") {
		t.Fatalf("a run was started twice: %v", err)
	}
}

func TestSandboxRefusesPathsOutsideTheRun(t *testing.T) {
	_, socket := startSandbox(t, os.Geteuid() == 0)
	client := newClient(socket)
	ctx := context.Background()
	for _, body := range []execBody{
		{Put: []fileData{{Path: "../escape", Data: []byte("x")}}},
		{Put: []fileData{{Path: "/etc/escape", Data: []byte("x")}}},
		{Put: []fileData{{Path: `a\..\..\b`, Data: []byte("x")}}},
		{Put: []fileData{{Path: ".", Data: []byte("x")}}},
		{Delete: []string{"../.."}},
		{Dirs: []string{"../up"}},
		{Dir: "../.."},
		{SkillDir: "/etc"},
		{Env: map[string]string{"ARTIFACT_DIR": "/etc"}},
		{Env: map[string]string{"RUN_DIR": "runs/../../.."}},
		{Env: map[string]string{"PYTHONPATH": "lib:/usr/lib/evil"}},
		{Env: map[string]string{"OPENAI_API_KEY": "leak"}},
		{Env: map[string]string{"NODE_PATH": "/tmp/evil"}},
	} {
		body.Command = "true"
		if _, err := client.send(ctx, body); err == nil || !strings.HasPrefix(err.Error(), "400") {
			t.Fatalf("the sandbox took %+v: %v", body, err)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(socket), "escape")); !os.IsNotExist(err) {
		t.Fatal("a file landed outside the run")
	}
}

func TestSandboxMapsRunPathsIntoTheRun(t *testing.T) {
	server, socket := startSandbox(t, os.Geteuid() == 0)
	client := newClient(socket)
	answer, err := client.send(context.Background(), execBody{
		Command:  `echo "$ARTIFACT_DIR|$PYTHONPATH|$SKILL_DIR"; pwd`,
		Dir:      "runs/r1",
		SkillDir: "skills/xlsx",
		Env:      map[string]string{"ARTIFACT_DIR": "runs/r1/artifacts", "PYTHONPATH": "lib:skills/xlsx"},
	})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(server.Workspaces, client.run)
	want := fmt.Sprintf("%s/runs/r1/artifacts|%s/lib:%s/skills/xlsx|%s/skills/xlsx\n%s/runs/r1\n", dir, dir, dir, dir, dir)
	if answer.Output != want {
		t.Fatalf("output = %q, want %q", answer.Output, want)
	}
}

func TestSandboxPassesItsOwnNodePathOnly(t *testing.T) {
	server, socket := startSandbox(t, os.Geteuid() == 0)
	server.NodePath = "/opt/openneko/skills/node_modules"
	res, err := newClient(socket).Exec(context.Background(), `echo "[$NODE_PATH]"`)
	if err != nil || strings.TrimSpace(res.Output) != "[/opt/openneko/skills/node_modules]" {
		t.Fatalf("NODE_PATH = %+v, %v", res, err)
	}
}

func TestSandboxClearsRunFoldersFromAnEarlierStart(t *testing.T) {
	root := t.TempDir()
	runs := filepath.Join(root, "runs")
	for _, name := range []string{"run-aaaaaaaaaaaa", "keep"} {
		if err := os.MkdirAll(filepath.Join(runs, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := (&Server{Workspaces: runs}).prepareWorkspaces(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(runs, "run-aaaaaaaaaaaa")); !os.IsNotExist(err) {
		t.Fatal("an old run folder is still there")
	}
	if _, err := os.Stat(filepath.Join(runs, "keep")); err != nil {
		t.Fatal("a folder that is not a run was removed")
	}
	if info, _ := os.Stat(runs); info.Mode().Perm() != 0o711 {
		t.Fatalf("runs folder mode = %v, want 711", info.Mode().Perm())
	}
}

func TestSandboxKeepsRunsApart(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("switching run users needs root")
	}
	server, socket := startSandbox(t, true)
	ctx := context.Background()
	runA, runB := newClient(socket), newClient(socket)
	res, err := runA.Exec(ctx, "id -G; echo made > mine.txt")
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("run A could not use its own folder: %+v %v", res, err)
	}
	if groups := strings.Fields(strings.SplitN(res.Output, "\n", 2)[0]); len(groups) != 1 || groups[0] != strconv.Itoa(server.BaseUID) {
		t.Fatalf("run A's groups = %v, want only its own", groups)
	}
	if _, err := runB.send(ctx, execBody{Command: "true", Put: []fileData{{Path: "secret.txt", Data: []byte("run b only")}}}); err != nil {
		t.Fatal(err)
	}
	b := filepath.Join(server.Workspaces, runB.run)
	for _, cmd := range []string{"cat " + filepath.Join(b, "secret.txt"), "ls " + server.Workspaces, "touch " + filepath.Join(b, "planted"), "ls " + server.Homes} {
		res, err := runA.Exec(ctx, cmd)
		if err != nil {
			t.Fatal(err)
		}
		if res.ExitCode == 0 {
			t.Fatalf("run A could %q: %s", cmd, res.Output)
		}
	}
	info, err := os.Stat(filepath.Join(b, "secret.txt"))
	if err != nil || info.Sys().(*syscall.Stat_t).Uid != uint32(server.BaseUID+1) || info.Mode().Perm() != 0o600 {
		t.Fatalf("run B's file: %v %v", info, err)
	}
}

func TestSandboxSocketBelongsToTheSharedGroup(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("only root can hand the socket to another group")
	}
	root := t.TempDir()
	server := &Server{Workspaces: filepath.Join(root, "runs"), Homes: filepath.Join(root, "homes"), BaseUID: 23000, Users: 1, GID: 23999, SwitchUsers: true, PATH: "/usr/bin:/bin"}
	socket := filepath.Join(root, "sandbox.sock")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe(ctx, "unix://"+socket) }()
	defer func() {
		cancel()
		<-done
	}()
	var info os.FileInfo
	for i := 0; i < 100; i++ {
		if found, err := os.Stat(socket); err == nil && found.Sys().(*syscall.Stat_t).Gid == 23999 {
			info = found
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if info == nil {
		t.Fatal("the socket was not handed to the shared group")
	}
	if perm := info.Mode().Perm(); perm != 0o660 {
		t.Fatalf("socket mode = %o, want 660", perm)
	}
}

func processesOf(uid int) []int {
	var pids []int
	entries, _ := os.ReadDir("/proc")
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		status, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "status"))
		if err != nil || strings.Contains(string(status), "State:\tZ") {
			continue
		}
		for _, line := range strings.Split(string(status), "\n") {
			if fields := strings.Fields(line); len(fields) > 1 && fields[0] == "Uid:" && fields[1] == strconv.Itoa(uid) {
				pids = append(pids, pid)
			}
		}
	}
	return pids
}

func TestSandboxChecksTheKey(t *testing.T) {
	root := t.TempDir()
	server := &Server{Workspaces: filepath.Join(root, "runs"), Homes: filepath.Join(root, "homes"), Key: "sandbox-key", BaseUID: 23300, Users: 1, GID: os.Getgid(), PATH: "/usr/bin:/bin"}
	listener, err := server.listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx, listener) }()
	t.Cleanup(func() { cancel(); <-done })

	client := &testClient{http: http.DefaultClient, base: "http://" + listener.Addr().String(), run: "run-keykeykey"}
	resp, err := client.do(ctx, http.MethodGet, "/v2/health", nil)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("health = %v, %v", resp, err)
	}
	resp.Body.Close()
	for _, key := range []string{"", "wrong"} {
		client.key = key
		if _, err := client.Exec(ctx, "true"); err == nil || !strings.HasPrefix(err.Error(), "401") {
			t.Fatalf("key %q: err = %v, want 401", key, err)
		}
	}
	client.key = "sandbox-key"
	if res, err := client.Exec(ctx, "echo ok"); err != nil || strings.TrimSpace(res.Output) != "ok" {
		t.Fatalf("exec with the key = %+v, %v", res, err)
	}
}

func TestSandboxRefusesTCPWithoutAKey(t *testing.T) {
	if _, err := (&Server{}).listen("127.0.0.1:0"); err == nil {
		t.Fatal("a TCP listener without a key was opened")
	}
}
