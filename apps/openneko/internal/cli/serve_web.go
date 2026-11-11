package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/open-neko/neko/apps/openneko/internal/webfront"
)

type webFront struct {
	done  chan error
	close func()
}

// startWebFront reads sessions, devices and groups with the main database
// login, because the narrow serve roles cannot read those tables.
func startWebFront(ctx context.Context, out io.Writer, addr, dir, workerURL string) (*webFront, error) {
	if err := webfront.Check(dir); err != nil {
		return nil, err
	}
	conn, err := defaultConn()
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.New(ctx, conn.DSN())
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		pool.Close()
		return nil, err
	}
	front := &webfront.Front{App: os.DirFS(dir), Gate: &webfront.Gate{WorkerURL: workerURL, DB: pool}}
	srv := &http.Server{Handler: front, ReadHeaderTimeout: 10 * time.Second}
	fmt.Fprintf(out, "openneko web listening on %s, app from %s\n", ln.Addr(), dir)
	done := make(chan error, 1)
	go func() {
		if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
			done <- err
		}
	}()
	return &webFront{done: done, close: func() {
		shut, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(shut)
		pool.Close()
	}}, nil
}
