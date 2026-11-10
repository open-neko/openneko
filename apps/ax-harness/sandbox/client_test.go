package sandbox

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

type seen struct {
	method, path, auth string
	body               map[string]any
}

func fakeSandbox(t *testing.T, got *[]seen) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entry := seen{method: r.Method, path: r.URL.Path, auth: r.Header.Get("Authorization")}
		_ = json.NewDecoder(r.Body).Decode(&entry.body)
		*got = append(*got, entry)
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_, _ = w.Write([]byte(`{"exit_code":0,"output":"hi\n"}`))
	})
}

func TestClientSendsTheKeyAndTheRunOverHTTP(t *testing.T) {
	var got []seen
	server := httptest.NewServer(fakeSandbox(t, &got))
	defer server.Close()
	client, err := NewClient(Config{URL: server.URL + "/", Key: "k1"}, "/runs/a/workspace")
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Exec(context.Background(), "echo hi", "/runs/a/skills/xlsx", map[string]string{"RUN_DIR": "/runs/a/run"}, 30, 2000)
	if err != nil || result.Output != "hi\n" {
		t.Fatalf("exec = %+v, %v", result, err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].method != http.MethodPost || got[0].path != "/v1/runs/"+client.run+"/exec" || got[1].method != http.MethodDelete {
		t.Fatalf("requests = %+v", got)
	}
	for _, request := range got {
		if request.auth != "Bearer k1" {
			t.Fatalf("auth = %q", request.auth)
		}
	}
	body := got[0].body
	if body["workspace"] != "/runs/a/workspace" || body["command"] != "echo hi" || body["skill_dir"] != "/runs/a/skills/xlsx" || body["timeout_seconds"] != float64(30) {
		t.Fatalf("body = %v", body)
	}
}

func TestClientReachesAUnixSocket(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "sandbox.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	var got []seen
	server := &http.Server{Handler: fakeSandbox(t, &got)}
	go server.Serve(listener)
	defer server.Close()
	client, err := NewClient(Config{URL: "unix://" + socket}, "/w")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Exec(context.Background(), "true", "", nil, 5, 1000); err != nil {
		t.Fatal(err)
	}
	if got[0].auth != "" {
		t.Fatalf("a client without a key sent %q", got[0].auth)
	}
}

func TestClientReportsAnUnreachableOrRefusingSandbox(t *testing.T) {
	client, _ := NewClient(Config{URL: "unix://" + filepath.Join(t.TempDir(), "none.sock")}, "/w")
	if _, err := client.Exec(context.Background(), "true", "", nil, 5, 1000); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("err = %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer server.Close()
	client, _ = NewClient(Config{URL: server.URL}, "/w")
	if _, err := client.Exec(context.Background(), "true", "", nil, 5, 1000); err == nil || !strings.Contains(err.Error(), "unauthorized") {
		t.Fatalf("err = %v", err)
	}
}

func TestClientRefusesBadURLs(t *testing.T) {
	if (Config{}).Enabled() || (Config{}).Check() != nil {
		t.Fatal("an empty URL must mean off")
	}
	for _, raw := range []string{"ftp://x", "unix://", "http://", "sandbox:8080"} {
		if _, err := NewClient(Config{URL: raw}, "/w"); err == nil || (Config{URL: raw}).Check() == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
}
