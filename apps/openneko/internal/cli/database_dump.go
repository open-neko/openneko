package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/spf13/cobra"

	"github.com/open-neko/neko/apps/openneko/internal/db"
)

func newDatabaseInfoCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "info",
		Short:  "Print the login, the database and the server version",
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
			var identity string
			if err := conn.QueryRow(cmd.Context(), `select current_user || '|' || current_database() || '|' || current_setting('server_version')`).Scan(&identity); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), identity)
			return nil
		},
	}
}

func newDatabaseDumpCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "dump",
		Short:  "Write a pg_dump custom-format archive of OpenNeko's database to stdout",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			target, err := defaultConn()
			if err != nil {
				return err
			}
			dsn, password, err := splitDatabasePassword(target.DSN())
			if err != nil {
				return err
			}
			conn, err := pgx.Connect(cmd.Context(), target.DSN())
			if err != nil {
				return fmt.Errorf("connect to OpenNeko's database: %w", err)
			}
			exclude, err := dumpExclusions(cmd.Context(), conn)
			conn.Close(context.Background())
			if err != nil {
				return err
			}
			args := append([]string{"--format=custom", "--no-password", "--dbname=" + dsn}, exclude...)
			return runPostgresTool(cmd.Context(), nil, cmd.OutOrStdout(), password, "pg_dump", args...)
		},
	}
}

func newDatabaseRestoreCmd() *cobra.Command {
	var dropForeign bool
	cmd := &cobra.Command{
		Use:    "restore",
		Short:  "Replace OpenNeko's objects in its database with the pg_dump archive on stdin",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			target, err := defaultConn()
			if err != nil {
				return err
			}
			return restoreExternalDatabase(cmd.Context(), target.DSN(), cmd.InOrStdin(), cmd.OutOrStdout(), dropForeign)
		},
	}
	cmd.Flags().BoolVar(&dropForeign, "drop-foreign-objects", false, "also drop the objects that the login owns and OpenNeko did not make")
	return cmd
}

// dumpExclusions keeps the dump to what the login owns, the same objects
// that restore replaces. pg_dump otherwise locks every table and fails on one
// that the login cannot read.
func dumpExclusions(ctx context.Context, conn *pgx.Conn) ([]string, error) {
	rows, err := conn.Query(ctx, `
WITH me AS (SELECT oid FROM pg_roles WHERE rolname = current_user),
other_schemas AS (
  SELECT n.oid, n.nspname FROM pg_namespace n, me
  WHERE n.nspowner <> me.oid AND n.nspname <> 'public' AND n.nspname <> 'information_schema'
    AND n.nspname NOT LIKE 'pg\_%'
)
SELECT '--exclude-schema=' || quote_ident(nspname) FROM other_schemas
UNION ALL
SELECT '--exclude-table=' || quote_ident(n.nspname) || '.' || quote_ident(c.relname)
FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace, me
WHERE c.relowner <> me.oid AND c.relkind IN ('r', 'p', 'v', 'm', 'f', 'S')
  AND n.nspname <> 'information_schema' AND n.nspname NOT LIKE 'pg\_%'
  AND n.oid NOT IN (SELECT oid FROM other_schemas)
  AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.classid = 'pg_class'::regclass AND d.objid = c.oid AND d.deptype = 'e')
ORDER BY 1`)
	if err != nil {
		return nil, fmt.Errorf("list the objects that other roles own: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// splitDatabasePassword takes the password out of a URL, so it reaches
// pg_dump and pg_restore through PGPASSWORD and not the command line.
func splitDatabasePassword(dsn string) (string, string, error) {
	u, err := url.Parse(dsn)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		return "", "", fmt.Errorf("OpenNeko's database URL must be a postgres:// URL")
	}
	if u.User == nil {
		return dsn, "", nil
	}
	password, _ := u.User.Password()
	u.User = url.User(u.User.Username())
	return u.String(), password, nil
}

func runPostgresTool(ctx context.Context, stdin io.Reader, stdout io.Writer, password string, name string, args ...string) error {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = &stderr
	cmd.Env = os.Environ()
	if password != "" {
		cmd.Env = append(cmd.Env, "PGPASSWORD="+password)
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// clearOwnedObjectsSQL drops every object that the login owns in the
// database, except extensions and their members. Extensions stay because
// the login may not have the right to create them again. DROP OWNED is not
// used: it also revokes grants that an operator gave the login.
const clearOwnedObjectsSQL = `
DO $$
DECLARE
  item record;
  me oid := (SELECT oid FROM pg_roles WHERE rolname = current_user);
BEGIN
  FOR item IN
    SELECT format('DROP SCHEMA IF EXISTS %I CASCADE', n.nspname) AS statement
    FROM pg_namespace n
    WHERE n.nspowner = me AND n.nspname <> 'public'
      AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.classid = 'pg_namespace'::regclass AND d.objid = n.oid AND d.deptype = 'e')
  LOOP
    EXECUTE item.statement;
  END LOOP;

  FOR item IN
    SELECT format('DROP %s IF EXISTS %s CASCADE',
             CASE c.relkind WHEN 'v' THEN 'VIEW' WHEN 'm' THEN 'MATERIALIZED VIEW' WHEN 'f' THEN 'FOREIGN TABLE' WHEN 'S' THEN 'SEQUENCE' ELSE 'TABLE' END,
             c.oid::regclass::text) AS statement
    FROM pg_class c
    WHERE c.relowner = me AND c.relkind IN ('r', 'p', 'v', 'm', 'f', 'S')
      AND c.relnamespace = 'public'::regnamespace
      AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.classid = 'pg_class'::regclass AND d.objid = c.oid AND d.deptype IN ('e', 'a', 'i'))
    ORDER BY c.relkind = 'S'
  LOOP
    EXECUTE item.statement;
  END LOOP;

  FOR item IN
    SELECT format('DROP ROUTINE IF EXISTS %s CASCADE', p.oid::regprocedure::text) AS statement
    FROM pg_proc p
    WHERE p.proowner = me AND p.pronamespace = 'public'::regnamespace AND p.prokind <> 'a'
      AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.classid = 'pg_proc'::regclass AND d.objid = p.oid AND d.deptype = 'e')
  LOOP
    EXECUTE item.statement;
  END LOOP;

  FOR item IN
    SELECT format('DROP %s IF EXISTS %s CASCADE', CASE t.typtype WHEN 'd' THEN 'DOMAIN' ELSE 'TYPE' END, t.oid::regtype::text) AS statement
    FROM pg_type t
    WHERE t.typowner = me AND t.typnamespace = 'public'::regnamespace
      AND t.typtype IN ('e', 'd', 'r', 'm', 'c') AND t.typelem = 0
      AND (t.typtype <> 'c' OR (SELECT relkind FROM pg_class WHERE oid = t.typrelid) = 'c')
      AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.classid = 'pg_type'::regclass AND d.objid = t.oid AND d.deptype = 'e')
  LOOP
    EXECUTE item.statement;
  END LOOP;
END $$`

// restoreExternalDatabase replaces OpenNeko's objects in a database that the
// operator runs. It cannot drop and recreate the database, so it clears what
// the login owns, then restores the archive in one transaction and its grants
// in a second. The archive's owners, its extension entries and its grants to
// serve roles are skipped: the login owns what it restores, the extensions
// stay as the operator made them, and another install's role may not exist.
func restoreExternalDatabase(ctx context.Context, dsn string, archive io.Reader, out io.Writer, dropForeign bool) error {
	spool, err := spoolArchive(archive)
	if err != nil {
		return err
	}
	defer os.Remove(spool)

	plain, password, err := splitDatabasePassword(dsn)
	if err != nil {
		return err
	}
	archived, contents, err := archiveContents(ctx, spool)
	if err != nil {
		return err
	}
	list, err := os.CreateTemp("", "openneko-restore-*.list")
	if err != nil {
		return err
	}
	defer os.Remove(list.Name())
	mainList, grantList := restoreList(contents)
	if _, err := io.WriteString(list, mainList); err != nil {
		list.Close()
		return err
	}
	if err := list.Close(); err != nil {
		return err
	}
	grantFile, err := os.CreateTemp("", "openneko-restore-grants-*.list")
	if err != nil {
		return err
	}
	defer os.Remove(grantFile.Name())
	if _, err := io.WriteString(grantFile, grantList); err != nil {
		grantFile.Close()
		return err
	}
	if err := grantFile.Close(); err != nil {
		return err
	}

	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return fmt.Errorf("connect to OpenNeko's database: %w", err)
	}
	defer conn.Close(context.Background())
	var user, database string
	if err := conn.QueryRow(ctx, `select current_user, current_database()`).Scan(&user, &database); err != nil {
		return err
	}
	foreign, err := foreignObjects(ctx, conn, archived)
	if err != nil {
		return err
	}
	if len(foreign) > 0 && !dropForeign {
		return foreignObjectsError(user, database, foreign)
	}
	fmt.Fprintf(out, "Clearing the objects that %s owns in database %s...\n", user, database)
	if _, err := conn.Exec(ctx, clearOwnedObjectsSQL); err != nil {
		return fmt.Errorf("clear database %s: %w", database, err)
	}
	if _, err := conn.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS vector`); err != nil {
		return fmt.Errorf("database %s has no vector extension, and %s cannot create it: %w", database, user, err)
	}
	fmt.Fprintf(out, "Restoring database %s...\n", database)
	if err := runPostgresTool(ctx, nil, io.Discard, password, "pg_restore",
		"--no-owner", "--exit-on-error", "--single-transaction", "--no-password",
		"--use-list="+list.Name(), "--dbname="+plain, spool); err != nil {
		return err
	}
	if strings.TrimSpace(grantList) == "" {
		return nil
	}
	var script strings.Builder
	if err := runPostgresTool(ctx, nil, &script, "", "pg_restore", "--use-list="+grantFile.Name(), "--file=-", spool); err != nil {
		return err
	}
	grants := grantStatements(script.String())
	if grants == "" {
		return nil
	}
	if _, err := conn.Exec(ctx, grants); err != nil {
		return fmt.Errorf("restore the grants: %w", err)
	}
	return nil
}

// restoreList splits a pg_restore --list table of contents. The first list
// holds every entry except extensions and grants; the second holds the grants.
func restoreList(contents string) (string, string) {
	var main, grants strings.Builder
	for _, line := range strings.Split(contents, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 4 && !strings.HasPrefix(line, ";") {
			if fields[3] == "EXTENSION" || (fields[3] == "COMMENT" && len(fields) >= 6 && fields[5] == "EXTENSION") {
				continue
			}
			if fields[3] == "ACL" {
				grants.WriteString(line)
				grants.WriteByte('\n')
				continue
			}
		}
		main.WriteString(line)
		main.WriteByte('\n')
	}
	return main.String(), grants.String()
}

var grantee = regexp.MustCompile(`(?i)\s(?:TO|FROM)\s+("(?:[^"]|"")+"|[a-z0-9_$]+)`)

// grantStatements keeps the GRANT and REVOKE lines of a pg_restore script,
// except those that name a serve role of any install or release. serve-init
// grants this install's roles at the next start.
func grantStatements(script string) string {
	var b strings.Builder
	for _, line := range strings.Split(script, "\n") {
		upper := strings.ToUpper(line)
		if (strings.HasPrefix(upper, "GRANT ") || strings.HasPrefix(upper, "REVOKE ")) && !servesRole(line) {
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func servesRole(line string) bool {
	for _, m := range grantee.FindAllStringSubmatch(line, -1) {
		name := m[1]
		if strings.HasPrefix(name, `"`) {
			name = strings.ReplaceAll(name[1:len(name)-1], `""`, `"`)
		}
		if db.IsServeRole(name) {
			return true
		}
	}
	return false
}
