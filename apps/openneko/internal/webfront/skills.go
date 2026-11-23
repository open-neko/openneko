package webfront

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const maxSkillEditSize = 1 << 20

var (
	skillNamePattern   = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)
	skillFrontmatter   = regexp.MustCompile(`(?s)^---\n(.*?)\n---`)
	skillDescriptionRe = regexp.MustCompile(`(?m)^description:\s*([^\r\n\x{2028}\x{2029}]*)`)
)

type skillFile struct {
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"`
	mtime int64
}

type skillSummary struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	FileCount   int    `json:"fileCount"`
	UpdatedAt   string `json:"updatedAt"`
	Scope       string `json:"scope"`
	CanEdit     bool   `json:"canEdit"`
	CanShare    bool   `json:"canShare"`
}

type skillDetail struct {
	skillSummary
	Path          string      `json:"path"`
	SkillMarkdown string      `json:"skillMarkdown"`
	Files         []skillFile `json:"files"`
}

func skillDescription(markdown string) string {
	m := skillFrontmatter.FindStringSubmatch(markdown)
	if m == nil {
		return ""
	}
	d := skillDescriptionRe.FindStringSubmatch(m[1])
	if d == nil {
		return ""
	}
	v := strings.TrimFunc(d[1], isJSSpace)
	if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
		v = v[1 : len(v)-1]
	}
	return v
}

func isJSSpace(r rune) bool {
	return r == 0xFEFF || unicode.Is(unicode.Zs, r) || strings.ContainsRune("\t\n\v\f\r\u2028\u2029", r)
}

func listSkillFiles(dir string) []skillFile {
	var out []skillFile
	var walk func(dir, prefix string)
	walk = func(dir, prefix string) {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			rel := e.Name()
			if prefix != "" {
				rel = prefix + "/" + e.Name()
			}
			if e.IsDir() {
				walk(filepath.Join(dir, e.Name()), rel)
			} else if e.Type().IsRegular() {
				if st, err := os.Stat(filepath.Join(dir, e.Name())); err == nil {
					out = append(out, skillFile{Path: rel, Bytes: st.Size(), mtime: st.ModTime().UnixMilli()})
				}
			}
		}
	}
	walk(dir, "")
	return out
}

func summarizeSkill(name, dir string, files []skillFile) (skillSummary, string) {
	markdown, _ := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	latest := int64(0)
	for _, f := range files {
		latest = max(latest, f.mtime)
	}
	if latest == 0 {
		latest = time.Now().UnixMilli()
	}
	return skillSummary{Name: name, Description: skillDescription(string(markdown)), FileCount: len(files),
		UpdatedAt: jsTime(time.UnixMilli(latest))}, string(markdown)
}

func listSkillFolders(root string) []skillSummary {
	entries, _ := os.ReadDir(root)
	out := []skillSummary{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		files := listSkillFiles(dir)
		if len(files) == 0 {
			continue
		}
		s, _ := summarizeSkill(e.Name(), dir, files)
		out = append(out, s)
	}
	return out
}

func listWorkSkills(roots orgRoots) []skillSummary {
	out := listSkillFolders(roots.Skills)
	slices.SortFunc(out, func(a, b skillSummary) int { return localeCompare(a.Name, b.Name) })
	return out
}

// visibleSkills is the actor's own skills and the team skills the actor
// holds. An own skill hides a team skill of the same name.
func visibleSkills(roots orgRoots, act actor, held heldSet) []skillSummary {
	out := []skillSummary{}
	own := map[string]bool{}
	if act.UserID != "" {
		for _, s := range listSkillFolders(memberSkillsRoot(roots.Org, act.UserID)) {
			if validSkillName(s.Name) {
				own[s.Name] = true
				out = append(out, s.withFlags(act, skillScopeOwn))
			}
		}
	}
	for _, s := range listSkillFolders(roots.Skills) {
		if held.has(s.Name) && !own[s.Name] {
			out = append(out, s.withFlags(act, skillScopeTeam))
		}
	}
	slices.SortFunc(out, func(a, b skillSummary) int { return localeCompare(a.Name, b.Name) })
	return out
}

func (s skillSummary) withFlags(act actor, scope string) skillSummary {
	f := skillFlags(act, visibleSkill{Name: s.Name, Scope: scope})
	s.Scope, s.CanEdit, s.CanShare = f.Scope, f.CanEdit, f.CanShare
	return s
}

func (a *API) workSkills(w http.ResponseWriter, r *http.Request) {
	act, err := a.currentActor(r.Context(), r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	roots, err := orgWorkspace(act.OrgID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	held, err := a.heldItems(r.Context(), act, "skill")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	sendJSON(w, http.StatusOK, struct {
		Skills    []skillSummary `json:"skills"`
		CanImport bool           `json:"canImport"`
		CanCopy   bool           `json:"canCopy"`
	}{visibleSkills(roots, act, held), act.Admin, act.UserID != ""})
}

func validSkillName(name string) bool {
	return filepath.Base(name) == name && name != "." && name != ".." && skillNamePattern.MatchString(name)
}

func (a *API) workSkill(w http.ResponseWriter, r *http.Request) {
	act, _, s, ok := a.skillTarget(w, r, "Skill not found")
	if !ok {
		return
	}
	files := listSkillFiles(s.Dir)
	summary, markdown := summarizeSkill(s.Name, s.Dir, files)
	summary = summary.withFlags(act, s.Scope)
	slices.SortFunc(files, func(a, b skillFile) int { return localeCompare(a.Path, b.Path) })
	sendJSON(w, http.StatusOK, map[string]any{"skill": skillDetail{skillSummary: summary, Path: s.Dir, SkillMarkdown: markdown, Files: files}})
}

func (a *API) deleteSkill(w http.ResponseWriter, r *http.Request) {
	act, _, s, ok := a.skillTarget(w, r, "Skill not found")
	if !ok {
		return
	}
	if !canChangeSkill(act, s) {
		sendError(w, http.StatusForbidden, teamSkillChange)
		return
	}
	if err := os.RemoveAll(s.Dir); err != nil {
		a.fail(w, r, err)
		return
	}
	a.recordSkillChange(r.Context(), act, s, "Deleted skill: "+s.Name)
	sendJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// skillFilePath resolves a path inside one skill folder. Unlike the Node
// route, it refuses a symlink that leads out of the skill folder.
func skillFilePath(dir, rel string) (string, bool) {
	if rel == "" {
		return "", false
	}
	normalized := strings.TrimLeft(strings.ReplaceAll(rel, "\\", "/"), "/")
	for _, part := range strings.Split(normalized, "/") {
		if part == "" || part == "." || part == ".." {
			return "", false
		}
	}
	abs := filepath.Join(dir, filepath.FromSlash(normalized))
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return abs, isWithin(dir, abs)
	}
	if realAbs, err := filepath.EvalSymlinks(abs); err == nil && !isWithin(realDir, realAbs) {
		return "", false
	}
	return abs, isWithin(dir, abs)
}

func looksBinary(data []byte) bool {
	return bytes.IndexByte(data[:min(len(data), 8192)], 0) >= 0
}

// decodeUTF8 is Buffer.toString("utf8"): each invalid byte becomes U+FFFD.
func decodeUTF8(data []byte) string {
	if utf8.Valid(data) {
		return string(data)
	}
	var b strings.Builder
	for len(data) > 0 {
		r, size := utf8.DecodeRune(data)
		if r == utf8.RuneError && size == 1 {
			b.WriteRune('�')
		} else {
			b.Write(data[:size])
		}
		data = data[size:]
	}
	return b.String()
}

func (a *API) skillFileGet(w http.ResponseWriter, r *http.Request) {
	_, _, s, ok := a.skillTarget(w, r, "File not found")
	if !ok {
		return
	}
	rel := r.URL.Query().Get("path")
	if rel == "" {
		sendError(w, http.StatusBadRequest, "Missing file path")
		return
	}
	abs, ok := skillFilePath(s.Dir, rel)
	st, statErr := os.Stat(abs)
	if !ok || statErr != nil || !st.Mode().IsRegular() {
		sendError(w, http.StatusNotFound, "File not found")
		return
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	file := map[string]any{"path": rel, "bytes": st.Size(), "binary": true, "editable": false, "truncated": false, "text": nil}
	if !looksBinary(data) {
		truncated := len(data) > maxSkillEditSize
		if truncated {
			data = data[:maxSkillEditSize]
		}
		file = map[string]any{"path": rel, "bytes": st.Size(), "binary": false, "editable": !truncated, "truncated": truncated, "text": decodeUTF8(data)}
	}
	sendJSON(w, http.StatusOK, map[string]any{"file": orderedFile(file)})
}

func orderedFile(f map[string]any) json.RawMessage {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range []string{"path", "bytes", "binary", "editable", "truncated", "text"} {
		if i > 0 {
			buf.WriteByte(',')
		}
		key, _ := json.Marshal(k)
		var v bytes.Buffer
		enc := json.NewEncoder(&v)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(f[k])
		buf.Write(key)
		buf.WriteByte(':')
		buf.Write(bytes.TrimRight(v.Bytes(), "\n"))
	}
	buf.WriteByte('}')
	return buf.Bytes()
}

func (a *API) skillFilePut(w http.ResponseWriter, r *http.Request) {
	act, _, s, ok := a.skillTarget(w, r, "File not found")
	if !ok {
		return
	}
	if !canChangeSkill(act, s) {
		sendError(w, http.StatusForbidden, teamSkillChange)
		return
	}
	rel := r.URL.Query().Get("path")
	if rel == "" {
		sendError(w, http.StatusBadRequest, "Missing file path")
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 8*maxSkillEditSize+1))
	if err == nil && len(raw) > 8*maxSkillEditSize {
		sendError(w, http.StatusRequestEntityTooLarge, "File is too large to save from here.")
		return
	}
	var body map[string]any
	if err != nil || json.Unmarshal(raw, &body) != nil {
		sendError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	content, isString := body["content"].(string)
	if !isString {
		sendError(w, http.StatusBadRequest, "content must be a string")
		return
	}
	if len(content) > maxSkillEditSize {
		sendError(w, http.StatusRequestEntityTooLarge, "File is too large to save from here.")
		return
	}
	abs, ok := skillFilePath(s.Dir, rel)
	if !ok {
		sendError(w, http.StatusBadRequest, "Invalid file path")
		return
	}
	if st, err := os.Stat(abs); err != nil || !st.Mode().IsRegular() {
		sendError(w, http.StatusNotFound, "File not found")
		return
	}
	existing, err := os.ReadFile(abs)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if looksBinary(existing) {
		sendError(w, http.StatusUnsupportedMediaType, "This file is not a text file and cannot be edited here.")
		return
	}
	if err := writeFileKeepMode(abs, []byte(content)); err != nil {
		a.fail(w, r, err)
		return
	}
	a.recordSkillChange(r.Context(), act, s, "Updated skill: "+s.Name)
	sendJSON(w, http.StatusOK, struct {
		OK    bool `json:"ok"`
		Bytes int  `json:"bytes"`
	}{true, len(content)})
}

type assetLink struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

func (a *API) workAssets(w http.ResponseWriter, r *http.Request) {
	orgID, err := a.OrgID(r.Context())
	if err != nil {
		a.fail(w, r, err)
		return
	}
	roots, err := orgWorkspace(orgID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	skills, memory := []assetLink{}, []assetLink{}
	entries, _ := os.ReadDir(roots.Skills)
	for _, e := range entries {
		if e.IsDir() {
			skills = append(skills, assetLink{e.Name(), "/api/v1/work/files/skills/" + e.Name() + "/SKILL.md"})
		}
	}
	entries, _ = os.ReadDir(roots.Memory)
	for _, e := range entries {
		if e.Type().IsRegular() && strings.HasSuffix(e.Name(), ".md") {
			memory = append(memory, assetLink{e.Name(), "/api/v1/work/files/memory/" + e.Name()})
		}
	}
	byName := func(a, b assetLink) int { return localeCompare(a.Name, b.Name) }
	slices.SortFunc(skills, byName)
	slices.SortFunc(memory, byName)
	sendJSON(w, http.StatusOK, struct {
		Skills []assetLink `json:"skills"`
		Memory []assetLink `json:"memory"`
	}{skills, memory})
}

const (
	skillArchiveMax  = 16 << 20
	skillArchiveForm = 17 << 20
)

func (a *API) importSkill(w http.ResponseWriter, r *http.Request) {
	orgID, ok := a.requireAdmin(w, r)
	if !ok {
		return
	}
	userID := ""
	if u, err := a.currentUser(r.Context(), r, orgID); err == nil && u != nil {
		userID = u.ID
	}
	tooLarge := "Skill archive must be at most 16 MB."
	if r.ContentLength > skillArchiveForm {
		sendError(w, http.StatusRequestEntityTooLarge, tooLarge)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, skillArchiveForm+(1<<20))
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			sendError(w, http.StatusRequestEntityTooLarge, tooLarge)
			return
		}
		sendError(w, http.StatusBadRequest, "Choose a .skill or ZIP archive.")
		return
	}
	defer r.MultipartForm.RemoveAll()
	files := r.MultipartForm.File["file"]
	lower := ""
	if len(files) > 0 {
		lower = strings.ToLower(files[0].Filename)
	}
	if len(files) == 0 || !(strings.HasSuffix(lower, ".skill") || strings.HasSuffix(lower, ".zip")) {
		sendError(w, http.StatusBadRequest, "Choose a .skill or ZIP archive.")
		return
	}
	if files[0].Size == 0 || files[0].Size > skillArchiveMax {
		sendError(w, http.StatusRequestEntityTooLarge, tooLarge)
		return
	}
	f, err := files[0].Open()
	if err != nil {
		a.fail(w, r, err)
		return
	}
	data, err := io.ReadAll(f)
	f.Close()
	if err != nil {
		a.fail(w, r, err)
		return
	}
	roots, err := orgWorkspace(orgID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	name, err := importSkillArchive(roots, data)
	var archiveErr *skillArchiveError
	if errors.As(err, &archiveErr) {
		sendError(w, archiveErr.status, archiveErr.msg)
		return
	}
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.recordConfigChange(r.Context(), orgID, "Imported skill: "+name, "skill", name, userID)
	sendJSON(w, http.StatusCreated, map[string]string{"name": name})
}
