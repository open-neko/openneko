package llm

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/open-neko/openneko/apps/ax-harness/keydoor"
)

func setWait(t *testing.T, v *time.Duration, d time.Duration) {
	old := *v
	*v = d
	t.Cleanup(func() { *v = old })
}

func blockingUpstream(t *testing.T) (*httptest.Server, chan struct{}) {
	t.Helper()
	arrived := make(chan struct{}, 16)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		arrived <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
		}
		_, _ = io.WriteString(w, `{}`)
	}))
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }); srv.Close() })
	return srv, arrived
}

func TestKeyDoorEndsACallWhoseUpstreamDoesNotAnswer(t *testing.T) {
	setWait(t, &doorHeaderWait, 300*time.Millisecond)
	door := &KeyDoor{}
	client := doorClient(t, door)
	srv, arrived := blockingUpstream(t)
	pass := issuePass(t, door, map[string]DoorRoute{"primary": {Upstream: srv.URL, Key: "k"}}, 5)
	began := time.Now()
	status, _ := post(t, client, keydoor.URL("primary")+"/chat/completions", "Authorization", "Bearer "+pass.Token)
	<-arrived
	if status != http.StatusBadGateway || time.Since(began) > 5*time.Second {
		t.Fatalf("a silent upstream: status %d after %v, want 502 after the wait", status, time.Since(began))
	}
}

func TestKeyDoorKeepsAStreamThatOutlastsTheWaits(t *testing.T) {
	setWait(t, &doorHeaderWait, 300*time.Millisecond)
	setWait(t, &doorBodyWait, 300*time.Millisecond)
	door := &KeyDoor{}
	client := doorClient(t, door)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for i := range 8 {
			fmt.Fprintf(w, "data: %d\n\n", i)
			w.(http.Flusher).Flush()
			time.Sleep(150 * time.Millisecond)
		}
	}))
	t.Cleanup(srv.Close)
	pass := issuePass(t, door, map[string]DoorRoute{"primary": {Upstream: srv.URL, Key: "k"}}, 5)
	status, body := post(t, client, keydoor.URL("primary")+"/chat/completions", "Authorization", "Bearer "+pass.Token)
	if status != 200 || strings.Count(body, "data: ") != 8 {
		t.Fatalf("a 1.2 s stream: status %d body %q", status, body)
	}
}

func TestKeyDoorEndsARequestWhoseBodyStalls(t *testing.T) {
	setWait(t, &doorBodyWait, 300*time.Millisecond)
	door := &KeyDoor{}
	path := filepath.Join(t.TempDir(), keydoor.SocketName)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if _, err := ListenDoor(ctx, path, door); err != nil {
		t.Fatal(err)
	}
	srv, got := upstream(t, 200, `{}`)
	pass := issuePass(t, door, map[string]DoorRoute{"primary": {Upstream: srv.URL, Key: "k"}}, 5)
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	fmt.Fprintf(conn, "POST /r/primary/chat/completions HTTP/1.1\r\nHost: door\r\nAuthorization: Bearer %s\r\nContent-Length: 100\r\n\r\n{\"model\"", pass.Token)
	began := time.Now()
	res, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("no answer to a stalled body after %v: %v", time.Since(began), err)
	}
	if res.StatusCode != http.StatusRequestTimeout || time.Since(began) > 5*time.Second {
		t.Fatalf("a stalled body: status %d after %v, want 408", res.StatusCode, time.Since(began))
	}
	noCall(t, got, "a stalled body")
	if pass.Calls() != 0 {
		t.Errorf("the door counted %d calls for a stalled body", pass.Calls())
	}
}

func TestKeyDoorBoundsTheCallsAtOnce(t *testing.T) {
	door := &KeyDoor{MaxOpen: 2}
	client := doorClient(t, door)
	srv, arrived := blockingUpstream(t)
	pass := issuePass(t, door, map[string]DoorRoute{"primary": {Upstream: srv.URL, Key: "k"}}, 10)
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequestWithContext(ctx, http.MethodPost, keydoor.URL("primary")+"/chat/completions", strings.NewReader(`{}`))
			req.Header.Set("Authorization", "Bearer "+pass.Token)
			if res, err := client.Do(req); err == nil {
				res.Body.Close()
			}
		}()
	}
	<-arrived
	<-arrived
	third, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	req, _ := http.NewRequestWithContext(third, http.MethodPost, keydoor.URL("primary")+"/chat/completions", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+pass.Token)
	if res, err := client.Do(req); err != nil {
		t.Errorf("a third call at once: %v, want 503", err)
	} else {
		res.Body.Close()
		if res.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("a third call at once: status %d, want 503", res.StatusCode)
		}
	}
	cancel()
	wg.Wait()
	deadline := time.Now().Add(5 * time.Second)
	for {
		door.mu.Lock()
		open := door.open
		door.mu.Unlock()
		if open == 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	go func() { <-arrived }()
	srvOK, got := upstream(t, 200, `{}`)
	ok := issuePass(t, door, map[string]DoorRoute{"primary": {Upstream: srvOK.URL, Key: "k"}}, 1)
	if status, _ := post(t, client, keydoor.URL("primary")+"/chat/completions", "Authorization", "Bearer "+ok.Token); status != 200 {
		t.Errorf("a call after the others ended: status %d", status)
	}
	<-got
}
