package webfront

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/jackc/pgx/v5"
)

const (
	maxLibraryUpload        = 100 * 1024 * 1024
	maxLibraryUploadRequest = maxLibraryUpload + 4*1024*1024
	libraryOverLimit        = "The selected files exceed the 100 MB import limit. Choose fewer or smaller files."
)

type libraryDocument struct {
	ID             string  `json:"id"`
	OrgID          string  `json:"orgId"`
	UserID         *string `json:"userId"`
	SourceThreadID *string `json:"sourceThreadId"`
	Filename       string  `json:"filename"`
	RelativePath   string  `json:"relativePath"`
	ContentHash    string  `json:"contentHash"`
	SizeBytes      int     `json:"sizeBytes"`
	Status         string  `json:"status"`
	Error          *string `json:"error"`
	CreatedAt      string  `json:"createdAt"`
	UpdatedAt      string  `json:"updatedAt"`
}

const libraryColumns = `id::text, org_id, user_id, source_thread_id::text, filename, relative_path, content_hash, size_bytes, status, error, created_at, updated_at`

func scanLibraryDocument(row pgx.Row) (*libraryDocument, error) {
	var d libraryDocument
	var created, updated time.Time
	err := row.Scan(&d.ID, &d.OrgID, &d.UserID, &d.SourceThreadID, &d.Filename, &d.RelativePath, &d.ContentHash,
		&d.SizeBytes, &d.Status, &d.Error, &created, &updated)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	d.CreatedAt, d.UpdatedAt = jsTime(created), jsTime(updated)
	return &d, nil
}

type libraryBrowse struct {
	query, status, sort string
	page, pageSize      int
}

func parseLibraryBrowse(r *http.Request) (libraryBrowse, bool) {
	q := r.URL.Query()
	get := func(name, fallback string) string {
		if q.Has(name) {
			return q.Get(name)
		}
		return fallback
	}
	o := libraryBrowse{query: strings.TrimFunc(get("q", ""), isJSSpace), status: get("status", ""), sort: get("sort", "recent")}
	page, okPage := jsNumber(get("page", "1"))
	size, okSize := jsNumber(get("pageSize", "50"))
	if (o.sort != "recent" && o.sort != "name") ||
		len(utf16.Encode([]rune(o.query))) > 200 ||
		!okPage || page != math.Trunc(page) || page < 1 || page > 1_000_000 ||
		!okSize || size != math.Trunc(size) || size < 1 || size > 100 ||
		(o.status != "" && o.status != "ready" && o.status != "failed") {
		return o, false
	}
	o.page, o.pageSize = int(page), int(size)
	return o, true
}

var likeSpecial = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

func (a *API) library(w http.ResponseWriter, r *http.Request) {
	o, ok := parseLibraryBrowse(r)
	if !ok {
		sendError(w, http.StatusBadRequest, "Invalid library filters.")
		return
	}
	ctx := r.Context()
	act, err := a.currentActor(ctx, r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	where := `org_id = $1 and user_id is not distinct from nullif($2::text, '') and ($3::text = '' or status = $3)`
	args := []any{act.OrgID, act.UserID, o.status}
	for _, word := range strings.FieldsFunc(o.query, isJSSpace) {
		args = append(args, "%"+likeSpecial.Replace(word)+"%")
		where += fmt.Sprintf(" and filename ilike $%d", len(args))
	}
	var total int
	if err := a.DB.QueryRow(ctx, `select count(*)::int from library_document where `+where, args...).Scan(&total); err != nil {
		log.Printf("%s %s: %v", r.Method, r.URL.Path, err)
		sendError(w, http.StatusServiceUnavailable, "Library could not be loaded. Try again.")
		return
	}
	page := min(o.page, max(1, (total+o.pageSize-1)/o.pageSize))
	order := "created_at desc, id asc"
	if o.sort == "name" {
		order = "filename asc, id asc"
	}
	rows, err := a.DB.Query(ctx, fmt.Sprintf(`select id::text, filename, relative_path, size_bytes, status, error, created_at
		from library_document where %s order by %s limit %d offset %d`, where, order, o.pageSize, (page-1)*o.pageSize), args...)
	if err != nil {
		log.Printf("%s %s: %v", r.Method, r.URL.Path, err)
		sendError(w, http.StatusServiceUnavailable, "Library could not be loaded. Try again.")
		return
	}
	type item struct {
		ID           string  `json:"id"`
		Filename     string  `json:"filename"`
		RelativePath string  `json:"relativePath"`
		SizeBytes    int     `json:"sizeBytes"`
		Status       string  `json:"status"`
		Error        *string `json:"error"`
		CreatedAt    string  `json:"createdAt"`
	}
	documents := []item{}
	for rows.Next() {
		var d item
		var created time.Time
		if err := rows.Scan(&d.ID, &d.Filename, &d.RelativePath, &d.SizeBytes, &d.Status, &d.Error, &created); err != nil {
			rows.Close()
			a.fail(w, r, err)
			return
		}
		d.CreatedAt = jsTime(created)
		documents = append(documents, d)
	}
	if err := rows.Err(); err != nil {
		log.Printf("%s %s: %v", r.Method, r.URL.Path, err)
		sendError(w, http.StatusServiceUnavailable, "Library could not be loaded. Try again.")
		return
	}
	sendJSON(w, http.StatusOK, struct {
		Total     int    `json:"total"`
		Page      int    `json:"page"`
		PageSize  int    `json:"pageSize"`
		Documents []item `json:"documents"`
	}{total, page, o.pageSize, documents})
}

func (a *API) libraryDocumentGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !uuidPattern.MatchString(id) {
		sendError(w, http.StatusNotFound, "Not found.")
		return
	}
	ctx := r.Context()
	act, err := a.currentActor(ctx, r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	d, err := scanLibraryDocument(a.DB.QueryRow(ctx, `select `+libraryColumns+` from library_document
		where org_id = $1 and id = $2::uuid and user_id is not distinct from nullif($3::text, '')`, act.OrgID, id, act.UserID))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if d == nil {
		sendError(w, http.StatusNotFound, "Not found.")
		return
	}
	sendJSON(w, http.StatusOK, map[string]any{"document": d})
}

func (a *API) libraryDocumentDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !uuidPattern.MatchString(id) {
		sendError(w, http.StatusNotFound, "not found")
		return
	}
	ctx := r.Context()
	act, err := a.currentActor(ctx, r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var path string
	err = a.DB.QueryRow(ctx, `delete from library_document
		where org_id = $1 and id = $2::uuid and user_id is not distinct from nullif($3::text, '') returning relative_path`,
		act.OrgID, id, act.UserID).Scan(&path)
	if errors.Is(err, pgx.ErrNoRows) {
		sendError(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if act.UserID != "" && strings.HasPrefix(path, "files/") && serveURL() != "" {
		if err := a.serveFile(ctx, act, http.MethodDelete, strings.TrimPrefix(path, "files/"), nil, ""); err != nil {
			log.Printf("[library] stored file not removed: %v", err)
		}
	}
	sendJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *API) libraryUpload(w http.ResponseWriter, r *http.Request) {
	if r.ContentLength > maxLibraryUploadRequest {
		sendError(w, http.StatusRequestEntityTooLarge, libraryOverLimit)
		return
	}
	entries, tooLarge, err := readFormEntries(w, r, maxLibraryUploadRequest)
	if tooLarge {
		sendError(w, http.StatusRequestEntityTooLarge, libraryOverLimit)
		return
	}
	if err != nil {
		sendError(w, http.StatusBadRequest, "Invalid multipart body")
		return
	}
	var uploads []formFile
	for _, field := range []string{"files", "file"} {
		for _, e := range entries {
			if f, isFile := e.value.(formFile); isFile && e.name == field {
				uploads = append(uploads, f)
			}
		}
	}
	if len(uploads) == 0 {
		sendError(w, http.StatusBadRequest, "Choose at least one file to import.")
		return
	}
	total := 0
	for _, f := range uploads {
		if len(f.data) == 0 {
			sendError(w, http.StatusBadRequest, fmt.Sprintf(`"%s" is empty. Choose a non-empty file.`, f.name))
			return
		}
		total += len(f.data)
		if total > maxLibraryUpload {
			sendError(w, http.StatusRequestEntityTooLarge, libraryOverLimit)
			return
		}
	}
	names := map[string]bool{}
	for _, f := range uploads {
		clean := safeFileName(f.name)
		if names[clean] {
			sendError(w, http.StatusBadRequest, fmt.Sprintf(`More than one selected file becomes "%s" after filename cleanup. Rename one and try again.`, clean))
			return
		}
		names[clean] = true
		ext := ""
		if i := strings.LastIndex(f.name, "."); i > 0 {
			ext = strings.ToLower(f.name[i:])
		}
		if !uploadExtensions[ext] {
			shown := ext
			if shown == "" {
				shown = "(none)"
			}
			sendError(w, http.StatusUnsupportedMediaType, fmt.Sprintf(`Unsupported file type "%s".`, shown))
			return
		}
	}
	if serveURL() == "" {
		sendError(w, http.StatusServiceUnavailable, "File storage is off: openneko serve is not configured.")
		return
	}
	ctx := r.Context()
	act, err := a.currentActor(ctx, r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if act.UserID == "" {
		sendError(w, http.StatusUnauthorized, "Sign in to add files to your library.")
		return
	}
	type stored struct {
		Document *libraryDocument `json:"document"`
		Created  bool             `json:"created"`
	}
	documents := []stored{}
	for _, f := range uploads {
		name := safeFileName(f.name)
		hash, path := libraryPath(name, f.data)
		if err := a.putServeFile(ctx, act, path, f.data, f.contentType); err != nil {
			var se *serveError
			status, message := http.StatusBadGateway, "openneko serve did not answer"
			if errors.As(err, &se) {
				message = se.message
				if se.status < 500 {
					status = se.status
				}
			} else {
				log.Printf("[library] %s not stored: %v", name, err)
			}
			sendJSON(w, status, struct {
				Error     string   `json:"error"`
				Documents []stored `json:"documents"`
			}{fmt.Sprintf(`"%s" was not stored: %s`, f.name, message), documents})
			return
		}
		d, created, err := a.createLibraryDocument(ctx, act, name, "files/"+path, hash, len(f.data))
		if err != nil {
			a.fail(w, r, err)
			return
		}
		documents = append(documents, stored{d, created})
	}
	sendJSON(w, http.StatusOK, struct {
		OK        bool             `json:"ok"`
		Documents []stored         `json:"documents"`
		Document  *libraryDocument `json:"document"`
		Created   bool             `json:"created"`
	}{true, documents, documents[0].Document, documents[0].Created})
}

// createLibraryDocument returns the user's row for the same content when one
// exists.
func (a *API) createLibraryDocument(ctx context.Context, act actor, name, path, hash string, size int) (*libraryDocument, bool, error) {
	d, err := scanLibraryDocument(a.DB.QueryRow(ctx, `select `+libraryColumns+` from library_document
		where org_id = $1 and user_id = $2 and content_hash = $3 limit 1`, act.OrgID, act.UserID, hash))
	if err != nil || d != nil {
		return d, false, err
	}
	d, err = scanLibraryDocument(a.DB.QueryRow(ctx, `insert into library_document
		(org_id, user_id, filename, relative_path, content_hash, size_bytes, status)
		values ($1, $2, $3, $4, $5, $6, 'ready') returning `+libraryColumns, act.OrgID, act.UserID, name, path, hash, size))
	return d, d != nil, err
}
