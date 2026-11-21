package cli

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/open-neko/neko/apps/openneko/internal/confine"
	"github.com/open-neko/neko/apps/openneko/internal/testsock"
	"github.com/open-neko/openneko/apps/ax-harness/harness"
	"github.com/open-neko/openneko/apps/ax-harness/keydoor"
	"github.com/open-neko/openneko/apps/ax-harness/serve"
)

const (
	winProbeEnv = "OPENNEKO_TEST_WIN_PROBE"
	winTurnEnv  = "OPENNEKO_TEST_WIN_TURN"
)

type winProbeIn struct {
	Secret, Home, AppLocal, Outside string
	TCP, TCP6, UDP, Token           string
	Pipe, Socket, AgentSocket       string
	Parent                          int
	ParentAddr                      uintptr
}

type winProbeResult struct {
	Notes   []string        `json:"notes"`
	Refused map[string]bool `json:"refused"`
	Allowed map[string]bool `json:"allowed"`
	Limited map[string]bool `json:"limited"`
	PID     int             `json:"pid"`
}

// winWebSecret stands for a secret in web's memory.
var winWebSecret = "planted-secret-in-web-memory"

// winControlFree lists checks that may fail with no confinement too: they
// need the internet, IPv6 or a desktop.
var winControlFree = map[string]bool{
	"TCP to 1.1.1.1:443": true, "TCP to example.com:443": true, "get a DNS answer from 1.1.1.1:53": true,
	"resolve a name": true, "TCP over IPv6 loopback": true, "open the clipboard": true,
}

// TestWinRunnerProbeChild is the agent runner of the Windows proof tests:
// the real runner code with the harness replaced by the checks.
func TestWinRunnerProbeChild(t *testing.T) {
	raw := os.Getenv(winProbeEnv)
	if raw == "" {
		t.Skip("helper process")
	}
	var in winProbeIn
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		t.Fatal(err)
	}
	serve.SetRunnerTurn(func(ctx context.Context, turn harness.Turn) (int, error) {
		env := map[string]string{}
		for _, e := range turn.Environ {
			if k, v, ok := strings.Cut(e, "="); ok {
				env[k] = v
			}
		}
		workspace, mode := env["OPENNEKO_HARNESS_WORKSPACE_DIR"], env[winTurnEnv]
		res := winProbeResult{Refused: map[string]bool{}, Allowed: map[string]bool{}, PID: os.Getpid()}
		switch {
		case mode == "hold":
			_ = os.WriteFile(filepath.Join(workspace, "pid"), []byte(strconv.Itoa(os.Getpid())), 0o600)
			for range 900 {
				if _, err := os.Stat(filepath.Join(workspace, "release")); err == nil {
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
		case strings.HasPrefix(mode, "sibling:"):
			pidText, dir, _ := strings.Cut(strings.TrimPrefix(mode, "sibling:"), ":")
			pid, _ := strconv.Atoi(pidText)
			res.Refused["open the other runner with PROCESS_QUERY_LIMITED_INFORMATION"] = openProcess(pid, windows.PROCESS_QUERY_LIMITED_INFORMATION) != nil
			res.Refused["open the other runner with PROCESS_VM_READ"] = openProcess(pid, windows.PROCESS_VM_READ) != nil
			_, err := os.ReadDir(dir)
			res.Refused["list the other turn's workspace"] = err != nil
			_, err = os.ReadFile(filepath.Join(dir, "pid"))
			res.Refused["read a file of the other turn"] = err != nil
		case strings.HasPrefix(mode, "read:"):
			_, err := os.ReadFile(strings.TrimPrefix(mode, "read:"))
			res.Refused["read the planted secret key through a link in its workspace"] = err != nil
			if err != nil {
				res.Notes = append(res.Notes, err.Error())
			}
		default:
			res = winRunnerChecks(ctx, in, workspace)
		}
		b, _ := json.Marshal(res)
		_, err := turn.Out.Write(append(b, '\n'))
		return 0, err
	})
	if err := serve.RunRunner(context.Background(), confineRunner); err != nil {
		t.Fatal(err)
	}
}

func winRunnerChecks(ctx context.Context, in winProbeIn, workspace string) winProbeResult {
	out := winProbeResult{Refused: map[string]bool{}, Allowed: map[string]bool{}, PID: os.Getpid()}
	refused := func(what string, err error) {
		out.Refused[what] = err != nil
		if err != nil {
			out.Notes = append(out.Notes, fmt.Sprintf("%s: %v", what, err))
		}
	}
	_, err := os.ReadFile(in.Secret)
	refused("read the planted secret key", err)
	_, err = os.ReadDir(filepath.Dir(in.Secret))
	refused("list the config folder", err)
	_, err = os.ReadDir(in.Home)
	refused("list the home folder", err)
	_, err = os.ReadDir(in.AppLocal)
	refused("list AppData\\Local", err)
	refused("write outside its folders", os.WriteFile(in.Outside, []byte("x"), 0o600))
	refused("TCP to web's loopback listener", winDial("tcp", in.TCP))
	if in.TCP6 != "" {
		refused("TCP over IPv6 loopback", winDial("tcp", in.TCP6))
	}
	refused("TCP to 1.1.1.1:443", winDial("tcp", "1.1.1.1:443"))
	refused("TCP to example.com:443", winDial("tcp", "example.com:443"))
	lctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	_, err = net.DefaultResolver.LookupHost(lctx, "example.com")
	cancel()
	refused("resolve a name", err)
	refused("get a DNS answer from 1.1.1.1:53", dnsAnswer("1.1.1.1:53"))
	if err := sendUDP(in.UDP, in.Token); err != nil {
		out.Notes = append(out.Notes, "UDP to loopback: "+err.Error())
	}
	msg, err := exec.Command(filepath.Join(os.Getenv("SYSTEMROOT"), "System32", "cmd.exe"), "/c", "echo ran").Output()
	if err == nil && !strings.Contains(string(msg), "ran") {
		err = errors.New("no output")
	}
	refused("run another program", err)
	refused("open web with PROCESS_QUERY_LIMITED_INFORMATION", openProcess(in.Parent, windows.PROCESS_QUERY_LIMITED_INFORMATION))
	refused("read web's memory", readMemory(in.Parent, in.ParentAddr))
	refused("connect to a Unix socket outside its folders", winDial("unix", in.Socket))
	refused("connect to serve's agent socket", winDial("unix", in.AgentSocket))
	refused("open a named pipe of web", openPipe(in.Pipe))
	refused("open the clipboard", openClipboard())

	made := filepath.Join(workspace, "made.txt")
	err = os.WriteFile(made, []byte("kept"), 0o600)
	if err == nil {
		var b []byte
		b, err = os.ReadFile(made)
		if err == nil && string(b) != "kept" {
			err = fmt.Errorf("read %q", b)
		}
	}
	out.Allowed["write and read its workspace"] = err == nil
	if err != nil {
		out.Notes = append(out.Notes, "workspace: "+err.Error())
	}
	refused("open a file it made to every app container", openToAllPackages(made))
	out.Allowed["reach the key door through the broker"] = winKeyDoorAnswers(&out)
	out.Limited = jobLimits()
	return out
}

func winDial(network, addr string) error {
	c, err := net.DialTimeout(network, addr, 3*time.Second)
	if err == nil {
		c.Close()
	}
	return err
}

func sendUDP(addr, token string) error {
	c, err := net.DialTimeout("udp", addr, 2*time.Second)
	if err != nil {
		return err
	}
	defer c.Close()
	_, err = c.Write([]byte(token))
	return err
}

// dnsAnswer asks addr for the A record of example.com and waits for any
// answer: an answer proves that a packet left the host and came back.
func dnsAnswer(addr string) error {
	c, err := net.DialTimeout("udp", addr, 2*time.Second)
	if err != nil {
		return err
	}
	defer c.Close()
	q := []byte{0x4f, 0x4e, 1, 0, 0, 1, 0, 0, 0, 0, 0, 0}
	for _, label := range []string{"example", "com"} {
		q = append(q, byte(len(label)))
		q = append(q, label...)
	}
	q = append(q, 0, 0, 1, 0, 1)
	if _, err := c.Write(q); err != nil {
		return err
	}
	_ = c.SetReadDeadline(time.Now().Add(4 * time.Second))
	buf := make([]byte, 512)
	n, err := c.Read(buf)
	if err != nil {
		return err
	}
	if n < 12 || binary.BigEndian.Uint16(buf) != 0x4f4e {
		return fmt.Errorf("no DNS answer in %d bytes", n)
	}
	return nil
}

func openProcess(pid int, access uint32) error {
	h, err := windows.OpenProcess(access, false, uint32(pid))
	if err != nil {
		return err
	}
	return windows.CloseHandle(h)
}

func readMemory(pid int, addr uintptr) error {
	h, err := windows.OpenProcess(windows.PROCESS_VM_READ, false, uint32(pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	got := make([]byte, len(winWebSecret))
	var n uintptr
	if err := windows.ReadProcessMemory(h, addr, &got[0], uintptr(len(got)), &n); err != nil {
		return err
	}
	if string(got) != winWebSecret {
		return fmt.Errorf("read %q, not the secret", got)
	}
	return nil
}

func openPipe(name string) error {
	p, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return err
	}
	return windows.CloseHandle(h)
}

var (
	user32            = windows.NewLazySystemDLL("user32.dll")
	procOpenClipboard = user32.NewProc("OpenClipboard")
	procCloseClip     = user32.NewProc("CloseClipboard")
)

func openClipboard() error {
	if r, _, err := procOpenClipboard.Call(0); r == 0 {
		return err
	}
	procCloseClip.Call()
	return nil
}

// openToAllPackages adds ALL APPLICATION PACKAGES to the access list of a
// file the runner made, so a runner of another turn could read it.
func openToAllPackages(path string) error {
	all, err := windows.StringToSid("S-1-15-2-1")
	if err != nil {
		return err
	}
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	old, _, err := sd.DACL()
	if err != nil {
		return err
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{AccessPermissions: windows.GENERIC_READ, AccessMode: windows.GRANT_ACCESS,
		Trustee: windows.TRUSTEE{TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeType: windows.TRUSTEE_IS_WELL_KNOWN_GROUP, TrusteeValue: windows.TrusteeValueFromSID(all)}}}, old)
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}

func winKeyDoorAnswers(out *winProbeResult) bool {
	client, err := keydoor.Client()
	if err == nil {
		var res *http.Response
		if res, err = client.Get("http://door/ping"); err == nil {
			body, _ := io.ReadAll(res.Body)
			res.Body.Close()
			if string(body) != "pong" {
				err = fmt.Errorf("the door answered %q", body)
			}
		}
	}
	if err != nil {
		out.Notes = append(out.Notes, "key door: "+err.Error())
	}
	return err == nil
}

// jobLimits reads the job object that holds this process.
func jobLimits() map[string]bool {
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	var n uint32
	err := windows.QueryInformationJobObject(0, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), &n)
	var ui windows.JOBOBJECT_BASIC_UI_RESTRICTIONS
	if err == nil {
		err = windows.QueryInformationJobObject(0, windows.JobObjectBasicUIRestrictions, uintptr(unsafe.Pointer(&ui)), uint32(unsafe.Sizeof(ui)), &n)
	}
	flags := info.BasicLimitInformation.LimitFlags
	return map[string]bool{
		"one process":                     err == nil && flags&windows.JOB_OBJECT_LIMIT_ACTIVE_PROCESS != 0 && info.BasicLimitInformation.ActiveProcessLimit == 1,
		"killed when serve ends":          err == nil && flags&windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE != 0,
		"memory capped at 8 GB":           err == nil && flags&windows.JOB_OBJECT_LIMIT_PROCESS_MEMORY != 0 && info.ProcessMemoryLimit == 8<<30,
		"CPU time capped at the deadline": err == nil && flags&windows.JOB_OBJECT_LIMIT_PROCESS_TIME != 0 && info.BasicLimitInformation.PerProcessUserTimeLimit <= 31*60*10_000_000,
		"no clipboard, desktop or atoms":  err == nil && ui.UIRestrictionsClass&confineUIRestrictions == confineUIRestrictions,
	}
}

const confineUIRestrictions = windows.JOB_OBJECT_UILIMIT_READCLIPBOARD | windows.JOB_OBJECT_UILIMIT_WRITECLIPBOARD | windows.JOB_OBJECT_UILIMIT_DESKTOP |
	windows.JOB_OBJECT_UILIMIT_GLOBALATOMS | windows.JOB_OBJECT_UILIMIT_HANDLES

// winTargets are the things outside the runner that the checks try to
// reach. The test makes its own, so the control proves each check.
type winTargets struct {
	in        winProbeIn
	runs      string
	workspace string
	received  func(token string) bool
}

func newWinTargets(t *testing.T) winTargets {
	t.Helper()
	base := t.TempDir()
	config := filepath.Join(base, "config", "openneko")
	other := filepath.Join(base, "other")
	runs := filepath.Join(base, "agent-runs")
	workspace := filepath.Join(runs, "turn-1")
	for _, d := range []string{config, other, workspace} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	home, _ := os.UserHomeDir()
	in := winProbeIn{Secret: filepath.Join(config, "secret-key"), Home: home, AppLocal: filepath.Join(home, "AppData", "Local"),
		Outside: filepath.Join(other, "planted-by-runner"), Parent: os.Getpid(), ParentAddr: uintptr(unsafe.Pointer(unsafe.StringData(winWebSecret)))}
	if err := os.WriteFile(in.Secret, []byte("planted-secret-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	in.TCP = winAcceptAll(t, "tcp", "127.0.0.1:0")
	in.TCP6 = winAcceptAll(t, "tcp", "[::1]:0")
	sock := filepath.Join(testsock.Dir(t), "web.sock")
	in.Socket = winAcceptAll(t, "unix", sock)
	in.Pipe = fmt.Sprintf(`\\.\pipe\openneko-probe-%d`, os.Getpid())

	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	in.UDP = pc.LocalAddr().String()
	var mu sync.Mutex
	got := map[string]bool{}
	go func() {
		buf := make([]byte, 256)
		for {
			n, _, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			mu.Lock()
			got[string(buf[:n])] = true
			mu.Unlock()
		}
	}()
	received := func(token string) bool {
		time.Sleep(2 * time.Second)
		mu.Lock()
		defer mu.Unlock()
		return got[token]
	}
	return winTargets{in: in, runs: runs, workspace: workspace, received: received}
}

func winAcceptAll(t *testing.T, network, addr string) string {
	t.Helper()
	ln, err := net.Listen(network, addr)
	if err != nil {
		return ""
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	return ln.Addr().String()
}

// servePipe holds one instance of the named pipe with its default access
// list, as a pipe of web would have, while a turn runs.
func servePipe(t *testing.T, name string) func() {
	t.Helper()
	p, err := windows.UTF16PtrFromString(name)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateNamedPipe(p, windows.PIPE_ACCESS_DUPLEX, windows.PIPE_TYPE_BYTE|windows.PIPE_WAIT, windows.PIPE_UNLIMITED_INSTANCES, 512, 512, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	return func() { _ = windows.CloseHandle(h) }
}

func winFakeDoor(t *testing.T) {
	t.Helper()
	door := filepath.Join(testsock.Dir(t), "keydoor.sock")
	ln, err := net.Listen("unix", door)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "pong") }),
		ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	keydoor.SetSocket(door)
	t.Cleanup(func() { keydoor.SetSocket("") })
}

type winServeMode int

const (
	winControl winServeMode = iota
	winConfined
	winNoContainer
)

// startWinProbeServe starts a serve that runs up to turns runners of the
// test binary at once, and returns its socket. winControl starts them with
// no confinement; winNoContainer asks them to confine themselves but starts
// them outside an app container.
func startWinProbeServe(t *testing.T, tg *winTargets, mode winServeMode, turns int) string {
	t.Helper()
	sock := filepath.Join(testsock.Dir(t), "serve.sock")
	tg.in.AgentSocket = sock
	raw, _ := json.Marshal(tg.in)
	ln, err := serve.Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &serve.Server{Getenv: os.Getenv, MaxTurns: turns, ConfineRunner: mode != winControl,
		Runner:    []string{os.Args[0], "-test.run=^TestWinRunnerProbeChild$", "-test.count=1"},
		RunnerEnv: []string{"PATH=" + os.Getenv("PATH"), "SYSTEMROOT=" + os.Getenv("SYSTEMROOT"), winProbeEnv + "=" + string(raw)}}
	switch mode {
	case winControl:
		srv.RunUnconfinedForTest()
	case winConfined:
		srv.StartRunner = runnerStarter(io.Discard, tg.runs)
	case winNoContainer:
		srv.StartRunner = func(r serve.RunnerStart) (serve.RunnerProcess, error) {
			p, err := os.StartProcess(r.Args[0], r.Args, &os.ProcAttr{Env: r.Env, Files: []*os.File{nil, r.Stderr, r.Stderr}})
			if err != nil {
				return nil, err
			}
			return osRunner{p}, nil
		}
	}
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, ln) }()
	t.Cleanup(func() { stop(); <-done })
	return sock
}

type osRunner struct{ p *os.Process }

func (r osRunner) Pid() int     { return r.p.Pid }
func (r osRunner) Kill() error  { _ = r.p.Kill(); return nil }
func (r osRunner) Close() error { return nil }
func (r osRunner) Wait() error {
	st, err := r.p.Wait()
	if err == nil && !st.Success() {
		err = fmt.Errorf("exit status %d", st.ExitCode())
	}
	return err
}

// winTurn sends one turn and returns what the runner's checks found and
// the turn's error.
func winTurn(sock, workspace string, turnMode string) (winProbeResult, string, error) {
	conn, err := net.Dial("unix", sock)
	if err != nil {
		return winProbeResult{}, "", err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(150 * time.Second))
	environ := []string{"OPENNEKO_HARNESS_WORKSPACE_DIR=" + workspace, "OPENNEKO_MCP_ORG_ID=org-1", "OPENNEKO_MCP_THREAD_ID=thread-1"}
	if turnMode != "" {
		environ = append(environ, winTurnEnv+"="+turnMode)
	}
	if err := json.NewEncoder(conn).Encode(serve.Frame{Type: "start", Spec: json.RawMessage(`{}`), Environ: environ}); err != nil {
		return winProbeResult{}, "", err
	}
	var res winProbeResult
	dec := json.NewDecoder(conn)
	for {
		var f serve.Frame
		if err := dec.Decode(&f); err != nil {
			return res, "", fmt.Errorf("the turn ended with no exit frame: %w", err)
		}
		switch f.Type {
		case "event":
			_ = json.Unmarshal(f.Event, &res)
		case "exit":
			return res, f.Error, nil
		}
	}
}

func requireWinRunnerConfinement(t *testing.T) {
	t.Helper()
	st := confine.RunnerAvailable()
	t.Logf("%s, %s, gaps %v", confine.Kernel(), st, st.Gaps)
	if !st.Confined {
		if os.Getenv("CI") != "" || os.Getenv("OPENNEKO_TEST_REQUIRE_CONFINE") != "" {
			t.Fatalf("this host cannot prove the agent runner's confinement: %s", st.Reason)
		}
		t.Skip(st.Reason)
	}
}

// probeRun runs the checks in one runner, with a fresh pipe and UDP token.
func probeRun(t *testing.T, tg winTargets, mode winServeMode, name string) (winProbeResult, bool) {
	t.Helper()
	tg.in.Token = fmt.Sprintf("openneko-udp-%s-%d", name, time.Now().UnixNano())
	closePipe := servePipe(t, tg.in.Pipe)
	defer closePipe()
	sock := startWinProbeServe(t, &tg, mode, 1)
	res, msg, err := winTurn(sock, tg.workspace, "")
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if res.Refused == nil {
		t.Fatalf("%s: the runner gave no result: %s", name, msg)
	}
	_ = os.Remove(tg.in.Outside)
	return res, tg.received(tg.in.Token)
}

// TestConfinedRunnerWindows proves each control of the app container: with
// no container the same checks reach everything.
func TestConfinedRunnerWindows(t *testing.T) {
	requireWinRunnerConfinement(t)
	tg := newWinTargets(t)
	winFakeDoor(t)

	control, controlUDP := probeRun(t, tg, winControl, "control")
	for what, refused := range control.Refused {
		if refused && !winControlFree[what] {
			t.Errorf("control: with no confinement the runner could not %s, so the check proves nothing %v", what, control.Notes)
		}
	}
	if !controlUDP {
		t.Errorf("control: the UDP receiver on loopback got nothing from an unconfined runner, so the UDP check proves nothing")
	}
	if control.Refused["get a DNS answer from 1.1.1.1:53"] {
		t.Logf("control: no DNS answer from 1.1.1.1:53 on this host, so the confined check of it proves less: %v", control.Notes)
	}

	got, gotUDP := probeRun(t, tg, winConfined, "confined")
	t.Logf("confined runner notes: %v", got.Notes)
	if len(got.Refused) != len(control.Refused) || len(got.Refused) < 20 {
		t.Fatalf("the runner ran %d checks confined and %d unconfined", len(got.Refused), len(control.Refused))
	}
	for what, refused := range got.Refused {
		if !refused {
			t.Errorf("the confined runner could %s", what)
		}
	}
	if gotUDP {
		t.Errorf("the confined runner sent a UDP packet that the receiver on loopback got")
	}
	for what, ok := range got.Allowed {
		if !ok {
			t.Errorf("the confined runner could not %s %v", what, got.Notes)
		}
	}
	if _, err := os.Stat(tg.in.Outside); err == nil {
		t.Errorf("the confined runner wrote outside its folders")
	}
	if len(got.Limited) < 5 {
		t.Errorf("the runner checked %d limits", len(got.Limited))
	}
	for what, held := range got.Limited {
		if !held {
			t.Errorf("the runner's job limit did not hold: %s", what)
		}
	}
	if left := waitNoContainerACEs(t, tg.workspace); len(left) > 0 {
		t.Errorf("the workspace still grants the runner's app container after the turn: %v", left)
	}
}

// TestWindowsRunnerRefusesATurnOutsideAnAppContainer: a runner that serve
// asked to confine itself and that is not in an app container runs no
// agent code.
func TestWindowsRunnerRefusesATurnOutsideAnAppContainer(t *testing.T) {
	requireWinRunnerConfinement(t)
	tg := newWinTargets(t)
	sock := startWinProbeServe(t, &tg, winNoContainer, 1)
	res, msg, err := winTurn(sock, tg.workspace, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Refused != nil || !strings.Contains(msg, "could not confine itself") || !strings.Contains(msg, "not in an app container") {
		t.Fatalf("a runner outside an app container ran the turn: %+v, %q", res, msg)
	}
}

// TestWindowsRunnersCannotReachEachOther: two turns at once run in two app
// containers, and neither can open the other or its workspace.
func TestWindowsRunnersCannotReachEachOther(t *testing.T) {
	requireWinRunnerConfinement(t)
	tg := newWinTargets(t)
	winFakeDoor(t)
	first := filepath.Join(tg.runs, "turn-a")
	second := filepath.Join(tg.runs, "turn-b")
	for _, d := range []string{first, second} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	sock := startWinProbeServe(t, &tg, winConfined, 2)
	held := make(chan string, 1)
	go func() {
		_, msg, err := winTurn(sock, first, "hold")
		if err != nil {
			msg = err.Error()
		}
		held <- msg
	}()
	var pid string
	for range 600 {
		if b, err := os.ReadFile(filepath.Join(first, "pid")); err == nil {
			pid = string(b)
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if pid == "" {
		t.Fatal("the first runner did not start")
	}
	res, msg, err := winTurn(sock, second, "sibling:"+pid+":"+first)
	_ = os.WriteFile(filepath.Join(first, "release"), nil, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Refused) != 4 {
		t.Fatalf("the second runner gave no result: %+v, %q", res, msg)
	}
	for what, refused := range res.Refused {
		if !refused {
			t.Errorf("a runner could %s", what)
		}
	}
	if msg := <-held; msg != "" {
		t.Errorf("the first turn: %s", msg)
	}
}

// TestWindowsRunnerGetsNoFolderThroughALink: a junction in a workspace,
// such as one a runner of an earlier turn made, does not carry the grant
// of the next turn to the folder it points to.
func TestWindowsRunnerGetsNoFolderThroughALink(t *testing.T) {
	requireWinRunnerConfinement(t)
	tg := newWinTargets(t)
	config := filepath.Dir(tg.in.Secret)
	link := filepath.Join(tg.workspace, "j")
	if err := makeJunction(link, config); err != nil {
		t.Fatal(err)
	}
	if _, err := os.ReadFile(filepath.Join(link, "secret-key")); err != nil {
		t.Fatalf("the test cannot read through its own junction: %v", err)
	}
	sock := startWinProbeServe(t, &tg, winConfined, 1)
	res, msg, err := winTurn(sock, tg.workspace, "read:"+filepath.Join(link, "secret-key"))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Refused) != 1 {
		t.Fatalf("the runner gave no result: %+v, %q", res, msg)
	}
	for what, refused := range res.Refused {
		if !refused {
			t.Errorf("the confined runner could %s", what)
		}
	}
	t.Logf("runner notes: %v", res.Notes)
	if left := waitNoContainerACEs(t, config); len(left) > 0 {
		t.Errorf("the config folder grants an app container: %v", left)
	}
}

// TestWindowsStarterKeepsTheTurnInsideAgentRuns: a turn whose folder is
// not in the agent runs folder gets no runner.
func TestWindowsStarterKeepsTheTurnInsideAgentRuns(t *testing.T) {
	runs := filepath.Join(t.TempDir(), "agent-runs")
	start := runnerStarter(io.Discard, runs)
	home, _ := os.UserHomeDir()
	for _, folders := range [][]string{nil, {home}, {runs}, {filepath.Join(runs, "..", "config")}, {"relative"}} {
		if _, err := start(serve.RunnerStart{Args: []string{os.Args[0]}, Folders: folders}); err == nil {
			t.Errorf("the starter took the folders %v", folders)
		}
	}
}

// TestWindowsTurnsAreOffWithNoAppContainer: on a Windows with no app
// containers, serve refuses every turn with the one line.
func TestWindowsTurnsAreOffWithNoAppContainer(t *testing.T) {
	confine.SetForTest(true)
	t.Cleanup(func() { confine.SetForTest(false) })
	_, off := runnerConfinement(io.Discard)
	if !strings.HasPrefix(off, "agent confinement: off. Windows ") || !strings.Contains(off, "has no app containers, so agent turns are off") {
		t.Fatalf("off = %q", off)
	}
	if _, err := confine.StartRunner(confine.Spec{}, []string{os.Args[0]}, nil, os.Stderr, confine.Limits{}); !errors.Is(err, confine.ErrUnsupported) {
		t.Fatalf("StartRunner on a Windows with no app containers: %v", err)
	}
}

// TestStaleRunnerContainersGo: a profile whose serve has ended goes at the
// next start; one whose serve runs stays.
func TestStaleRunnerContainersGo(t *testing.T) {
	requireWinRunnerConfinement(t)
	dead := exec.Command(filepath.Join(os.Getenv("SYSTEMROOT"), "System32", "whoami.exe"))
	if err := dead.Run(); err != nil {
		t.Fatal(err)
	}
	gone := confine.StaleProfileForTest(t, dead.Process.Pid)
	kept := confine.StaleProfileForTest(t, os.Getpid())
	if _, err := confine.RemoveStaleRunners(); err != nil {
		t.Fatal(err)
	}
	if confine.ProfileExistsForTest(gone) {
		t.Errorf("the profile of an ended serve is still there: %s", gone)
	}
	if !confine.ProfileExistsForTest(kept) {
		t.Errorf("the profile of a running serve went: %s", kept)
	}
}

// waitNoContainerACEs waits until serve has taken back the grants of the
// last turn on path, and returns those that are left.
func waitNoContainerACEs(t *testing.T, path string) []string {
	t.Helper()
	var left []string
	for range 100 {
		if left = containerACEs(t, path); len(left) == 0 {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return left
}

// containerACEs lists the app container SIDs that the access list of path
// names.
func containerACEs(t *testing.T, path string) []string {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, part := range strings.Split(sd.String(), "(") {
		if i := strings.Index(part, ";S-1-15-2-"); i >= 0 {
			out = append(out, strings.TrimSuffix(part[i+1:], ")"))
		}
	}
	return out
}

// makeJunction makes link a junction to target, as a runner may.
func makeJunction(link, target string) error {
	if err := os.Mkdir(link, 0o700); err != nil {
		return err
	}
	p, err := windows.UTF16PtrFromString(link)
	if err != nil {
		return err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	sub, _ := windows.UTF16FromString(`\??\` + target)
	print, _ := windows.UTF16FromString(target)
	subBytes, printBytes := 2*(len(sub)-1), 2*(len(print)-1)
	path := make([]byte, 0, 2*(len(sub)+len(print)))
	for _, u := range append(sub, print...) {
		path = binary.LittleEndian.AppendUint16(path, u)
	}
	buf := binary.LittleEndian.AppendUint32(nil, 0xA0000003)
	buf = binary.LittleEndian.AppendUint16(buf, uint16(8+len(path)))
	buf = binary.LittleEndian.AppendUint16(buf, 0)
	buf = binary.LittleEndian.AppendUint16(buf, 0)
	buf = binary.LittleEndian.AppendUint16(buf, uint16(subBytes))
	buf = binary.LittleEndian.AppendUint16(buf, uint16(subBytes+2))
	buf = binary.LittleEndian.AppendUint16(buf, uint16(printBytes))
	buf = append(buf, path...)
	var n uint32
	return windows.DeviceIoControl(h, 0x000900A4, &buf[0], uint32(len(buf)), nil, 0, &n, nil)
}
