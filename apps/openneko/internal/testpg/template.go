package testpg

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/open-neko/neko/apps/openneko/assets"
)

const (
	templatePrefix = "ontpl_"
	testDBPrefix   = "ontest_"
	templateSchema = "neko_test"
	staleAfter     = 2 * time.Hour
)

var (
	errNoCreateDB = errors.New("testpg: the test role cannot create databases")

	templateOnce sync.Once
	templateName string
	templateErr  error
	testDBSeq    atomic.Int64
)

// Migrated returns a pool on a database of its own with every embedded
// migration applied. The migrations run once, into a template database;
// each test gets a copy. A role that cannot create databases gets a new
// schema with the migrations applied in place.
func Migrated(t testing.TB) *pgxpool.Pool {
	t.Helper()
	url := URL(t)
	templateOnce.Do(func() { templateName, templateErr = ensureTemplate(url) })
	if errors.Is(templateErr, errNoCreateDB) {
		return migratedSchema(t)
	}
	if templateErr != nil {
		t.Fatal(templateErr)
	}
	ctx := context.Background()
	name := fmt.Sprintf("%s%d_%d_%d", testDBPrefix, time.Now().Unix(), os.Getpid(), testDBSeq.Add(1))
	admin, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	if err := copyTemplate(ctx, admin, name, templateName); err != nil {
		admin.Close(ctx) //nolint:errcheck
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		admin.Exec(ctx, "drop database if exists "+name+" with (force)") //nolint:errcheck
		admin.Close(ctx)                                                 //nolint:errcheck
	})
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.Database = name
	cfg.ConnConfig.RuntimeParams["search_path"] = templateSchema + ", public"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// copyTemplate retries while another process still holds a session on the
// template, which Postgres refuses to copy.
func copyTemplate(ctx context.Context, admin *pgx.Conn, name, template string) error {
	var err error
	for try := 0; try < 40; try++ {
		_, err = admin.Exec(ctx, "create database "+name+" template "+template)
		var pgErr *pgconn.PgError
		if err == nil || !errors.As(err, &pgErr) || pgErr.Code != "55006" {
			return err
		}
		time.Sleep(250 * time.Millisecond)
	}
	return err
}

func migrationNames() ([]string, string, error) {
	files, err := fs.Glob(assets.MigrationsFS, "migrations/*.sql")
	if err != nil {
		return nil, "", err
	}
	sort.Strings(files)
	sum := sha256.New()
	names := make([]string, len(files))
	for i, file := range files {
		names[i] = path.Base(file)
		ddl, err := assets.MigrationsFS.ReadFile(file)
		if err != nil {
			return nil, "", err
		}
		fmt.Fprintf(sum, "%s %d\n", names[i], len(ddl))
		sum.Write(ddl)
	}
	return names, hex.EncodeToString(sum.Sum(nil))[:16], nil
}

// ensureTemplate returns the template database for this set of migrations
// and builds it when it is missing. A lock in the test database lets one
// process build it; a build that another database started is waited for.
func ensureTemplate(url string) (string, error) {
	ctx := context.Background()
	names, hash, err := migrationNames()
	if err != nil {
		return "", err
	}
	template := templatePrefix + hash
	admin, err := pgx.Connect(ctx, url)
	if err != nil {
		return "", err
	}
	defer admin.Close(ctx) //nolint:errcheck
	var can bool
	if err := admin.QueryRow(ctx, `select rolcreatedb or rolsuper from pg_roles where rolname = current_user`).Scan(&can); err != nil {
		return "", err
	}
	if !can {
		return "", errNoCreateDB
	}
	if _, err := admin.Exec(ctx, `select pg_advisory_lock(hashtext('testpg-template'))`); err != nil {
		return "", err
	}
	defer admin.Exec(ctx, `select pg_advisory_unlock(hashtext('testpg-template'))`) //nolint:errcheck
	dropStale(ctx, admin)
	deadline := time.Now().Add(3 * time.Minute)
	for {
		state, err := templateState(ctx, admin, template)
		if err != nil {
			return "", err
		}
		if state == "ready" {
			return template, nil
		}
		if state == "building" && time.Now().Before(deadline) {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		if state != "" {
			if _, err := admin.Exec(ctx, "drop database if exists "+template+" with (force)"); err != nil {
				return "", err
			}
		}
		_, err = admin.Exec(ctx, "create database "+template)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "42P04" {
			continue
		}
		if err != nil {
			return "", err
		}
		if _, err := admin.Exec(ctx, "comment on database "+template+" is 'building'"); err != nil {
			return "", err
		}
		if err := migrateTemplate(ctx, url, template, names); err != nil {
			admin.Exec(ctx, "drop database if exists "+template+" with (force)") //nolint:errcheck
			return "", err
		}
		if _, err := admin.Exec(ctx, "comment on database "+template+" is 'ready'"); err != nil {
			return "", err
		}
		return template, nil
	}
}

func templateState(ctx context.Context, admin *pgx.Conn, template string) (string, error) {
	var state string
	err := admin.QueryRow(ctx,
		`select coalesce(shobj_description(oid, 'pg_database'), 'building') from pg_database where datname = $1`,
		template).Scan(&state)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return state, err
}

func migrateTemplate(ctx context.Context, url, template string, names []string) error {
	cfg, err := pgx.ParseConfig(url)
	if err != nil {
		return err
	}
	cfg.Database = template
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return err
	}
	defer conn.Close(ctx) //nolint:errcheck
	setup := []string{
		`create extension if not exists vector schema public`,
		`create schema ` + templateSchema,
		`set search_path to ` + templateSchema + `, public`,
	}
	for _, stmt := range setup {
		if _, err := conn.Exec(ctx, stmt); err != nil {
			return err
		}
	}
	for _, name := range names {
		ddl, err := assets.MigrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		if _, err := conn.Exec(ctx, string(ddl)); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}

// dropStale removes test databases that a killed test process left behind.
func dropStale(ctx context.Context, admin *pgx.Conn) {
	rows, err := admin.Query(ctx, `select datname from pg_database where datname like $1`, testDBPrefix+`%`)
	if err != nil {
		return
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return
	}
	for _, name := range names {
		parts := strings.Split(strings.TrimPrefix(name, testDBPrefix), "_")
		made, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil || time.Since(time.Unix(made, 0)) < staleAfter {
			continue
		}
		admin.Exec(ctx, "drop database if exists "+name+" with (force)") //nolint:errcheck
	}
}
