package llm

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/textproto"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/open-neko/neko/apps/openneko/internal/testsock"
	"github.com/open-neko/openneko/apps/ax-harness/keydoor"
)

func TestKeyDoorCallsOverTheLimitWaitForASlot(t *testing.T) {
	door := &KeyDoor{MaxOpen: func() int { return 2 }}
	client := doorClient(t, door)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		_, _ = io.WriteString(w, `{}`)
	}))
	t.Cleanup(srv.Close)
	pass := issuePass(t, door, map[string]DoorRoute{"primary": {Upstream: srv.URL, Key: "k"}}, 100)
	var wg sync.WaitGroup
	statuses := make(chan int, 4)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest(http.MethodPost, keydoor.URL("primary")+"/chat/completions", strings.NewReader(`{}`))
			req.Header.Set("Authorization", "Bearer "+pass.Token)
			res, err := client.Do(req)
			if err != nil {
				statuses <- 0
				return
			}
			res.Body.Close()
			statuses <- res.StatusCode
		}()
	}
	wg.Wait()
	close(statuses)
	for s := range statuses {
		if s != 200 {
			t.Errorf("a call over the limit of 2: status %d, want 200 after a wait", s)
		}
	}
}

func TestKeyDoorCapsTheCallsAtOnceOfOneToken(t *testing.T) {
	door := &KeyDoor{MaxOpen: func() int { return 2 }}
	path := filepath.Join(testsock.Dir(t), keydoor.SocketName)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if _, err := ListenDoor(ctx, path, door); err != nil {
		t.Fatal(err)
	}
	keydoor.SetSocket(path)
	t.Cleanup(func() { keydoor.SetSocket("") })
	srv, got := upstream(t, 200, `{}`)
	hostile := issuePass(t, door, map[string]DoorRoute{"primary": {Upstream: srv.URL, Key: "k"}}, 100)
	other, err := door.Issue("org-2", "test", map[string]DoorRoute{"primary": {Upstream: srv.URL, Key: "k2"}}, time.Minute, 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(other.Revoke)
	slow := func() net.Conn {
		conn, err := net.Dial("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.Close() })
		fmt.Fprintf(conn, "POST /r/primary/chat/completions HTTP/1.1\r\nHost: door\r\nAuthorization: Bearer %s\r\nContent-Length: %d\r\n\r\n{", hostile.Token, maxDoorBody)
		return conn
	}
	for range MaxGrantOpen {
		slow()
	}
	time.Sleep(200 * time.Millisecond)
	client, err := keydoor.Client()
	if err != nil {
		t.Fatal(err)
	}
	client.Timeout = 5 * time.Second
	if status, _ := post(t, client, keydoor.URL("primary")+"/chat/completions", "Authorization", "Bearer "+hostile.Token); status != http.StatusTooManyRequests {
		t.Errorf("a call over the token's %d at once: status %d, want 429", MaxGrantOpen, status)
	}
	began := time.Now()
	if status, _ := post(t, client, keydoor.URL("primary")+"/chat/completions", "Authorization", "Bearer "+other.Token); status != 200 {
		t.Fatalf("another org's call beside %d slow bodies: status %d", MaxGrantOpen, status)
	}
	if waited := time.Since(began); waited > 2*time.Second {
		t.Errorf("another org's call waited %v behind slow bodies", waited)
	}
	<-got
	noCall(t, got, "a slow body")
}

func TestKeyDoorRefusesABodyOverTheCap(t *testing.T) {
	door := &KeyDoor{}
	client := doorClient(t, door)
	srv, got := upstream(t, 200, `{}`)
	pass := issuePass(t, door, map[string]DoorRoute{"primary": {Upstream: srv.URL, Key: "k"}}, 5)
	req, _ := http.NewRequest(http.MethodPost, keydoor.URL("primary")+"/chat/completions", bytes.NewReader(make([]byte, 32<<20+1)))
	req.Header.Set("Authorization", "Bearer "+pass.Token)
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("a body of 32 MiB and one byte: status %d, want 413", res.StatusCode)
	}
	noCall(t, got, "a body over the cap")
}

func TestKeyDoorPassesNoInformationalAnswerOrUpgrade(t *testing.T) {
	door := &KeyDoor{}
	client := doorClient(t, door)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") != "" {
			w.Header().Set("Connection", "Upgrade")
			w.Header().Set("Upgrade", "websocket")
			w.WriteHeader(http.StatusSwitchingProtocols)
			return
		}
		w.Header().Set("Link", "<"+r.Header.Get("Authorization")+">")
		w.WriteHeader(http.StatusEarlyHints)
		w.Header().Del("Link")
		_, _ = io.WriteString(w, `{}`)
	}))
	t.Cleanup(srv.Close)
	pass := issuePass(t, door, map[string]DoorRoute{"primary": {Upstream: srv.URL, Key: "k"}}, 5)
	var early []int
	trace := &httptrace.ClientTrace{Got1xxResponse: func(code int, _ textproto.MIMEHeader) error {
		early = append(early, code)
		return nil
	}}
	req, _ := http.NewRequestWithContext(httptrace.WithClientTrace(context.Background(), trace), http.MethodPost,
		keydoor.URL("primary")+"/chat/completions", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+pass.Token)
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 || len(early) != 0 {
		t.Errorf("an upstream 103: status %d, 1xx answers at serve %v", res.StatusCode, early)
	}
	req, _ = http.NewRequest(http.MethodPost, keydoor.URL("primary")+"/chat/completions", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+pass.Token)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	res, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("an upgrade request: status %d, want 400", res.StatusCode)
	}
}
