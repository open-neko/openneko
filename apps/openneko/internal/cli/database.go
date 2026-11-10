package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/spf13/cobra"
)

var errDatabaseNotReady = errors.New("OpenNeko's database is not ready; run the SQL above, then start OpenNeko again")

const (
	minPostgresMajor = 16
	minPgvector      = "0.6.0"
)

func newDatabaseCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "database",
		Short: "Check OpenNeko's database, or move a bundled one to a database that you run",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "check",
		Short: "Connect to OpenNeko's database and check TLS, the vector extension and the rights the start steps need",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			target, err := defaultConn()
			if err != nil {
				return err
			}
			return checkDatabase(cmd.Context(), target.DSN(), cmd.OutOrStdout())
		},
	})
	cmd.AddCommand(newDatabaseInfoCmd(), newDatabaseDumpCmd(), newDatabaseRestoreCmd(), newDatabaseForeignObjectsCmd(), newDatabaseEmptyCmd(), newDatabaseMoveCmd())
	return cmd
}

type databaseFinding struct {
	level string
	text  string
	sql   []string
}

func checkDatabase(ctx context.Context, dsn string, out io.Writer) error {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return fmt.Errorf("OpenNeko's database URL: %w", err)
	}
	fmt.Fprintf(out, "database: %s@%s:%d/%s\n", cfg.User, cfg.Host, cfg.Port, cfg.Database)
	connectCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	conn, err := pgx.ConnectConfig(connectCtx, cfg)
	if err != nil {
		return fmt.Errorf("cannot connect to OpenNeko's database at %s:%d: %w", cfg.Host, cfg.Port, err)
	}
	defer conn.Close(context.Background())

	findings, err := databaseFindings(ctx, conn, sslModeOf(dsn), cfg.User, cfg.Database)
	if err != nil {
		return err
	}
	failed := false
	for _, f := range findings {
		fmt.Fprintf(out, "  %-4s  %s\n", f.level, f.text)
		if len(f.sql) > 0 {
			fmt.Fprintf(out, "        Run this SQL as a superuser, in database %s:\n", cfg.Database)
			for _, line := range f.sql {
				fmt.Fprintf(out, "          %s\n", line)
			}
		}
		if f.level == "FAIL" {
			failed = true
		}
	}
	if failed {
		return errDatabaseNotReady
	}
	return nil
}

func databaseFindings(ctx context.Context, conn *pgx.Conn, sslMode, user, database string) ([]databaseFinding, error) {
	var findings []databaseFinding
	var version string
	var versionNum int
	if err := conn.QueryRow(ctx, "select current_setting('server_version'), current_setting('server_version_num')::int").Scan(&version, &versionNum); err != nil {
		return nil, err
	}
	if versionNum/10000 < minPostgresMajor {
		findings = append(findings, databaseFinding{level: "FAIL", text: fmt.Sprintf("connected to PostgreSQL %s. OpenNeko needs PostgreSQL %d or later", version, minPostgresMajor)})
	} else {
		findings = append(findings, databaseFinding{level: "ok", text: "connected (PostgreSQL " + version + ")"})
	}

	var tlsOn bool
	var tlsVersion string
	if err := conn.QueryRow(ctx, `select coalesce(ssl, false), coalesce(version, '') from pg_stat_ssl where pid = pg_backend_pid()`).Scan(&tlsOn, &tlsVersion); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	switch {
	case !tlsOn:
		findings = append(findings, databaseFinding{level: "warn", text: fmt.Sprintf("TLS is off (sslmode=%s). CMMC 3.13.8 needs sslmode=verify-full", sslMode)})
	case sslMode != "verify-full":
		findings = append(findings, databaseFinding{level: "warn", text: fmt.Sprintf("TLS is on (%s), and sslmode=%s does not check the server's host name. CMMC 3.13.8 needs sslmode=verify-full", tlsVersion, sslMode)})
	default:
		findings = append(findings, databaseFinding{level: "ok", text: fmt.Sprintf("TLS is on (%s), sslmode=verify-full", tlsVersion)})
	}

	quotedUser := pgx.Identifier{user}.Sanitize()
	var ownsDatabase, createInDatabase, createInSchema bool
	if err := conn.QueryRow(ctx, `select pg_has_role(d.datdba, 'USAGE'), has_database_privilege(d.oid, 'CREATE'), has_schema_privilege('public', 'CREATE') from pg_database d where d.datname = current_database()`).Scan(&ownsDatabase, &createInDatabase, &createInSchema); err != nil {
		return nil, err
	}
	switch {
	case ownsDatabase:
		findings = append(findings, databaseFinding{level: "ok", text: user + " owns " + database})
	case createInDatabase:
		findings = append(findings, databaseFinding{level: "ok", text: user + " can create schemas in " + database})
	default:
		findings = append(findings, databaseFinding{level: "FAIL", text: user + " cannot create schemas in " + database + ". The start steps and the job queue create their own schemas",
			sql: []string{fmt.Sprintf("GRANT CREATE ON DATABASE %s TO %s;", pgx.Identifier{database}.Sanitize(), quotedUser)}})
	}
	if createInSchema {
		findings = append(findings, databaseFinding{level: "ok", text: user + " can create tables in schema public"})
	} else {
		findings = append(findings, databaseFinding{level: "FAIL", text: user + " cannot create tables in schema public",
			sql: []string{fmt.Sprintf("GRANT CREATE ON SCHEMA public TO %s;", quotedUser)}})
	}

	findings = append(findings, vectorFinding(ctx, conn, database))
	for _, role := range []string{filesRole, subsRole} {
		f, err := roleFinding(ctx, conn, role, quotedUser)
		if err != nil {
			return nil, err
		}
		findings = append(findings, f)
	}
	foreign, err := foreignObjects(ctx, conn, nil)
	if err != nil {
		return nil, err
	}
	if len(foreign) > 0 {
		shown := foreign
		if len(shown) > 5 {
			shown = append(shown[:5:5], fmt.Sprintf("and %d more", len(foreign)-5))
		}
		findings = append(findings, databaseFinding{level: "warn", text: fmt.Sprintf("%s owns %d objects in %s that OpenNeko did not make (%s). A restore stops on them. Give OpenNeko a database and a login of its own",
			user, len(foreign), database, strings.Join(shown, ", "))})
	}
	return findings, nil
}

func vectorFinding(ctx context.Context, conn *pgx.Conn, database string) databaseFinding {
	var installed, available string
	_ = conn.QueryRow(ctx, `select coalesce((select extversion from pg_extension where extname = 'vector'), ''), coalesce((select default_version from pg_available_extensions where name = 'vector'), '')`).Scan(&installed, &available)
	return vectorVerdict(installed, available, database, func() bool { return probe(ctx, conn, "create extension vector") })
}

func vectorVerdict(installed, available, database string, canCreate func() bool) databaseFinding {
	switch {
	case installed != "" && olderVersion(installed, minPgvector):
		if available != "" && !olderVersion(available, minPgvector) {
			return databaseFinding{level: "FAIL", text: fmt.Sprintf("the vector extension is version %s. OpenNeko needs pgvector %s or later, and the server has %s", installed, minPgvector, available),
				sql: []string{"ALTER EXTENSION vector UPDATE;"}}
		}
		return databaseFinding{level: "FAIL", text: fmt.Sprintf("the vector extension is version %s. Install pgvector %s or later on the Postgres server, then update the extension", installed, minPgvector),
			sql: []string{"ALTER EXTENSION vector UPDATE;"}}
	case installed != "":
		return databaseFinding{level: "ok", text: "the vector extension exists (pgvector " + installed + ")"}
	case available == "":
		return databaseFinding{level: "FAIL", text: "the server has no pgvector. Install pgvector " + minPgvector + " or later on the Postgres server first",
			sql: []string{"CREATE EXTENSION vector;"}}
	case olderVersion(available, minPgvector):
		return databaseFinding{level: "FAIL", text: fmt.Sprintf("the server has pgvector %s. Install pgvector %s or later on the Postgres server first", available, minPgvector),
			sql: []string{"CREATE EXTENSION vector;"}}
	case canCreate():
		return databaseFinding{level: "ok", text: "the vector extension can be created (pgvector " + available + ")"}
	default:
		return databaseFinding{level: "FAIL", text: "the vector extension does not exist, and this login cannot create it in " + database,
			sql: []string{"CREATE EXTENSION vector;"}}
	}
}

func olderVersion(version, floor string) bool {
	have, want := versionParts(version), versionParts(floor)
	for i := range want {
		if i >= len(have) {
			return true
		}
		if have[i] != want[i] {
			return have[i] < want[i]
		}
	}
	return false
}

func versionParts(version string) []int {
	var parts []int
	for _, field := range strings.Split(version, ".") {
		n := 0
		for _, r := range field {
			if r < '0' || r > '9' {
				break
			}
			n = n*10 + int(r-'0')
		}
		parts = append(parts, n)
	}
	return parts
}

func roleFinding(ctx context.Context, conn *pgx.Conn, role, quotedUser string) (databaseFinding, error) {
	var exists bool
	if err := conn.QueryRow(ctx, `select exists (select from pg_roles where rolname = $1)`, role).Scan(&exists); err != nil {
		return databaseFinding{}, err
	}
	quotedRole := pgx.Identifier{role}.Sanitize()
	if !exists {
		if probe(ctx, conn, "create role "+quotedRole+" nologin") {
			return databaseFinding{level: "ok", text: "the " + role + " role can be created"}, nil
		}
		return databaseFinding{level: "FAIL", text: "this login cannot create the " + role + " role",
			sql: []string{fmt.Sprintf("ALTER ROLE %s CREATEROLE;", quotedUser)}}, nil
	}
	if probe(ctx, conn, "alter role "+quotedRole+" with login password null") {
		return databaseFinding{level: "ok", text: "the " + role + " role exists, and this login can set its password"}, nil
	}
	return databaseFinding{level: "FAIL", text: "the " + role + " role exists, and this login cannot set its password",
		sql: []string{
			fmt.Sprintf("ALTER ROLE %s CREATEROLE;", quotedUser),
			fmt.Sprintf("GRANT %s TO %s WITH ADMIN OPTION;", quotedRole, quotedUser),
		}}, nil
}

// probe runs statement in a transaction that it always rolls back.
func probe(ctx context.Context, conn *pgx.Conn, statement string) bool {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return false
	}
	defer tx.Rollback(context.Background())
	_, err = tx.Exec(ctx, statement)
	return err == nil
}

func sslModeOf(dsn string) string {
	if u, err := url.Parse(dsn); err == nil && (u.Scheme == "postgres" || u.Scheme == "postgresql") {
		if mode := u.Query().Get("sslmode"); mode != "" {
			return mode
		}
		return "prefer"
	}
	for _, field := range strings.Fields(dsn) {
		if mode, ok := strings.CutPrefix(field, "sslmode="); ok {
			return mode
		}
	}
	return "prefer"
}
