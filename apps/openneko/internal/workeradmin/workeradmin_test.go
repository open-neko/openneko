package workeradmin

import (
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestEndpointDialsUnixSocket(t *testing.T) {
	dir, err := os.MkdirTemp("", "wa")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "worker.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.URL.Path)
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	base, client, err := Endpoint("unix://" + socket + "/")
	if err != nil {
		t.Fatal(err)
	}
	res, err := client.Get(base + "/admin/auth/status")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if string(body) != "/admin/auth/status" {
		t.Fatalf("body %q", body)
	}
}

func TestEndpointKeepsHTTPAddress(t *testing.T) {
	base, client, err := Endpoint(" http://worker:4100/ ")
	if err != nil || base != "http://worker:4100" || client != http.DefaultClient {
		t.Fatalf("got %q %v %v", base, client, err)
	}
	if base, _, _ := Endpoint(""); base != DefaultURL {
		t.Fatalf("empty gave %q", base)
	}
}

func TestEndpointRefusesRelativeSocket(t *testing.T) {
	if _, _, err := Endpoint("unix:run/worker.sock"); err == nil {
		t.Fatal("a relative socket path was accepted")
	}
}
