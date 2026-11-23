package filecontent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dosco/graphjin/core/v3/fstable"

	"github.com/open-neko/neko/apps/openneko/internal/testpg"
)

func localSource(t *testing.T, files map[string][]byte) (string, fstable.Backend) {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	b, err := fstable.NewLocal(fstable.LocalConfig{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	return root, b
}

func TestIndexSyncSearchAndTables(t *testing.T) {
	ctx := context.Background()
	const u = "u/1/"
	root, b := localSource(t, map[string][]byte{
		u + "policies/travel.md":     []byte("# Travel\nBook economy.\n# Per diem\nTier 1 cities pay 70 per day.\n"),
		u + "pricing/q4 prices.csv":  []byte("SKU,Material,Price\nHI-1,Rogers 4350B,12.5\nHI-2,FR-4,7\n"),
		u + "pricing/Regional.xlsx":  workbook(t),
		u + "misc/photo.png":         {0x89, 0x50},
		u + "archive/old/prices.csv": []byte("SKU,Price\nX,1\n"),
		u + "archive/new/prices.csv": []byte("SKU,Price\nY,2\n"),
		"u/2/policies/travel.md":     []byte("# Per diem\nTier 1 cities pay 90 per day.\n"),
	})
	ix, err := Open(testpg.Pool(t, "0121_file_content.sql"), t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	st, err := ix.Sync(ctx, b, u, 0)
	if err != nil {
		t.Fatal(err)
	}
	if st.Indexed != 5 || st.Failed != 0 {
		t.Fatalf("sync = %+v", st)
	}

	m, err := ix.Search(ctx, u, "What is the per diem for tier 1 cities?", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(m) == 0 || m[0].Key != u+"policies/travel.md" || m[0].Passages[0].Location != "section Per diem" {
		t.Fatalf("search = %+v", m)
	}
	for _, f := range m {
		if !strings.HasPrefix(f.Key, u) {
			t.Fatalf("search left its scope: %+v", f)
		}
	}
	if !strings.Contains(m[0].Passages[0].Snippet, "«") {
		t.Fatalf("snippet = %q", m[0].Passages[0].Snippet)
	}
	if m, err = ix.Search(ctx, u, `q4 "prices" (AND* & !:`, 5); err != nil || len(m) == 0 || m[0].Key != u+"pricing/q4 prices.csv" {
		t.Fatalf("name search = %+v, %v", m, err)
	}
	if m, err = ix.Search(ctx, u, "the of and", 5); err != nil || len(m) != 0 {
		t.Fatalf("stop words = %+v, %v", m, err)
	}

	tables, err := ix.Tables(ctx, u+"pricing/q4 prices.csv")
	if err != nil || len(tables) != 2 || tables[1].Name != "q4_prices" {
		t.Fatalf("tables = %+v, %v", tables, err)
	}
	a, _ := ix.Tables(ctx, u+"archive/old/prices.csv")
	c, _ := ix.Tables(ctx, u+"archive/new/prices.csv")
	if len(a) != 2 || len(c) != 2 || a[1].Name == c[1].Name {
		t.Fatalf("same-name tables = %+v / %+v", a, c)
	}

	e, err := b.Stat(ctx, u+"pricing/Regional.xlsx")
	if err != nil {
		t.Fatal(err)
	}
	text, ok, err := ix.Text(ctx, e)
	if err != nil || !ok || !strings.Contains(text, "[sheet Sales 2025]") {
		t.Fatalf("text = %q %v %v", text, ok, err)
	}

	if err := os.Remove(filepath.Join(root, "u", "1", "policies", "travel.md")); err != nil {
		t.Fatal(err)
	}
	if st, err = ix.Sync(ctx, b, u, time.Hour); err != nil || st != (SyncStats{}) {
		t.Fatalf("throttled sync = %+v, %v", st, err)
	}
	if st, err = ix.Sync(ctx, b, u, 0); err != nil || st.Removed != 1 || st.Indexed != 0 {
		t.Fatalf("sync after delete = %+v, %v", st, err)
	}
	if m, _ = ix.Search(ctx, u, "per diem", 5); len(m) != 0 {
		t.Fatalf("removed file still found: %+v", m)
	}
	if _, err := ix.Sync(ctx, b, "u/2/", 0); err != nil {
		t.Fatal(err)
	}
	if m, _ = ix.Search(ctx, "u/2/", "per diem", 5); len(m) != 1 || m[0].Key != "u/2/policies/travel.md" {
		t.Fatalf("other scope = %+v", m)
	}
}

func duckdbPath(t *testing.T) string {
	t.Helper()
	p := os.Getenv("OPENNEKO_TEST_DUCKDB")
	if p == "" {
		p = "duckdb"
	}
	if _, err := exec.LookPath(p); err != nil {
		t.Skip("DuckDB CLI not installed; set OPENNEKO_TEST_DUCKDB")
	}
	return p
}

func TestRunSQLQueriesTablesInsideTheSandbox(t *testing.T) {
	duck := duckdbPath(t)
	ctx := context.Background()
	_, b := localSource(t, map[string][]byte{
		"s/prices.csv": []byte("SKU,Price\nHI-1,12.5\nHI-2,7\nHI-3,1\n"),
		"s/other.csv":  []byte("A,B\n1,2\n"),
	})
	ix, err := Open(testpg.Pool(t, "0121_file_content.sql"), t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ix.Sync(ctx, b, "s/", 0); err != nil {
		t.Fatal(err)
	}
	files, err := ix.TableFiles(ctx, "s/", []string{"prices", "nope"})
	if err != nil || len(files) != 1 {
		t.Fatalf("table files = %+v, %v", files, err)
	}
	if other, _ := ix.TableFiles(ctx, "t/", []string{"prices"}); len(other) != 0 {
		t.Fatalf("table from another scope: %+v", other)
	}
	opts := SQLOptions{DuckDB: duck, MaxRows: 2}
	res, err := RunSQL(ctx, files, "select sku, price from prices order by price desc;", opts)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(res.Columns, ",") != "sku,price" || len(res.Rows) != 2 || !res.Truncated || res.Rows[0][0] != "HI-1" {
		t.Fatalf("result = %+v", res)
	}

	other, _ := ix.TableFiles(ctx, "s/", []string{"other"})
	for _, q := range []string{
		"select * from read_csv('/etc/passwd')",
		"select * from read_parquet('" + other[0].Path + "')",
		"select 1; select 2",
		"select getenv('HOME')",
	} {
		if _, err := RunSQL(ctx, files, q, opts); err == nil {
			t.Fatalf("query should be refused: %s", q)
		}
	}
}
