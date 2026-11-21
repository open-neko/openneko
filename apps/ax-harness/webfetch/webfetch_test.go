package webfetch

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestWebFetchStaysOnAllowlist(t *testing.T) {
	var other *httptest.Server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/page":
			if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
				t.Errorf("credentials sent")
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(`<html><head><title>T</title><script>var x=1</script></head><body><h1>Prices</h1><p>Widget <b>$4</b></p><svg/><p>Done</p></body></html>`))
		case "/data":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/image":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte{0x89, 'P', 'N', 'G'})
		case "/away":
			http.Redirect(w, r, other.URL+"/page", http.StatusFound)
		}
	}))
	defer server.Close()
	other = httptest.NewServer(http.NotFoundHandler())
	defer other.Close()
	// Both test servers listen on 127.0.0.1; name them by host to test the allowlist.
	rename := func(raw, host string) string {
		u, _ := url.Parse(raw)
		return "http://" + host + ":" + u.Port()
	}
	allowed, blocked := rename(server.URL, "localhost"), rename(other.URL, "127.0.0.1")
	other.URL = blocked
	web, err := Open("localhost, *.example.com")
	if err != nil {
		t.Fatal(err)
	}
	fetch := func(target string) (map[string]any, error) {
		input, _ := json.Marshal(map[string]string{"url": target})
		raw, err := web.Fetch(context.Background(), input)
		var out map[string]any
		_ = json.Unmarshal(raw, &out)
		return out, err
	}
	if _, err := fetch(allowed + "/page"); err == nil || !strings.Contains(err.Error(), "public addresses") {
		t.Fatalf("private address admitted: %v", err)
	}
	web.allowIP = func(ip net.IP) bool { return ip.IsLoopback() }
	page, err := fetch(allowed + "/page")
	if err != nil || page["content"] != "Prices\nWidget $4\nDone" || page["status"] != float64(200) {
		t.Fatalf("page=%v err=%v", page, err)
	}
	if data, err := fetch(allowed + "/data"); err != nil || data["content"] != `{"ok":true}` {
		t.Fatalf("data=%v err=%v", data, err)
	}
	if _, err := fetch(allowed + "/image"); err == nil {
		t.Fatal("binary content admitted")
	}
	if _, err := fetch(blocked + "/page"); err == nil || !strings.Contains(err.Error(), "allowlist") {
		t.Fatalf("host outside the allowlist admitted: %v", err)
	}
	if _, err := fetch(allowed + "/away"); err == nil || !strings.Contains(err.Error(), "allowlist") {
		t.Fatalf("redirect off the allowlist admitted: %v", err)
	}
	for _, bad := range []string{"file:///etc/passwd", "http://user:pw@localhost/"} {
		if _, err := fetch(bad); err == nil {
			t.Fatalf("admitted %s", bad)
		}
	}
	for _, bad := range []string{"", "a.com/x", "*a.com", "a.*.com"} {
		if _, err := Open(bad); err == nil {
			t.Fatalf("allowlist %q admitted", bad)
		}
	}
	if web.check(&url.URL{Scheme: "https", Host: "api.example.com"}) != nil || web.check(&url.URL{Scheme: "https", Host: "example.com.evil.io"}) == nil {
		t.Fatal("wildcard match is wrong")
	}
}

func TestWebFetchRefusesSharedAndNAT64Addresses(t *testing.T) {
	for addr, want := range map[string]bool{
		"8.8.8.8": true, "2001:4860:4860::8888": true, "100.63.255.255": true, "100.128.0.1": true,
		"100.64.0.1": false, "100.127.255.254": false, "64:ff9b::a00:1": false, "64:ff9b::808:808": false, "64:ff9b:1::1": false,
		"10.0.0.1": false, "127.0.0.1": false, "169.254.169.254": false, "::1": false, "fd00::1": false, "::ffff:100.64.0.1": false,
	} {
		if got := publicIP(net.ParseIP(addr)); got != want {
			t.Errorf("publicIP(%s) = %v, want %v", addr, got, want)
		}
	}
}

func TestWebFetchChecksTheTargetBehindAProxy(t *testing.T) {
	var asked []string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Host)
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("through the proxy"))
	}))
	defer proxy.Close()
	proxyURL, _ := url.Parse(proxy.URL)
	web, err := Open("*")
	if err != nil {
		t.Fatal(err)
	}
	web.proxy = func(*http.Request) (*url.URL, error) { return proxyURL, nil }
	names := map[string]string{"public.test": "93.184.216.34", "shared.test": "100.64.1.1", "nat64.test": "64:ff9b::a9fe:a9fe",
		"private.test": "10.1.2.3", "mixed.test": "10.1.2.3"}
	web.lookup = func(_ context.Context, host string) ([]net.IPAddr, error) {
		out := []net.IPAddr{{IP: net.ParseIP(names[host])}}
		if host == "mixed.test" {
			out = append([]net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, out...)
		}
		return out, nil
	}
	fetch := func(target string) error {
		input, _ := json.Marshal(map[string]string{"url": target})
		_, err := web.Fetch(context.Background(), input)
		return err
	}
	if err := fetch("http://public.test/page"); err != nil || len(asked) != 1 || asked[0] != "public.test" {
		t.Fatalf("a public host through the proxy: %v, the proxy saw %v", err, asked)
	}
	for _, target := range []string{"http://shared.test/", "http://nat64.test/", "http://private.test/", "http://mixed.test/", "http://100.64.0.1/", "http://[64:ff9b::a00:1]/", "http://169.254.169.254/"} {
		if err := fetch(target); err == nil || !strings.Contains(err.Error(), "public addresses") {
			t.Errorf("%s through the proxy: %v", target, err)
		}
	}
	if len(asked) != 1 {
		t.Fatalf("the proxy got a request for a host that is not public: %v", asked)
	}
}
