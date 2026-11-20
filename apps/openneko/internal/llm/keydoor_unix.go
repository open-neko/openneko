//go:build !windows

package llm

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"syscall"
	"time"
)

// ListenDoor serves the door on a Unix socket that the owner and the group
// may use; a stale socket at path is replaced.
func ListenDoor(ctx context.Context, path string, door *KeyDoor) (<-chan error, error) {
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSocket != 0 {
		_ = os.Remove(path)
	}
	old := syscall.Umask(0o117)
	ln, err := net.Listen("unix", path)
	syscall.Umask(old)
	if err != nil {
		return nil, fmt.Errorf("key door socket unavailable: %w", err)
	}
	srv := &http.Server{Handler: door, ReadHeaderTimeout: 30 * time.Second}
	done := make(chan error, 1)
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	go func() {
		err := srv.Serve(ln)
		_ = os.Remove(path)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		done <- err
	}()
	return done, nil
}
