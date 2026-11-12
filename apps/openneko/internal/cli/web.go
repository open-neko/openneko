package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"

	"github.com/open-neko/neko/apps/openneko/internal/version"
	"github.com/open-neko/neko/apps/openneko/internal/webfront"
)

const nodeStopGrace = 10 * time.Second

func newWebCmd() *cobra.Command {
	var addr, dir, nodeAddr, workerURL string
	cmd := &cobra.Command{
		Use:   "web [-- node command]",
		Short: "Serve the web app with its request gate and pass the API and server pages to Node",
		Long: `Serves the static web app, applies the request gate and passes /api and
the server pages to the Node server on --node-addr. With a command after
--, web starts that command as Node with PORT and HOSTNAME set from
--node-addr, and stops when it stops, with its exit code.

Web answers the version, sign-in, session, sign-out, device sign-in and
SSO setup routes, the business profile route and the personal pack
connection route itself. It marks its cookies Secure when NODE_ENV is
production. It reads and writes users, sessions, devices, groups, the
business profile, personal pack connections and the audit chain with the
main database login, and reads secrets.json for pack OAuth client IDs.

The admin password route changes the neko role's password and writes it to
config.json. With OPENNEKO_RESTART_WEB_ON_PASSWORD_CHANGE=1, web then stops
with exit code 0, so its supervisor starts it again.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			if err := webfront.Check(dir); err != nil {
				return err
			}
			host, port, err := net.SplitHostPort(nodeAddr)
			if err != nil {
				return fmt.Errorf("--node-addr: %w", err)
			}
			conn, err := defaultConn()
			if err != nil {
				return err
			}
			pcfg, err := pgxpool.ParseConfig(conn.DSN())
			if err != nil {
				return err
			}
			if conn.URL == "" {
				pcfg.BeforeConnect = func(_ context.Context, cc *pgx.ConnConfig) error {
					if fresh, err := defaultConn(); err == nil && fresh.URL == "" {
						cc.Password = fresh.Password
					}
					return nil
				}
			}
			pool, err := pgxpool.NewWithConfig(ctx, pcfg)
			if err != nil {
				return err
			}
			defer pool.Close()
			ln, err := net.Listen("tcp", addr)
			if err != nil {
				return err
			}
			var node *exec.Cmd
			nodeDone := make(chan error, 1)
			if len(args) > 0 {
				node = exec.Command(args[0], args[1:]...)
				node.Env = append(os.Environ(), "PORT="+port, "HOSTNAME="+host)
				node.Stdout, node.Stderr = cmd.OutOrStdout(), cmd.ErrOrStderr()
				if err := node.Start(); err != nil {
					_ = ln.Close()
					return fmt.Errorf("start Node: %w", err)
				}
				go func() { nodeDone <- node.Wait() }()
			}
			gate := &webfront.Gate{WorkerURL: workerURL, DB: pool}
			front := &webfront.Front{
				App:  os.DirFS(dir),
				Gate: gate,
				API: &webfront.API{Gate: gate, DB: pool, Version: webfront.AppVersion(dir, version.Version),
					SecureCookies: os.Getenv("NODE_ENV") == "production", Restart: stop},
				Node: webfront.NodeProxy(&url.URL{Scheme: "http", Host: nodeAddr}),
			}
			srv := &http.Server{Handler: front, ReadHeaderTimeout: 10 * time.Second}
			fmt.Fprintf(cmd.OutOrStdout(), "openneko web listening on %s, app from %s, Node on %s\n", ln.Addr(), dir, nodeAddr)
			errc := make(chan error, 1)
			go func() { errc <- srv.Serve(ln) }()

			var result error
			select {
			case err := <-errc:
				result = err
			case err := <-nodeDone:
				node = nil
				result = nodeExit(err)
			case <-ctx.Done():
			}
			shut, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_ = srv.Shutdown(shut)
			if node != nil {
				_ = node.Process.Signal(syscall.SIGTERM)
				select {
				case err := <-nodeDone:
					if result == nil && ctx.Err() == nil {
						result = nodeExit(err)
					}
				case <-time.After(nodeStopGrace):
					_ = node.Process.Kill()
					<-nodeDone
				}
			}
			return result
		},
	}
	cmd.Flags().StringVar(&addr, "addr", envOr("OPENNEKO_WEB_ADDR", webPortAddr()), "Listen address; without OPENNEKO_WEB_ADDR, PORT sets the port on all interfaces")
	cmd.Flags().StringVar(&dir, "dir", envOr("OPENNEKO_WEB_DIR", "apps/web/dist"), "Folder of the built web app, from pnpm --filter @neko/web build:spa")
	cmd.Flags().StringVar(&nodeAddr, "node-addr", envOr("OPENNEKO_WEB_NODE_ADDR", "127.0.0.1:3001"), "Address of the Node server")
	cmd.Flags().StringVar(&workerURL, "worker-admin-url", envOr("WORKER_ADMIN_URL", "http://127.0.0.1:4100"), "Worker admin address that reports whether sign-in is on")
	return cmd
}

func nodeExit(err error) error {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return WithExit(max(exit.ExitCode(), 1), fmt.Errorf("Node stopped: %w", err))
	}
	if err != nil {
		return fmt.Errorf("Node stopped: %w", err)
	}
	return nil
}

func webPortAddr() string {
	if port := os.Getenv("PORT"); port != "" {
		return ":" + port
	}
	return "127.0.0.1:3000"
}
