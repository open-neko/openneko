package webfront

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/open-neko/neko/apps/openneko/internal/config"
	"github.com/open-neko/neko/apps/openneko/internal/files"
)

const runLibraryMaxFile = 25 << 20

var runLibraryBudget int64 = 100 << 20

type runLibraryFile struct {
	hash, filename, path string
	size                 int64
}

// runLibraryFiles is the person's own ready uploads, newest first. The
// library holds no team layer since the concepts left it.
func (a *API) runLibraryFiles(ctx context.Context, orgID, userID string) ([]runLibraryFile, error) {
	rows, err := a.DB.Query(ctx, `select content_hash, filename, relative_path, size_bytes from library_document
		where org_id = $1 and user_id = $2 and status = 'ready' order by created_at desc, id`, orgID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []runLibraryFile
	for rows.Next() {
		var f runLibraryFile
		if err := rows.Scan(&f.hash, &f.filename, &f.path, &f.size); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// stageRunLibrary copies the uploads of the run's person to
// library/uploads/<user>/<hash>/<name> under the staged org root, the 4.9.1
// layout that skills walk up to. A file over the per-file limit or the run
// budget stays out, with a log line.
func (a *API) stageRunLibrary(ctx context.Context, orgID, runID, userID, stageOrg string) error {
	if userID == "" {
		return nil
	}
	docs, err := a.runLibraryFiles(ctx, orgID, userID)
	if err != nil {
		return err
	}
	left := runLibraryBudget
	skip := func(d runLibraryFile, why string) {
		log.Printf("[agent-run] run %s: library file %s (%d bytes) left out: %s", runID, d.filename, d.size, why)
	}
	for _, d := range docs {
		if d.size > runLibraryMaxFile || d.size > left {
			skip(d, fmt.Sprintf("over the %d MB file limit or the %d MB run budget", runLibraryMaxFile>>20, runLibraryBudget>>20))
			continue
		}
		var rel string
		var data []byte
		if stored, ok := strings.CutPrefix(d.path, "files/"); ok {
			name := path.Base(stored)
			if name == "." || name == ".." || name == "/" || strings.ContainsAny(name, "\\\x00") {
				continue
			}
			rel = path.Join("library/uploads", stageSegment(userID), stageSegment(d.hash), name)
			data, err = a.getServeFile(ctx, actor{OrgID: orgID, UserID: userID}, stored, runLibraryMaxFile)
		} else if rel = path.Clean(strings.TrimLeft(strings.ReplaceAll(d.path, `\`, "/"), "/")); strings.HasPrefix(rel, "library/uploads/") {
			var src string
			if src, err = libraryUploadPath(orgID, rel); err == nil {
				data, err = readCappedFile(src, runLibraryMaxFile)
			}
		} else {
			continue
		}
		if err != nil {
			skip(d, err.Error())
			continue
		}
		if int64(len(data)) > left {
			skip(d, fmt.Sprintf("over the %d MB run budget", runLibraryBudget>>20))
			continue
		}
		target := filepath.Join(stageOrg, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(target, data, 0o644); err != nil {
			return err
		}
		left -= int64(len(data))
	}
	return nil
}

// lockRunLibrary leaves the staged library readable by the run and by
// serve's group, and writable by neither.
func lockRunLibrary(stageOrg string) error {
	root := filepath.Join(stageOrg, "library")
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.Chmod(p, 0o750)
		}
		return os.Chmod(p, 0o440)
	})
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

func readCappedFile(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err == nil && int64(len(data)) > limit {
		err = fmt.Errorf("file is larger than %d bytes", limit)
	}
	return data, err
}

// getServeFile reads one of the user's files from openneko serve.
func (a *API) getServeFile(ctx context.Context, act actor, path string, limit int64) ([]byte, error) {
	if serveURL() == "" {
		return nil, errors.New("openneko serve is off")
	}
	key, err := config.SigningSecret(a.SecretsDir, files.TokenLabel)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, serveURL()+"/v1/files/content?path="+encodeURIComponent(path), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+files.MintToken(key, act.OrgID, act.UserID, time.Now().Add(time.Minute)))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, &serveError{res.StatusCode, fmt.Sprintf("openneko serve answered %d", res.StatusCode)}
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err == nil && int64(len(data)) > limit {
		err = fmt.Errorf("file is larger than %d bytes", limit)
	}
	return data, err
}
