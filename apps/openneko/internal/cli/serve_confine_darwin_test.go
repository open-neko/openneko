package cli

import (
	"bufio"
	"bytes"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/open-neko/neko/apps/openneko/internal/confine"
	"github.com/open-neko/neko/apps/openneko/internal/testsock"
)

const (
	networkProbeEnv = "OPENNEKO_TEST_NETWORK_PROBE"
	sleeperEnv      = "OPENNEKO_TEST_SLEEPER"
)

type networkProbeIn struct {
	Loosen, Mode                              string
	Secret, Login, Credentials, Home, RunFile string
	Files, Index, Subs, GraphJin, Check       string
	Outside, Stream, TCP, Chain, Source       string
	Parent, Web                               int
}

// networkLoosened names, for each rule added to the profile, the checks that
// the rule must open.
var networkLoosened = map[string][]string{
	"(allow file-read*)": {"read the planted secret key", "read the main database login", "list the config folder", "list the home folder",
		"read the serve credentials", "read an agent run file", "let a child it starts read the planted secret key"},
	"(allow file-write*)":                     {"write outside its folders"},
	"(allow process-exec) (allow file-read*)": {"run another program", "run a file it wrote"},
	"(allow signal)":                          {"signal web (kill 0)"},
	"(allow network*)":                        {"connect to a Unix socket outside its list"},
}

// TestServeNetworkSleeper is a child that the network probe starts: it tries
// the planted secret, says what it got, and waits to be stopped.
func TestServeNetworkSleeper(t *testing.T) {
	secret := os.Getenv(sleeperEnv)
	if secret == "" {
		t.Skip("helper process")
	}
	if _, err := os.ReadFile(secret); err != nil {
		fmt.Println("refused")
	} else {
		fmt.Println("read")
	}
	time.Sleep(time.Minute)
}

// TestServeNetworkProbeChild is the network part of serve for
// TestConfinedServeNetworkPart: serve's own spec and code, then the checks.
func TestServeNetworkProbeChild(t *testing.T) {
	raw := os.Getenv(networkProbeEnv)
	if raw == "" {
		t.Skip("helper process")
	}
	var in networkProbeIn
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		t.Fatal(err)
	}
	confine.SetForTest(in.Mode == "nosandbox", in.Loosen)
	f := serveFolders{files: in.Files, index: in.Index, subsDir: in.Subs, gjConfig: in.GraphJin, checkSocket: in.Check}
	var log bytes.Buffer
	st, err := confineServe(&log, f, in.Mode == "control")
	out := probeResult{Refused: map[string]bool{}, Allowed: map[string]bool{}, Errors: map[string]string{}}
	out.Notes = append(out.Notes, strings.TrimSpace(log.String()), fmt.Sprintf("state %s, err %v", st, err))
	if err != nil {
		_ = json.NewEncoder(os.Stdout).Encode(out)
		return
	}
	refused := func(what string, err error) {
		out.Refused[what] = err != nil
		if err != nil {
			out.Errors[what] = err.Error()
		}
	}
	allowed := func(what string, err error) {
		out.Allowed[what] = err == nil
		if err != nil {
			out.Errors[what] = err.Error()
		}
	}
	for what, p := range map[string]string{"read the planted secret key": in.Secret, "read the main database login": in.Login,
		"read the serve credentials": in.Credentials, "read an agent run file": in.RunFile} {
		_, err := os.ReadFile(p)
		refused(what, err)
	}
	_, err = os.ReadDir(filepath.Dir(in.Secret))
	refused("list the config folder", err)
	_, err = os.ReadDir(in.Home)
	refused("list the home folder", err)
	refused("write outside its folders", os.WriteFile(in.Outside, []byte("x"), 0o600))
	msg, err := exec.Command("/bin/sh", "-c", "echo ran").Output()
	if err == nil && !strings.Contains(string(msg), "ran") {
		err = errors.New("no output")
	}
	refused("run another program", err)
	tool := filepath.Join(in.Files, "tool")
	if err = copyFile(in.Source, tool); err == nil {
		err = runOutput(exec.Command(tool))
	}
	refused("run a file it wrote", err)
	refused("signal web (kill 0)", unix.Kill(in.Parent, 0))
	readWebProcess(in.Web, refused)
	refused("connect to a Unix socket outside its list", dial("unix", in.Stream))

	kept := filepath.Join(in.Files, "kept.txt")
	err = os.WriteFile(kept, []byte("kept"), 0o600)
	if err == nil {
		_, err = os.ReadFile(kept)
	}
	allowed("write and read its files folder", err)
	allowed("connect to a TCP service of the install", dial("tcp", in.TCP))
	allowed("listen on TCP and accept", listenAndAccept("tcp", "127.0.0.1:0"))
	allowed("listen on the config check socket", listenAndAccept("unix", in.Check))
	_, err = net.LookupHost("example.com")
	allowed("resolve a name", err)
	allowed("verify a TLS certificate with the system trust store", verifyAppleChain(in.Chain))
	exe, _ := os.Executable()
	allowed("run its own program", exec.Command(exe, "-test.run=^$").Run())
	got, err := startAndStop(exe, in.Secret)
	allowed("stop a child it started", err)
	var childRead error = errors.New(got)
	if got == "read" {
		childRead = nil
	}
	refused("let a child it starts read the planted secret key", childRead)
	if got != "read" && got != "refused" {
		out.Notes = append(out.Notes, fmt.Sprintf("the child said %q", got))
	}
	_ = json.NewEncoder(os.Stdout).Encode(out)
}

func listenAndAccept(network, addr string) error {
	ln, err := net.Listen(network, addr)
	if err != nil {
		return err
	}
	defer ln.Close()
	accepted := make(chan error, 1)
	go func() {
		c, err := ln.Accept()
		if err == nil {
			c.Close()
		}
		accepted <- err
	}()
	if err := dial(network, ln.Addr().String()); err != nil {
		return err
	}
	select {
	case err := <-accepted:
		return err
	case <-time.After(5 * time.Second):
		return errors.New("no connection came in")
	}
}

func verifyAppleChain(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var certs []*x509.Certificate
	for block, rest := pem.Decode(raw); block != nil; block, rest = pem.Decode(rest) {
		c, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return err
		}
		certs = append(certs, c)
	}
	if len(certs) < 2 {
		return errors.New("the chain file holds no intermediate")
	}
	inter := x509.NewCertPool()
	for _, c := range certs[1:] {
		inter.AddCert(c)
	}
	_, err = certs[0].Verify(x509.VerifyOptions{DNSName: "www.apple.com", Intermediates: inter, CurrentTime: time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)})
	return err
}

// startAndStop starts this test binary as a child, reads whether it could
// read secret, then stops it with SIGTERM.
func startAndStop(exe, secret string) (string, error) {
	cmd := exec.Command(exe, "-test.run=^TestServeNetworkSleeper$")
	cmd.Env = append(os.Environ(), sleeperEnv+"="+secret)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	if err := cmd.Start(); err != nil {
		return "", err
	}
	line, _ := bufio.NewReader(out).ReadString('\n')
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return strings.TrimSpace(line), err
	}
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	select {
	case <-waited:
		return strings.TrimSpace(line), nil
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		<-waited
		return strings.TrimSpace(line), errors.New("the child did not stop on SIGTERM")
	}
}

type networkTargets struct {
	in   networkProbeIn
	home string
	tmp  string
}

func newNetworkTargets(t *testing.T) networkTargets {
	t.Helper()
	base := testsock.Dir(t)
	config := filepath.Join(base, "config", "openneko")
	creds := filepath.Join(base, "data", "credentials")
	home := filepath.Join(base, "home")
	runs := filepath.Join(base, "data", "agent-runs", "turn-1")
	other := filepath.Join(base, "other")
	tg := networkTargets{home: home, tmp: filepath.Join(base, "data", "tmp")}
	in := networkProbeIn{Secret: filepath.Join(config, "secret-key"), Login: filepath.Join(config, "config.json"),
		Credentials: filepath.Join(creds, "database.url"), Home: home, RunFile: filepath.Join(runs, "notes.md"),
		Files: filepath.Join(base, "data", "files"), Index: filepath.Join(base, "data", "file-index"), Subs: filepath.Join(base, "data", "subs"),
		GraphJin: filepath.Join(base, "data", "graphjin", "agentic.yml"), Check: filepath.Join(base, "data", "run", "check.sock"),
		Outside: filepath.Join(other, "planted-by-serve"), Stream: filepath.Join(other, "docker.sock"), Parent: os.Getpid(), Web: startWebStandIn(t)}
	for _, d := range []string{config, creds, home, runs, other, tg.tmp, in.Files, in.Index, in.Subs, filepath.Dir(in.GraphJin), filepath.Dir(in.Check)} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for p, body := range map[string]string{in.Secret: "planted-secret-key", in.Login: `{"pg":{"password":"planted"}}`, in.Credentials: "postgres://serve:planted@db/x",
		filepath.Join(home, ".zsh_history"): "planted", in.RunFile: "planted", in.GraphJin: "app_name: x\n"} {
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	chain, err := os.ReadFile(filepath.Join("testdata", "www.apple.com-chain.pem"))
	if err != nil {
		t.Fatal(err)
	}
	in.Source = filepath.Join(in.Files, "true-source")
	if err := copyFile("/usr/bin/true", in.Source); err != nil {
		t.Fatal(err)
	}
	in.Chain = filepath.Join(in.Subs, "www.apple.com-chain.pem")
	if err := os.WriteFile(in.Chain, chain, 0o600); err != nil {
		t.Fatal(err)
	}
	if acceptAll(t, "unix", in.Stream) == nil {
		t.Fatal("cannot listen on", in.Stream)
	}
	in.TCP = acceptAll(t, "tcp", "127.0.0.1:0").Addr().String()
	tg.in = in
	return tg
}

func runNetworkProbe(t *testing.T, tg networkTargets, mode string) (probeResult, string) {
	t.Helper()
	in := tg.in
	in.Mode = mode
	raw, _ := json.Marshal(in)
	cmd := exec.Command(os.Args[0], "-test.run=^TestServeNetworkProbeChild$", "-test.count=1")
	cmd.Env = append(os.Environ(), networkProbeEnv+"="+string(raw), "HOME="+tg.home, "TMPDIR="+tg.tmp)
	if mode == "control" {
		cmd.Env = append(cmd.Env, "OPENNEKO_ENV=development", serveConfineEnv+"=off")
	} else {
		cmd.Env = append(cmd.Env, "OPENNEKO_ENV=production", serveConfineEnv+"=")
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.Output()
	var res probeResult
	for _, line := range strings.Split(string(stdout), "\n") {
		if strings.HasPrefix(line, "{") && json.Unmarshal([]byte(line), &res) == nil {
			break
		}
	}
	_ = os.Remove(in.Outside)
	_ = os.Remove(filepath.Join(in.Files, "tool"))
	return res, fmt.Sprintf("%v %s %s", err, stdout, stderr.String())
}

// TestConfinedServeNetworkPart runs the network part of serve under its
// Seatbelt profile. Each refusal must open when one rule loosens the profile,
// and each thing the part needs (TCP, a listening socket, name lookup, TLS
// checks, its own program and its children) must still work.
func TestConfinedServeNetworkPart(t *testing.T) {
	requireRunnerConfinement(t)
	tg := newNetworkTargets(t)
	control, msg := runNetworkProbe(t, tg, "control")
	if control.Refused == nil {
		t.Fatalf("control: the probe gave no result: %s", msg)
	}
	for what, refused := range control.Refused {
		if refused {
			t.Errorf("control: with no confinement the probe could not %s, so the check proves nothing", what)
		}
	}
	got, msg := runNetworkProbe(t, tg, "full")
	if got.Refused == nil {
		t.Fatalf("the confined part gave no result: %s", msg)
	}
	for _, n := range got.Notes {
		t.Logf("confined: %s", n)
	}
	for what, e := range got.Errors {
		t.Logf("confined: %s: %s", what, e)
	}
	if len(got.Refused) != len(control.Refused) || len(got.Refused) < 13 {
		t.Fatalf("the probe ran %d checks confined and %d unconfined", len(got.Refused), len(control.Refused))
	}
	checkProcargsGap(t, "the confined network part", confine.Available(), control, got)
	for what, refused := range got.Refused {
		if !refused {
			t.Errorf("the confined network part could %s", what)
		}
	}
	for what, ok := range got.Allowed {
		if !ok && control.Allowed[what] {
			t.Errorf("the confined network part could not %s: %s", what, got.Errors[what])
		} else if !ok {
			t.Logf("neither run could %s here", what)
		}
	}
	if len(got.Allowed) < 8 {
		t.Fatalf("the probe ran %d allowed checks", len(got.Allowed))
	}
	if fileExists(tg.in.Outside) {
		t.Error("the confined network part wrote outside its folders")
	}

	proved := map[string]bool{}
	for rule, opens := range networkLoosened {
		loose := tg
		loose.in.Loosen = rule
		res, msg := runNetworkProbe(t, loose, "full")
		if res.Refused == nil {
			t.Errorf("with %s the probe gave no result: %s", rule, msg)
			continue
		}
		for _, what := range opens {
			proved[what] = true
			if res.Refused[what] {
				t.Errorf("with %s the network part still could not %s: the check does not prove that rule: %s", rule, what, res.Errors[what])
			}
		}
	}
	for what := range control.Refused {
		if !proved[what] {
			t.Errorf("no loosened profile proves the check %q", what)
		}
	}
}

// TestServeNetworkPartFailsClosed: a profile that macOS refuses stops the
// network part in production; a Mac with no sandbox_init runs it unconfined
// with a warning, and release-check then fails (TestReleaseCheckServeSocket).
func TestServeNetworkPartFailsClosed(t *testing.T) {
	requireRunnerConfinement(t)
	tg := newNetworkTargets(t)
	tg.in.Loosen = "(allow no-such-operation)"
	res, msg := runNetworkProbe(t, tg, "full")
	if res.Refused != nil && len(res.Refused) > 0 {
		t.Fatalf("the network part ran its work under a refused profile: %s", msg)
	}
	if !strings.Contains(msg, "the network part of openneko serve cannot confine itself") {
		t.Fatalf("a refused profile: %s", msg)
	}
	tg.in.Loosen = ""
	_, msg = runNetworkProbe(t, tg, "nosandbox")
	if !strings.Contains(msg, "warning: the network part of openneko serve runs unconfined") || !strings.Contains(msg, "has no sandbox_init") {
		t.Fatalf("a Mac with no sandbox_init: %s", msg)
	}
}
