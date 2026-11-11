package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"

	"github.com/open-neko/neko/apps/openneko/internal/config"
	"github.com/open-neko/neko/apps/openneko/internal/filecontent"
	"github.com/open-neko/neko/apps/openneko/internal/files"
	"github.com/open-neko/neko/apps/openneko/internal/filestore"
	"github.com/open-neko/neko/apps/openneko/internal/gjhost"
	"github.com/open-neko/openneko/apps/ax-harness/sandbox"
)

func newServeCmd() *cobra.Command {
	var addr, filesDir, indexDir, duckdb, agentSocket, checkSocket, sandboxURL, credentials, gjConfig, gjAddr, subsAddr, subsDir, webAddr, webDir, workerURL string
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
			store, err := filestore.Open(ctx, filestore.FromEnv(filesDir))
			if err != nil {
				return err
			}
			reportFileStore(ctx, cmd.ErrOrStderr(), store, filesDir)
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
			var checkDone <-chan error
			if checkSocket != "" && gjConfig != "" {
				if checkDone, err = serveConfigCheck(ctx, cmd.OutOrStdout(), checkSocket, gjConfig); err != nil {
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
			subsDone := make(chan error, 1)
			if subsAddr != "" && credentials != "" {
				subs := &gjhost.Subscriptions{URLFile: filepath.Join(credentials, serveSubsFile), Dir: subsDir, Addr: subsAddr, Log: cmd.ErrOrStderr()}
				go func() { subsDone <- subs.Run(ctx) }()
			} else {
				if subsAddr != "" {
					fmt.Fprintln(cmd.ErrOrStderr(), "subscriptions GraphJin off: it needs --credentials from `openneko serve-init`")
				}
				subsDone <- nil
			}
			webDone := make(chan error, 1)
			if webAddr != "" {
				web, err := startWebFront(ctx, cmd.OutOrStdout(), webAddr, webDir, workerURL)
				if err != nil {
					_ = ln.Close()
					return err
				}
				defer web.close()
				webDone = web.done
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
			case err := <-checkDone:
				stop()
				_ = srv.Close()
				return fmt.Errorf("config check socket: %w", err)
			case err := <-webDone:
				stop()
				_ = srv.Close()
				return fmt.Errorf("web front: %w", err)
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
			wait := time.After(7 * time.Second)
			for _, done := range []chan error{gjDone, subsDone} {
				select {
				case <-done:
				case <-wait:
				}
			}
			return nil
		},
	}
	data := dataHome()
	cmd.Flags().StringVar(&addr, "addr", envOr("OPENNEKO_SERVE_ADDR", "127.0.0.1:8090"), "Listen address")
	cmd.Flags().StringVar(&filesDir, "files-dir", envOr("OPENNEKO_FILES_DIR", filepath.Join(data, "openneko", "files")), "Folder that holds each user's private files. OPENNEKO_FILES_BUCKET=s3://bucket[/prefix] keeps them in a bucket instead")
	cmd.Flags().StringVar(&indexDir, "index-dir", envOr("OPENNEKO_FILES_INDEX_DIR", filepath.Join(data, "openneko", "file-index")), "Folder for the sheet tables derived from the files; safe to delete")
	cmd.Flags().StringVar(&agentSocket, "agent-socket", os.Getenv("OPENNEKO_SERVE_SOCKET"), "Unix socket for agent turns; empty turns them off")
	cmd.Flags().StringVar(&checkSocket, "config-check-socket", os.Getenv("OPENNEKO_CONFIG_CHECK_SOCKET"), "Unix socket where web and worker ask serve to load a candidate GraphJin config with the pinned loader; empty turns the check off")
	cmd.Flags().StringVar(&sandboxURL, "sandbox-url", os.Getenv("OPENNEKO_SANDBOX_URL"), "Sandbox service for agent terminal commands (unix:///path or http(s)://host:port); empty turns the terminal off. Its key is read from OPENNEKO_SANDBOX_KEY")
	cmd.Flags().IntVar(&maxTurns, "max-turns", envInt("OPENNEKO_SERVE_MAX_TURNS", 32), "Agent turns that run at once; more are refused")
	cmd.Flags().StringVar(&credentials, "credentials", os.Getenv(serveCredentialsEnv), "Folder written by `openneko serve-init`: the token key and the files role database URL. Empty reads the secret-key and the main database login")
	cmd.Flags().StringVar(&gjConfig, "graphjin-config", os.Getenv("OPENNEKO_GRAPHJIN_CONFIG"), "Customer GraphJin config file (agentic.yml); empty runs no GraphJin. GraphJin runs as a child process; a new token in .openneko-graphjin-restart beside it restarts the child")
	cmd.Flags().StringVar(&gjAddr, "graphjin-addr", envOr("OPENNEKO_GRAPHJIN_ADDR", "127.0.0.1:8080"), "Listen address for the customer GraphJin")
	cmd.Flags().StringVar(&subsAddr, "subscriptions-addr", envOr("OPENNEKO_SUBSCRIPTIONS_ADDR", "127.0.0.1:8089"), "Listen address for the subscriptions GraphJin, which serves workflow output subscriptions as the subscriptions role; empty turns it off")
	cmd.Flags().StringVar(&subsDir, "subscriptions-dir", subscriptionsDir(), "Private folder for the subscriptions GraphJin config and state")
	cmd.Flags().StringVar(&webAddr, "web-addr", os.Getenv("OPENNEKO_WEB_ADDR"), "Listen address for the web app and its request gate; empty turns it off")
	cmd.Flags().StringVar(&webDir, "web-dir", envOr("OPENNEKO_WEB_DIR", "apps/web/dist"), "Folder of the built web app (`pnpm --filter @neko/web build:spa`)")
	cmd.Flags().StringVar(&workerURL, "worker-admin-url", envOr("WORKER_ADMIN_URL", "http://127.0.0.1:4100"), "Worker admin address that reports whether sign-in is on")
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

func subscriptionsDir() string {
	return envOr("OPENNEKO_SUBSCRIPTIONS_DIR", filepath.Join(os.TempDir(), "openneko-subscriptions"))
}

func reportFileStore(ctx context.Context, w io.Writer, store filestore.Store, dir string) {
	if err := store.Check(ctx); err != nil {
		fmt.Fprintf(w, "file store: cannot reach it: %v. The file tools fail until serve can reach it\n", err)
	} else {
		fmt.Fprintf(w, "file store: %s\n", store.Describe())
	}
	if store.Name() == "local" {
		return
	}
	if n := filestore.LocalFilesLeft(dir, 1000); n > 0 {
		count := fmt.Sprintf("%d files", n)
		if n == 1 {
			count = "1 file"
		} else if n >= 1000 {
			count = "1000 or more files"
		}
		fmt.Fprintf(w, "file store: the local folder %s still holds %s, and serve does not read them. To copy them to the bucket, run `openneko files copy-to-bucket`. When the bucket holds them, remove them from the folder\n", dir, count)
	}
}
