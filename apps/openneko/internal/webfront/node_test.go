package webfront

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func proxyTo(t *testing.T, node http.Handler) *httptest.Server {
	t.Helper()
	upstream := httptest.NewServer(node)
	t.Cleanup(upstream.Close)
	target, _ := url.Parse(upstream.URL)
	front := httptest.NewServer(NodeProxy(target))
	t.Cleanup(front.Close)
	return front
}

func TestNodeProxyKeepsHostAndClient(t *testing.T) {
	seen := make(chan http.Header, 1)
	hosts := make(chan string, 1)
	front := proxyTo(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Clone()
		hosts <- r.Host
		w.WriteHeader(http.StatusTeapot)
	}))

	req, _ := http.NewRequest("POST", front.URL+"/api/v1/work?x=1", strings.NewReader("{}"))
	req.Host = "neko.example.com"
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusTeapot {
		t.Fatalf("status %d", res.StatusCode)
	}
	h := <-seen
	if host := <-hosts; host != "neko.example.com" {
		t.Fatalf("host %q", host)
	}
	if h.Get("X-Forwarded-For") != "127.0.0.1" || h.Get("X-Forwarded-Host") != "neko.example.com" || h.Get("X-Forwarded-Proto") != "http" {
		t.Fatalf("forwarded headers %v", h)
	}

	req, _ = http.NewRequest("GET", front.URL+"/api/v1/work", nil)
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	req.Header.Set("X-Forwarded-Host", "public.example.com")
	req.Header.Set("X-Forwarded-Proto", "https")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	h = <-seen
	<-hosts
	if h.Get("X-Forwarded-For") != "203.0.113.9, 127.0.0.1" || h.Get("X-Forwarded-Host") != "public.example.com" || h.Get("X-Forwarded-Proto") != "https" {
		t.Fatalf("forwarded headers from an outer proxy %v", h)
	}
}

func TestNodeProxyStreams(t *testing.T) {
	release := make(chan struct{})
	front := proxyTo(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: one\n\n")
		w.(http.Flusher).Flush()
		<-release
		fmt.Fprint(w, "data: two\n\n")
	}))
	res, err := http.Get(front.URL + "/api/v1/chat/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	lines := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(res.Body).ReadString('\n')
		lines <- line
	}()
	select {
	case line := <-lines:
		if line != "data: one\n" {
			t.Fatalf("first line %q", line)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the first event did not arrive before the stream ended")
	}
	close(release)
}

func TestNodeProxyNodeDown(t *testing.T) {
	target, _ := url.Parse("http://127.0.0.1:1")
	front := httptest.NewServer(NodeProxy(target))
	defer front.Close()

	res, err := http.Get(front.URL + "/api/v1/work")
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]string
	_ = json.NewDecoder(res.Body).Decode(&body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusBadGateway || res.Header.Get("Content-Type") != "application/json" || body["error"] == "" {
		t.Fatalf("api: %d %q %v", res.StatusCode, res.Header.Get("Content-Type"), body)
	}

	res, err = http.Get(front.URL + "/admin")
	if err != nil {
		t.Fatal(err)
	}
	text, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusBadGateway || !strings.Contains(string(text), "Node server did not answer") {
		t.Fatalf("page: %d %q", res.StatusCode, text)
	}
}
