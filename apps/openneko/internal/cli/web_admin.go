package cli

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/open-neko/neko/apps/openneko/internal/ownonly"
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

// webAdminToken is the file beside the socket that holds the token a caller
// must send. Landlock cannot refuse serve a connect to a named socket below
// ABI 9, and serve cannot read this file.
func webAdminToken(socket string) string { return socket + ".token" }

// serveWebAdmin listens on path with mode 0600: only the web's own OS user
// (and root) can call it, with the token from webAdminToken. Every caller
// acts as an admin.
func serveWebAdmin(path string, handler http.Handler) (func(), error) {
	if err := ownonly.Mkdir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	token := hex.EncodeToString(raw)
	tokenFile := webAdminToken(path)
	_ = os.Remove(tokenFile)
	if err := os.WriteFile(tokenFile, []byte(token+"\n"), 0o600); err != nil {
		return nil, err
	}
	if err := ownonly.Set(tokenFile); err != nil {
		return nil, err
	}
	want := []byte("Bearer " + token)
	inner := handler
	handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), want) != 1 {
			http.Error(w, "the web admin socket needs the token from "+tokenFile, http.StatusUnauthorized)
			return
		}
		inner.ServeHTTP(w, r)
	})
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
		_ = os.Remove(tokenFile)
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
		_ = os.Remove(tokenFile)
	}, nil
}

func webAdminClient(path string) *http.Client {
	var dialer net.Dialer
	base := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return dialer.DialContext(ctx, "unix", path)
	}}
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		raw, err := os.ReadFile(webAdminToken(path))
		if err != nil {
			return nil, err
		}
		r = r.Clone(r.Context())
		r.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(raw)))
		return base.RoundTrip(r)
	})}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func webAdminUnreachable(path string, err error) error {
	if errors.Is(err, fs.ErrPermission) {
		return fmt.Errorf("pack service at %s: permission denied; run the command as the user of openneko web, or with sudo on a server install", path)
	}
	return fmt.Errorf("pack service at %s is unreachable; start OpenNeko first: %w", path, err)
}
