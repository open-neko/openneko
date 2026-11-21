package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/open-neko/neko/apps/openneko/internal/confine"
	"github.com/open-neko/openneko/apps/ax-harness/sandbox"
	"github.com/open-neko/openneko/apps/ax-harness/serve"
)

func setProbeMode(in runnerProbeIn) {
	confine.SetForTest(in.ABI, in.Mode == "seccomp", in.Mode == "landlock")
}

// runnerControlFree lists checks that may fail with no confinement too: they
// need the internet or a right that an unconfined runner lacks as well.
var runnerControlFree = map[string]bool{
	"TCP to 1.1.1.1:443": true, "TCP to example.com:443": true, "UDP to 1.1.1.1:53": true, "resolve a name": true,
	"open a raw, packet or ICMP socket": true, "TCP over IPv6 loopback": true,
	"connect to the Docker socket": true, "connect to the D-Bus session bus": true, "send to /dev/log": true,
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
	_, err = syscall.ForkExec(filepath.Join(workspace, "no-such-program"), nil, nil)
	if !errors.Is(err, syscall.EPERM) {
		err = nil
	}
	refused("start a child process", err)
	refused("open a raw, packet or ICMP socket", rawSocket())
	mem, err := os.Open(fmt.Sprintf("/proc/%d/mem", in.Parent))
	if err == nil {
		got := make([]byte, len(webSecret))
		_, err = mem.ReadAt(got, int64(in.ParentAddr))
		mem.Close()
		if err == nil && string(got) != webSecret {
			err = fmt.Errorf("read %q, not the secret", got)
		}
	}
	refused("read web's /proc/<pid>/mem", err)
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", in.Parent))
	if err == nil && !strings.Contains(string(b), "PATH=") {
		err = fmt.Errorf("read no variable")
	}
	refused("read web's /proc/<pid>/environ", err)
	if ptraceRefused(in.Parent) {
		err = fmt.Errorf("refused")
	} else {
		err = nil
	}
	refused("ptrace web", err)
	buf := make([]byte, len(webSecret))
	_, err = unix.ProcessVMReadv(in.Parent, []unix.Iovec{{Base: &buf[0], Len: uint64(len(buf))}},
		[]unix.RemoteIovec{{Base: in.ParentAddr, Len: len(buf)}}, 0)
	refused("process_vm_readv web", err)
	refused("signal web (kill 0)", unix.Kill(in.Parent, 0))
	refused("signal a thread of web (tgkill 0)", unix.Tgkill(in.Parent, in.Parent, 0))
	fd, err := unix.PidfdOpen(in.Parent, 0)
	if err == nil {
		unix.Close(fd)
	}
	refused("open a pidfd of web", err)
	refused("truncate a file outside its folders", unix.Truncate(in.Victim, 0))
	refused("write outside its folders", os.WriteFile(in.Outside, []byte("x"), 0o600))
	refused("connect to a Unix socket outside its folders", dial("unix", in.Stream))
	refused("send to a datagram socket outside its folders", sendUnixgram(in.Datagram))
	refused("connect to an abstract socket", dial("unix", "@"+in.Abstract))
	if in.Docker != "" {
		refused("connect to the Docker socket", dial("unix", in.Docker))
	}
	if in.DBus != "" {
		refused("connect to the D-Bus session bus", dial("unix", in.DBus))
	}
	if _, err := os.Stat("/dev/log"); err == nil {
		refused("send to /dev/log", sendUnixgram("/dev/log"))
	}
	tool := filepath.Join(workspace, "tool")
	err = copyFile("/bin/true", tool)
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

func rawSocket() error {
	for _, f := range [][3]int{{unix.AF_PACKET, unix.SOCK_RAW, 0}, {unix.AF_INET, unix.SOCK_RAW, unix.IPPROTO_ICMP}, {unix.AF_INET, unix.SOCK_DGRAM, unix.IPPROTO_ICMP}} {
		if fd, err := unix.Socket(f[0], f[1], f[2]); err == nil {
			unix.Close(fd)
			return nil
		}
	}
	return fmt.Errorf("refused")
}

func newRunnerTargets(t *testing.T) runnerTargets {
	t.Helper()
	base := t.TempDir()
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
		Victim: filepath.Join(other, "victim"), Parent: os.Getpid(), ParentAddr: uintptr(unsafe.Pointer(unsafe.StringData(webSecret)))}
	for p, body := range map[string]string{in.Secret: "planted-secret-key", filepath.Join(home, ".bash_history"): "planted", in.Victim: "keep me"} {
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
	in.Abstract = fmt.Sprintf("openneko-runner-probe-%d", os.Getpid())
	if acceptAll(t, "unix", "@"+in.Abstract) == nil {
		t.Fatal("cannot listen on an abstract socket")
	}
	if host, ok := strings.CutPrefix(os.Getenv("DOCKER_HOST"), "unix://"); ok {
		in.Docker = host
	} else if _, err := os.Stat("/var/run/docker.sock"); err == nil {
		in.Docker = "/var/run/docker.sock"
	}
	if bus := fmt.Sprintf("/run/user/%d/bus", os.Getuid()); fileExists(bus) {
		in.DBus = bus
	}
	return runnerTargets{in: in, workspace: workspace, tmp: tmp}
}

// TestConfinedRunner runs the checks of the CONF0 probe, plus truncate,
// signal and pidfd checks, inside a real agent runner in a one-account
// layout: the test process stands for serve and web, with the same uid.
func TestConfinedRunner(t *testing.T) {
	st := requireRunnerConfinement(t)
	tg := newRunnerTargets(t)
	serveFakeDoor(t)
	_ = unix.Prctl(unix.PR_SET_PTRACER, unix.PR_SET_PTRACER_ANY, 0, 0, 0)
	t.Cleanup(func() { _ = unix.Prctl(unix.PR_SET_PTRACER, 0, 0, 0, 0) })

	control, msg := runRunnerProbe(t, tg, "control")
	if control.Refused == nil {
		t.Fatalf("control: the runner gave no result: %s", msg)
	}
	for what, refused := range control.Refused {
		if refused && !runnerControlFree[what] {
			t.Errorf("control: with no confinement the runner could not %s, so the check proves nothing", what)
		}
	}
	abis := []int{0}
	if st.ABI > 1 {
		abis = append(abis, 1)
	}
	for _, abi := range abis {
		capped := tg
		capped.in.ABI = abi
		got, msg := runRunnerProbe(t, capped, "full")
		name := fmt.Sprintf("ABI %d", st.ABI)
		if abi > 0 {
			name = fmt.Sprintf("ABI capped at %d", abi)
		}
		if got.Refused == nil {
			if strings.Contains(msg, "this build uses cgo") && os.Getenv("OPENNEKO_TEST_REQUIRE_CONFINE") == "" {
				t.Logf("%s needs a CGO_ENABLED=0 build, as the release is: %s", name, msg)
				continue
			}
			t.Fatalf("%s: the confined runner gave no result: %s", name, msg)
		}
		if len(got.Refused) != len(control.Refused) || len(got.Refused) < 26 {
			t.Fatalf("%s: the runner ran %d checks confined and %d unconfined", name, len(got.Refused), len(control.Refused))
		}
		for what, refused := range got.Refused {
			if !refused {
				t.Errorf("%s: the confined runner could %s", name, what)
			}
		}
		for what, ok := range got.Allowed {
			if !ok {
				t.Errorf("%s: the confined runner could not %s %v", name, what, got.Notes)
			}
		}
		if fileExists(tg.in.Outside) {
			t.Errorf("%s: the confined runner wrote outside its folders", name)
		}
	}

	noSeccomp, _ := runRunnerProbe(t, tg, "landlock")
	for _, what := range []string{"connect to a Unix socket outside its folders", "send to a datagram socket outside its folders", "UDP to loopback", "start a child process"} {
		if noSeccomp.Refused[what] {
			t.Errorf("with Landlock only, the runner still could not %s: the check does not prove the seccomp filter", what)
		}
	}
	noLandlock, _ := runRunnerProbe(t, tg, "seccomp")
	for _, what := range []string{"read the planted secret key", "write outside its folders", "read web's /proc/<pid>/mem"} {
		if noLandlock.Refused[what] {
			t.Errorf("with seccomp only, the runner still could not %s: the check does not prove Landlock", what)
		}
	}
}

func TestRunnerRefusesATurnWhenItCannotConfine(t *testing.T) {
	tg := newRunnerTargets(t)
	tg.workspace = ""
	_, msg := runRunnerProbe(t, tg, "full")
	if !strings.Contains(msg, "could not confine itself, so the turn did not run") {
		t.Fatalf("a runner with no folder to keep ran the turn: %q", msg)
	}
}

func TestTurnsAreOffWhenRunnersCannotBeConfined(t *testing.T) {
	t.Setenv(serveConfineEnv, "")
	kernelLacks := confine.State{Mechanism: "landlock+seccomp", Reason: "Linux 4.18.0 has no Landlock: use kernel 5.13 or later with landlock in the lsm= boot list"}
	defer func(old func() confine.State) { runnerAvailable = old }(runnerAvailable)
	runnerAvailable = func() confine.State { return kernelLacks }

	var log strings.Builder
	st, confineIt, off := runnerConfinement(&log, false)
	want := "agent confinement: off. Linux 4.18.0 has no Landlock: use kernel 5.13 or later with landlock in the lsm= boot list, so agent turns are off"
	if off != want || confineIt || st.Confined || !strings.Contains(log.String(), want) {
		t.Fatalf("production: off=%q confine=%v log=%q", off, confineIt, log.String())
	}
	if _, _, off := runnerConfinement(io.Discard, true); off != "" {
		t.Fatalf("development mode turned turns off: %q", off)
	}
	runnerAvailable = func() confine.State { return confine.State{Reason: "confinement on darwin is not built yet"} }
	if _, confineIt, off := runnerConfinement(io.Discard, false); off != "" || confineIt {
		t.Fatalf("a system with no runner confinement built yet: off=%q confine=%v", off, confineIt)
	}

	runnerAvailable = func() confine.State { return kernelLacks }
	sock := filepath.Join(t.TempDir(), "serve.sock")
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	t.Setenv("OPENNEKO_ENV", "production")
	cleanServeEnv(t)
	done, err := serveAgentTurns(ctx, io.Discard, sock, 1, sandbox.Config{}, confine.State{Mechanism: "landlock", ABI: 8, Confined: true})
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
		t.Fatalf("a turn on a host that cannot confine its runner: %+v, %v", exit, err)
	}
	if _, _, err := checkServeSocket(context.Background(), sock); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("release-check passed a serve that refuses turns: %v", err)
	}
}

func TestDoctorNamesTheConfinementAndTheModules(t *testing.T) {
	defer func(old func() confine.State) { runnerAvailable = old }(runnerAvailable)
	runnerAvailable = func() confine.State {
		return confine.State{Mechanism: "landlock+seccomp", Reason: "the container engine blocks Landlock (Linux 6.8.0 has it): use an engine whose default seccomp profile allows the landlock calls (INSTALL.md, \"Agent confinement\")"}
	}
	var out strings.Builder
	if !confinementDoctorLines(&out) {
		t.Fatal("doctor passed a host that cannot confine its agent runners")
	}
	for _, want := range []string{"kernel security modules: ", "agent confinement: off. the container engine blocks Landlock", "so agent turns are off"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("doctor does not say %q:\n%s", want, out.String())
		}
	}
	runnerAvailable = confine.RunnerAvailable
	out.Reset()
	if st := confine.RunnerAvailable(); st.Confined && (confinementDoctorLines(&out) || !strings.Contains(out.String(), "agent confinement: landlock+seccomp ABI")) {
		t.Errorf("doctor on this host:\n%s", out.String())
	}
}
