package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/open-neko/neko/apps/openneko/assets"
	"github.com/open-neko/neko/apps/openneko/internal/db"
	"github.com/open-neko/neko/apps/openneko/internal/jobs"
	"github.com/open-neko/neko/apps/openneko/internal/version"
	"github.com/open-neko/neko/apps/openneko/internal/webfront"
)

const (
	nodeStopGrace = 10 * time.Second
	jobsStopGrace = 5 * time.Second
)

func newWebCmd() *cobra.Command {
	var addr, dir, nodeAddr, workerURL string
	var runJobs bool
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

Web runs the background jobs that have moved from the Node worker to the
River queue in the river schema. With --jobs=false (OPENNEKO_WEB_JOBS=false)
web runs no job; it still queues jobs for a web that runs them.

POST /internal/jobs/wake with "Authorization: Bearer $OPENNEKO_JOBS_WAKE_TOKEN"
inserts the due cron jobs. On a web with its jobs off, the call then runs the
queues until no job is ready or max_seconds (default 50, at most 300) passes.
Without OPENNEKO_JOBS_WAKE_TOKEN, every call gets 401.

Web refuses to start on a database that lacks a migration of this release,
unless OPENNEKO_SKIP_MIGRATE is set.

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
			pool, err := db.Open(ctx, conn, "openneko web", defaultConn)
			if err != nil {
				return err
			}
			defer pool.Close()
			if err := checkSchema(ctx, cmd.ErrOrStderr(), pool); err != nil {
				return err
			}
			ln, err := net.Listen("tcp", addr)
			if err != nil {
				return err
			}
			inserter, err := jobs.NewInserter(pool, "")
			if err != nil {
				_ = ln.Close()
				return err
			}
			gate := &webfront.Gate{WorkerURL: workerURL, DB: pool}
			api := &webfront.API{Gate: gate, DB: pool, Version: webfront.AppVersion(dir, version.Version),
				SecureCookies: os.Getenv("NODE_ENV") == "production", Restart: stop, Jobs: inserter}
			queue := &jobs.Queue{Pool: pool, MetricSweep: api.MetricSweep, CronSweep: api.CronSweep,
				DirectorySync: api.DirectorySync, ActionExecute: api.ActionExecute,
				Embedding: jobs.Embedding{Model: api.EmbeddingModel, Embed: api.EmbedText},
				Logf:      func(format string, args ...any) { fmt.Fprintf(cmd.OutOrStdout(), format+"\n", args...) }}
			runner := startJobs(context.WithoutCancel(ctx), runJobs, queue, cmd.OutOrStdout(), cmd.ErrOrStderr())
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
			front := &webfront.Front{
				App:  os.DirFS(dir),
				Gate: gate,
				API:  api,
				Node: webfront.NodeProxy(&url.URL{Scheme: "http", Host: nodeAddr}),
				Wake: &webfront.Wake{Token: strings.TrimSpace(os.Getenv("OPENNEKO_JOBS_WAKE_TOKEN")), Queue: queue, JobsOn: runner != nil},
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
			if runner != nil {
				stopJobs, cancelJobs := context.WithTimeout(context.Background(), jobsStopGrace)
				runner.Stop(stopJobs)
				cancelJobs()
			}
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
	cmd.Flags().BoolVar(&runJobs, "jobs", envBool("OPENNEKO_WEB_JOBS", true), "Run the background jobs; false leaves them to another web")
	cmd.Flags().StringVar(&workerURL, "worker-admin-url", envOr("WORKER_ADMIN_URL", "http://127.0.0.1:4100"), "Worker admin address that reports whether sign-in is on")
	return cmd
}

const schemaCheckTimeout = 10 * time.Second

func envBool(name string, fallback bool) bool {
	if v, err := strconv.ParseBool(strings.TrimSpace(os.Getenv(name))); err == nil {
		return v
	}
	return fallback
}

// startJobs starts the River client, or no client when on is false. A client
// that does not start leaves web serving with its jobs off.
func startJobs(ctx context.Context, on bool, q *jobs.Queue, out, errOut io.Writer) *jobs.Runner {
	if !on {
		fmt.Fprintln(out, "openneko web: background jobs are off (--jobs=false)")
		return nil
	}
	runner, err := q.Start(ctx)
	if err != nil {
		fmt.Fprintf(errOut, "openneko web: background jobs are off: %v\n", err)
		return nil
	}
	return runner
}

// checkSchema refuses to serve a database that lacks a migration of this
// release. A database that cannot be read yet does not stop web.
func checkSchema(ctx context.Context, w io.Writer, q db.Querier) error {
	ctx, cancel := context.WithTimeout(ctx, schemaCheckTimeout)
	defer cancel()
	st, err := (&db.Migrator{FS: assets.MigrationsFS, Dir: "migrations"}).Status(ctx, q)
	if err != nil {
		fmt.Fprintf(w, "openneko web: the migration check did not run: %v\n", err)
		return nil
	}
	if n := len(st.Unknown); n > 0 {
		fmt.Fprintf(w, "openneko web: the database holds %d migration(s) that this release does not know, the last %s. A newer release migrated it.\n", n, st.Unknown[n-1])
	}
	if len(st.Pending) == 0 {
		return nil
	}
	if envTruthy("OPENNEKO_SKIP_MIGRATE") {
		fmt.Fprintf(w, "openneko web: the database lacks %d migration(s), the first %s; OPENNEKO_SKIP_MIGRATE is set, so web starts\n", len(st.Pending), st.Pending[0])
		return nil
	}
	return fmt.Errorf("the database lacks %d migration(s) of this release, the first %s: run `openneko migrate`, then start web again", len(st.Pending), st.Pending[0])
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
