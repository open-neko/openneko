package cli

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/open-neko/neko/apps/openneko/assets"
	"github.com/open-neko/neko/apps/openneko/internal/db"
	"github.com/open-neko/neko/apps/openneko/internal/testpg"
)

func TestConvertAdventureWorksBcpFile(t *testing.T) {
	dir := t.TempDir()

	bcpPath := filepath.Join(dir, "BusinessEntity.csv")
	bcpRaw := strings.Join([]string{
		`1` + awNUL + `+|<root>a"b</root>+|` + awTAB + `with-tab&|`,
		`2+|multi`,
		`line+||474946383961&|`,
	}, awCR+awLF)
	if err := os.WriteFile(bcpPath, []byte(bcpRaw), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := convertAdventureWorksBcpFile(bcpPath); err != nil {
		t.Fatal(err)
	}
	gotBcp, err := os.ReadFile(bcpPath)
	if err != nil {
		t.Fatal(err)
	}
	wantBcp := strings.Join([]string{
		`1` + awTAB + `"<root>a""b</root>"` + awTAB + `"` + awTAB + `with-tab"`,
		`2` + awTAB + `multi` + awBS + `nline` + awTAB + `|` + awBS + awBS + `x474946383961`,
	}, awLF)
	if string(gotBcp) != wantBcp {
		t.Fatalf("converted BCP: got=%q want=%q", string(gotBcp), wantBcp)
	}

	tsvPath := filepath.Join(dir, "Address.csv")
	tsvRaw := "10" + awTAB + "E6100000010C&|" + awLF
	if err := os.WriteFile(tsvPath, []byte(tsvRaw), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := convertAdventureWorksBcpFile(tsvPath); err != nil {
		t.Fatal(err)
	}
	gotTsv, err := os.ReadFile(tsvPath)
	if err != nil {
		t.Fatal(err)
	}
	wantTsv := "10" + awTAB + awBS + awBS + "xE6100000010C" + awLF
	if string(gotTsv) != wantTsv {
		t.Fatalf("converted TSV: got=%q want=%q", string(gotTsv), wantTsv)
	}
}

func TestRunAdventureWorksLoadAndIdempotency(t *testing.T) {
	rawURL := testpg.URL(t)
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	host := parsed.Hostname()
	port, _ := strconv.Atoi(parsed.Port())
	if port == 0 {
		port = 5432
	}
	user := parsed.User.Username()
	password, _ := parsed.User.Password()

	ctx := context.Background()
	awDB := fmt.Sprintf("aw_load_test_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		admin, err := pgx.Connect(context.Background(), rawURL)
		if err == nil {
			_, _ = admin.Exec(context.Background(), "drop database if exists "+pgx.Identifier{awDB}.Sanitize())
			_ = admin.Close(context.Background())
		}
	})

	var zipBuf bytes.Buffer
	zw := zip.NewWriter(&zipBuf)
	addZipFile := func(name, content string) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	addZipFile("BusinessEntity.csv", "1+|2020-01-01&|"+awLF+"2+|2020-01-02&|"+awLF)
	addZipFile("Address.csv", "10"+awTAB+"1970 Napa Ct."+awLF)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(zipBuf.Bytes())
	}))
	defer srv.Close()

	installSQL := strings.Join([]string{
		awBS + `pset tuples_only on`,
		`CREATE SCHEMA person;`,
		`SET search_path TO person;`,
		`CREATE TABLE BusinessEntity (BusinessEntityID INT PRIMARY KEY, ModifiedDate TEXT NOT NULL);`,
		`CREATE TABLE Address (AddressID INT PRIMARY KEY, AddressLine1 TEXT NOT NULL);`,
		awBS + `copy Person.BusinessEntity FROM './BusinessEntity.csv' DELIMITER E'` + awBS + `t' CSV;`,
		awBS + `copy Person.Address FROM './Address.csv' DELIMITER E'` + awBS + `t' CSV ENCODING 'latin1';`,
		awBS + `pset tuples_only off`,
		awBS + `dt (person).*`,
	}, awLF)

	var out bytes.Buffer
	cfg := adventureWorksLoadConfig{
		Host:       host,
		Port:       port,
		User:       user,
		Password:   password,
		SSLMode:    "disable",
		Database:   awDB,
		CacheDir:   t.TempDir(),
		InstallSQL: []byte(installSQL),
		ZipURL:     srv.URL + "/ms.zip",
		Out:        &out,
	}

	if err := runAdventureWorksLoad(ctx, cfg); err != nil {
		t.Fatalf("first load: %v", err)
	}

	conn, err := cfg.connect(ctx, awDB)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)

	var count int
	if err := conn.QueryRow(ctx, "select count(*) from person.businessentity").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("person.businessentity count = %d, want 2", count)
	}
	if err := conn.QueryRow(ctx, "select count(*) from person.address").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("person.address count = %d, want 1", count)
	}
	if err := conn.QueryRow(ctx, "select count(*) from public._openneko_aw_load_complete").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("_openneko_aw_load_complete count = %d, want 1", count)
	}

	out.Reset()
	if err := runAdventureWorksLoad(ctx, cfg); err != nil {
		t.Fatalf("second load: %v", err)
	}
	if !strings.Contains(out.String(), "already populated; skipping load") {
		t.Fatalf("second load output = %q, want skip message", out.String())
	}
}

func TestRunAdventureWorksOrgSeed(t *testing.T) {
	rawURL := testpg.URL(t)
	ctx := context.Background()
	seedDB := fmt.Sprintf("aw_org_seed_test_%d", time.Now().UnixNano())

	admin, err := pgx.Connect(ctx, rawURL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "create database "+pgx.Identifier{seedDB}.Sanitize()); err != nil {
		_ = admin.Close(ctx)
		t.Fatal(err)
	}
	_ = admin.Close(ctx)
	t.Cleanup(func() {
		c, err := pgx.Connect(context.Background(), rawURL)
		if err == nil {
			_, _ = c.Exec(context.Background(), "drop database if exists "+pgx.Identifier{seedDB}.Sanitize())
			_ = c.Close(context.Background())
		}
	})

	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	parsed.Path = "/" + seedDB
	target := db.ConnConfig{URL: parsed.String()}

	conn, err := db.Connect(ctx, target, "test migrate")
	if err != nil {
		t.Fatal(err)
	}
	mig := &db.Migrator{FS: assets.MigrationsFS, Dir: "migrations"}
	if _, err := mig.Apply(ctx, conn, nil); err != nil {
		_ = conn.Close(ctx)
		t.Fatal(err)
	}
	_ = conn.Close(ctx)

	var out bytes.Buffer
	cfg := adventureWorksOrgSeedConfig{
		Target:       target,
		GraphJinRoot: "http://graphjin:8080",
		AuthMode:     "none",
		Out:          &out,
	}
	if err := runAdventureWorksOrgSeed(ctx, cfg); err != nil {
		t.Fatalf("first org seed: %v", err)
	}

	check, err := db.Connect(ctx, target, "test check")
	if err != nil {
		t.Fatal(err)
	}
	defer check.Close(ctx)

	var orgID, orgName string
	if err := check.QueryRow(ctx, "select id, name from organization").Scan(&orgID, &orgName); err != nil {
		t.Fatal(err)
	}
	if orgID != "adventureworks" || orgName != "AdventureWorks Cycles" {
		t.Fatalf("org = (%q, %q)", orgID, orgName)
	}

	var wfCount, subCount, policyCount int
	if err := check.QueryRow(ctx, "select count(*) from workflow_definition where org_id = $1", orgID).Scan(&wfCount); err != nil {
		t.Fatal(err)
	}
	if wfCount != 4 {
		t.Fatalf("workflow count = %d, want 4", wfCount)
	}
	if err := check.QueryRow(ctx, "select count(*) from subscription where org_id = $1", orgID).Scan(&subCount); err != nil {
		t.Fatal(err)
	}
	if subCount != 1 {
		t.Fatalf("subscription count = %d, want 1", subCount)
	}
	if err := check.QueryRow(ctx, "select count(*) from action_policy where org_id = $1 and name = 'revenue_alerts_auto'", orgID).Scan(&policyCount); err != nil {
		t.Fatal(err)
	}
	if policyCount != 1 {
		t.Fatalf("action_policy count = %d, want 1", policyCount)
	}

	// Re-run with ReconcileAuthMode=true and AuthMode="jwt".
	cfg.AuthMode = "jwt"
	cfg.ReconcileAuthMode = true
	if err := runAdventureWorksOrgSeed(ctx, cfg); err != nil {
		t.Fatalf("second org seed: %v", err)
	}
	var authMode string
	if err := check.QueryRow(ctx, "select auth_mode from data_source where org_id = $1", orgID).Scan(&authMode); err != nil {
		t.Fatal(err)
	}
	if authMode != "jwt" {
		t.Fatalf("reconciled auth_mode = %q, want jwt", authMode)
	}
}
