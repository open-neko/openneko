package llm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"
)

// ListenDoor serves the door on a Unix socket that the owner and the group
// may use; a stale socket at path is replaced. On Windows the socket takes
// the access list of its folder.
func ListenDoor(ctx context.Context, path string, door *KeyDoor) (<-chan error, error) {
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSocket != 0 {
		_ = os.Remove(path)
	}
	ln, err := listenSocket(path)
	if err != nil {
		return nil, fmt.Errorf("key door socket unavailable: %w", err)
	}
	srv := &http.Server{Handler: door, ReadHeaderTimeout: 30 * time.Second, IdleTimeout: doorBodyWait}
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
