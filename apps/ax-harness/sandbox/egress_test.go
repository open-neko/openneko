package sandbox

import (
	"context"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

func TestNormalizeEgressHosts(t *testing.T) {
	got, err := NormalizeEgressHosts([]string{" API.Example.com ", "api.example.com:443", "example.com:8443", "*.Files.example.com.", "1.1.1.1", "[2606:4700::1111]:443", "[::ffff:8.8.8.8]:80"})
	want := []string{"api.example.com", "example.com:8443", "*.files.example.com", "1.1.1.1", "[2606:4700::1111]"}
	if err != nil || !slices.Equal(got[:5], want) || len(got) != 6 || got[5] != "8.8.8.8:80" {
		t.Fatalf("normalize = %v, %v; want %v and one more", got, err, want)
	}
	for _, bad := range []string{"", "*", "*.com", "https://example.com", "example.com/path", "example.com:0", "127.1", "a b"} {
		if _, err := NormalizeEgressHosts([]string{bad}); err == nil {
			t.Errorf("%q passed", bad)
		}
	}
	for _, suffix := range []string{"*.co.uk", "*.github.io:443", "*.s3.amazonaws.com", "*.internal"} {
		if _, err := NormalizeEgressHosts([]string{suffix}); err == nil || !strings.Contains(err.Error(), "public suffix") {
			t.Errorf("%q: %v, want the public suffix refused", suffix, err)
		}
	}
	if _, err := NormalizeEgressHosts([]string{"*.example.co.uk", "*.amazonaws.com"}); err != nil {
		t.Errorf("a wildcard under one domain: %v", err)
	}
	for _, private := range []string{"[::a9fe:a9fe]", "[::ffff:0:a9fe:a9fe]:80", "10.0.0.5", "127.0.0.1:8080", "169.254.169.254:80", "192.168.1.1", "172.16.0.1", "100.64.0.1", "0.0.0.0", "[::1]", "[fe80::1]", "[fd00::1]", "[::ffff:10.0.0.1]:80", "[64:ff9b::a00:1]"} {
		if _, err := NormalizeEgressHosts([]string{private}); err == nil || !strings.Contains(err.Error(), "not a public address") {
			t.Errorf("%q: %v, want the private address refused", private, err)
		}
	}
	long := make([]string, MaxEgressHosts+1)
	for i := range long {
		long[i] = "h" + strings.Repeat("x", i%50) + ".example.com:" + string(rune('1'+i%9))
	}
	if _, err := NormalizeEgressHosts(long); err == nil {
		t.Error("a list over the limit passed")
	}
}

func TestClientSendsTheRunsEgress(t *testing.T) {
	fake := &fakeSandbox{}
	server := httptest.NewServer(fake)
	defer server.Close()
	client, err := NewClient(Config{URL: server.URL}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Exec(context.Background(), "true", "", nil, 5, 1000); err != nil {
		t.Fatal(err)
	}
	if err := client.SetEgress(Egress{Org: "org 1", Label: "work-run-1", Hosts: []string{"API.example.com"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Exec(context.Background(), "true", "", nil, 5, 1000); err != nil {
		t.Fatal(err)
	}
	first, second := fake.got[0].body.Egress, fake.got[1].body.Egress
	if first == nil || len(first.Hosts) != 0 {
		t.Fatalf("a run with no list sent egress %+v", first)
	}
	if second == nil || second.Org != "org1" || second.Label != "work-run-1" || !slices.Equal(second.Hosts, []string{"api.example.com"}) {
		t.Fatalf("egress = %+v", second)
	}
	if err := client.SetEgress(Egress{Hosts: []string{"http://x"}}); err == nil {
		t.Fatal("a bad list was taken")
	}
}
