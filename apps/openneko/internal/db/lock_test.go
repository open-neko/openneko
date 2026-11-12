package db

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"
)

func testDatabase(t *testing.T) (ConnConfig, string) {
	t.Helper()
	url := os.Getenv("OPENNEKO_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set OPENNEKO_TEST_DATABASE_URL to a Postgres database")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("lock_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "create schema "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		admin.Exec(context.Background(), "drop schema "+schema+" cascade") //nolint:errcheck
		admin.Close(context.Background())
	})
	return ConnConfig{URL: url}, schema
}

type lines struct {
	mu  sync.Mutex
	all []string
}

func (l *lines) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.all = append(l.all, fmt.Sprintf(format, args...))
}

func (l *lines) text() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.all, "\n")
}

func TestLockNamesTheHolderAndWaits(t *testing.T) {
	target, _ := testDatabase(t)
	ctx := context.Background()
	key := time.Now().UnixNano() | 1<<40
	holder, err := Connect(ctx, target, "openneko lock holder")
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close(ctx)
	unlockHolder, err := Lock(ctx, holder, key, "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	var holderPID uint32 = holder.PgConn().PID()

	waiter, err := Connect(ctx, target, "openneko lock waiter")
	if err != nil {
		t.Fatal(err)
	}
	defer waiter.Close(ctx)

	short, cancel := context.WithTimeout(ctx, 600*time.Millisecond)
	defer cancel()
	var log lines
	if _, err := Lock(short, waiter, key, "test", log.logf); err == nil || !strings.Contains(err.Error(), "deadline") {
		t.Fatalf("Lock while held = %v, want a deadline error", err)
	}
	want := fmt.Sprintf("waiting for the test lock: process %d (openneko lock holder) holds it, connected ", holderPID)
	if !strings.Contains(log.text(), want) {
		t.Fatalf("log = %q, want %q", log.text(), want)
	}

	got := make(chan error, 1)
	go func() {
		unlock, err := Lock(ctx, waiter, key, "test", nil)
		if err == nil {
			unlock()
		}
		got <- err
	}()
	time.Sleep(300 * time.Millisecond)
	unlockHolder()
	select {
	case err := <-got:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the waiter did not get the lock after the holder let it go")
	}
}

func TestConcurrentMigratorsApplyEachFileOnce(t *testing.T) {
	target, schema := testDatabase(t)
	ctx := context.Background()
	mig := &Migrator{FS: fstest.MapFS{
		"migrations/0001_slow.sql": {Data: []byte("select pg_sleep(0.6); create table once (x int);")},
		"migrations/0002_next.sql": {Data: []byte("insert into once values (1);")},
	}, Dir: "migrations"}

	type result struct {
		ran int
		err error
	}
	results := make(chan result, 2)
	var log lines
	for i := 0; i < 2; i++ {
		conn, err := Connect(ctx, target, fmt.Sprintf("openneko migrate %d", i))
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close(ctx)
		if _, err := conn.Exec(ctx, "set search_path to "+schema); err != nil {
			t.Fatal(err)
		}
		go func() {
			ran, err := mig.Apply(ctx, conn, log.logf)
			results <- result{ran, err}
		}()
	}
	total := 0
	for i := 0; i < 2; i++ {
		r := <-results
		if r.err != nil {
			t.Fatal(r.err)
		}
		total += r.ran
	}
	if total != 2 {
		t.Fatalf("the two migrators applied %d files, want 2", total)
	}
	if !strings.Contains(log.text(), "waiting for the migration lock: process ") {
		t.Fatalf("the second migrator did not report the holder:\n%s", log.text())
	}

	check, err := Connect(ctx, target, "check")
	if err != nil {
		t.Fatal(err)
	}
	defer check.Close(ctx)
	if _, err := check.Exec(ctx, "set search_path to "+schema); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := check.QueryRow(ctx, "select count(*) from once").Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("0002 ran %d times, want 1", rows)
	}
	st, err := mig.Status(ctx, check)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Pending) != 0 || len(st.Unknown) != 0 {
		t.Fatalf("status after both migrators = %+v, want nothing pending or unknown", st)
	}
}

func TestStatusPendingAndUnknown(t *testing.T) {
	target, schema := testDatabase(t)
	ctx := context.Background()
	conn, err := Connect(ctx, target, "check")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, "set search_path to "+schema); err != nil {
		t.Fatal(err)
	}
	mig := &Migrator{FS: fstest.MapFS{
		"migrations/0001_a.sql": {Data: []byte("select 1;")},
		"migrations/0002_b.sql": {Data: []byte("select 1;")},
		"migrations/README.md":  {Data: []byte("not a migration")},
	}, Dir: "migrations"}

	st, err := mig.Status(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(st.Pending, ",") != "0001_a.sql,0002_b.sql" || len(st.Unknown) != 0 {
		t.Fatalf("status with no ledger = %+v", st)
	}

	if _, err := conn.Exec(ctx, `create table schema_migrations (name text primary key, applied_at timestamptz not null default now());
insert into schema_migrations(name) values ('0001_a.sql'), ('0003_c.sql'), ('0004_d.sql')`); err != nil {
		t.Fatal(err)
	}
	st, err = mig.Status(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(st.Pending, ",") != "0002_b.sql" || strings.Join(st.Unknown, ",") != "0003_c.sql,0004_d.sql" {
		t.Fatalf("status with a partial ledger = %+v", st)
	}
}
