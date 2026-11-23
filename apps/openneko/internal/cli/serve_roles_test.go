package cli

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/open-neko/neko/apps/openneko/assets"
	"github.com/open-neko/neko/apps/openneko/internal/db"
	"github.com/open-neko/neko/apps/openneko/internal/gjhost"
	"github.com/open-neko/neko/apps/openneko/internal/testpg"
)

type testInstall struct {
	database, configDir, credentials string
	conn                             db.ConnConfig
}

func newTestInstall(t *testing.T, ctx context.Context, admin *pgx.Conn, base *pgx.ConnConfig, prefix string) testInstall {
	t.Helper()
	name := fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "create database "+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		admin.Exec(context.Background(), "drop database "+name+" with (force)")             //nolint:errcheck
		admin.Exec(context.Background(), "drop role if exists "+db.FilesRole(name))         //nolint:errcheck
		admin.Exec(context.Background(), "drop role if exists "+db.SubscriptionsRole(name)) //nolint:errcheck
	})
	conn := db.ConnConfig{Host: base.Host, Port: int(base.Port), User: base.User, Password: base.Password, Database: name, SSLMode: "disable"}
	mig, err := pgx.Connect(ctx, conn.DSN())
	if err != nil {
		t.Fatal(err)
	}
	defer mig.Close(ctx)
	if _, err := (&db.Migrator{FS: assets.MigrationsFS, Dir: "migrations"}).Apply(ctx, mig, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	return testInstall{database: name, configDir: t.TempDir(), credentials: filepath.Join(t.TempDir(), "credentials"), conn: conn}
}

func (i testInstall) start(t *testing.T, ctx context.Context) {
	t.Helper()
	if err := writeServeCredentials(ctx, i.conn, i.configDir, i.credentials); err != nil {
		t.Fatalf("serve-init for %s: %v", i.database, err)
	}
}

func (i testInstall) checkLogins(t *testing.T, ctx context.Context) {
	t.Helper()
	for file, query := range map[string]string{
		serveDatabaseFile: "select count(*) from file_content_file",
		serveSubsFile:     "select count(*) from workflow_output",
	} {
		raw, err := os.ReadFile(filepath.Join(i.credentials, file))
		if err != nil {
			t.Fatal(err)
		}
		dsn := strings.TrimSpace(string(raw))
		u, _ := url.Parse(dsn)
		want := db.FilesRole(i.database)
		if file == serveSubsFile {
			want = db.SubscriptionsRole(i.database)
			if _, err := gjhost.RenderSubscriptionsConfig(dsn); err != nil {
				t.Fatalf("%s: %v", i.database, err)
			}
		}
		if u.User.Username() != want {
			t.Fatalf("%s logs in as %s, want %s", file, u.User.Username(), want)
		}
		conn, err := pgx.Connect(ctx, dsn)
		if err != nil {
			t.Fatalf("%s: log in as %s: %v", i.database, want, err)
		}
		_, err = conn.Exec(ctx, query)
		conn.Close(ctx)
		if err != nil {
			t.Fatalf("%s as %s: %v", i.database, want, err)
		}
	}
}

func testServerAdmin(t *testing.T, ctx context.Context) (*pgx.Conn, *pgx.ConnConfig) {
	t.Helper()
	dsn := testpg.URL(t)
	base, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "select pg_advisory_lock(7129)"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close(context.Background()) })
	return admin, base
}

func TestTwoInstallsOnOneServerKeepEachOthersLogins(t *testing.T) {
	ctx := context.Background()
	admin, base := testServerAdmin(t, ctx)
	first := newTestInstall(t, ctx, admin, base, "install_a")
	second := newTestInstall(t, ctx, admin, base, "install_b")

	first.start(t, ctx)
	first.checkLogins(t, ctx)
	second.start(t, ctx)
	first.checkLogins(t, ctx)
	second.checkLogins(t, ctx)
	first.start(t, ctx)
	first.checkLogins(t, ctx)
	second.checkLogins(t, ctx)

	other, err := os.ReadFile(filepath.Join(second.credentials, serveSubsFile))
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(strings.TrimSpace(string(other)))
	u.Path = "/" + first.database
	conn, err := pgx.Connect(ctx, u.String())
	if err == nil {
		_, err = conn.Exec(ctx, "select count(*) from workflow_output")
		conn.Close(ctx)
	}
	if err == nil {
		t.Fatalf("%s reads workflow_output in %s", u.User.Username(), first.database)
	}
}

func TestServeInitUpgradesAnInstallThatUsedTheSharedRoles(t *testing.T) {
	ctx := context.Background()
	admin, base := testServerAdmin(t, ctx)
	install := newTestInstall(t, ctx, admin, base, "upgrade")

	for _, role := range db.LegacyServeRoles {
		var exists bool
		if err := admin.QueryRow(ctx, "select exists (select from pg_roles where rolname = $1)", role).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			if _, err := admin.Exec(ctx, "create role "+role+" nologin"); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				admin.Exec(context.Background(), "drop database if exists "+install.database+" with (force)") //nolint:errcheck
				admin.Exec(context.Background(), "drop role if exists "+role)                                 //nolint:errcheck
			})
		}
	}
	old, err := pgx.Connect(ctx, install.conn.DSN())
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close(ctx)
	if _, err := old.Exec(ctx, `grant usage on schema public to neko_files, neko_subscriptions;
grant select, insert, update, delete on file_content_file, file_content_unit, file_content_table to neko_files;
grant usage, select on sequence file_content_unit_id_seq to neko_files;
grant select on workflow_output to neko_subscriptions`); err != nil {
		t.Fatal(err)
	}

	install.start(t, ctx)
	install.checkLogins(t, ctx)

	rights := func(role string) []string {
		t.Helper()
		var got []string
		rows, err := old.Query(ctx, `select table_name || ':' || privilege_type from information_schema.role_table_grants where grantee = $1 order by 1`, role)
		if err != nil {
			t.Fatal(err)
		}
		got, err = pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	for legacy, current := range map[string]string{db.FilesRolePrefix: db.FilesRole(install.database), db.SubscriptionsRolePrefix: db.SubscriptionsRole(install.database)} {
		before, after := strings.Join(rights(legacy), ","), strings.Join(rights(current), ",")
		if before == "" || before != after {
			t.Fatalf("%s has %q; %s has %q", legacy, before, current, after)
		}
	}

	var out bytes.Buffer
	if err := checkDatabase(ctx, install.conn.DSN(), &out); err != nil {
		t.Fatalf("check: %v\n%s", err, &out)
	}
	for _, want := range []string{
		"warn  the neko_files role is from an earlier release",
		"warn  the neko_subscriptions role is from an earlier release",
		`DROP OWNED BY "neko_files";`,
		`DROP ROLE "neko_subscriptions";`,
		"ok    the " + db.FilesRole(install.database) + " role exists, and this login can set its password",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("check output lacks %q:\n%s", want, &out)
		}
	}
}
