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
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/open-neko/neko/apps/openneko/internal/confine"
	"github.com/open-neko/neko/apps/openneko/internal/testsock"
	"github.com/open-neko/openneko/apps/ax-harness/sandbox"
	"github.com/open-neko/openneko/apps/ax-harness/serve"
)

const peerChildEnv = "OPENNEKO_TEST_AGENT_SOCKET_PEER"

// TestAgentSocketPeerChild asks the agent socket for its status and says
// whether serve answered.
func TestAgentSocketPeerChild(t *testing.T) {
	socket := os.Getenv(peerChildEnv)
	if socket == "" {
		t.Skip("helper process")
	}
	answer := "refused"
	if conn, err := net.Dial("unix", socket); err == nil {
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		var reply serve.Frame
		if json.NewEncoder(conn).Encode(serve.Frame{Type: "status"}) == nil && json.NewDecoder(conn).Decode(&reply) == nil && reply.Type == "status" {
			answer = "answered"
		}
		conn.Close()
	}
	os.Stdout.WriteString("peer:" + answer + "\n")
}

// peerAnswer runs the helper as the child of a shell, as GraphJin runs as a
// child of the network part, and returns the shell's pid and the answer.
func peerAnswer(t *testing.T, socket string, network *atomic.Int64) string {
	t.Helper()
	cmd := exec.Command("sh", "-c", `read go; "$0" -test.run='^TestAgentSocketPeerChild$' -test.count=1; true`, os.Args[0])
	cmd.Env = append(os.Environ(), peerChildEnv+"="+socket)
	in, _ := cmd.StdinPipe()
	var out strings.Builder
	cmd.Stdout = &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	network.Store(int64(cmd.Process.Pid))
	_, _ = io.WriteString(in, "go\n")
	_ = cmd.Wait()
	for _, line := range strings.Split(out.String(), "\n") {
		if v, ok := strings.CutPrefix(line, "peer:"); ok {
			return v
		}
	}
	t.Fatalf("the helper said nothing: %q", out.String())
	return ""
}

// TestAgentSocketRefusesTheNetworkPart: a child of the network part cannot
// use the agent socket; with the check removed it can.
func TestAgentSocketRefusesTheNetworkPart(t *testing.T) {
	cleanServeEnv(t)
	var network atomic.Int64
	for _, check := range []bool{false, true} {
		agentPeerRefused = nil
		if check {
			agentPeerRefused = func(pid int) bool { return refuseNetworkPeers(int(network.Load()))(pid) }
		}
		sock := filepath.Join(testsock.Dir(t), "serve.sock")
		ctx, stop := context.WithCancel(context.Background())
		done, err := serveAgentTurns(ctx, io.Discard, sock, 1, turnHostStart{sandbox: sandbox.Config{}}, confine.State{}, confine.State{})
		if err != nil {
			t.Fatal(err)
		}
		got := peerAnswer(t, sock, &network)
		want := map[bool]string{false: "answered", true: "refused"}[check]
		if got != want {
			t.Errorf("check %v: a child of the network part was %s, want %s", check, got, want)
		}
		if check {
			if _, _, _, err := checkServeSocket(context.Background(), sock); err != nil && strings.Contains(err.Error(), "did not report") {
				t.Errorf("serve refused web as well: %v", err)
			}
		}
		stop()
		<-done
	}
	agentPeerRefused = nil
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
			child, _ = strconv.Atoi(f[0])
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
