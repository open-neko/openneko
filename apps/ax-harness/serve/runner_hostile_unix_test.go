//go:build !windows

package serve

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	osexec "os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// hostileRunner acts on serve's descriptors as native code would: it starts
// a child, ignores EOF and SIGTERM, and never exits on its own.
func hostileRunner(mode string) {
	signal.Ignore(syscall.SIGTERM, syscall.SIGHUP, syscall.SIGPIPE)
	link := os.NewFile(runnerLinkFD, "link")
	broker := os.NewFile(runnerBrokerFD, "broker")
	child := osexec.Command("/bin/sleep", "600")
	if err := child.Start(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	event, _ := json.Marshal(map[string]any{"pid": os.Getpid(), "child": child.Process.Pid})
	line, _ := json.Marshal(map[string]any{"type": "event", "event": json.RawMessage(event)})
	_, _ = link.Write(append(line, '\n'))
	switch mode {
	case "hostile-exit":
		_, _ = link.Write([]byte(`{"code":0,"type":"exit"}` + "\n"))
	case "hostile-broker", "paced-broker":
		answers := bufio.NewReader(broker)
		n := 0
		last := ""
		for i := range maxBrokerRequests + 100 {
			if mode == "paced-broker" && i == maxBrokerRequests {
				time.Sleep(time.Second)
			}
			if _, err := broker.Write([]byte("nothing\n")); err != nil {
				break
			}
			answer, err := answers.ReadString('\n')
			if err != nil {
				break
			}
			n++
			last = strings.TrimSpace(answer)
		}
		event, _ := json.Marshal(map[string]any{"answers": n, "last": last})
		line, _ := json.Marshal(map[string]any{"type": "event", "event": json.RawMessage(event)})
		_, _ = link.Write(append(line, '\n'))
	}
	if mode == "hostile-orphan" {
		os.Exit(0)
	}
	for {
		time.Sleep(time.Hour)
	}
}

func alive(pid int) bool {
	if syscall.Kill(pid, 0) != nil {
		return false
	}
	if runtime.GOOS == "linux" {
		stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			return false
		}
		if i := strings.LastIndexByte(string(stat), ')'); i > 0 && strings.HasPrefix(string(stat[i+1:]), " Z") {
			return false
		}
	}
	return true
}

func hostilePids(t *testing.T, c *client) (runner, child int) {
	t.Helper()
	select {
	case frame := <-c.frames:
		var pids struct{ Pid, Child int }
		if frame.Type != "event" || json.Unmarshal(frame.Event, &pids) != nil || pids.Child == 0 {
			t.Fatalf("the hostile runner sent %+v first", frame)
		}
		t.Cleanup(func() {
			_ = syscall.Kill(pids.Child, syscall.SIGKILL)
			_ = syscall.Kill(pids.Pid, syscall.SIGKILL)
		})
		return pids.Pid, pids.Child
	case <-time.After(30 * time.Second):
		t.Fatal("the hostile runner sent nothing")
	}
	return 0, 0
}

// requireDead allows the time the kernel takes to end a killed process.
func requireDead(t *testing.T, what string, pids ...int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for _, pid := range pids {
		for alive(pid) {
			if time.Now().After(deadline) {
				t.Fatalf("%s: process %d still runs", what, pid)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
}

func TestServeSendsTheExitFrameOnlyAfterTheRunnerGroupIsDead(t *testing.T) {
	server := runnerServer(t, "hostile-exit", &Server{MaxTurns: 1})
	c := dial(t, startServer(t, server), Frame{Type: "start", Spec: json.RawMessage(spec)})
	runner, child := hostilePids(t, c)
	exit, _ := waitExit(t, c)
	if exit.Code == nil || *exit.Code != 0 {
		t.Fatalf("exit = %+v", exit)
	}
	requireDead(t, "after the exit frame", runner, child)
}

func TestServeKillsARunnerThatOutlivesItsDeadline(t *testing.T) {
	old := runnerTurnGrace
	runnerTurnGrace = 0
	t.Cleanup(func() { runnerTurnGrace = old })
	server := runnerServer(t, "hostile-hold", &Server{MaxTurns: 1})
	socket := startServer(t, server)
	c := dial(t, socket, Frame{Type: "start", Spec: json.RawMessage(`{"version":1,"run_id":"run-1","timeout_ms":1000}`)})
	runner, child := hostilePids(t, c)
	exit, _ := waitExit(t, c)
	if exit.Code == nil || *exit.Code != 2 || !strings.Contains(exit.Error, "agent runner stopped") {
		t.Fatalf("exit = %+v", exit)
	}
	requireDead(t, "after the deadline", runner, child)
	freed := time.Now().Add(10 * time.Second)
	for !server.acquire() {
		if time.Now().After(freed) {
			t.Fatal("the turn slot stayed taken")
		}
		time.Sleep(10 * time.Millisecond)
	}
	server.release()
	next := dial(t, socket, Frame{Type: "start", Spec: json.RawMessage(spec)})
	if _, child := hostilePids(t, next); child == 0 {
		t.Fatal("the turn slot stayed taken")
	}
}

func TestServeKillsAHostileRunnerWhenTheCallerHangsUp(t *testing.T) {
	server := runnerServer(t, "hostile-hold", &Server{})
	c := dial(t, startServer(t, server), Frame{Type: "start", Spec: json.RawMessage(spec)})
	runner, child := hostilePids(t, c)
	_ = c.conn.Close()
	deadline := time.Now().Add(runnerStop + 5*time.Second)
	for alive(runner) || alive(child) {
		if time.Now().After(deadline) {
			t.Fatal("the runner or its child kept running after the caller hung up")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestServeEndsATurnWhoseRunnerLeftAChildOnTheLink(t *testing.T) {
	server := runnerServer(t, "hostile-orphan", &Server{})
	c := dial(t, startServer(t, server), Frame{Type: "start", Spec: json.RawMessage(spec)})
	runner, child := hostilePids(t, c)
	exit, _ := waitExit(t, c)
	if exit.Code == nil || *exit.Code != 2 {
		t.Fatalf("exit = %+v", exit)
	}
	requireDead(t, "after the exit frame", runner, child)
}

func TestBrokerCapsTheRequestsOfATurn(t *testing.T) {
	server := runnerServer(t, "hostile-broker", &Server{})
	c := dial(t, startServer(t, server), Frame{Type: "start", Spec: json.RawMessage(spec)})
	hostilePids(t, c)
	select {
	case frame := <-c.frames:
		var got struct {
			Answers int
			Last    string
		}
		if json.Unmarshal(frame.Event, &got) != nil {
			t.Fatalf("frame = %+v", frame)
		}
		want := fmt.Sprintf("-the turn asked for more than %d connections in 1m0s", maxBrokerRequests)
		if got.Answers != maxBrokerRequests+1 || got.Last != want {
			t.Fatalf("the broker answered %d requests, the last with %q", got.Answers, got.Last)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("no broker report")
	}
	_ = c.conn.Close()
}

// TestBrokerKeepsALongTurnThatPacesItsRequests: a turn that asks for more
// connections in all than the cap, but not within one window, gets each.
func TestBrokerKeepsALongTurnThatPacesItsRequests(t *testing.T) {
	brokerWindow = 500 * time.Millisecond
	t.Cleanup(func() { brokerWindow = time.Minute })
	server := runnerServer(t, "paced-broker", &Server{})
	c := dial(t, startServer(t, server), Frame{Type: "start", Spec: json.RawMessage(spec)})
	hostilePids(t, c)
	select {
	case frame := <-c.frames:
		var got struct {
			Answers int
			Last    string
		}
		if json.Unmarshal(frame.Event, &got) != nil {
			t.Fatalf("frame = %+v", frame)
		}
		if got.Answers != maxBrokerRequests+100 || !strings.Contains(got.Last, "serve passes no connection named") {
			t.Fatalf("the broker answered %d requests, the last with %q", got.Answers, got.Last)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("no broker report")
	}
	_ = c.conn.Close()
}
