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
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"

	"github.com/open-neko/neko/apps/openneko/assets"
	"github.com/open-neko/neko/apps/openneko/internal/config"
	"github.com/open-neko/neko/apps/openneko/internal/db"
	"github.com/open-neko/neko/apps/openneko/internal/jobs"
	"github.com/open-neko/neko/apps/openneko/internal/native"
	"github.com/open-neko/neko/apps/openneko/internal/version"
	"github.com/open-neko/neko/apps/openneko/internal/webfront"
)

const jobsStopGrace = 5 * time.Second

func newWebCmd() *cobra.Command {
	var addr, dir string
	var runJobs bool
	cmd := &cobra.Command{
		Use:   "web",
		Args:  cobra.NoArgs,
		Short: "Serve the web app, its request gate, and the /api/v1 HTTP API",
		Long: `Serves the static web app, applies the request gate, and answers /api/v1
and its /api aliases.

Web runs in production mode unless OPENNEKO_ENV is development. Its cookies
are Secure when OPENNEKO_PUBLIC_URL is https, or when no public URL is set in
production mode; an http public URL gives plain cookies. It reads and writes users, sessions, devices, groups, the
business profile, personal pack connections and the audit chain with the main
database login, and reads secrets.json for pack OAuth client IDs and sign-in
settings.

Web runs the background jobs on the River queue in the river schema. With
--jobs=false (OPENNEKO_WEB_JOBS=false) web runs no job; it still queues jobs
for a web that runs them.

POST /internal/jobs/wake with "Authorization: Bearer $OPENNEKO_JOBS_WAKE_TOKEN"
inserts the due cron jobs. On a web with its jobs off, the call then runs the
queues until no job is ready or max_seconds (default 50, at most 300) passes.
Without OPENNEKO_JOBS_WAKE_TOKEN, every call gets 401.

With OPENNEKO_EVAL_TRACE_FILE set, web appends each run's semantic trace
with its step content (query text and rows, scrubbed) to that file, mode
600. Only an eval install sets it. Web refuses a path inside the agent,
stage or files folders.

Web refuses to start on a database that lacks a migration of this release,
unless OPENNEKO_SKIP_MIGRATE is set.

With OPENNEKO_WEB_ADMIN_SOCKET set, web serves the pack routes of
` + "`openneko pack`" + ` on that Unix socket, mode 600. The socket has no other
check.

The admin password route changes the neko role's password and writes it to
config.json. With OPENNEKO_RESTART_WEB_ON_PASSWORD_CHANGE=1, web then stops
with exit code 0, so its supervisor starts it again.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			native.FollowSupervisor()
			if local, _ := config.ReadLocal(""); local.SecretsBackendError() != nil {
				return local.SecretsBackendError()
			}
			if err := webfront.Check(dir); err != nil {
				return err
			}
			if err := bundledAssetsCheck(); err != nil {
				return err
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
			webfront.ReportDeploymentProfile(func(format string, args ...any) { fmt.Fprintf(cmd.OutOrStdout(), format+"\n", args...) })
			tracePath := os.Getenv(webfront.EvalTraceEnv)
			trace, err := webfront.OpenEvalTrace(tracePath, webfront.EvalTraceRefused(envOr("OPENNEKO_FILES_DIR", filepath.Join(dataHome(), "openneko", "files"))), os.Environ())
			if err != nil {
				return err
			}
			defer trace.Close()
			if trace != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "openneko web: the eval trace is on; every run's queries and rows go to %s\n", tracePath)
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
			secretsDir := config.Dir("")
			gate := &webfront.Gate{SecretsDir: secretsDir, DB: pool}
			api := newWebAPI(gate, pool, webfront.AppVersion(dir, version.Version), secretsDir, stop, inserter, trace, cmd.ErrOrStderr())
			api.CheckAuditWebhook(cmd.ErrOrStderr())
			doorCtx, stopDoor := context.WithCancel(context.WithoutCancel(ctx))
			defer stopDoor()
			doorDone, err := api.StartKeyDoor(doorCtx, cmd.OutOrStdout())
			if err != nil {
				_ = ln.Close()
				return err
			}
			if socket := strings.TrimSpace(os.Getenv(webAdminSocketEnv)); socket != "" {
				stopAdmin, err := serveWebAdmin(socket, api.OperatorHandler())
				if err != nil {
					_ = ln.Close()
					return fmt.Errorf("admin socket: %w", err)
				}
				defer stopAdmin()
			}
			results, err := api.ConvertConfigRepos(ctx, false)
			reportConfigRepos(cmd.OutOrStdout(), results, false)
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "openneko web: config repository not moved: %v\n", err)
			}
			queue := &jobs.Queue{Pool: pool, MetricsOn: api.MetricsRefresh, MetricSweep: api.MetricSweep, MemorySweep: api.MemorySweep, CronSweep: api.CronSweep,
				DirectorySync: api.DirectorySync, ActionExecute: api.ActionExecute,
				SkillLearn: api.SkillLearn, BotNameSweep: api.BotNameSweep, Build: api.RunBuild, WorkflowRunFire: api.WorkflowRunFire,
				WorkflowAPISweep: api.WorkflowAPISweep, SSOSetupPoll: api.SSOSetupPoll, WorkRun: api.WorkRun, ChannelDeliver: api.ChannelDeliver, WebPush: api.WebPushDeliver,
				Embedding: jobs.Embedding{Model: api.EmbeddingModel, Embed: api.EmbedText},
				Logf:      func(format string, args ...any) { fmt.Fprintf(cmd.OutOrStdout(), format+"\n", args...) }}
			runner := startWebJobs(ctx, api, cmd.OutOrStdout(), cmd.ErrOrStderr(), func() *jobs.Runner {
				return startJobs(context.WithoutCancel(ctx), runJobs, queue, cmd.OutOrStdout(), cmd.ErrOrStderr())
			})
			webfront.SweepStagesEvery(ctx, 5*time.Minute)
			if _, err := api.MigrateLegacyChannelSecrets(); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "openneko web: channel settings not moved: %v\n", err)
			}
			if _, err := api.MigrateLegacySignInSecrets(); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "openneko web: sign-in settings not moved: %v\n", err)
			}
			if err := api.EnsureVAPIDKeys(); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "openneko web: VAPID keys not ready: %v\n", err)
			}
			if warning := api.ProviderCredentialWarning(ctx); warning != "" {
				fmt.Fprintln(cmd.ErrOrStderr(), warning)
			}
			if err := api.SeedDefaultActionPolicies(ctx); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "openneko web: default action policies not seeded: %v\n", err)
			}
			if strings.TrimSpace(os.Getenv("OPENNEKO_SERVE_URL")) != "" {
				go func() {
					moved, missing, err := api.MoveLibraryDocuments(context.WithoutCancel(ctx))
					if err != nil {
						fmt.Fprintf(cmd.ErrOrStderr(), "openneko web: library move stopped: %v\n", err)
					}
					if moved > 0 || missing > 0 {
						fmt.Fprintf(cmd.OutOrStdout(), "openneko web: moved %d library document(s) into openneko serve; %d failed\n", moved, missing)
					}
				}()
			}
			if runner != nil {
				api.StartChannelInbound(ctx)
				api.StartPackOAuthRefresh(ctx)
				api.StartWorkflowScheduler(ctx)
				api.StartRunReconcile(ctx)
				api.StartKnowledgeRefresh(ctx)
				api.StartSubscriptionManager(ctx)
			}
			front := &webfront.Front{
				App:  os.DirFS(dir),
				Gate: gate,
				API:  api,
				Wake: &webfront.Wake{Token: strings.TrimSpace(os.Getenv("OPENNEKO_JOBS_WAKE_TOKEN")), Queue: queue, JobsOn: runner != nil},
			}
			srv := &http.Server{Handler: front, ReadHeaderTimeout: 10 * time.Second}
			fmt.Fprintf(cmd.OutOrStdout(), "openneko web listening on %s, app from %s\n", ln.Addr(), dir)
			errc := make(chan error, 1)
			go func() { errc <- srv.Serve(ln) }()

			var result error
			select {
			case err := <-errc:
				if !errors.Is(err, http.ErrServerClosed) {
					result = err
				}
			case err := <-doorDone:
				result = fmt.Errorf("the key door stopped: %w", err)
			case <-ctx.Done():
			}
			shut, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_ = srv.Shutdown(shut)
			stopTurns, cancelTurns := context.WithTimeout(context.Background(), webfront.TurnStopGrace)
			api.StopTurns(stopTurns)
			cancelTurns()
			if runner != nil {
				stopJobs, cancelJobs := context.WithTimeout(context.Background(), jobsStopGrace)
				runner.Stop(stopJobs)
				cancelJobs()
			}
			return result
		},
	}
	cmd.Flags().StringVar(&addr, "addr", envOr("OPENNEKO_WEB_ADDR", webPortAddr()), "Listen address; without OPENNEKO_WEB_ADDR, PORT sets the port on all interfaces")
	cmd.Flags().StringVar(&dir, "dir", envOr("OPENNEKO_WEB_DIR", "apps/web/dist"), "Folder of the built web app, from pnpm --filter @neko/web build:spa")
	cmd.Flags().BoolVar(&runJobs, "jobs", envBool("OPENNEKO_WEB_JOBS", true), "Run the background jobs; false leaves them to another web")
	return cmd
}

// startWebJobs applies the Concurrent jobs setting before the first job can
// take an agent slot.
func startWebJobs(ctx context.Context, api *webfront.API, out, errOut io.Writer, start func() *jobs.Runner) *jobs.Runner {
	if n, err := api.ApplyAgentConcurrency(ctx); err != nil {
		fmt.Fprintf(errOut, "openneko web: the Concurrent jobs setting was not read: %v\n", err)
	} else {
		fmt.Fprintf(out, "openneko web: %d agent turns run at once\n", n)
	}
	api.StartAgentConcurrencyRefresh(ctx, func(format string, args ...any) { fmt.Fprintf(errOut, format+"\n", args...) })
	return start()
}

func newWebAPI(gate *webfront.Gate, pool *pgxpool.Pool, appVersion, secretsDir string, stop func(), inserter *jobs.Inserter, trace *webfront.EvalTrace, errOut io.Writer) *webfront.API {
	api := &webfront.API{Gate: gate, DB: pool, Version: appVersion, Development: webfront.Development(), SecretsDir: secretsDir,
		Restart: stop, Jobs: inserter, EvalTrace: trace, MetricsRefresh: jobs.MetricsRefreshOn()}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if warning := api.CookieWarning(ctx); warning != "" {
		fmt.Fprintln(errOut, warning)
	}
	return api
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

func webPortAddr() string {
	if port := os.Getenv("PORT"); port != "" {
		return ":" + port
	}
	return "127.0.0.1:3000"
}
