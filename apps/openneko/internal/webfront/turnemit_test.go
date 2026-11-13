package webfront

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func floodServe(t *testing.T, steps int) (string, <-chan struct{}) {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "serve.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	written := make(chan struct{})
	pad := strings.Repeat("x", 4096)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		if _, err := bufio.NewReader(conn).ReadString('\n'); err != nil {
			return
		}
		for i := 0; i < steps; i++ {
			line := fmt.Sprintf(`{"type":"event","event":{"version":1,"run_id":"workrun-1","sequence":%d,"type":"actor.step","data":{"text":"step %d %s"}}}`+"\n", i+1, i, pad)
			if _, err := conn.Write([]byte(line)); err != nil {
				return
			}
		}
		fin := fmt.Sprintf(`{"type":"event","event":{"version":1,"run_id":"workrun-1","sequence":%d,"type":"run.finished","result":{"status":"completed","answer":"Done."}}}`+"\n", steps+1)
		_, _ = conn.Write([]byte(fin))
		close(written)
		_, _ = conn.Write([]byte(`{"type":"exit","code":0}` + "\n"))
	}()
	return socket, written
}

func TestAxTurnSlowEmitDoesNotHoldTheReadLoop(t *testing.T) {
	clearAxEnv(t, nil)
	const steps = 400
	socket, written := floodServe(t, steps)
	var mu sync.Mutex
	var got []string
	waited := false
	first := true
	res, err := runAx(context.Background(), plainCfg, axTurnOptions{Prompt: "p", Tag: "work run-1", Socket: socket,
		Emit: func(_ context.Context, event any) error {
			if first {
				first = false
				select {
				case <-written:
				case <-time.After(10 * time.Second):
					waited = true
				}
			}
			time.Sleep(time.Millisecond)
			e := event.(*jsObj)
			if e.vals["type"] == "status" {
				msg, _ := e.vals["message"].(string)
				mu.Lock()
				got = append(got, strings.Fields(msg)[1])
				mu.Unlock()
			}
			return nil
		}})
	if err != nil || res.Status != "completed" || res.FinalText != "Done." {
		t.Fatalf("got %+v %v", res, err)
	}
	if waited {
		t.Fatal("the read loop waited for the emit: serve could not write the turn")
	}
	if len(got) != steps {
		t.Fatalf("got %d status events, want %d", len(got), steps)
	}
	for i, n := range got {
		if n != fmt.Sprint(i) {
			t.Fatalf("event %d is step %s: order lost", i, n)
		}
	}
}

func TestAxTurnFailsWhenEventsCannotDrain(t *testing.T) {
	clearAxEnv(t, nil)
	saved := axEmitDrainTimeout
	axEmitDrainTimeout = 50 * time.Millisecond
	defer func() { axEmitDrainTimeout = saved }()
	socket, _ := floodServe(t, 3)
	release := make(chan struct{})
	defer close(release)
	_, err := runAx(context.Background(), plainCfg, axTurnOptions{Prompt: "p", Tag: "work run-1", Socket: socket,
		Emit: func(context.Context, any) error { <-release; return nil }})
	if err != errEmitDrain {
		t.Fatalf("got %v", err)
	}
}
