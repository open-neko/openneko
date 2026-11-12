package webfront

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/open-neko/neko/apps/openneko/internal/config"
	"github.com/open-neko/neko/apps/openneko/internal/files"
)

const maxUploadSize = 10 * 1024 * 1024

var (
	uploadExtensions = map[string]bool{".pdf": true, ".docx": true, ".pptx": true, ".xlsx": true, ".csv": true,
		".md": true, ".markdown": true, ".txt": true}
	mimeByExt = map[string]string{
		".csv":      "text/csv; charset=utf-8",
		".docx":     "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		".md":       "text/markdown; charset=utf-8",
		".markdown": "text/markdown; charset=utf-8",
		".pdf":      "application/pdf",
		".png":      "image/png",
		".jpg":      "image/jpeg",
		".jpeg":     "image/jpeg",
		".pptx":     "application/vnd.openxmlformats-officedocument.presentationml.presentation",
		".txt":      "text/plain; charset=utf-8",
		".xlsx":     "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	}
	unsafeUploadChar = regexp.MustCompile(`[^a-zA-Z0-9._-]`)
	leadingDots      = regexp.MustCompile(`^\.+`)
)

// auditExport is the SIEM export: the audit chain as NDJSON, with the chain
// check in headers so an importer can refuse a broken chain.
func (a *API) auditExport(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	act, err := a.currentActor(ctx, r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if !act.Admin {
		sendError(w, http.StatusForbidden, "admin only")
		return
	}
	since := 0.0
	if s := strings.TrimSpace(r.URL.Query().Get("since")); s != "" {
		if n, err := strconv.ParseFloat(s, 64); err == nil && !math.IsNaN(n) {
			since = math.Max(0, n)
		}
	}
	ok, length, brokenAt, err := a.verifyAuditChain(ctx, act.OrgID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	rows, err := a.DB.Query(ctx, `
		select seq, entity_kind, entity_id, event, payload::text, payload_hash, prev_hash, chain_hash, created_at
		from audit_chain where org_id = $1 and seq > $2::float8 order by seq limit 5000`, act.OrgID, since)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var lines [][]byte
	for rows.Next() {
		var e struct {
			Seq         int             `json:"seq"`
			EntityKind  string          `json:"entityKind"`
			EntityID    string          `json:"entityId"`
			Event       string          `json:"event"`
			Payload     json.RawMessage `json:"payload"`
			PayloadHash string          `json:"payloadHash"`
			PrevHash    string          `json:"prevHash"`
			ChainHash   string          `json:"chainHash"`
			CreatedAt   string          `json:"createdAt"`
		}
		var payload string
		var created time.Time
		if err := rows.Scan(&e.Seq, &e.EntityKind, &e.EntityID, &e.Event, &payload, &e.PayloadHash, &e.PrevHash, &e.ChainHash, &created); err != nil {
			rows.Close()
			a.fail(w, r, err)
			return
		}
		if e.Payload, err = jsJSON([]byte(payload)); err != nil {
			rows.Close()
			a.fail(w, r, err)
			return
		}
		e.CreatedAt = jsTime(created)
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(e); err != nil {
			rows.Close()
			a.fail(w, r, err)
			return
		}
		lines = append(lines, bytes.TrimRight(buf.Bytes(), "\n"))
	}
	if err := rows.Err(); err != nil {
		a.fail(w, r, err)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/x-ndjson")
	h.Set("X-Audit-Chain-Ok", strconv.FormatBool(ok))
	h.Set("X-Audit-Chain-Length", strconv.Itoa(length))
	if brokenAt > 0 {
		h.Set("X-Audit-Chain-Broken-At", strconv.Itoa(brokenAt))
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(bytes.Join(lines, []byte("\n")))
}

// verifyAuditChain is verifyAuditChain() in
// packages/llm/src/workflows/audit-chain.ts.
func (a *API) verifyAuditChain(ctx context.Context, orgID string) (ok bool, length, brokenAt int, err error) {
	type link struct {
		seq                     int
		payload                 string
		payloadHash, prev, hash string
	}
	rows, err := a.DB.Query(ctx, `select seq, payload::text, payload_hash, prev_hash, chain_hash from audit_chain where org_id = $1 order by seq`, orgID)
	if err != nil {
		return false, 0, 0, err
	}
	links, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (link, error) {
		var l link
		return l, row.Scan(&l.seq, &l.payload, &l.payloadHash, &l.prev, &l.hash)
	})
	if err != nil {
		return false, 0, 0, err
	}
	prev := genesisHash
	for i, l := range links {
		expected := i + 1
		if l.seq != expected {
			return false, len(links), expected, nil
		}
		dec := json.NewDecoder(strings.NewReader(l.payload))
		dec.UseNumber()
		var v any
		if err := dec.Decode(&v); err != nil {
			return false, 0, 0, err
		}
		canonical, err := canonicalJSON(v)
		if err != nil {
			return false, 0, 0, err
		}
		payloadHash := sha256Hex(canonical)
		if payloadHash != l.payloadHash || l.prev != prev ||
			sha256Hex(prev+"\x00"+strconv.Itoa(l.seq)+"\x00"+payloadHash) != l.hash {
			return false, len(links), l.seq, nil
		}
		prev = l.hash
	}
	return true, len(links), 0, nil
}

// workFile is a download boundary, not a workspace browser: only a file
// that an artifact event of a run the caller owns names is served. Every
// refusal and every failure gives the same 404.
func (a *API) workFile(w http.ResponseWriter, r *http.Request) {
	notFound := func() { sendError(w, http.StatusNotFound, "not found") }
	segments := strings.Split(r.PathValue("path"), "/")
	if len(segments) < 4 || segments[0] != "runs" || segments[2] != "artifacts" {
		notFound()
		return
	}
	runID := segments[1]
	ctx := r.Context()
	act, err := a.currentActor(ctx, r)
	if err != nil {
		notFound()
		return
	}
	if owned, err := a.workRunOwned(ctx, act, runID); err != nil || !owned {
		notFound()
		return
	}
	emitted, err := a.emittedRunArtifact(ctx, act.OrgID, runID, r.PathValue("path"))
	if err != nil || !emitted {
		notFound()
		return
	}
	data, name, err := readRunArtifact(act.OrgID, runID, strings.Join(segments[3:], "/"))
	if err != nil {
		notFound()
		return
	}
	mimeType := mimeByExt[strings.ToLower(filepath.Ext(name))]
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	h := w.Header()
	h.Set("Content-Type", mimeType)
	h.Set("Content-Disposition", `attachment; filename="`+strings.ReplaceAll(name, `"`, "")+`"`)
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (a *API) emittedRunArtifact(ctx context.Context, orgID, runID, requested string) (bool, error) {
	want, ok := canonicalRunArtifactPath(requested, runID)
	if !ok {
		return false, nil
	}
	rows, err := a.DB.Query(ctx, `select payload::text from work_run_event where org_id = $1 and run_id = $2 order by id`, orgID, runID)
	if err != nil {
		return false, err
	}
	payloads, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return false, err
	}
	for _, p := range payloads {
		var e struct {
			Type     string `json:"type"`
			Artifact *struct {
				Path any `json:"path"`
			} `json:"artifact"`
		}
		if json.Unmarshal([]byte(p), &e) != nil || e.Type != "artifact" || e.Artifact == nil {
			continue
		}
		if s, isString := e.Artifact.Path.(string); isString {
			if got, ok := canonicalRunArtifactPath(s, runID); ok && got == want {
				return true, nil
			}
		}
	}
	return false, nil
}

// canonicalRunArtifactPath is canonicalRunArtifactPath() of the web app:
// only a path under this run's artifacts folder has a canonical form.
func canonicalRunArtifactPath(value, runID string) (string, bool) {
	if strings.TrimFunc(value, isJSSpace) == "" {
		return "", false
	}
	raw := strings.ReplaceAll(value, `\`, "/")
	prefix := "runs/" + runID + "/artifacts/"
	candidate := raw
	if !strings.HasPrefix(raw, prefix) {
		i := strings.LastIndex(raw, "/"+prefix)
		if i < 0 {
			return "", false
		}
		candidate = raw[i+1:]
	}
	suffix := candidate[len(prefix):]
	if suffix == "" || strings.Contains(suffix, "\x00") || badRelPath(suffix) {
		return "", false
	}
	return prefix + suffix, true
}

func badRelPath(p string) bool {
	if strings.HasPrefix(p, "/") {
		return true
	}
	for _, part := range strings.Split(p, "/") {
		if part == "" || part == "." || part == ".." {
			return true
		}
	}
	return false
}

// readRunArtifact reads a regular file that lies, after symlinks, inside
// the run's artifacts folder.
func readRunArtifact(orgID, runID, artifactPath string) ([]byte, string, error) {
	if runID == "" || strings.ContainsAny(runID, `/\`) || runID == "." || runID == ".." {
		return nil, "", errors.New("invalid work run id")
	}
	normalized := strings.ReplaceAll(artifactPath, `\`, "/")
	if normalized == "" || badRelPath(normalized) {
		return nil, "", errors.New("invalid artifact path")
	}
	roots, err := orgWorkspace(orgID)
	if err != nil {
		return nil, "", err
	}
	declaredRoot := filepath.Join(roots.Runs, runID, "artifacts")
	declaredFile := filepath.Join(declaredRoot, filepath.FromSlash(normalized))
	if !isWithin(declaredRoot, declaredFile) {
		return nil, "", errors.New("artifact path escapes the run")
	}
	physicalRoot, err := filepath.EvalSymlinks(declaredRoot)
	if err != nil {
		return nil, "", err
	}
	physicalFile, err := filepath.EvalSymlinks(declaredFile)
	if err != nil {
		return nil, "", err
	}
	if !isWithin(physicalRoot, physicalFile) {
		return nil, "", errors.New("artifact resolves outside the run")
	}
	st, err := os.Stat(physicalFile)
	if err != nil {
		return nil, "", err
	}
	if !st.Mode().IsRegular() {
		return nil, "", errors.New("artifact is not a regular file")
	}
	data, err := os.ReadFile(physicalFile)
	return data, filepath.Base(physicalFile), err
}

type formFile struct {
	name, contentType string
	data              []byte
}

// readForm reads a form body as Request.formData() does, keeping the first
// entry of each name. tooLarge is set when the body passes the upload cap.
func readForm(w http.ResponseWriter, r *http.Request) (fields map[string]any, tooLarge bool, err error) {
	body := http.MaxBytesReader(w, r.Body, maxUploadSize+64*1024)
	mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		return nil, false, err
	}
	fields = map[string]any{}
	if mediaType == "application/x-www-form-urlencoded" {
		raw, err := io.ReadAll(body)
		if err != nil {
			return nil, isMaxBytes(err), err
		}
		values, err := url.ParseQuery(string(raw))
		if err != nil {
			return nil, false, err
		}
		for k, v := range values {
			fields[k] = v[0]
		}
		return fields, false, nil
	}
	if mediaType != "multipart/form-data" || params["boundary"] == "" {
		return nil, false, errors.New("not a form body")
	}
	mr := multipart.NewReader(body, params["boundary"])
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			return fields, false, nil
		}
		if err != nil {
			return nil, isMaxBytes(err), err
		}
		data, err := io.ReadAll(part)
		if err != nil {
			return nil, isMaxBytes(err), err
		}
		name := part.FormName()
		if _, seen := fields[name]; seen || name == "" {
			continue
		}
		_, dp, _ := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
		if filename, isFile := dp["filename"]; isFile {
			fields[name] = formFile{name: filename, contentType: strings.ToLower(part.Header.Get("Content-Type")), data: data}
		} else {
			fields[name] = string(data)
		}
	}
}

func isMaxBytes(err error) bool {
	var mb *http.MaxBytesError
	return errors.As(err, &mb)
}

func formString(v any, fallback string) string {
	switch t := v.(type) {
	case string:
		return t
	case formFile:
		return "[object File]"
	}
	return fallback
}

func (a *API) workUpload(w http.ResponseWriter, r *http.Request) {
	overLimit := fmt.Sprintf("File is over %d MB.", maxUploadSize/(1024*1024))
	if r.ContentLength > maxUploadSize+4096 {
		sendError(w, http.StatusRequestEntityTooLarge, overLimit)
		return
	}
	form, tooLarge, err := readForm(w, r)
	if tooLarge {
		sendError(w, http.StatusRequestEntityTooLarge, overLimit)
		return
	}
	if err != nil {
		sendError(w, http.StatusBadRequest, "Invalid multipart body")
		return
	}
	threadID := strings.TrimFunc(formString(form["threadId"], ""), isJSSpace)
	file, isFile := form["file"].(formFile)
	if threadID == "" || !isFile {
		sendError(w, http.StatusBadRequest, "threadId and file are required")
		return
	}
	if strings.HasPrefix(file.contentType, "image/") {
		sendError(w, http.StatusUnsupportedMediaType, "Image uploads are not supported yet.")
		return
	}
	if len(file.data) > maxUploadSize {
		sendError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf(`"%s" is over %d MB.`, file.name, maxUploadSize/(1024*1024)))
		return
	}
	ext := ""
	if i := strings.LastIndex(file.name, "."); i > 0 {
		ext = strings.ToLower(file.name[i:])
	}
	if !uploadExtensions[ext] {
		shown := ext
		if shown == "" {
			shown = "(none)"
		}
		sendError(w, http.StatusUnsupportedMediaType, fmt.Sprintf(`Unsupported file type "%s".`, shown))
		return
	}
	ctx := r.Context()
	act, err := a.currentActor(ctx, r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if owned, err := a.workThreadOwned(ctx, act, threadID); err != nil {
		a.fail(w, r, err)
		return
	} else if !owned {
		sendError(w, http.StatusNotFound, "Thread not found")
		return
	}
	roots, err := orgWorkspace(act.OrgID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	name := file.name
	if name == "" {
		name = "upload.bin"
	}
	name = safeFileName(name)
	dir := filepath.Join(roots.Uploads, threadID)
	absolute := filepath.Join(dir, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		a.fail(w, r, err)
		return
	}
	if err := os.WriteFile(absolute, file.data, 0o644); err != nil {
		a.fail(w, r, err)
		return
	}
	if formString(form["catalog"], "true") != "false" && serveURL() != "" {
		if err := a.catalogUpload(ctx, act, threadID, name, file.data); err != nil {
			log.Printf("[work-upload] library auto-add failed (upload succeeded): %v", err)
		}
	}
	sendJSON(w, http.StatusOK, map[string]any{"file": struct {
		Name         string `json:"name"`
		Size         int    `json:"size"`
		RelativePath string `json:"relativePath"`
		AbsolutePath string `json:"absolutePath"`
	}{name, len(file.data), "uploads/" + threadID + "/" + name, absolute}})
}

// safeFileName is safeFileName() in apps/web/src/lib/work-files.ts.
func safeFileName(name string) string {
	base := strings.TrimRight(name, "/")
	if base != "" {
		base = base[strings.LastIndex(base, "/")+1:]
	}
	base = base[strings.LastIndex(base, `\`)+1:]
	var b strings.Builder
	for _, r := range base {
		switch {
		case r > 0xffff:
			b.WriteString("__")
		case !unsafeUploadChar.MatchString(string(r)):
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := leadingDots.ReplaceAllString(b.String(), "_")
	if len(out) > 200 {
		out = out[:200]
	}
	if out == "" {
		return "upload.bin"
	}
	return out
}

func serveURL() string {
	return strings.TrimRight(strings.TrimSpace(os.Getenv("OPENNEKO_SERVE_URL")), "/")
}

// catalogUpload copies the upload into the user's folder in openneko serve
// and records it, as storeLibraryFile() and createLibraryDocument() do.
func (a *API) catalogUpload(ctx context.Context, act actor, threadID, name string, data []byte) error {
	if act.UserID == "" {
		return errors.New("no signed-in user")
	}
	key, err := config.SigningSecret(a.SecretsDir, files.TokenLabel)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	path := "library/" + hash + "/" + name
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, serveURL()+"/v1/files/content?path="+encodeURIComponent(path), bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+files.MintToken(key, act.OrgID, act.UserID, time.Now().Add(time.Minute)))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	body, _ := io.ReadAll(io.LimitReader(res.Body, 64*1024))
	res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode > 299 {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(body, &e) == nil && e.Error != "" {
			return errors.New(e.Error)
		}
		return fmt.Errorf("openneko serve answered %d", res.StatusCode)
	}
	_, err = a.DB.Exec(ctx, `
		insert into library_document (org_id, user_id, source_thread_id, filename, relative_path, content_hash, size_bytes, status)
		select $1, $2, $3, $4, $5, $6, $7, 'ready'
		where not exists (select 1 from library_document where org_id = $1 and user_id = $2 and content_hash = $6)`,
		act.OrgID, act.UserID, threadID, name, "files/"+path, hash, len(data))
	return err
}
