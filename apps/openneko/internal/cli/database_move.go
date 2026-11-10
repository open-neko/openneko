package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/spf13/cobra"

	"github.com/open-neko/neko/apps/openneko/assets"
	"github.com/open-neko/neko/apps/openneko/internal/compose"
	"github.com/open-neko/neko/apps/openneko/internal/config"
	"github.com/open-neko/neko/apps/openneko/internal/prompt"
)

func newDatabaseMoveCmd() *cobra.Command {
	var target, confirmation string
	cmd := &cobra.Command{
		Use:   "move --database-url URL --confirm MOVE",
		Short: "Move a prod install from the bundled neko-db to a Postgres database that you run",
		Long: `Move the data of an install that runs the bundled neko-db into the database
that the URL names. The command checks the database and refuses it when the
URL's login already owns tables there. It stops the stack, takes a backup,
restores the backup into the database and starts the stack on it. Only then
does the install store the URL. If a step fails, the install still uses
neko-db; run ` + "`openneko start -d`" + ` to go back to it.

The neko-db volume stays until you remove it.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if confirmation != "MOVE" {
				return errors.New("move stops the stack and writes to the database that the URL names; pass --confirm MOVE exactly")
			}
			target = strings.TrimSpace(target)
			if target == "" {
				if !prompt.IsInteractive() {
					return errors.New("--database-url is required")
				}
				answer, err := prompt.Hidden("The new database URL (postgres://USER:PASSWORD@HOST:5432/DATABASE?sslmode=verify-full): ")
				if err != nil {
					return err
				}
				target = strings.TrimSpace(answer)
			}
			return runDatabaseMove(cmd.Context(), cmd, target)
		},
	}
	cmd.Flags().StringVar(&target, "database-url", "", "URL of the database to move to (asked for when not given)")
	cmd.Flags().StringVar(&confirmation, "confirm", "", "type MOVE to allow the move")
	return cmd
}

func runDatabaseMove(ctx context.Context, cmd *cobra.Command, target string) error {
	out := cmd.OutOrStdout()
	if err := checkDatabaseURLShape(target); err != nil {
		return err
	}
	sup := compose.New(assets.ComposeFS)
	project, err := sup.ProjectName("")
	if err != nil {
		return err
	}
	if mode, ok := compose.ModeFromProjectName(project); ok && mode != compose.ModeProd {
		return fmt.Errorf("%s runs in %s mode, which always runs its own database; move is for a prod install", project, mode)
	}
	current, err := resolveStackDatabase(ctx, sup, compose.ModeProd, project)
	if err != nil {
		return err
	}
	if !current.bundled {
		return fmt.Errorf("%s already uses a database URL; move is for an install that runs the bundled neko-db", project)
	}
	oldFiles, err := stackFiles(ctx, sup, compose.ModeProd, project)
	if err != nil {
		return err
	}
	oldStack := backupStack{sup: sup, project: project, files: oldFiles}
	if running, err := oldStack.serviceRunning(ctx, "neko-db"); err != nil {
		return err
	} else if !running {
		return fmt.Errorf("neko-db is not running in %s; start the stack (`openneko start -d`) before the move", project)
	}

	urlFile, err := os.CreateTemp("", "openneko-move-url-*")
	if err != nil {
		return err
	}
	defer os.Remove(urlFile.Name())
	if _, err := io.WriteString(urlFile, target); err != nil {
		urlFile.Close()
		return err
	}
	if err := urlFile.Close(); err != nil {
		return err
	}
	previous, hadPrevious := os.LookupEnv("OPENNEKO_DATABASE_URL_FILE")
	if err := os.Setenv("OPENNEKO_DATABASE_URL_FILE", urlFile.Name()); err != nil {
		return err
	}
	defer func() {
		if hadPrevious {
			_ = os.Setenv("OPENNEKO_DATABASE_URL_FILE", previous)
		} else {
			_ = os.Unsetenv("OPENNEKO_DATABASE_URL_FILE")
		}
	}()
	newFiles, err := stackFiles(ctx, sup, compose.ModeProd, project)
	if err != nil {
		return err
	}
	newStack := backupStack{sup: sup, project: project, files: newFiles, external: true}

	fmt.Fprintf(out, "Moving %s to %s.\n", project, describeDatabaseURL(target))
	unchanged := func(step string, err error) error {
		return fmt.Errorf("move failed while %s. Nothing is stored: %s still uses neko-db. Run `openneko start -d` to go back to it: %w", step, project, err)
	}
	if err := runStartStep(ctx, sup, project, newFiles, startSteps(compose.ModeProd, false)[0]); err != nil {
		return fmt.Errorf("the new database failed its check; nothing was changed: %w", err)
	}
	if err := newStack.databaseTool(ctx, nil, out, "empty"); err != nil {
		return fmt.Errorf("nothing was changed: %w", err)
	}

	fmt.Fprintln(out, "Stopping web, worker and openneko-serve...")
	if err := oldStack.compose(ctx, nil, os.Stdout, "stop", "web", "worker", "openneko-serve"); err != nil {
		return unchanged("stopping the stack", err)
	}
	fmt.Fprintln(out, "Preparing the new database...")
	if err := runStartSteps(ctx, sup, project, newFiles, compose.ModeProd, nil); err != nil {
		return unchanged("preparing the new database", err)
	}
	backup, err := createBackup(ctx, sup, project, oldFiles, "", out)
	if err != nil {
		return unchanged("taking the backup", err)
	}
	if err := oldStack.compose(ctx, nil, os.Stdout, "rm", "--force", "--stop", "neko-db"); err != nil {
		return unchanged("stopping neko-db", err)
	}
	if err := runRestore(ctx, cmd, backup, "", false); err != nil {
		return unchanged("restoring the backup into the new database", err)
	}
	if err := config.WriteLocalDatabaseURL("", target); err != nil {
		return fmt.Errorf("the stack runs on the new database, and openneko could not store its URL; run `openneko setup --database-url` with the same URL before the next start: %w", err)
	}
	volume, _ := oldStack.volumeName(ctx, bundledDatabaseVolume)
	if volume == "" {
		volume = project + "_" + bundledDatabaseVolume
	}
	fmt.Fprintf(out, "Moved. %s now uses %s.\nThe backup from neko-db is %s.\nThe volume %s still holds the old data. Remove it when you no longer need it:\n  docker volume rm %s\n",
		project, describeDatabaseURL(target), backup, volume, volume)
	return nil
}

func runStartStep(ctx context.Context, sup *compose.Supervisor, project string, files []string, step startStep) error {
	code, err := sup.Run(ctx, project, files, startStepArgs(step, nil), os.Stdout, os.Stderr)
	if err != nil {
		return err
	}
	if code != 0 {
		return WithExit(code, fmt.Errorf("%s (step %s)", step.failure, step.service))
	}
	return nil
}

func newDatabaseEmptyCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "empty",
		Short:  "Fail when the login owns tables in OpenNeko's database",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			target, err := defaultConn()
			if err != nil {
				return err
			}
			conn, err := pgx.Connect(cmd.Context(), target.DSN())
			if err != nil {
				return fmt.Errorf("connect to OpenNeko's database: %w", err)
			}
			defer conn.Close(context.Background())
			return requireNoOwnedTables(cmd.Context(), conn)
		},
	}
}

func requireNoOwnedTables(ctx context.Context, conn *pgx.Conn) error {
	rows, err := conn.Query(ctx, `
SELECT c.oid::regclass::text
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE c.relowner = (SELECT oid FROM pg_roles WHERE rolname = current_user)
  AND c.relkind IN ('r', 'p', 'v', 'm', 'f')
  AND n.nspname NOT IN ('pg_catalog', 'information_schema') AND n.nspname NOT LIKE 'pg_toast%'
  AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.classid = 'pg_class'::regclass AND d.objid = c.oid AND d.deptype = 'e')
ORDER BY 1`)
	if err != nil {
		return err
	}
	tables, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	if len(tables) == 0 {
		return nil
	}
	var user, database string
	if err := conn.QueryRow(ctx, `select current_user, current_database()`).Scan(&user, &database); err != nil {
		return err
	}
	shown := tables
	if len(shown) > 5 {
		shown = shown[:5]
	}
	return fmt.Errorf("%s already owns %d tables in database %s (%s); give a database where it owns none", user, len(tables), database, strings.Join(shown, ", "))
}
