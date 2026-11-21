package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/open-neko/neko/apps/openneko/internal/confine"
	"github.com/open-neko/neko/apps/openneko/internal/testsock"
	"github.com/open-neko/openneko/apps/ax-harness/sandbox"
	"github.com/open-neko/openneko/apps/ax-harness/serve"
)

func setProbeMode(in runnerProbeIn) {
	confine.SetForTest(in.Mode == "nosandbox", in.Loosen)
}

// runnerControlFree lists checks that may fail with no confinement too.
var runnerControlFree = map[string]bool{
	"TCP to 1.1.1.1:443": true, "TCP to example.com:443": true, "UDP to 1.1.1.1:53": true, "resolve a name": true,
	"TCP over IPv6 loopback": true, "connect to the Docker socket": true, "send to the system log socket": true,
}

// runnerLoosened names, for each rule added to the profile, the checks that
// the rule must open. Each check that the control can do is in one of them,
// so each refusal is proved to come from the profile.
var runnerLoosened = map[string][]string{
	"(allow network*)": {"TCP to web's loopback listener", "UDP to loopback",
		"connect to a Unix socket outside its folders", "send to a datagram socket outside its folders"},
	"(allow file-read*)":  {"read the planted secret key", "list the config folder", "list the home folder"},
	"(allow file-write*)": {"write outside its folders", "truncate a file outside its folders"},
	"(allow process-fork) (allow process-exec) (allow file-read*)": {"run another program", "run a file it wrote"},
	"(allow signal)": {"signal web (kill 0)"},
	"(allow sysctl-read) (allow process-info*)": {"read web's arguments and environment"},
}

func runnerChecks(ctx context.Context, in runnerProbeIn, workspace string) probeResult {
	out := probeResult{Refused: map[string]bool{}, Allowed: map[string]bool{}}
	refused := func(what string, err error) {
		out.Refused[what] = err != nil
		if err != nil && os.Getenv("OPENNEKO_TEST_VERBOSE") != "" {
			out.Notes = append(out.Notes, fmt.Sprintf("%s: %v", what, err))
		}
	}
	_, err := os.ReadFile(in.Secret)
	refused("read the planted secret key", err)
	_, err = os.ReadDir(filepath.Dir(in.Secret))
	refused("list the config folder", err)
	_, err = os.ReadDir(in.Home)
	refused("list the home folder", err)
	refused("TCP to web's loopback listener", dial("tcp", in.TCP))
	refused("TCP to 1.1.1.1:443", dial("tcp", "1.1.1.1:443"))
	refused("TCP to example.com:443", dial("tcp", "example.com:443"))
	refused("UDP to loopback", sendUDP(in.UDP))
	refused("UDP to 1.1.1.1:53", sendUDP("1.1.1.1:53"))
	if in.TCP6 != "" {
		refused("TCP over IPv6 loopback", dial("tcp", in.TCP6))
	}
	lctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	_, err = net.DefaultResolver.LookupHost(lctx, "example.com")
	cancel()
	refused("resolve a name", err)
	msg, err := exec.Command("/bin/sh", "-c", "echo ran").Output()
	if err == nil && !strings.Contains(string(msg), "ran") {
		err = fmt.Errorf("no output")
	}
	refused("run another program", err)
	refused("signal web (kill 0)", unix.Kill(in.Parent, 0))
	args, err := unix.SysctlRaw("kern.procargs2", in.Parent)
	if err == nil && !strings.Contains(string(args), "PATH=") {
		err = fmt.Errorf("read no variable")
	}
	refused("read web's arguments and environment", err)
	refused("truncate a file outside its folders", unix.Truncate(in.Victim, 0))
	refused("write outside its folders", os.WriteFile(in.Outside, []byte("x"), 0o600))
	refused("connect to a Unix socket outside its folders", dial("unix", in.Stream))
	refused("send to a datagram socket outside its folders", sendUnixgram(in.Datagram))
	if in.Docker != "" {
		refused("connect to the Docker socket", dial("unix", in.Docker))
	}
	if _, err := os.Stat("/var/run/syslog"); err == nil {
		refused("send to the system log socket", sendUnixgram("/var/run/syslog"))
	}
	tool := filepath.Join(workspace, "tool")
	err = copyFile("/usr/bin/true", tool)
	if err == nil {
		err = exec.Command(tool).Run()
	}
	refused("run a file it wrote", err)

	err = os.WriteFile(filepath.Join(workspace, "work.txt"), []byte("kept"), 0o600)
	if err == nil {
		_, err = os.ReadFile(filepath.Join(workspace, "work.txt"))
	}
	out.Allowed["write and read its workspace"] = err == nil
	f, err := os.OpenFile(filepath.Join(workspace, "work.txt"), os.O_RDWR, 0)
	if err == nil {
		err = f.Truncate(1)
		f.Close()
	}
	out.Allowed["truncate a workspace file through its descriptor"] = err == nil
	out.Allowed["reach the key door through the broker"] = keyDoorAnswers(&out)
	return out
}

func newRunnerTargets(t *testing.T) runnerTargets {
	t.Helper()
	base := testsock.Dir(t)
	other := filepath.Join(base, "other")
	config := filepath.Join(base, "config", "openneko")
	home := filepath.Join(base, "home")
	workspace := filepath.Join(base, "agent-runs", "turn-1")
	tmp := filepath.Join(base, "tmp")
	for _, d := range []string{other, config, home, workspace, tmp} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	in := runnerProbeIn{Secret: filepath.Join(config, "secret-key"), Home: home, Outside: filepath.Join(other, "planted-by-runner"),
		Victim: filepath.Join(other, "victim"), Parent: os.Getpid()}
	for p, body := range map[string]string{in.Secret: "planted-secret-key", filepath.Join(home, ".zsh_history"): "planted", in.Victim: "keep me"} {
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	in.TCP = acceptAll(t, "tcp", "127.0.0.1:0").Addr().String()
	if ln := acceptAll(t, "tcp", "[::1]:0"); ln != nil {
		in.TCP6 = ln.Addr().String()
	}
	udp, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { udp.Close() })
	in.UDP = udp.LocalAddr().String()
	in.Stream = filepath.Join(other, "docker.sock")
	if acceptAll(t, "unix", in.Stream) == nil {
		t.Fatal("cannot listen on", in.Stream)
	}
	in.Datagram = filepath.Join(other, "log.sock")
	gram, err := net.ListenPacket("unixgram", in.Datagram)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { gram.Close() })
	if host, ok := strings.CutPrefix(os.Getenv("DOCKER_HOST"), "unix://"); ok {
		in.Docker = host
	} else if fileExists("/var/run/docker.sock") {
		in.Docker = "/var/run/docker.sock"
	}
	return runnerTargets{in: in, workspace: workspace, tmp: tmp}
}

// TestConfinedRunner runs the 17 checks of the CONF0 macOS probe, plus
// truncate, the parent's arguments and a file it wrote, inside a real agent
// runner. Each refusal must open when one rule loosens the profile.
func TestConfinedRunner(t *testing.T) {
	requireRunnerConfinement(t)
	tg := newRunnerTargets(t)
	serveFakeDoor(t)

	control, msg := runRunnerProbe(t, tg, "control")
	if control.Refused == nil {
		t.Fatalf("control: the runner gave no result: %s", msg)
	}
	for what, refused := range control.Refused {
		if refused && !runnerControlFree[what] {
			t.Errorf("control: with no confinement the runner could not %s, so the check proves nothing", what)
		}
	}
	got, msg := runRunnerProbe(t, tg, "full")
	if got.Refused == nil {
		t.Fatalf("the confined runner gave no result: %s", msg)
	}
	if len(got.Refused) != len(control.Refused) || len(got.Refused) < 17 {
		t.Fatalf("the runner ran %d checks confined and %d unconfined", len(got.Refused), len(control.Refused))
	}
	for what, refused := range got.Refused {
		if !refused {
			t.Errorf("the confined runner could %s", what)
		}
	}
	for what, ok := range got.Allowed {
		if !ok {
			t.Errorf("the confined runner could not %s %v", what, got.Notes)
		}
	}
	if fileExists(tg.in.Outside) {
		t.Error("the confined runner wrote outside its folders")
	}

	proved := map[string]bool{}
	for rule, opens := range runnerLoosened {
		loose := tg
		loose.in.Loosen = rule
		res, msg := runRunnerProbe(t, loose, "full")
		if res.Refused == nil {
			t.Errorf("with %s the runner gave no result: %s", rule, msg)
			continue
		}
		for _, what := range opens {
			proved[what] = true
			if res.Refused[what] {
				t.Errorf("with %s the runner still could not %s: the check does not prove that rule", rule, what)
			}
		}
	}
	for what, refused := range control.Refused {
		if !refused && !proved[what] {
			t.Errorf("no loosened profile proves the check %q", what)
		}
	}
}

func TestRunnerRefusesATurnWhenItCannotConfine(t *testing.T) {
	for _, c := range []struct{ name, mode, loosen string }{
		{"no workspace", "full", ""},
		{"no sandbox_init", "nosandbox", ""},
		{"a refused profile", "full", "(allow no-such-operation)"},
	} {
		t.Run(c.name, func(t *testing.T) {
			tg := newRunnerTargets(t)
			if c.name == "no workspace" {
				tg.workspace = ""
			}
			tg.in.Loosen = c.loosen
			_, msg := runRunnerProbe(t, tg, c.mode)
			if !strings.Contains(msg, "could not confine itself, so the turn did not run") {
				t.Fatalf("the runner ran the turn: %q", msg)
			}
		})
	}
}

func TestTurnsAreOffWhenRunnersCannotBeConfined(t *testing.T) {
	t.Setenv(serveConfineEnv, "")
	confine.SetForTest(true, "")
	t.Cleanup(func() { confine.SetForTest(false, "") })
	st := confine.RunnerAvailable()
	want := "agent confinement: off. " + confine.Kernel() + " has no sandbox_init, the call that confines an agent runner, so agent turns are off"
	if _, confineIt, off := runnerConfinement(&strings.Builder{}, false); off != want || confineIt || st.Confined {
		t.Fatalf("production: off=%q confine=%v state=%+v", off, confineIt, st)
	}
	var out strings.Builder
	if !confinementDoctorLines(context.Background(), &out) || !strings.Contains(out.String(), want) {
		t.Fatalf("doctor:\n%s", out.String())
	}
	if _, _, off := runnerConfinement(&strings.Builder{}, true); off != "" {
		t.Fatalf("development mode turned turns off: %q", off)
	}

	sock := filepath.Join(testsock.Dir(t), "serve.sock")
	ctx, stop := context.WithCancel(context.Background())
	t.Setenv("OPENNEKO_ENV", "production")
	cleanServeEnv(t)
	done, err := serveAgentTurns(ctx, io.Discard, sock, 1, sandbox.Config{}, confine.State{}, confine.Available())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { stop(); <-done }()
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	start := serve.Frame{Type: "start", Spec: json.RawMessage(`{}`), Environ: []string{"OPENNEKO_HARNESS_WORKSPACE_DIR=" + t.TempDir()}}
	if err := json.NewEncoder(conn).Encode(start); err != nil {
		t.Fatal(err)
	}
	var exit serve.Frame
	if err := json.NewDecoder(conn).Decode(&exit); err != nil || exit.Type != "exit" || exit.Error != want {
		t.Fatalf("a turn on a Mac that cannot confine its runner: %+v, %v", exit, err)
	}
	if _, _, _, err := checkServeSocket(context.Background(), sock); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("release-check passed a serve that refuses turns: %v", err)
	}

	confine.SetForTest(false, "")
	out.Reset()
	if confinementDoctorLines(context.Background(), &out) || !strings.Contains(out.String(), "agent confinement: seatbelt, on macOS") {
		t.Fatalf("doctor on this Mac:\n%s", out.String())
	}
}

// floodThreads runs on Linux only: macOS has no per-process thread cap.
func floodThreads(int) int { return 0 }

func removeProbeLeftovers(runnerProbeIn) {}
