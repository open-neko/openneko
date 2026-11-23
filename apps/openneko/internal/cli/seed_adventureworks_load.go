package cli

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/jackc/pgx/v5"

	"github.com/open-neko/neko/apps/openneko/assets"
	"github.com/open-neko/neko/apps/openneko/internal/db"
)

const defaultAdventureWorksZipURL = "https://github.com/Microsoft/sql-server-samples/releases/download/adventureworks/AdventureWorks-oltp-install-script.zip"

var (
	awNUL = string(rune(0))
	awTAB = string(rune(9))
	awLF  = string(rune(10))
	awCR  = string(rune(13))
	awBS  = string(rune(92))
)

type adventureWorksLoadConfig struct {
	Host       string
	Port       int
	User       string
	Password   string
	SSLMode    string
	Database   string
	CacheDir   string
	InstallSQL []byte
	ZipURL     string
	Out        io.Writer
}

func adventureWorksLoadConfigFromEnv(out io.Writer) (adventureWorksLoadConfig, error) {
	sqlBytes := assets.AdventureWorksInstallSQL
	if p := strings.TrimSpace(os.Getenv("ADVENTUREWORKS_INSTALL_SQL")); p != "" {
		raw, err := os.ReadFile(p)
		if err != nil {
			return adventureWorksLoadConfig{}, fmt.Errorf("read ADVENTUREWORKS_INSTALL_SQL (%s): %w", p, err)
		}
		sqlBytes = raw
	}
	return adventureWorksLoadConfig{
		Host:       envOr("PGHOST", "localhost"),
		Port:       envInt("PGPORT", 5432),
		User:       envOr("PGUSER", "postgres"),
		Password:   envOr("PGPASSWORD", "postgres"),
		SSLMode:    envOr("PGSSLMODE", "disable"),
		Database:   envOr("ADVENTUREWORKS_DB", "adventureworks"),
		CacheDir:   envOr("ADVENTUREWORKS_CACHE_DIR", "/cache"),
		InstallSQL: sqlBytes,
		ZipURL:     envOr("ADVENTUREWORKS_ZIP_URL", defaultAdventureWorksZipURL),
		Out:        out,
	}, nil
}

func (c adventureWorksLoadConfig) logf(format string, args ...any) {
	if c.Out != nil {
		fmt.Fprintln(c.Out, "[adventureworks] "+fmt.Sprintf(format, args...))
	}
}

func (c adventureWorksLoadConfig) connect(ctx context.Context, database string) (*pgx.Conn, error) {
	target := db.ConnConfig{
		Host:     c.Host,
		Port:     c.Port,
		User:     c.User,
		Password: c.Password,
		Database: database,
		SSLMode:  c.SSLMode,
	}
	return db.Connect(ctx, target, "openneko seed --load")
}

func runAdventureWorksLoad(ctx context.Context, cfg adventureWorksLoadConfig) error {
	loaded, err := cfg.isAlreadyLoaded(ctx)
	if err != nil {
		return err
	}
	if loaded {
		cfg.logf("'%s' already populated; skipping load", cfg.Database)
		return nil
	}

	workDir := filepath.Join(cfg.CacheDir, "adventureworks")
	zipPath := filepath.Join(workDir, "ms.zip")

	if err := cfg.ensureMsZip(ctx, workDir, zipPath); err != nil {
		return err
	}
	if err := cfg.ensureExtracted(workDir, zipPath); err != nil {
		return err
	}
	if err := cfg.convertAllCsvs(workDir); err != nil {
		return err
	}
	if err := cfg.createDatabase(ctx); err != nil {
		return err
	}
	if err := cfg.runInstallSQL(ctx, workDir); err != nil {
		return err
	}
	conn, err := cfg.connect(ctx, cfg.Database)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	if err := markAdventureWorksLoadComplete(ctx, conn); err != nil {
		return err
	}
	cfg.logf("done")
	return nil
}

func (c adventureWorksLoadConfig) isAlreadyLoaded(ctx context.Context) (bool, error) {
	probe, err := c.connect(ctx, "postgres")
	if err != nil {
		return false, err
	}
	var dbExists bool
	err = probe.QueryRow(ctx, "select exists(select 1 from pg_database where datname = $1) as exists", c.Database).Scan(&dbExists)
	_ = probe.Close(ctx)
	if err != nil {
		return false, err
	}
	if !dbExists {
		return false, nil
	}

	conn, err := c.connect(ctx, c.Database)
	if err != nil {
		return false, nil
	}
	defer conn.Close(ctx)

	var markerExists bool
	if err := conn.QueryRow(ctx, `
		select exists(
			select 1 from information_schema.tables
			 where table_schema = 'public' and table_name = '_openneko_aw_load_complete'
		)`).Scan(&markerExists); err != nil {
		return false, nil
	}
	if markerExists {
		return true, nil
	}

	var tableCount int
	if err := conn.QueryRow(ctx, `
		select count(*)
		  from information_schema.tables
		 where table_schema in ('person','humanresources','production','purchasing','sales')`).Scan(&tableCount); err != nil || tableCount < 50 {
		return false, nil
	}

	var orderCount int
	if err := conn.QueryRow(ctx, `select count(*) from sales.salesorderheader`).Scan(&orderCount); err != nil || orderCount == 0 {
		return false, nil
	}

	if err := markAdventureWorksLoadComplete(ctx, conn); err != nil {
		return false, nil
	}
	return true, nil
}

func markAdventureWorksLoadComplete(ctx context.Context, conn *pgx.Conn) error {
	if _, err := conn.Exec(ctx, `
		create table if not exists public._openneko_aw_load_complete (
			loaded_at timestamptz not null default now()
		)`); err != nil {
		return err
	}
	_, err := conn.Exec(ctx, `insert into public._openneko_aw_load_complete default values`)
	return err
}

func (c adventureWorksLoadConfig) createDatabase(ctx context.Context) error {
	conn, err := c.connect(ctx, "postgres")
	if err != nil {
		return err
	}
	defer conn.Close(ctx)

	var exists bool
	if err := conn.QueryRow(ctx, "select exists(select 1 from pg_database where datname = $1) as exists", c.Database).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		quoted := pgx.Identifier{c.Database}.Sanitize()
		if _, err := conn.Exec(ctx, "create database "+quoted); err != nil {
			return err
		}
	}
	return nil
}

func (c adventureWorksLoadConfig) ensureMsZip(ctx context.Context, workDir, zipPath string) error {
	if _, err := os.Stat(zipPath); err == nil {
		c.logf("re-using cached MS zip at %s", zipPath)
		return nil
	}
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return err
	}
	c.logf("downloading %s", c.ZipURL)
	partial := zipPath + ".partial"
	var lastErr error
	for attempt := 0; attempt <= 5; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(3 * time.Second):
			}
		}
		if err := downloadAdventureWorksZip(ctx, c.ZipURL, partial); err != nil {
			lastErr = err
			continue
		}
		return os.Rename(partial, zipPath)
	}
	return fmt.Errorf("download %s: %w", c.ZipURL, lastErr)
}

func downloadAdventureWorksZip(ctx context.Context, url, dst string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("unexpected HTTP status %d", resp.StatusCode)
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		_ = f.Close()
		_ = os.Remove(dst)
		return err
	}
	return f.Close()
}

func (c adventureWorksLoadConfig) ensureExtracted(workDir, zipPath string) error {
	if _, err := os.Stat(filepath.Join(workDir, "BusinessEntity.csv")); err == nil {
		c.logf("re-using extracted CSVs in %s", workDir)
		return nil
	}
	c.logf("extracting %s", zipPath)
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer zr.Close()

	cleanWorkDir := filepath.Clean(workDir) + string(os.PathSeparator)
	for _, zf := range zr.File {
		target := filepath.Join(workDir, filepath.FromSlash(zf.Name))
		if !strings.HasPrefix(filepath.Clean(target)+string(os.PathSeparator), cleanWorkDir) && filepath.Clean(target) != filepath.Clean(workDir) {
			return fmt.Errorf("zip entry %q escapes work directory", zf.Name)
		}
		if zf.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		rc, err := zf.Open()
		if err != nil {
			return err
		}
		out, err := os.Create(target)
		if err != nil {
			_ = rc.Close()
			return err
		}
		_, copyErr := io.Copy(out, rc)
		closeErr := out.Close()
		_ = rc.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func (c adventureWorksLoadConfig) convertAllCsvs(workDir string) error {
	c.logf("converting CSVs to tab-delimited form")
	entries, err := os.ReadDir(workDir)
	if err != nil {
		return err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".csv") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		if err := convertAdventureWorksBcpFile(filepath.Join(workDir, name)); err != nil {
			return fmt.Errorf("convert %s: %w", name, err)
		}
	}
	c.logf("converted %d CSV files", len(names))
	return nil
}

func convertAdventureWorksBcpFile(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var decoded string
	switch {
	case len(raw) >= 2 && raw[0] == 0xff && raw[1] == 0xfe:
		body := raw[2:]
		u16 := make([]uint16, len(body)/2)
		for i := range u16 {
			u16[i] = uint16(body[2*i]) | uint16(body[2*i+1])<<8
		}
		decoded = string(utf16.Decode(u16))
	case len(raw) >= 3 && raw[0] == 0xef && raw[1] == 0xbb && raw[2] == 0xbf:
		decoded = strings.ToValidUTF8(string(raw[3:]), string(rune(0xFFFD)))
	default:
		decoded = strings.ToValidUTF8(string(raw), string(rune(0xFFFD)))
	}

	text := strings.ReplaceAll(strings.ReplaceAll(decoded, awNUL, ""), awCR, "")
	head := text
	if len(head) > 1024 {
		head = head[:1024]
	}
	isBcp := strings.Contains(head, "+|")

	lines := strings.Split(text, awLF)
	out := make([]string, 0, len(lines))
	escapedNewline := awBS + "n"

	if isBcp {
		var acc strings.Builder
		for _, line := range lines {
			munged := strings.ReplaceAll(convertAdventureWorksBytea(line), `"`, `""`)
			if strings.HasSuffix(munged, "&|") {
				acc.WriteString(munged)
				record := acc.String()
				record = record[:len(record)-2]
				fields := strings.Split(record, "+|")
				for i, part := range fields {
					if len(part) >= 2 && part[0] == '<' && part[len(part)-1] == '>' {
						fields[i] = `"` + part + `"`
					} else if strings.Contains(part, awTAB) {
						fields[i] = `"` + part + `"`
					}
				}
				out = append(out, strings.Join(fields, awTAB))
				acc.Reset()
			} else {
				acc.WriteString(munged)
				acc.WriteString(escapedNewline)
			}
		}
	} else {
		for _, line := range lines {
			munged := strings.ReplaceAll(convertAdventureWorksBytea(line), `"`, `""`)
			munged = strings.TrimSuffix(munged, "&|")
			out = append(out, munged)
		}
	}

	return os.WriteFile(path, []byte(strings.Join(out, awLF)), 0o644)
}

func convertAdventureWorksBytea(s string) string {
	doubleBS := awBS + awBS
	s = strings.ReplaceAll(s, "|474946383961", "|"+doubleBS+"x474946383961")
	s = strings.ReplaceAll(s, awTAB+"E6100000010C", awTAB+doubleBS+"xE6100000010C")
	return s
}

func (c adventureWorksLoadConfig) runInstallSQL(ctx context.Context, workDir string) error {
	c.logf("running install.sql against '%s'", c.Database)
	conn, err := c.connect(ctx, c.Database)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)

	var sqlBuf strings.Builder
	flushSQL := func() error {
		chunk := sqlBuf.String()
		sqlBuf.Reset()
		if strings.TrimSpace(chunk) == "" {
			return nil
		}
		_, err := conn.PgConn().Exec(ctx, chunk).ReadAll()
		return err
	}

	lines := strings.Split(string(c.InstallSQL), awLF)
	for lineNo, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, awBS) {
			sqlBuf.WriteString(line)
			sqlBuf.WriteString(awLF)
			continue
		}
		if err := flushSQL(); err != nil {
			return fmt.Errorf("install.sql before line %d: %w", lineNo+1, err)
		}
		switch {
		case strings.HasPrefix(trimmed, awBS+"pset"), strings.HasPrefix(trimmed, awBS+"dt"):
			continue
		case strings.HasPrefix(trimmed, awBS+"copy "):
			if err := runAdventureWorksCopyLine(ctx, conn, workDir, trimmed); err != nil {
				return fmt.Errorf("install.sql line %d (%s): %w", lineNo+1, trimmed, err)
			}
		default:
			return fmt.Errorf("unsupported psql directive at line %d: %s", lineNo+1, trimmed)
		}
	}
	return flushSQL()
}

func runAdventureWorksCopyLine(ctx context.Context, conn *pgx.Conn, workDir, line string) error {
	rest := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(strings.TrimPrefix(line, awBS+"copy ")), ";"))
	fromIdx := strings.Index(strings.ToUpper(rest), " FROM ")
	if fromIdx < 0 {
		return fmt.Errorf("malformed copy directive: %s", line)
	}
	table := strings.TrimSpace(rest[:fromIdx])
	afterFrom := strings.TrimSpace(rest[fromIdx+len(" FROM "):])
	if !strings.HasPrefix(afterFrom, "'") {
		return fmt.Errorf("expected quoted path in copy: %s", line)
	}
	endQuote := strings.Index(afterFrom[1:], "'")
	if endQuote < 0 {
		return fmt.Errorf("unterminated path in copy: %s", line)
	}
	relPath := afterFrom[1 : 1+endQuote]
	options := strings.TrimSpace(afterFrom[1+endQuote+1:])

	csvPath := filepath.Join(workDir, filepath.FromSlash(relPath))
	f, err := os.Open(csvPath)
	if err != nil {
		return err
	}
	defer f.Close()

	copySQL := fmt.Sprintf("COPY %s FROM STDIN %s", table, options)
	_, err = conn.PgConn().CopyFrom(ctx, f, copySQL)
	return err
}
