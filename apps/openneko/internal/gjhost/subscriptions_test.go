package gjhost

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/open-neko/neko/apps/openneko/assets"
	"github.com/open-neko/neko/apps/openneko/internal/db"
)

func TestRenderSubscriptionsConfigNeedsTheNarrowRole(t *testing.T) {
	conf, err := RenderSubscriptionsConfig(`postgres://neko_subscriptions:p%22w@db:5432/neko?sslmode=verify-full`)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`connection_string: "postgres://neko_subscriptions:p%22w@db:5432/neko?sslmode=verify-full"`,
		`dbname: "neko"`,
		"mcp:\n  disable: true",
		"reload_on_config_change: false",
	} {
		if !strings.Contains(conf, want) {
			t.Errorf("config lacks %q:\n%s", want, conf)
		}
	}
	for _, bad := range []string{
		"postgres://neko:secret@db:5432/neko",
		"postgres://neko_subscriptions:p@db:5432",
		"not a url",
	} {
		if _, err := RenderSubscriptionsConfig(bad); err == nil {
			t.Errorf("%q: want an error", bad)
		}
	}
}

func TestSubscriptionsChildGetsNoCustomerGraphJinSettings(t *testing.T) {
	t.Setenv("GO_ENV", "agentic")
	t.Setenv("GJ_DATABASE_HOST", "customer-db")
	t.Setenv("OPENNEKO_SERVE_CREDENTIALS", "/credentials")
	cmd := SubscriptionsCommand("/tmp/x/subscriptions.yml")("/tmp/x.sock")
	for _, kv := range cmd.Env {
		name, _, _ := strings.Cut(kv, "=")
		if name == "GO_ENV" || strings.HasPrefix(name, "GJ_") || strings.HasPrefix(name, "OPENNEKO_") {
			t.Errorf("subscriptions child env holds %s", kv)
		}
	}
	if got := strings.Join(cmd.Args[1:], " "); got != "graphjin serve --child --config /tmp/x/subscriptions.yml --addr unix:/tmp/x.sock" {
		t.Errorf("child args = %q", got)
	}
}

func graphql(t *testing.T, addr, query string) (int, string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"query": query})
	c := http.Client{Timeout: 5 * time.Second}
	res, err := c.Post("http://"+addr+"/api/v1/graphql", "application/json", strings.NewReader(string(body)))
	if err != nil {
		return 0, err.Error()
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(raw)
}

func TestSubscriptionsServeWorkflowOutputsAsTheNarrowRole(t *testing.T) {
	dsn := os.Getenv("OPENNEKO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set OPENNEKO_TEST_DATABASE_URL to a Postgres database")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "select pg_advisory_lock(7129)"); err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("subs_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "create database "+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		admin.Exec(context.Background(), "drop database "+name+" with (force)") //nolint:errcheck
		admin.Close(context.Background())
	})
	base, _ := url.Parse(dsn)
	base.Path = "/" + name
	conn, err := pgx.Connect(ctx, base.String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	if _, err := (&db.Migrator{FS: assets.MigrationsFS, Dir: "migrations"}).Apply(ctx, conn, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `insert into organization (id, name) values ('org-1', 'x')`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `set session_replication_role = replica`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `insert into workflow_output (org_id, workflow_run_id, work_run_id, kind, title) values ('org-1', gen_random_uuid(), gen_random_uuid(), 'briefing', 'stock is low')`); err != nil {
		t.Fatal(err)
	}

	urlPath := filepath.Join(t.TempDir(), "subscriptions.url")
	setPassword := func(pw string) {
		t.Helper()
		if _, err := admin.Exec(ctx, fmt.Sprintf("alter role %s with login password '%s'", SubscriptionsRole, pw)); err != nil {
			t.Fatal(err)
		}
		u := *base
		u.User = url.UserPassword(SubscriptionsRole, pw)
		q := u.Query()
		q.Set("sslmode", "disable")
		u.RawQuery = q.Encode()
		if err := os.WriteFile(urlPath, []byte(u.String()+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	setPassword("first-password")

	addr := freeAddr(t)
	dir := filepath.Join(t.TempDir(), "subs")
	log := &syncBuffer{}
	s := &Subscriptions{URLFile: urlPath, Dir: dir, Addr: addr, Log: log, Poll: 100 * time.Millisecond,
		Command: func(file string) func(string) *exec.Cmd { return testCommand(file) }}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- s.Run(runCtx) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	file := filepath.Join(dir, SubscriptionsConfig)
	waitFor(t, "the subscriptions GraphJin", func() bool { return state(t, file).State == StateRunning })
	var out string
	waitFor(t, "workflow_output rows", func() bool {
		_, out = graphql(t, addr, `{ workflow_output { title kind org_id } }`)
		return strings.Contains(out, "stock is low")
	})
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("config folder mode: %v %v", info.Mode().Perm(), err)
	}
	if _, out := graphql(t, addr, `{ organization { id } }`); strings.Contains(out, "org-1") || !strings.Contains(out, "permission denied for table organization") {
		t.Fatalf("the role must see workflow_output only, got %s", out)
	}

	restarts := state(t, file).Restarts
	setPassword("second-password")
	waitFor(t, "a restart on the new URL", func() bool {
		st := state(t, file)
		return st.Restarts > restarts && st.State == StateRunning
	})
	waitFor(t, "workflow_output rows after the restart", func() bool {
		_, out = graphql(t, addr, `{ workflow_output { title } }`)
		return strings.Contains(out, "stock is low")
	})

	if err := os.WriteFile(urlPath, []byte("postgres://neko:secret@db/neko\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the bad URL in the log", func() bool { return strings.Contains(log.String(), "must log in as neko_subscriptions") })
	if _, out := graphql(t, addr, `{ workflow_output { title } }`); !strings.Contains(out, "stock is low") {
		t.Fatalf("a bad URL must keep the running engine, got %s", out)
	}
}
