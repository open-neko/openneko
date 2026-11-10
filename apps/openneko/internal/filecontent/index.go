package filecontent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/dosco/graphjin/core/v3/fstable"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Index keeps the keyword index of a file store in Postgres and the Parquet
// tables of its sheets under dir. Both are derived from the files, so either
// may be deleted and rebuilt.
type Index struct {
	db           *pgxpool.Pool
	dir          string
	maxFileBytes int64

	mu       sync.Mutex
	lastSync map[string]time.Time
}

const DefaultMaxFileBytes = 100 << 20

const nameLocation = "file name"

// searchChunk keeps a long sheet or section ranking by its matching part.
const searchChunk = 8000

func Open(db *pgxpool.Pool, dir string, maxFileBytes int64) (*Index, error) {
	if maxFileBytes <= 0 {
		maxFileBytes = DefaultMaxFileBytes
	}
	if err := os.MkdirAll(filepath.Join(dir, "tables"), 0o750); err != nil {
		return nil, err
	}
	return &Index{db: db, dir: dir, maxFileBytes: maxFileBytes, lastSync: map[string]time.Time{}}, nil
}

type SyncStats struct {
	Indexed int
	Removed int
	Failed  int
}

// Sync brings the files under scope up to date. A scope synced within maxAge
// is not read again.
func (ix *Index) Sync(ctx context.Context, b fstable.Backend, scope string, maxAge time.Duration) (SyncStats, error) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	var st SyncStats
	if maxAge > 0 && time.Since(ix.lastSync[scope]) < maxAge {
		return st, nil
	}
	started := time.Now()

	known := map[string]string{}
	rows, err := ix.db.Query(ctx, `select key, version from file_content_file where scope = $1`, scope)
	if err != nil {
		return st, err
	}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			rows.Close()
			return st, err
		}
		known[k] = v
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return st, err
	}

	seen := map[string]bool{}
	after := ""
	for {
		entries, next, err := b.List(ctx, fstable.ListOpts{Prefix: scope, Limit: 1000, After: after})
		if err != nil {
			if errors.Is(err, fstable.ErrNotFound) {
				break
			}
			return st, err
		}
		for _, e := range entries {
			if !strings.HasPrefix(e.Key, scope) || !Supported(e.Key) {
				continue
			}
			seen[e.Key] = true
			if known[e.Key] == version(e) {
				continue
			}
			if err := ix.index(ctx, b, scope, e); err != nil {
				if ctx.Err() != nil {
					return st, ctx.Err()
				}
				st.Failed++
			} else {
				st.Indexed++
			}
		}
		if next == "" {
			break
		}
		after = next
	}
	for k := range known {
		if !seen[k] {
			if err := ix.Remove(ctx, k); err != nil {
				return st, err
			}
			st.Removed++
		}
	}
	ix.lastSync[scope] = started
	return st, nil
}

// Forget makes the next Sync of scope read the files again.
func (ix *Index) Forget(scope string) {
	ix.mu.Lock()
	delete(ix.lastSync, scope)
	ix.mu.Unlock()
}

func version(e fstable.Entry) string {
	return fmt.Sprintf("%s|%d|%s", e.ETag, e.Size, e.ModifiedAt.UTC().Format(time.RFC3339Nano))
}

func fileID(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:8])
}

// index records a file that fails to extract with its error, so it is not
// read again until it changes.
func (ix *Index) index(ctx context.Context, b fstable.Backend, scope string, e fstable.Entry) error {
	var doc *Document
	var exErr error
	if e.Size > ix.maxFileBytes {
		exErr = fmt.Errorf("file is larger than %d bytes", ix.maxFileBytes)
	} else {
		body, _, err := b.Get(ctx, e.Key)
		if err != nil {
			return err
		}
		data, err := io.ReadAll(io.LimitReader(body, ix.maxFileBytes+1))
		body.Close()
		if err != nil {
			return err
		}
		doc, exErr = Extract(path.Base(e.Key), data)
	}

	id := fileID(e.Key)
	base, err := ix.tableBase(ctx, scope, e.Key, id)
	if err != nil {
		return err
	}
	tableDir := filepath.Join(ix.dir, "tables", id)
	if err := os.RemoveAll(tableDir); err != nil {
		return err
	}
	var tables []Table
	if exErr == nil && len(doc.Sheets) > 0 {
		if err := os.MkdirAll(tableDir, 0o750); err != nil {
			return err
		}
		if tables, err = WriteTables(doc, tableDir, base); err != nil {
			exErr = err
			tables = nil
		}
	}

	tx, err := ix.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `delete from file_content_file where key = $1`, e.Key); err != nil {
		return err
	}
	var kind, errText, text any
	if exErr != nil {
		errText = exErr.Error()
	} else {
		kind, text = string(doc.Kind), strings.ToValidUTF8(doc.Text(), "")
	}
	if _, err := tx.Exec(ctx,
		`insert into file_content_file (key, scope, id, base, version, kind, error, text) values ($1, $2, $3, $4, $5, $6, $7, $8)`,
		e.Key, scope, id, base, version(e), kind, errText, text); err != nil {
		return err
	}
	batch := &pgx.Batch{}
	batch.Queue(`insert into file_content_unit (key, location, body) values ($1, $2, $3)`,
		e.Key, nameLocation, nameWords(strings.TrimPrefix(e.Key, scope)))
	if doc != nil {
		for _, u := range doc.Units {
			for _, part := range chunks(u.Text, searchChunk) {
				batch.Queue(`insert into file_content_unit (key, location, body) values ($1, $2, $3)`,
					e.Key, u.Label, strings.ToValidUTF8(part, ""))
			}
		}
	}
	for i, t := range tables {
		info, _ := json.Marshal(t)
		batch.Queue(`insert into file_content_table (scope, name, key, ord, file, info) values ($1, $2, $3, $4, $5, $6)`,
			scope, t.Name, e.Key, i, filepath.Join("tables", id, t.File), info)
	}
	if err := tx.SendBatch(ctx, batch).Close(); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return exErr
}

func (ix *Index) tableBase(ctx context.Context, scope, key, id string) (string, error) {
	stem := path.Base(key)
	stem = strings.TrimSuffix(stem, path.Ext(stem))
	base := Identifier(stem)
	var n int
	if err := ix.db.QueryRow(ctx, `select count(*) from file_content_file where scope = $1 and base = $2 and key <> $3`,
		scope, base, key).Scan(&n); err != nil {
		return "", err
	}
	if n > 0 {
		base += "_" + id[:6]
	}
	return base, nil
}

// Remove drops a file from the index and deletes its tables.
func (ix *Index) Remove(ctx context.Context, key string) error {
	if _, err := ix.db.Exec(ctx, `delete from file_content_file where key = $1`, key); err != nil {
		return err
	}
	return os.RemoveAll(filepath.Join(ix.dir, "tables", fileID(key)))
}

func nameWords(key string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return r
		}
		return ' '
	}, key)
}

func chunks(s string, n int) []string {
	var out []string
	for len(s) > n {
		cut := strings.LastIndexByte(s[:n], '\n')
		if cut <= 0 {
			cut = n
			for cut > 0 && !utf8Start(s[cut]) {
				cut--
			}
		}
		out = append(out, s[:cut])
		s = s[cut:]
	}
	if strings.TrimSpace(s) != "" || len(out) == 0 {
		out = append(out, s)
	}
	return out
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

type Match struct {
	Key      string    `json:"key"`
	Score    float64   `json:"score"`
	Passages []Passage `json:"passages"`
}

type Passage struct {
	Location string `json:"location"`
	Snippet  string `json:"snippet"`
}

const orQuery = `(select string_agg(q, ' | ') from (
	select nullif(plainto_tsquery('english', w)::text, '') as q from unnest($2::text[]) w
) t where q is not null)::tsquery`

// Search ranks the files under scope. Any word may match. A file ranks by its
// best passage plus a little for each further passage, scaled by the share
// of the query's words found anywhere in the file.
func (ix *Index) Search(ctx context.Context, scope, query string, limit int) ([]Match, error) {
	words := queryWords(query)
	if len(words) == 0 {
		return nil, nil
	}
	if limit <= 0 {
		limit = 20
	}
	rows, err := ix.db.Query(ctx, `
		with q as (select `+orQuery+` as q),
		ranked as (
			select u.id, u.key, u.location, ts_rank_cd(u.tsv, q.q) as rank
			from file_content_unit u join file_content_file f on f.key = u.key, q
			where f.scope = $1 and q.q is not null and u.tsv @@ q.q
			order by rank desc limit $3
		)
		select r.key, r.location,
			ts_headline('english', u.body, q.q, 'StartSel=«, StopSel=», MaxWords=24, MinWords=8, MaxFragments=1'),
			r.rank
		from ranked r join file_content_unit u on u.id = r.id, q
		order by r.rank desc`, scope, words, limit*20)
	if err != nil {
		return nil, fmt.Errorf("filecontent: search: %w", err)
	}
	var out []Match
	byKey := map[string]int{}
	names := map[string]Passage{}
	for rows.Next() {
		var key, loc, snip string
		var rank float64
		if err := rows.Scan(&key, &loc, &snip, &rank); err != nil {
			rows.Close()
			return nil, err
		}
		i, ok := byKey[key]
		if !ok {
			i = len(out)
			byKey[key] = i
			out = append(out, Match{Key: key, Score: rank})
		} else {
			out[i].Score += rank * 0.1
		}
		p := Passage{Location: loc, Snippet: strings.TrimSpace(snip)}
		if loc == nameLocation {
			names[key] = p
			continue
		}
		if len(out[i].Passages) < 3 {
			out[i].Passages = append(out[i].Passages, p)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("filecontent: search: %w", err)
	}
	if len(out) == 0 {
		return nil, nil
	}
	for i := range out {
		if len(out[i].Passages) == 0 {
			out[i].Passages = []Passage{names[out[i].Key]}
		}
	}

	var terms int
	covered := map[string]int{}
	rows, err = ix.db.Query(ctx, `
		with t as (
			select distinct plainto_tsquery('english', w) as q from unnest($2::text[]) w
			where plainto_tsquery('english', w)::text <> ''
		)
		select '' as key, count(*) from t
		union all
		select u.key, count(distinct t.q::text)
		from file_content_unit u join file_content_file f on f.key = u.key, t
		where f.scope = $1 and u.key = any($3) and u.tsv @@ t.q
		group by u.key`, scope, words, keys(out))
	if err != nil {
		return nil, fmt.Errorf("filecontent: search: %w", err)
	}
	for rows.Next() {
		var key string
		var n int
		if err := rows.Scan(&key, &n); err != nil {
			rows.Close()
			return nil, err
		}
		if key == "" {
			terms = n
		} else {
			covered[key] = n
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("filecontent: search: %w", err)
	}
	if terms > 1 {
		for i := range out {
			f := float64(covered[out[i].Key]) / float64(terms)
			out[i].Score *= f * f
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func keys(ms []Match) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.Key
	}
	return out
}

func queryWords(q string) []string {
	words := strings.FieldsFunc(strings.ToLower(q), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	seen := map[string]bool{}
	var out []string
	for _, w := range words {
		if seen[w] {
			continue
		}
		seen[w] = true
		out = append(out, w)
	}
	return out
}

// Text returns a file's extracted text when the index holds that version.
func (ix *Index) Text(ctx context.Context, e fstable.Entry) (string, bool, error) {
	var text *string
	err := ix.db.QueryRow(ctx, `select text from file_content_file where key = $1 and version = $2`, e.Key, version(e)).Scan(&text)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil || text == nil {
		return "", false, err
	}
	return *text, true, nil
}

func (ix *Index) Tables(ctx context.Context, key string) ([]Table, error) {
	rows, err := ix.db.Query(ctx, `select info from file_content_table where key = $1 order by ord`, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Table
	for rows.Next() {
		var t Table
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

type TableFile struct {
	Name string
	Key  string
	Path string
}

// TableFiles resolves table names in scope to their Parquet files. Unknown
// names are left out.
func (ix *Index) TableFiles(ctx context.Context, scope string, names []string) ([]TableFile, error) {
	rows, err := ix.db.Query(ctx, `select name, key, file from file_content_table where scope = $1 and name = any($2) order by name`, scope, names)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TableFile
	for rows.Next() {
		var t TableFile
		var file string
		if err := rows.Scan(&t.Name, &t.Key, &file); err != nil {
			return nil, err
		}
		t.Path = filepath.Join(ix.dir, file)
		out = append(out, t)
	}
	return out, rows.Err()
}
