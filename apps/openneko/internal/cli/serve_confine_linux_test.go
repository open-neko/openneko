package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/open-neko/neko/apps/openneko/internal/confine"
	"github.com/open-neko/neko/apps/openneko/internal/native"
)

const confineProbeEnv = "OPENNEKO_TEST_CONFINE_PROBE"

// requireLandlock fails in CI and skips elsewhere when the kernel has no
// Landlock, so a runner that cannot prove the confinement shows red.
func requireLandlock(t *testing.T) confine.State {
	t.Helper()
	st := confine.Available()
	t.Logf("kernel %s, %s, ABI %d", confine.Kernel(), st.Mechanism, st.ABI)
	if st.ABI < 1 {
		if os.Getenv("CI") != "" {
			t.Fatalf("this runner cannot prove the confinement: %s gives Landlock ABI %d", confine.Kernel(), st.ABI)
		}
		t.Skipf("%s gives Landlock ABI %d", confine.Kernel(), st.ABI)
	}
	return st
}

// TestConfineProbeChild is the confined serve of TestConfinedServeOneAccount.
// It confines itself with serve's own spec and code, then tries each path.
func TestConfineProbeChild(t *testing.T) {
	if os.Getenv(confineProbeEnv) == "" {
		t.Skip("helper process")
	}
	var in struct {
		Folders   map[string]string
		Planted   []string
		WebAdmin  string
		Door      string
		WebPID    int
		WebAddr   uintptr
		Outside   string
		ExecCopy  string
		FilesFile string
		Part      string
		Hidden    []string
	}
	if err := json.Unmarshal([]byte(os.Getenv(confineProbeEnv)), &in); err != nil {
		t.Fatal(err)
	}
	f := serveFolders{files: in.Folders["files"], index: in.Folders["index"], agentRuns: in.Folders["agentRuns"], subsDir: in.Folders["subs"],
		gjConfig: in.Folders["gj"], agentSocket: in.Folders["agentSocket"], checkSocket: in.Folders["checkSocket"], duckdb: "duckdb"}
	tool := mustRead(t, "/bin/true")
	var log bytes.Buffer
	var st confine.State
	var err error
	if in.Part == "host" {
		st, err = confineTurnHost(&log, f, os.Getenv("OPENNEKO_ENV") == "development")
	} else {
		st, err = confineServe(&log, f, os.Getenv("OPENNEKO_ENV") == "development")
	}
	out := probeResult{Refused: map[string]bool{}, Allowed: map[string]bool{}}
	out.Notes = append(out.Notes, strings.TrimSpace(log.String()), fmt.Sprintf("state %s, err %v", st, err))

	for _, p := range in.Planted {
		_, err := os.ReadFile(p)
		out.Refused["read "+filepath.Base(p)] = err != nil
	}
	for _, p := range in.Hidden {
		_, err := os.ReadFile(p)
		out.Refused["read the other part's "+filepath.Base(p)] = err != nil
		err = os.WriteFile(p+".new", []byte("x"), 0o600)
		out.Refused["write beside the other part's "+filepath.Base(p)] = err != nil
	}
	token, err := os.ReadFile(in.WebAdmin + ".token")
	out.Refused["read the web admin token"] = err != nil
	out.Refused["call the web admin socket"] = !webAdminAnswers(in.WebAdmin, strings.TrimSpace(string(token)))
	mem, err := os.Open(fmt.Sprintf("/proc/%d/mem", in.WebPID))
	if err == nil {
		got := make([]byte, len(webSecret))
		_, err = mem.ReadAt(got, int64(in.WebAddr))
		mem.Close()
		if err == nil && string(got) != webSecret {
			err = fmt.Errorf("read %q, not the secret", got)
		}
	}
	out.Refused["read web's /proc/<pid>/mem"] = err != nil
	_, err = os.ReadFile(fmt.Sprintf("/proc/%d/environ", in.WebPID))
	out.Refused["read web's /proc/<pid>/environ"] = err != nil
	out.Refused["ptrace web"] = ptraceRefused(in.WebPID)
	err = os.WriteFile(in.Outside, []byte("x"), 0o600)
	out.Refused["write outside its folders"] = err != nil
	err = os.WriteFile(in.ExecCopy, tool, 0o755)
	out.Notes = append(out.Notes, fmt.Sprintf("write the exec copy: %v", err))
	if err == nil {
		err = exec.Command(in.ExecCopy).Run()
	}
	out.Refused["execute a file it wrote"] = err != nil

	err = os.WriteFile(in.FilesFile, []byte("kept"), 0o600)
	if err == nil {
		_, err = os.ReadFile(in.FilesFile)
	}
	out.Allowed["write and read its own folder"] = err == nil
	exe, _ := os.Executable()
	err = exec.Command(exe, "-test.run=^$").Run()
	out.Allowed["run its own program"] = err == nil
	if in.Part == "host" {
		conn, err := net.Dial("unix", in.Door)
		if err == nil {
			conn.Close()
		}
		out.Allowed["connect to the key door"] = err == nil
	}
	if path, lerr := exec.LookPath("duckdb"); lerr == nil && in.Part != "host" {
		cmd := exec.Command(path, "-c", "select 1")
		cmd.Env = []string{"HOME=" + os.TempDir()}
		msg, err := cmd.CombinedOutput()
		out.Allowed["run duckdb"] = err == nil
		if err != nil {
			out.Notes = append(out.Notes, fmt.Sprintf("duckdb: %v %s", err, msg))
		}
	}
	_ = json.NewEncoder(os.Stdout).Encode(out)
}

func mustRead(t *testing.T, path string) []byte {
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func webAdminAnswers(socket, token string) bool {
	c := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{DialContext: (&unixDialer{socket}).dial}}
	req, _ := http.NewRequest(http.MethodGet, "http://web/admin/packs", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := c.Do(req)
	if err != nil {
		return false
	}
	res.Body.Close()
	return res.StatusCode == http.StatusOK
}

func ptraceRefused(pid int) bool {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := unix.PtraceAttach(pid); err != nil {
		return true
	}
	var ws unix.WaitStatus
	_, _ = unix.Wait4(pid, &ws, unix.WALL, nil)
	_ = unix.PtraceDetach(pid)
	return false
}

func TestConfinedServeOneAccount(t *testing.T) {
	requireLandlock(t)
	base := t.TempDir()
	l := native.Layout{Root: filepath.Join(base, "data")}
	if err := l.Prepare(); err != nil {
		t.Fatal(err)
	}
	configDir := filepath.Join(base, "config", "openneko")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	var planted []string
	for name, body := range map[string]string{
		"secret-key":   "planted-secret-key",
		"secrets.json": `{"planted":"provider key"}`,
		"config.json":  `{"pg":{"password":"planted-database-password"}}`,
	} {
		p := filepath.Join(configDir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		planted = append(planted, p)
	}
	home := filepath.Join(base, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	homeFile := filepath.Join(home, ".bash_history")
	if err := os.WriteFile(homeFile, []byte("planted"), 0o600); err != nil {
		t.Fatal(err)
	}
	planted = append(planted, homeFile)

	stopAdmin, err := serveWebAdmin(l.WebAdmin(), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"packs":[]}`)
	}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stopAdmin)
	if !webAdminAnswersWithToken(t, l.WebAdmin()) {
		t.Fatal("the web admin socket refused its own token")
	}
	env := native.ServeEnv(l)
	door, err := net.Listen("unix", env["OPENNEKO_KEY_DOOR_SOCKET"])
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { door.Close() })
	go func() {
		for {
			c, err := door.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	_ = unix.Prctl(unix.PR_SET_PTRACER, unix.PR_SET_PTRACER_ANY, 0, 0, 0)

	runFile, userFile, gjFile := filepath.Join(l.AgentRuns(), "run-of-another-user.md"), filepath.Join(l.Files(), "user-upload.csv"), l.GraphJin()
	for _, p := range []string{runFile, userFile, gjFile} {
		if err := os.WriteFile(p, []byte("planted"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, part := range []string{"network", "host"} {
		t.Run(part, func(t *testing.T) {
			own, hidden := l.Files(), []string{runFile}
			if part == "host" {
				own, hidden = l.AgentRuns(), []string{userFile, gjFile}
			}
			in, _ := json.Marshal(map[string]any{
				"Folders": map[string]string{"files": l.Files(), "index": l.Index(), "agentRuns": l.AgentRuns(), "subs": l.Subscriptions(),
					"gj": l.GraphJin(), "agentSocket": env["OPENNEKO_SERVE_SOCKET"], "checkSocket": env["OPENNEKO_CONFIG_CHECK_SOCKET"]},
				"Planted": planted, "WebAdmin": l.WebAdmin(), "Door": env["OPENNEKO_KEY_DOOR_SOCKET"], "WebPID": os.Getpid(), "WebAddr": uintptr(unsafe.Pointer(unsafe.StringData(webSecret))),
				"Outside": filepath.Join(l.Root, "planted-by-serve"), "ExecCopy": filepath.Join(own, "tool"),
				"FilesFile": filepath.Join(own, "kept.txt"), "Part": part, "Hidden": hidden,
			})
			probeServePart(t, l, home, configDir, env, in)
		})
	}
}

func probeServePart(t *testing.T, l native.Layout, home, configDir string, env map[string]string, in []byte) {
	run := func(mode string) probeResult {
		cmd := exec.Command(os.Args[0], "-test.run=^TestConfineProbeChild$", "-test.count=1")
		cmd.Env = append(os.Environ(), confineProbeEnv+"="+string(in), "HOME="+home, "XDG_CONFIG_HOME="+filepath.Dir(configDir),
			"TMPDIR="+l.Tmp(), "OPENNEKO_AGENT_RUNS_DIR="+l.AgentRuns(), "OPENNEKO_KEY_DOOR_SOCKET="+env["OPENNEKO_KEY_DOOR_SOCKET"])
		if mode == "control" {
			cmd.Env = append(cmd.Env, "OPENNEKO_ENV=development", serveConfineEnv+"=off")
		} else {
			cmd.Env = append(cmd.Env, "OPENNEKO_ENV=production", serveConfineEnv+"=")
		}
		raw, err := cmd.Output()
		var res probeResult
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.HasPrefix(line, "{") && json.Unmarshal([]byte(line), &res) == nil {
				break
			}
		}
		if res.Refused == nil {
			t.Fatalf("%s probe: %v\n%s", mode, err, raw)
		}
		for _, n := range res.Notes {
			t.Logf("%s: %s", mode, n)
		}
		_ = os.Remove(filepath.Join(l.AgentRuns(), "tool"))
		_ = os.Remove(filepath.Join(l.Files(), "tool"))
		for _, p := range []string{filepath.Join(l.AgentRuns(), "run-of-another-user.md.new"), filepath.Join(l.Files(), "user-upload.csv.new"), l.GraphJin() + ".new"} {
			_ = os.Remove(p)
		}
		return res
	}
	control := run("control")
	_ = os.Remove(filepath.Join(l.Root, "planted-by-serve"))
	for what, refused := range control.Refused {
		if refused {
			t.Errorf("control: with no confinement the probe could not %s, so the check proves nothing", what)
		}
	}
	confined := run("confined")
	for _, n := range confined.Notes {
		if strings.Contains(n, "this build uses cgo") && os.Getenv("OPENNEKO_TEST_REQUIRE_CONFINE") == "" {
			t.Skip("below Landlock ABI 8 the proof needs a CGO_ENABLED=0 build, as the release is")
		}
	}
	if len(confined.Refused) != len(control.Refused) || len(confined.Refused) < 10 {
		t.Fatalf("the probe ran %d checks confined and %d unconfined", len(confined.Refused), len(control.Refused))
	}
	for what, refused := range confined.Refused {
		if !refused {
			t.Errorf("confined serve could %s", what)
		}
	}
	for what, ok := range confined.Allowed {
		if !ok {
			t.Errorf("confined serve could not %s", what)
		}
	}
	if _, err := os.Stat(filepath.Join(l.Root, "planted-by-serve")); err == nil {
		t.Error("confined serve wrote outside its folders")
	}
}

func webAdminAnswersWithToken(t *testing.T, socket string) bool {
	t.Helper()
	res, err := webAdminClient(socket).Get("http://web/admin/packs")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res.StatusCode == http.StatusOK
}

type unixDialer struct{ path string }

func (d *unixDialer) dial(ctx context.Context, _, _ string) (net.Conn, error) {
	var nd net.Dialer
	return nd.DialContext(ctx, "unix", d.path)
}

func TestServeRefusesToStartUnconfinedInProduction(t *testing.T) {
	t.Setenv(serveConfineEnv, "off")
	if _, err := confineServe(io.Discard, serveFolders{}, false); err == nil || !strings.Contains(err.Error(), "works only with OPENNEKO_ENV=development") {
		t.Fatalf("production with %s=off: %v", serveConfineEnv, err)
	}
	var out bytes.Buffer
	st, err := confineServe(&out, serveFolders{}, true)
	if err != nil || st.Confined || !strings.Contains(out.String(), "runs unconfined") {
		t.Fatalf("development with %s=off: %v, %v, %q", serveConfineEnv, st, err, out.String())
	}
}

func TestServeSpecKeepsOnlyServesFolders(t *testing.T) {
	l := native.Layout{Root: "/srv/on"}
	env := native.ServeEnv(l)
	t.Setenv("TMPDIR", l.Tmp())
	t.Setenv("OPENNEKO_KEY_DOOR_SOCKET", env["OPENNEKO_KEY_DOOR_SOCKET"])
	f := serveFolders{files: l.Files(), index: l.Index(), agentRuns: l.AgentRuns(), subsDir: l.Subscriptions(), gjConfig: l.GraphJin(),
		agentSocket: env["OPENNEKO_SERVE_SOCKET"], checkSocket: env["OPENNEKO_CONFIG_CHECK_SOCKET"], gjSocket: filepath.Join(l.Tmp(), "s", "gj.sock")}
	network, host := serveSpec(f), hostSpec(f)
	for name, spec := range map[string]confine.Spec{"network": network, "host": host} {
		all := append(append(append([]string{}, spec.Read...), spec.ReadWrite...), spec.Exec...)
		for _, p := range all {
			if p == l.Root || p == l.Run() || strings.HasPrefix(p, l.Credentials()) || p == "/" || p == "/home" || p == "/etc" || strings.HasPrefix(p, appdirConfigPrefix()) {
				t.Errorf("the %s part's spec holds %s", name, p)
			}
		}
		if len(spec.Exec) == 0 {
			t.Errorf("the %s part cannot run its own program", name)
		}
	}
	for _, want := range []string{l.Files(), l.Index(), l.Subscriptions(), l.Tmp(), filepath.Dir(l.GraphJin())} {
		if !hasPath(network.ReadWrite, want) {
			t.Errorf("the network part does not write %s: %v", want, network.ReadWrite)
		}
		if want != l.Tmp() && hasPath(host.ReadWrite, want) {
			t.Errorf("the turn part writes %s: %v", want, host.ReadWrite)
		}
	}
	if hasPath(network.ReadWrite, l.AgentRuns()) || hasPath(network.Sockets, env["OPENNEKO_KEY_DOOR_SOCKET"]) || hasPath(network.Listen, env["OPENNEKO_SERVE_SOCKET"]) {
		t.Errorf("the network part reaches the agent runs, the key door or the turn socket: %+v", network)
	}
	for _, want := range []string{env["OPENNEKO_CONFIG_CHECK_SOCKET"], f.gjSocket} {
		if !hasPath(network.Listen, want) {
			t.Errorf("the network part cannot listen on %s: %v", want, network.Listen)
		}
	}
	if !hasPath(host.ReadWrite, l.AgentRuns()) || !hasPath(host.Listen, env["OPENNEKO_SERVE_SOCKET"]) || !hasPath(host.Sockets, env["OPENNEKO_KEY_DOOR_SOCKET"]) {
		t.Errorf("the turn part cannot reach the agent runs, its socket or the key door: %+v", host)
	}
	if got := postgresCertPaths("postgres://u@db/x?sslmode=verify-full&sslrootcert=/etc/openneko/pg-ca.pem"); !hasPath(got, "/etc/openneko/pg-ca.pem") {
		t.Errorf("serve cannot read the CA file in the database URL: %v", got)
	}
}

func appdirConfigPrefix() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config")
}

func hasPath(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
