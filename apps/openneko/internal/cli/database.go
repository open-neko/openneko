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

func newDatabaseCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "database",
		Short: "Check OpenNeko's database",
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
	if err := conn.QueryRow(ctx, "show server_version").Scan(&version); err != nil {
		return nil, err
	}
	findings = append(findings, databaseFinding{level: "ok", text: "connected (PostgreSQL " + version + ")"})

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
	var ownsDatabase, createInSchema bool
	if err := conn.QueryRow(ctx, `select pg_has_role(d.datdba, 'USAGE'), has_schema_privilege('public', 'CREATE') from pg_database d where d.datname = current_database()`).Scan(&ownsDatabase, &createInSchema); err != nil {
		return nil, err
	}
	if ownsDatabase {
		findings = append(findings, databaseFinding{level: "ok", text: user + " owns " + database})
	} else {
		findings = append(findings, databaseFinding{level: "FAIL", text: user + " does not own " + database + ". The start steps create schemas and reindex the database",
			sql: []string{fmt.Sprintf("ALTER DATABASE %s OWNER TO %s;", pgx.Identifier{database}.Sanitize(), quotedUser)}})
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
	return findings, nil
}

func vectorFinding(ctx context.Context, conn *pgx.Conn, database string) databaseFinding {
	var installed, available bool
	_ = conn.QueryRow(ctx, `select exists (select from pg_extension where extname = 'vector'), exists (select from pg_available_extensions where name = 'vector')`).Scan(&installed, &available)
	switch {
	case installed:
		return databaseFinding{level: "ok", text: "the vector extension exists"}
	case !available:
		return databaseFinding{level: "FAIL", text: "the server has no pgvector. Install pgvector 0.8 or later on the Postgres server first",
			sql: []string{"CREATE EXTENSION vector;"}}
	case probe(ctx, conn, "create extension vector"):
		return databaseFinding{level: "ok", text: "the vector extension can be created"}
	default:
		return databaseFinding{level: "FAIL", text: "the vector extension does not exist, and this login cannot create it in " + database,
			sql: []string{"CREATE EXTENSION vector;"}}
	}
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
