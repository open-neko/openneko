// Package files keeps each user's private files and answers search, text and
// sheet-table queries over them for the agent's brokered tools.
package files

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dosco/graphjin/core/v3/fstable"

	"github.com/open-neko/neko/apps/openneko/internal/filecontent"
)

const TokenLabel = "openneko-serve:v1"

type Options struct {
	DuckDB       string
	MaxFileBytes int64
	MaxRows      int
	SQLTimeout   time.Duration
	Refresh      time.Duration
	Now          func() time.Time
}

type Service struct {
	store fstable.Backend
	index *filecontent.Index
	key   []byte
	opts  Options
}

func New(store fstable.Backend, index *filecontent.Index, key []byte, opts Options) *Service {
	if opts.Refresh <= 0 {
		opts.Refresh = 10 * time.Second
	}
	if opts.MaxFileBytes <= 0 {
		opts.MaxFileBytes = filecontent.DefaultMaxFileBytes
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Service{store: store, index: index, key: key, opts: opts}
}

type caller struct {
	org, user string
}

// scope is the caller's private folder. Every key the service touches for a
// caller starts with it.
func (c caller) scope() string { return "orgs/" + c.org + "/users/" + c.user + "/" }

var idPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

type claims struct {
	Org string `json:"org"`
	Sub string `json:"sub"`
	Exp int64  `json:"exp"`
}

func (s *Service) authenticate(r *http.Request) (caller, error) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return caller{}, errors.New("missing bearer token")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return caller{}, errors.New("malformed token")
	}
	var header struct {
		Alg string `json:"alg"`
	}
	if raw, err := base64.RawURLEncoding.DecodeString(parts[0]); err != nil || json.Unmarshal(raw, &header) != nil || header.Alg != "HS256" {
		return caller{}, errors.New("unsupported token")
	}
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(parts[0] + "." + parts[1]))
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || !hmac.Equal(sig, mac.Sum(nil)) {
		return caller{}, errors.New("bad token signature")
	}
	var c claims
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || json.Unmarshal(raw, &c) != nil {
		return caller{}, errors.New("malformed token")
	}
	if c.Exp <= s.opts.Now().Unix() {
		return caller{}, errors.New("token expired")
	}
	if !idPattern.MatchString(c.Org) || !idPattern.MatchString(c.Sub) {
		return caller{}, errors.New("token names no user")
	}
	return caller{org: c.Org, user: c.Sub}, nil
}

// cleanPath turns a caller's path into a key inside their folder.
func cleanPath(c caller, p string) (string, error) {
	if p == "" || strings.ContainsAny(p, "\\\x00") || !utf8.ValidString(p) {
		return "", errors.New("invalid path")
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return "", errors.New("invalid path")
		}
	}
	clean := strings.TrimPrefix(path.Clean("/"+p), "/")
	if clean == "" || clean == "." {
		return "", errors.New("invalid path")
	}
	return c.scope() + clean, nil
}

func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /v1/files", s.authed(s.list))
	mux.HandleFunc("PUT /v1/files/content", s.authed(s.put))
	mux.HandleFunc("DELETE /v1/files/content", s.authed(s.delete))
	mux.HandleFunc("POST /v1/files/search", s.authed(s.search))
	mux.HandleFunc("POST /v1/files/read", s.authed(s.read))
	mux.HandleFunc("POST /v1/files/sql", s.authed(s.sql))
	return mux
}

type httpError struct {
	status int
	msg    string
}

func (e httpError) Error() string { return e.msg }

func badRequest(format string, args ...any) error {
	return httpError{http.StatusBadRequest, fmt.Sprintf(format, args...)}
}

var errNotFound = httpError{http.StatusNotFound, "file not found"}

func (s *Service) authed(h func(context.Context, caller, *http.Request) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := s.authenticate(r)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
			return
		}
		out, err := h(r.Context(), c, r)
		if err != nil {
			status := http.StatusInternalServerError
			var he httpError
			if errors.As(err, &he) {
				status = he.status
			}
			writeJSON(w, status, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}

func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return badRequest("invalid request body: %v", err)
	}
	return nil
}

func (s *Service) duckDBAvailable() bool {
	bin := s.opts.DuckDB
	if bin == "" {
		bin = "duckdb"
	}
	_, err := exec.LookPath(bin)
	return err == nil
}

type checker interface {
	Check(ctx context.Context) error
}

func (s *Service) health(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"ok": true, "sheet_sql": s.duckDBAvailable(), "file_store": s.store.Name(), "file_store_ok": true}
	if c, ok := s.store.(checker); ok {
		if err := c.Check(r.Context()); err != nil {
			out["file_store_ok"] = false
			out["file_store_error"] = err.Error()
		}
	}
	writeJSON(w, http.StatusOK, out)
}

type FileInfo struct {
	Path       string    `json:"path"`
	Size       int64     `json:"size"`
	ModifiedAt time.Time `json:"modified_at"`
}

func info(c caller, e fstable.Entry) FileInfo {
	return FileInfo{Path: strings.TrimPrefix(e.Key, c.scope()), Size: e.Size, ModifiedAt: e.ModifiedAt}
}

func (s *Service) list(ctx context.Context, c caller, r *http.Request) (any, error) {
	prefix := c.scope()
	if p := r.URL.Query().Get("prefix"); p != "" {
		key, err := cleanPath(c, p)
		if err != nil {
			return nil, badRequest("%v", err)
		}
		prefix = key
	}
	out := []FileInfo{}
	after := ""
	for {
		entries, next, err := s.store.List(ctx, fstable.ListOpts{Prefix: prefix, Limit: 1000, After: after})
		if errors.Is(err, fstable.ErrNotFound) {
			break
		}
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if strings.HasPrefix(e.Key, c.scope()) {
				out = append(out, info(c, e))
			}
		}
		if next == "" {
			break
		}
		after = next
	}
	return map[string]any{"files": out}, nil
}

func (s *Service) put(ctx context.Context, c caller, r *http.Request) (any, error) {
	key, err := cleanPath(c, r.URL.Query().Get("path"))
	if err != nil {
		return nil, badRequest("%v", err)
	}
	body := &capped{r: r.Body, left: s.opts.MaxFileBytes}
	e, err := s.store.Put(ctx, key, body, fstable.PutMeta{ContentType: r.Header.Get("Content-Type")})
	if errors.Is(err, errTooLarge) {
		return nil, httpError{http.StatusRequestEntityTooLarge, fmt.Sprintf("file is larger than %d bytes", s.opts.MaxFileBytes)}
	}
	if err != nil {
		return nil, err
	}
	s.index.Forget(c.scope())
	return info(c, e), nil
}

var errTooLarge = errors.New("file too large")

// capped fails the read past the limit, so the store drops the partial write
// and keeps the file that was there before.
type capped struct {
	r    io.Reader
	left int64
}

func (c *capped) Read(p []byte) (int, error) {
	if c.left < 0 {
		return 0, errTooLarge
	}
	if int64(len(p)) > c.left+1 {
		p = p[:c.left+1]
	}
	n, err := c.r.Read(p)
	c.left -= int64(n)
	if c.left < 0 {
		return n, errTooLarge
	}
	return n, err
}

func (s *Service) delete(ctx context.Context, c caller, r *http.Request) (any, error) {
	key, err := cleanPath(c, r.URL.Query().Get("path"))
	if err != nil {
		return nil, badRequest("%v", err)
	}
	if err := s.store.Delete(ctx, key); err != nil {
		return nil, err
	}
	if err := s.index.Remove(ctx, key); err != nil {
		return nil, err
	}
	s.index.Forget(c.scope())
	return map[string]bool{"deleted": true}, nil
}

func (s *Service) synced(ctx context.Context, c caller) error {
	_, err := s.index.Sync(ctx, s.store, c.scope(), s.opts.Refresh)
	return err
}

type tableInfo struct {
	Name      string               `json:"name"`
	Sheet     string               `json:"sheet"`
	Raw       bool                 `json:"raw,omitempty"`
	HeaderRow int                  `json:"header_row,omitempty"`
	Rows      int                  `json:"rows"`
	Columns   []filecontent.Column `json:"columns"`
}

func (s *Service) tables(ctx context.Context, key string) ([]tableInfo, error) {
	ts, err := s.index.Tables(ctx, key)
	if err != nil {
		return nil, err
	}
	out := make([]tableInfo, 0, len(ts))
	for _, t := range ts {
		out = append(out, tableInfo{Name: t.Name, Sheet: t.Sheet, Raw: t.Raw, HeaderRow: t.HeaderRow, Rows: t.Rows, Columns: t.Columns})
	}
	return out, nil
}

type searchMatch struct {
	Path     string                `json:"path"`
	Score    float64               `json:"score"`
	Passages []filecontent.Passage `json:"passages"`
	Tables   []tableInfo           `json:"tables,omitempty"`
}

func (s *Service) search(ctx context.Context, c caller, r *http.Request) (any, error) {
	var req struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.Query) == "" {
		return nil, badRequest("query is required")
	}
	if req.Limit <= 0 || req.Limit > 50 {
		req.Limit = 10
	}
	if err := s.synced(ctx, c); err != nil {
		return nil, err
	}
	matches, err := s.index.Search(ctx, c.scope(), req.Query, req.Limit)
	if err != nil {
		return nil, err
	}
	out := make([]searchMatch, 0, len(matches))
	for _, m := range matches {
		ts, err := s.tables(ctx, m.Key)
		if err != nil {
			return nil, err
		}
		out = append(out, searchMatch{Path: strings.TrimPrefix(m.Key, c.scope()), Score: m.Score, Passages: m.Passages, Tables: ts})
	}
	return map[string]any{"matches": out}, nil
}

const (
	defaultReadChars = 20000
	maxReadChars     = 100000
)

func (s *Service) read(ctx context.Context, c caller, r *http.Request) (any, error) {
	var req struct {
		Path     string `json:"path"`
		Offset   int    `json:"offset"`
		MaxChars int    `json:"max_chars"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	key, err := cleanPath(c, req.Path)
	if err != nil {
		return nil, badRequest("%v", err)
	}
	if req.MaxChars <= 0 {
		req.MaxChars = defaultReadChars
	}
	req.MaxChars = min(req.MaxChars, maxReadChars)
	req.Offset = max(req.Offset, 0)

	e, err := s.store.Stat(ctx, key)
	if errors.Is(err, fstable.ErrNotFound) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	if !filecontent.Supported(key) {
		return nil, badRequest("%s has no text this service can read", req.Path)
	}
	if err := s.synced(ctx, c); err != nil {
		return nil, err
	}
	text, ok, err := s.index.Text(ctx, e)
	if err != nil {
		return nil, err
	}
	if !ok {
		if e.Size > s.opts.MaxFileBytes {
			return nil, badRequest("file is larger than %d bytes", s.opts.MaxFileBytes)
		}
		body, _, err := s.store.Get(ctx, key)
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(io.LimitReader(body, s.opts.MaxFileBytes))
		body.Close()
		if err != nil {
			return nil, err
		}
		doc, err := filecontent.Extract(path.Base(key), data)
		if err != nil {
			return nil, badRequest("%v", err)
		}
		text = doc.Text()
	}
	runes := []rune(text)
	total := len(runes)
	start := min(req.Offset, total)
	end := min(start+req.MaxChars, total)
	ts, err := s.tables(ctx, key)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"path":        req.Path,
		"text":        string(runes[start:end]),
		"offset":      start,
		"total_chars": total,
		"truncated":   end < total,
		"tables":      ts,
	}, nil
}

var sqlWord = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)

func (s *Service) sql(ctx context.Context, c caller, r *http.Request) (any, error) {
	var req struct {
		SQL string `json:"sql"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.SQL) == "" {
		return nil, badRequest("sql is required")
	}
	if !s.duckDBAvailable() {
		return nil, httpError{http.StatusServiceUnavailable, "sheet SQL is off: the DuckDB CLI is not installed"}
	}
	if err := s.synced(ctx, c); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var names []string
	for _, w := range sqlWord.FindAllString(req.SQL, -1) {
		w = strings.ToLower(w)
		if !seen[w] {
			seen[w] = true
			names = append(names, w)
		}
	}
	tables, err := s.index.TableFiles(ctx, c.scope(), names)
	if err != nil {
		return nil, err
	}
	if len(tables) == 0 {
		return nil, badRequest("the query names no sheet table; file search lists each file's tables")
	}
	res, err := filecontent.RunSQL(ctx, tables, req.SQL, filecontent.SQLOptions{
		DuckDB:  s.opts.DuckDB,
		MaxRows: s.opts.MaxRows,
		Timeout: s.opts.SQLTimeout,
	})
	if err != nil {
		if errors.Is(err, filecontent.ErrNoDuckDB) {
			return nil, httpError{http.StatusServiceUnavailable, err.Error()}
		}
		return nil, badRequest("%v", err)
	}
	return res, nil
}
