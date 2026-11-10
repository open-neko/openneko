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
	"github.com/open-neko/neko/apps/openneko/internal/gjhost"
	"github.com/open-neko/openneko/apps/ax-harness/sandbox"
)

func newServeCmd() *cobra.Command {
	var addr, filesDir, indexDir, duckdb, agentSocket, sandboxURL, credentials, gjConfig, gjAddr string
	var maxTurns int
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the OpenNeko core service (user files, file search, agent turns and the customer GraphJin)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			key, dsn, err := serveKeyAndDatabase(credentials)
			if err != nil {
				return err
			}
			pool, err := pgxpool.New(ctx, dsn)
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
			var agentDone <-chan error
			if agentSocket != "" {
				if agentDone, err = serveAgentTurns(ctx, cmd.OutOrStdout(), agentSocket, maxTurns, sandbox.Config{URL: sandboxURL, Key: os.Getenv("OPENNEKO_SANDBOX_KEY")}); err != nil {
					_ = ln.Close()
					return err
				}
			}
			gjDone := make(chan error, 1)
			if gjConfig != "" {
				gj := &gjhost.Supervisor{ConfigFile: gjConfig, Addr: gjAddr, Log: cmd.ErrOrStderr(), Command: gjhost.SelfCommand(gjConfig)}
				go func() { gjDone <- gj.Run(ctx) }()
			} else {
				gjDone <- nil
			}
			srv := &http.Server{Handler: svc.Handler(), ReadHeaderTimeout: 10 * time.Second}
			fmt.Fprintf(cmd.OutOrStdout(), "openneko serve listening on %s\n", ln.Addr())
			errc := make(chan error, 1)
			go func() { errc <- srv.Serve(ln) }()
			select {
			case err := <-errc:
				stop()
				return err
			case err := <-agentDone:
				stop()
				_ = srv.Close()
				return fmt.Errorf("agent socket: %w", err)
			case <-ctx.Done():
			}
			shut, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := srv.Shutdown(shut); err != nil && !errors.Is(err, context.DeadlineExceeded) {
				return err
			}
			if agentDone != nil {
				select {
				case <-agentDone:
				case <-shut.Done():
				}
			}
			select {
			case <-gjDone:
			case <-time.After(7 * time.Second):
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
	cmd.Flags().StringVar(&agentSocket, "agent-socket", os.Getenv("OPENNEKO_SERVE_SOCKET"), "Unix socket for agent turns; empty turns them off")
	cmd.Flags().StringVar(&sandboxURL, "sandbox-url", os.Getenv("OPENNEKO_SANDBOX_URL"), "Sandbox service for agent terminal commands (unix:///path or http(s)://host:port); empty turns the terminal off. Its key is read from OPENNEKO_SANDBOX_KEY")
	cmd.Flags().IntVar(&maxTurns, "max-turns", envInt("OPENNEKO_SERVE_MAX_TURNS", 32), "Agent turns that run at once; more are refused")
	cmd.Flags().StringVar(&credentials, "credentials", os.Getenv(serveCredentialsEnv), "Folder written by `openneko serve-init`: the token key and the neko_files database URL. Empty reads the secret-key and the main database login")
	cmd.Flags().StringVar(&gjConfig, "graphjin-config", os.Getenv("OPENNEKO_GRAPHJIN_CONFIG"), "Customer GraphJin config file (agentic.yml); empty runs no GraphJin. GraphJin runs as a child process; a new token in .openneko-graphjin-restart beside it restarts the child")
	cmd.Flags().StringVar(&gjAddr, "graphjin-addr", envOr("OPENNEKO_GRAPHJIN_ADDR", "127.0.0.1:8080"), "Listen address for the customer GraphJin")
	cmd.Flags().StringVar(&duckdb, "duckdb", envOr("OPENNEKO_DUCKDB", "duckdb"), "DuckDB CLI for SQL over sheet tables")
	return cmd
}

func serveKeyAndDatabase(credentials string) ([]byte, string, error) {
	if credentials != "" {
		key, dsn, err := readServeCredentials(credentials)
		if err != nil {
			return nil, "", fmt.Errorf("read the serve credentials: %w", err)
		}
		return key, dsn, nil
	}
	key, err := config.SigningSecret("", files.TokenLabel)
	if err != nil {
		return nil, "", fmt.Errorf("read the secret-key: %w", err)
	}
	conn, err := defaultConn()
	if err != nil {
		return nil, "", err
	}
	return key, conn.DSN(), nil
}
