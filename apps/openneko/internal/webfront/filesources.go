package webfront

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/text/unicode/norm"
	"gopkg.in/yaml.v3"
)

const (
	maxManagedFileSize        = 10 << 20
	maxManagedFileUploadSize  = 25 << 20
	maxManagedFileSourceSize  = 100 << 20
	maxManagedFileUploadCount = 10
	maxManagedFileSourceCount = 500
	managedFileLockStale      = 10 * time.Minute
)

var (
	managedSourceName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}$`)
	managedFileName   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._ ()-]{0,127}$`)
)

type managedFile struct {
	Name           string `json:"name"`
	Size           int64  `json:"size"`
	ContentType    string `json:"contentType"`
	ChecksumSha256 string `json:"checksumSha256"`
}

type managedSource struct {
	base, orgID, name string
}

func newManagedSource(configFile, orgID, name string) managedSource {
	base := strings.TrimSpace(os.Getenv("OPENNEKO_GRAPHJIN_FILES_LOCAL_DIR"))
	if base == "" {
		base = filepath.Join(filepath.Dir(configFile), "files")
	}
	return managedSource{base, orgID, name}
}

func (s managedSource) dirName() (string, error) {
	if !managedSourceName.MatchString(s.name) {
		return "", errors.New("register_source file name must start with a letter and contain only letters, digits, and underscores (64 characters maximum)")
	}
	sum := sha256.Sum256([]byte(s.orgID))
	return "org_" + hex.EncodeToString(sum[:])[:12] + "_" + s.name, nil
}

func (s managedSource) controlPath(suffix string) (string, error) {
	dir, err := s.dirName()
	if err != nil {
		return "", err
	}
	return filepath.Join(s.base, "."+dir+"."+suffix), nil
}

func writeNew(path string, content []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	_, err = f.Write(content)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

func (s managedSource) lock() (func(), error) {
	if err := os.MkdirAll(s.base, 0o755); err != nil {
		return nil, err
	}
	path, err := s.controlPath("lock")
	if err != nil {
		return nil, err
	}
	token := rand.Text()
	busy := errors.New("managed file source has another operation in progress")
	if err := writeNew(path, []byte(token), 0o600); err != nil {
		if !errors.Is(err, fs.ErrExist) {
			return nil, err
		}
		info, statErr := os.Stat(path)
		if statErr != nil || time.Since(info.ModTime()) <= managedFileLockStale {
			return nil, busy
		}
		_ = os.Remove(path)
		if err := writeNew(path, []byte(token), 0o600); err != nil {
			if errors.Is(err, fs.ErrExist) {
				return nil, busy
			}
			return nil, err
		}
	}
	return func() {
		if current, _ := os.ReadFile(path); string(current) == token {
			_ = os.Remove(path)
		}
	}, nil
}

func (s managedSource) assertMutable() error {
	path, err := s.controlPath("approved")
	if err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("managed file source approval marker is invalid")
	}
	return errors.New("managed file source is active; use a new source name for new content")
}

func safeManagedFileName(value string) (string, error) {
	name := norm.NFKC.String(value)
	if strings.ContainsAny(name, "/\\\x00") || !managedFileName.MatchString(name) || strings.HasSuffix(name, " ") || strings.HasSuffix(name, ".") {
		return "", errors.New("managed file names must be 1-128 safe filename characters and may not contain a path")
	}
	return name, nil
}

func (s managedSource) ensureDir() (string, error) {
	if err := os.MkdirAll(s.base, 0o755); err != nil {
		return "", err
	}
	baseReal, err := filepath.EvalSymlinks(s.base)
	if err != nil {
		return "", err
	}
	if baseReal, err = filepath.Abs(baseReal); err != nil {
		return "", err
	}
	name, err := s.dirName()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(s.base, name)
	info, err := os.Lstat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := os.Mkdir(dir, 0o755); err != nil {
			return "", err
		}
	case err != nil:
		return "", err
	case !info.IsDir():
		return "", errors.New("managed file source path is not a regular directory")
	}
	dirReal, err := filepath.EvalSymlinks(dir)
	if err == nil {
		dirReal, err = filepath.Abs(dirReal)
	}
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(dirReal, baseReal+string(filepath.Separator)) {
		return "", errors.New("managed file source directory escaped its storage root")
	}
	return dirReal, nil
}

func (s managedSource) list() ([]managedFile, error) {
	dir, err := s.ensureDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	files := []managedFile{}
	for _, e := range entries {
		if !e.Type().IsRegular() {
			return nil, errors.New(`managed file source contains an unsupported entry "` + e.Name() + `"`)
		}
		name, err := safeManagedFileName(e.Name())
		if err != nil {
			return nil, err
		}
		content, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(content)
		files = append(files, managedFile{name, int64(len(content)), "application/octet-stream", hex.EncodeToString(sum[:])})
	}
	sort.SliceStable(files, func(i, j int) bool { return localeCompare(files[i].Name, files[j].Name) < 0 })
	return files, nil
}

type managedUpload struct {
	name    string
	content []byte
}

func (s managedSource) stage(uploads []managedUpload) error {
	names := make([]string, len(uploads))
	seen := map[string]bool{}
	var size int64
	for i, u := range uploads {
		name, err := safeManagedFileName(u.name)
		if err != nil {
			return err
		}
		if seen[name] {
			return errors.New("the upload contains duplicate file names")
		}
		seen[name] = true
		names[i] = name
		size += int64(len(u.content))
	}
	if size > maxManagedFileUploadSize {
		return fmt.Errorf("upload exceeds the %d MB request limit", maxManagedFileUploadSize>>20)
	}
	existing, err := s.list()
	if err != nil {
		return err
	}
	if len(existing)+len(uploads) > maxManagedFileSourceCount {
		return errors.New("managed file source has reached its file-count limit")
	}
	for _, f := range existing {
		size += f.Size
	}
	if size > maxManagedFileSourceSize {
		return errors.New("managed file source has reached its storage limit")
	}
	dir, err := s.ensureDir()
	if err != nil {
		return err
	}
	var created []string
	for i, u := range uploads {
		target := filepath.Join(dir, names[i])
		if err := writeNew(target, u.content, 0o644); err != nil {
			if errors.Is(err, fs.ErrExist) {
				err = errors.New(`managed file "` + names[i] + `" already exists`)
			}
			for _, c := range created {
				_ = os.Remove(c)
			}
			return err
		}
		created = append(created, target)
	}
	return nil
}

// graphjinConfigHasSource is graphjinConfigHasSource() in
// packages/llm/src/graphjin/persist-source-config.ts.
func graphjinConfigHasSource(configFile, name string) (bool, error) {
	raw, err := os.ReadFile(configFile)
	if err != nil {
		return false, err
	}
	var doc yaml.Node
	if yaml.Unmarshal(raw, &doc) != nil || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return false, errors.New("GraphJin config is unavailable for source validation")
	}
	root := doc.Content[0]
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value != "sources" {
			continue
		}
		sources := root.Content[i+1]
		if sources.Kind != yaml.SequenceNode {
			return false, errors.New("GraphJin config sources must be a YAML list")
		}
		for _, item := range sources.Content {
			if item.Kind != yaml.MappingNode {
				continue
			}
			for j := 0; j+1 < len(item.Content); j += 2 {
				if v := item.Content[j+1]; item.Content[j].Value == "name" && v.Kind == yaml.ScalarNode && v.ShortTag() == "!!str" && v.Value == name {
					return true, nil
				}
			}
		}
		return false, nil
	}
	return false, nil
}

func (a *API) sourceConfigEnabled(r *http.Request, orgID string) (bool, error) {
	var enabled bool
	var raw []byte
	err := a.DB.QueryRow(r.Context(), `select enabled, config from llm_provider_config
		where org_id = $1 and scope = 'graphjin-config' limit 1`, orgID).Scan(&enabled, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return true, nil
	}
	return graphjinConfigSettingsFrom(true, enabled, raw).SourceConfigEnabled, err
}

// readUploadForm is `request.formData()`: it takes multipart and URL-encoded
// bodies and refuses any other type with the undici message.
func readUploadForm(r *http.Request) (*multipart.Form, error) {
	mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	switch mediaType {
	case "multipart/form-data":
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			return nil, err
		}
		return r.MultipartForm, nil
	case "application/x-www-form-urlencoded":
		if err := r.ParseForm(); err != nil {
			return nil, err
		}
		return &multipart.Form{Value: r.PostForm}, nil
	}
	return nil, errors.New(`Content-Type was not one of "multipart/form-data" or "application/x-www-form-urlencoded".`)
}

func (a *API) fileSourceUpload(w http.ResponseWriter, r *http.Request) {
	act, err := a.currentActor(r.Context(), r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if !act.Admin {
		sendError(w, http.StatusNotFound, "not found")
		return
	}
	if on, err := a.sourceConfigEnabled(r, act.OrgID); err != nil {
		a.fail(w, r, err)
		return
	} else if !on {
		sendError(w, http.StatusNotFound, "not found")
		return
	}
	configFile := strings.TrimSpace(os.Getenv("OPENNEKO_GRAPHJIN_CONFIG"))
	if configFile == "" {
		sendError(w, http.StatusServiceUnavailable, "managed GraphJin file storage is unavailable")
		return
	}
	const bodyLimit = maxManagedFileUploadSize + 1<<20
	if n, err := strconv.ParseFloat(strings.TrimSpace(r.Header.Get("Content-Length")), 64); err == nil && n > bodyLimit {
		sendError(w, http.StatusRequestEntityTooLarge, "file upload is too large")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, bodyLimit)
	form, err := readUploadForm(r)
	if form != nil && form.File != nil {
		defer form.RemoveAll()
	}
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		sendError(w, http.StatusRequestEntityTooLarge, "file upload is too large")
		return
	}
	if err != nil {
		if mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mediaType == "multipart/form-data" {
			err = errors.New("Failed to parse body as FormData.")
		}
		sendError(w, http.StatusBadRequest, err.Error())
		return
	}
	sourceName := ""
	if v := form.Value["sourceName"]; len(v) > 0 {
		sourceName = strings.TrimSpace(v[0])
	} else if len(form.File["sourceName"]) > 0 {
		sourceName = "[object File]"
	}
	headers := form.File["files"]
	if sourceName == "" || len(headers) == 0 {
		sendError(w, http.StatusBadRequest, "sourceName and at least one file are required")
		return
	}
	if len(headers) > maxManagedFileUploadCount {
		sendError(w, http.StatusBadRequest, fmt.Sprintf("upload no more than %d files at a time", maxManagedFileUploadCount))
		return
	}
	for _, h := range headers {
		if h.Size == 0 || h.Size > maxManagedFileSize {
			sendError(w, http.StatusBadRequest, fmt.Sprintf(`"%s" must be between 1 byte and %d MB`, partFilename(h), maxManagedFileSize>>20))
			return
		}
	}
	files, err := a.stageFileSource(newManagedSource(configFile, act.OrgID, sourceName), configFile, headers)
	if err != nil {
		sendError(w, http.StatusBadRequest, err.Error())
		return
	}
	var total int64
	for _, f := range files {
		total += f.Size
	}
	type manifest struct {
		SourceName string        `json:"sourceName"`
		Ready      bool          `json:"ready"`
		Files      []managedFile `json:"files"`
		TotalSize  int64         `json:"totalSize"`
	}
	sendJSON(w, http.StatusCreated, map[string]manifest{"managedLocalFiles": {sourceName, true, files, total}})
}

// partFilename is the name the browser sent. FileHeader.Filename drops the
// directory part, and Node refuses a name that holds one.
func partFilename(h *multipart.FileHeader) string {
	if _, params, err := mime.ParseMediaType(h.Header.Get("Content-Disposition")); err == nil {
		if name, ok := params["filename"]; ok {
			return name
		}
	}
	return h.Filename
}

func (a *API) stageFileSource(src managedSource, configFile string, headers []*multipart.FileHeader) ([]managedFile, error) {
	release, err := src.lock()
	if err != nil {
		return nil, err
	}
	defer release()
	if err := src.assertMutable(); err != nil {
		return nil, err
	}
	active, err := graphjinConfigHasSource(configFile, src.name)
	if err != nil {
		return nil, err
	}
	if active {
		return nil, errors.New("managed file source is active; use a new source name for new content")
	}
	uploads := make([]managedUpload, len(headers))
	for i, h := range headers {
		f, err := h.Open()
		if err != nil {
			return nil, err
		}
		content, err := io.ReadAll(f)
		f.Close()
		if err != nil {
			return nil, err
		}
		uploads[i] = managedUpload{partFilename(h), content}
	}
	if err := src.stage(uploads); err != nil {
		return nil, err
	}
	return src.list()
}
