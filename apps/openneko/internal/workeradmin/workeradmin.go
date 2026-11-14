// Package workeradmin reaches the worker admin endpoint over TCP or a Unix socket.
package workeradmin

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
)

const (
	DefaultURL   = "http://127.0.0.1:4100"
	socketOrigin = "http://worker"
)

// URL is WORKER_ADMIN_URL, or DefaultURL when it is not set.
func URL() string {
	if v := strings.TrimSpace(os.Getenv("WORKER_ADMIN_URL")); v != "" {
		return v
	}
	return DefaultURL
}

// Endpoint gives the base URL and the client for raw. A unix:///path address
// gives an origin that the client dials on that socket.
func Endpoint(raw string) (string, *http.Client, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		raw = DefaultURL
	}
	if !strings.HasPrefix(raw, "unix:") {
		return raw, http.DefaultClient, nil
	}
	path := strings.TrimPrefix(strings.TrimPrefix(raw, "unix:"), "//")
	if !strings.HasPrefix(path, "/") {
		return "", nil, fmt.Errorf("worker admin address %s: a unix address needs an absolute socket path", raw)
	}
	var dialer net.Dialer
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return dialer.DialContext(ctx, "unix", path)
	}}
	return socketOrigin, &http.Client{Transport: transport}, nil
}
