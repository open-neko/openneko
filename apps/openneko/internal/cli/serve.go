package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/dosco/graphjin/core/v3/fstable"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"

	"github.com/open-neko/neko/apps/openneko/internal/config"
	"github.com/open-neko/neko/apps/openneko/internal/filecontent"
	"github.com/open-neko/neko/apps/openneko/internal/files"
)

func newServeCmd() *cobra.Command {
	var addr, filesDir, indexDir, duckdb string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the OpenNeko core service (user files and file search)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			key, err := config.SigningSecret("", files.TokenLabel)
			if err != nil {
				return fmt.Errorf("read the secret-key: %w", err)
			}
			pool, err := pgxpool.New(ctx, defaultConn().DSN())
			if err != nil {
				return err
			}
			defer pool.Close()
			if err := os.MkdirAll(filesDir, 0o750); err != nil {
				return err
			}
			store, err := fstable.NewLocal(fstable.LocalConfig{Root: filesDir})
			if err != nil {
				return err
			}
			ix, err := filecontent.Open(pool, indexDir, 0)
			if err != nil {
				return err
			}
			svc := files.New(store, ix, key, files.Options{DuckDB: duckdb})

			ln, err := net.Listen("tcp", addr)
			if err != nil {
				return err
			}
			srv := &http.Server{Handler: svc.Handler(), ReadHeaderTimeout: 10 * time.Second}
			fmt.Fprintf(cmd.OutOrStdout(), "openneko serve listening on %s\n", ln.Addr())
			errc := make(chan error, 1)
			go func() { errc <- srv.Serve(ln) }()
			select {
			case err := <-errc:
				return err
			case <-ctx.Done():
			}
			shut, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := srv.Shutdown(shut); err != nil && !errors.Is(err, context.DeadlineExceeded) {
				return err
			}
			return nil
		},
	}
	data := os.Getenv("XDG_DATA_HOME")
	if data == "" {
		home, _ := os.UserHomeDir()
		data = filepath.Join(home, ".local", "share")
	}
	cmd.Flags().StringVar(&addr, "addr", envOr("OPENNEKO_SERVE_ADDR", "127.0.0.1:8090"), "Listen address")
	cmd.Flags().StringVar(&filesDir, "files-dir", envOr("OPENNEKO_FILES_DIR", filepath.Join(data, "openneko", "files")), "Folder that holds each user's private files")
	cmd.Flags().StringVar(&indexDir, "index-dir", envOr("OPENNEKO_FILES_INDEX_DIR", filepath.Join(data, "openneko", "file-index")), "Folder for the sheet tables derived from the files; safe to delete")
	cmd.Flags().StringVar(&duckdb, "duckdb", envOr("OPENNEKO_DUCKDB", "duckdb"), "DuckDB CLI for SQL over sheet tables")
	return cmd
}
