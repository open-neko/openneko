package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/open-neko/neko/apps/openneko/internal/confine"
	"github.com/open-neko/openneko/apps/ax-harness/sandbox"
	"github.com/open-neko/openneko/apps/ax-harness/serve"
)

const (
	peerChildEnv  = "OPENNEKO_TEST_AGENT_SOCKET_PEER"
	peerResultEnv = "OPENNEKO_TEST_AGENT_SOCKET_RESULT"
	peerOrphanEnv = "OPENNEKO_TEST_AGENT_SOCKET_ORPHAN"
	standInEnv    = "OPENNEKO_TEST_SERVE_STAND_IN"
)

func agentSocketAnswers(socket string) string {
	conn, err := net.Dial("unix", socket)
	if err != nil {
		return "refused"
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	var reply serve.Frame
	if json.NewEncoder(conn).Encode(serve.Frame{Type: "status"}) == nil && json.NewDecoder(conn).Decode(&reply) == nil && reply.Type == "status" {
		return "answered"
	}
	return "refused"
}

// TestAgentSocketPeerChild asks the agent socket for its status and writes
// whether serve answered. As an orphan, it first waits until the shell that
// started it is no longer its parent.
func TestAgentSocketPeerChild(t *testing.T) {
	socket := os.Getenv(peerChildEnv)
	if socket == "" {
		t.Skip("helper process")
	}
	if shell, err := strconv.Atoi(os.Getenv(peerOrphanEnv)); err == nil {
		for deadline := time.Now().Add(10 * time.Second); os.Getppid() == shell && time.Now().Before(deadline); {
			time.Sleep(10 * time.Millisecond)
		}
	}
	_ = os.WriteFile(os.Getenv(peerResultEnv), []byte(agentSocketAnswers(socket)), 0o600)
}

// TestServeStandInChild stands for serve: it guards and serves the agent
// socket, then starts a network part that runs one peer as its child and
// one as a double-forked orphan. It prints both answers, then serves until
// its stdin closes.
func TestServeStandInChild(t *testing.T) {
	mode := os.Getenv(standInEnv)
	if mode == "" {
		t.Skip("helper process")
	}
	var refuse func(int) bool
	switch mode {
	case "guard":
		var err error
		if refuse, err = guardAgentSocket(); err != nil {
			t.Fatal(err)
		}
	case "no-subreaper":
		self := os.Getpid()
		refuse = func(pid int) bool { return pid != 0 && descendsFrom(pid, self) }
	}
	agentPeerRefused = refuse
	dir, _ := os.MkdirTemp("", "openneko-peer-")
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "serve.sock")
	ctx, stop := context.WithCancel(context.Background())
	done, err := serveAgentTurns(ctx, io.Discard, sock, 1, turnHostStart{sandbox: sandbox.Config{}}, confine.State{}, confine.State{})
	if err != nil {
		t.Fatal(err)
	}
	child, orphan := filepath.Join(dir, "child"), filepath.Join(dir, "orphan")
	network := exec.Command("sh", "-c", `"$0" -test.run='^TestAgentSocketPeerChild$' -test.count=1 >/dev/null 2>&1
OPENNEKO_TEST_AGENT_SOCKET_RESULT="$1" OPENNEKO_TEST_AGENT_SOCKET_ORPHAN=$$ "$0" -test.run='^TestAgentSocketPeerChild$' -test.count=1 >/dev/null 2>&1 &
exit 0`, os.Args[0], orphan)
	network.Env = append(os.Environ(), peerChildEnv+"="+sock, peerResultEnv+"="+child)
	if err := network.Run(); err != nil {
		t.Fatal(err)
	}
	answers := map[string]string{}
	for deadline := time.Now().Add(30 * time.Second); len(answers) < 2 && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		for name, path := range map[string]string{"child": child, "orphan": orphan} {
			if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
				answers[name] = string(b)
			}
		}
	}
	fmt.Printf("\npeers child=%s orphan=%s socket=%s\n", answers["child"], answers["orphan"], sock)
	_, _ = io.Copy(io.Discard, os.Stdin)
	stop()
	<-done
}

type standIn struct {
	child, orphan, web string
}

func runServeStandIn(t *testing.T, mode string) standIn {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestServeStandInChild$", "-test.count=1")
	cmd.Env = append(os.Environ(), standInEnv+"="+mode)
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { stdin.Close(); _ = cmd.Wait() }()
	var got standIn
	scan := bufio.NewScanner(stdout)
	for scan.Scan() {
		rest, ok := strings.CutPrefix(scan.Text(), "peers ")
		if !ok {
			continue
		}
		var socket string
		for _, f := range strings.Fields(rest) {
			k, v, _ := strings.Cut(f, "=")
			switch k {
			case "child":
				got.child = v
			case "orphan":
				got.orphan = v
			case "socket":
				socket = v
			}
		}
		got.web = agentSocketAnswers(socket)
		break
	}
	go func() { _, _ = io.Copy(io.Discard, stdout) }()
	return got
}

// TestAgentSocketRefusesServesOwnTree: a child of serve's network part and
// an orphan of it that left by a double fork cannot use the agent socket;
// web, outside serve's tree, can. Without the subreaper the orphan gets in,
// so the test proves the subreaper holds the orphan in the tree.
func TestAgentSocketRefusesServesOwnTree(t *testing.T) {
	cleanServeEnv(t)
	got := runServeStandIn(t, "guard")
	if got != (standIn{child: "refused", orphan: "refused", web: "answered"}) {
		t.Errorf("guarded serve: %+v, want the child and the orphan refused and web answered", got)
	}
	open := runServeStandIn(t, "no-subreaper")
	if open.orphan != "answered" || open.child != "refused" {
		t.Errorf("serve with no subreaper: %+v, want the orphan answered: else the test does not prove the subreaper", open)
	}
}

func TestDescendsFromFollowsTheParentLinks(t *testing.T) {
	cmd := exec.Command("sh", "-c", "sleep 30; true")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	var child int
	for range 100 {
		b, _ := os.ReadFile(fmt.Sprintf("/proc/%d/task/%d/children", cmd.Process.Pid, cmd.Process.Pid))
		if f := strings.Fields(string(b)); len(f) > 0 {
			fmt.Sscan(f[0], &child)
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if child == 0 {
		t.Fatal("the shell started no child")
	}
	for _, c := range []struct {
		pid, ancestor int
		want          bool
	}{{child, cmd.Process.Pid, true}, {child, os.Getpid(), true}, {cmd.Process.Pid, cmd.Process.Pid, true}, {os.Getpid(), cmd.Process.Pid, false}, {1, cmd.Process.Pid, false}, {0, cmd.Process.Pid, false}} {
		if got := descendsFrom(c.pid, c.ancestor); got != c.want {
			t.Errorf("descendsFrom(%d, %d) = %v, want %v", c.pid, c.ancestor, got, c.want)
		}
	}
}

// TestDescendsFromRefusesWhatItCannotFollow: a chain deeper than the walk
// and a chain that keeps changing count as serve's children; a process that
// this account cannot see does not.
func TestDescendsFromRefusesWhatItCannotFollow(t *testing.T) {
	t.Cleanup(func() { parentOf = procParent })
	parentOf = func(pid int) (int, error) { return pid + 1, nil }
	if !descendsFrom(1000, 1) {
		t.Error("a chain deeper than the walk passed")
	}
	changing := 0
	parentOf = func(pid int) (int, error) {
		if pid == 500 {
			return 0, errors.New("hidden")
		}
		return pid - 1, nil
	}
	if descendsFrom(500, 1) {
		t.Error("a link that stays broken at the same process counted as a child")
	}
	parentOf = func(pid int) (int, error) {
		changing++
		if pid == 500 {
			return 500 - changing, nil
		}
		return 0, errors.New("gone")
	}
	if !descendsFrom(500, 1) {
		t.Error("a chain that keeps changing passed")
	}
}
