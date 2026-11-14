package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const webAdminSocketEnv = "OPENNEKO_WEB_ADMIN_SOCKET"

// webAdminSocket is the socket of `openneko web` that `openneko pack` calls.
// In a container the web's own environment names it.
func webAdminSocket() string {
	if v := strings.TrimSpace(os.Getenv(webAdminSocketEnv)); v != "" {
		return v
	}
	return nativeLayout().WebAdmin()
}

// serveWebAdmin listens on path with mode 0600: only the web's own OS user
// (and root) can call it, and every caller acts as an admin.
func serveWebAdmin(path string, handler http.Handler) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode().Type() != fs.ModeSocket {
			return nil, fmt.Errorf("%s exists and is not a socket", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return nil, err
	}
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
		_ = os.Remove(path)
	}, nil
}

func webAdminClient(path string) *http.Client {
	var dialer net.Dialer
	return &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return dialer.DialContext(ctx, "unix", path)
	}}}
}

func webAdminUnreachable(path string, err error) error {
	if errors.Is(err, fs.ErrPermission) {
		return fmt.Errorf("pack service at %s: permission denied; run the command as the user of openneko web, or with sudo on a server install", path)
	}
	return fmt.Errorf("pack service at %s is unreachable; start OpenNeko first: %w", path, err)
}
