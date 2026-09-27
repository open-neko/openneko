// Package localtool provides file capabilities for a host-selected run workspace.
// The host must keep credentials and privileged bridge state outside this root.
package localtool

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/open-neko/harness/internal/agent"
)

const maxFile = 64 << 10

type Files struct {
	root  *os.Root
	gate  sync.RWMutex
	mu    sync.Mutex
	reads map[string]string
}

func OpenFiles(dir string) (*Files, error) {
	if !filepath.IsAbs(dir) {
		return nil, fmt.Errorf("workspace path must be absolute")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	return &Files{root: root, reads: make(map[string]string)}, nil
}

func (f *Files) Close() error { return f.root.Close() }

// Restore rebuilds read-version state from the validated durable operation log.
// Changed files still fail the freshness check when the next edit runs.
func (f *Files) Restore(ctx context.Context, operations []agent.SavedOperation) error {
	f.gate.Lock()
	defer f.gate.Unlock()
	f.mu.Lock()
	defer f.mu.Unlock()
	clear(f.reads)
	for _, op := range operations {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !op.Finished || op.Error != "" {
			continue
		}
		if op.Tool != "file_read" && op.Tool != "file_edit" {
			continue
		}
		var input struct {
			Path string `json:"path"`
		}
		if json.Unmarshal([]byte(op.Instruction), &input) != nil || !validPath(input.Path) {
			return fmt.Errorf("invalid saved file path")
		}
		switch op.Tool {
		case "file_read":
			var result struct {
				Path, Content, Version string
			}
			if json.Unmarshal(op.Result, &result) != nil || result.Path != input.Path || digest([]byte(result.Content)) != result.Version {
				return fmt.Errorf("invalid saved file read")
			}
			f.reads[input.Path] = result.Version
		case "file_edit":
			var request struct {
				Content string `json:"content"`
			}
			var result struct{ Path, Version string }
			if json.Unmarshal([]byte(op.Instruction), &request) != nil || json.Unmarshal(op.Result, &result) != nil || result.Path != input.Path || digest([]byte(request.Content)) != result.Version {
				return fmt.Errorf("invalid saved file edit")
			}
			f.reads[input.Path] = result.Version
		}
	}
	return nil
}

func (f *Files) Capabilities() []agent.Capability {
	return []agent.Capability{
		{Name: "file_read", Version: "1", Origin: "workspace", Effect: "read", Description: "Read a small regular file in the run workspace and receive its version.", InputSchema: json.RawMessage(`{"type":"object","required":["path"],"properties":{"path":{"type":"string","minLength":1,"maxLength":1024}},"additionalProperties":false}`), Call: f.read},
		{Name: "file_edit", Version: "1", Origin: "workspace", Effect: "durable", Description: "Replace an existing file previously read in this run; supply its exact version. This is a workspace mutation.", InputSchema: json.RawMessage(`{"type":"object","required":["path","version","content"],"properties":{"path":{"type":"string","minLength":1,"maxLength":1024},"version":{"type":"string","minLength":64,"maxLength":64},"content":{"type":"string","maxLength":65536}},"additionalProperties":false}`), Call: f.edit},
		{Name: "file_write", Version: "1", Origin: "workspace", Effect: "durable", Description: "Create a new small text file in the run workspace; refuse to replace an existing path.", InputSchema: json.RawMessage(`{"type":"object","required":["path","content"],"properties":{"path":{"type":"string","minLength":1,"maxLength":1024},"content":{"type":"string","maxLength":65536}},"additionalProperties":false}`), Call: f.write},
		{Name: "file_search", Version: "1", Origin: "workspace", Effect: "read", Description: "Find small text files in the run workspace whose path or content contains a literal query; returns bounded paths only.", InputSchema: json.RawMessage(`{"type":"object","required":["query"],"properties":{"query":{"type":"string","minLength":1,"maxLength":256}},"additionalProperties":false}`), Call: f.search},
	}
}

// UploadCapabilities expose a separate staged upload root without granting
// mutations to the original files.
func (f *Files) UploadCapabilities() []agent.Capability {
	all := f.Capabilities()
	read, search := all[0], all[3]
	read.Name, read.Origin = "upload_read", "uploads"
	read.Description = "Read a small text file by path relative to this run's staged uploads directory."
	search.Name, search.Origin = "upload_search", "uploads"
	search.Description = "Search staged uploads by literal path or text content; returns bounded relative paths only."
	return []agent.Capability{read, search}
}

// SkillCapabilities expose only files staged for the current operator.
func (f *Files) SkillCapabilities() []agent.Capability {
	all := f.UploadCapabilities()
	all[0].Name, all[0].Origin = "skill_read", "skills"
	all[0].Description = "Read a staged skill instruction or supporting text file by path relative to the skills root. Skills cannot call models directly."
	all[1].Name, all[1].Origin = "skill_search", "skills"
	all[1].Description = "Find staged skill text by literal path or content; returns bounded relative paths only."
	return all
}

func validPath(path string) bool {
	return len(path) <= 1024 && filepath.IsLocal(path) && filepath.Clean(path) == path && path != "."
}

func (f *Files) read(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var input struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(raw, &input); err != nil || !validPath(input.Path) {
		return nil, fmt.Errorf("invalid workspace path")
	}
	f.gate.RLock()
	defer f.gate.RUnlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, _, err := f.load(input.Path)
	if err != nil {
		return nil, err
	}
	version := digest(data)
	f.mu.Lock()
	f.reads[input.Path] = version
	f.mu.Unlock()
	return json.Marshal(struct {
		Path    string `json:"path"`
		Content string `json:"content"`
		Version string `json:"version"`
	}{input.Path, string(data), version})
}

func (f *Files) edit(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var input struct {
		Path    string `json:"path"`
		Version string `json:"version"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(raw, &input); err != nil || !validPath(input.Path) || len(input.Content) > maxFile || len(input.Version) != 64 {
		return nil, fmt.Errorf("invalid file edit")
	}
	f.gate.Lock()
	defer f.gate.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	readVersion := f.reads[input.Path]
	f.mu.Unlock()
	if readVersion == "" || readVersion != input.Version {
		return nil, fmt.Errorf("file must be read at its current version before editing")
	}
	old, mode, err := f.load(input.Path)
	if err != nil {
		return nil, err
	}
	if digest(old) != readVersion {
		return nil, fmt.Errorf("file changed since read")
	}
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	tmp := filepath.Join(filepath.Dir(input.Path), ".harness-edit-"+hex.EncodeToString(nonce[:]))
	file, err := f.root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	defer f.root.Remove(tmp)
	if _, err = io.WriteString(file, input.Content); err == nil {
		err = file.Chmod(mode.Perm())
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	// Catch external edits during the write. Harness-owned edits are serialized
	// by gate; external writers require a shared lock for strict atomic CAS.
	current, _, err := f.load(input.Path)
	if err != nil || digest(current) != readVersion {
		return nil, fmt.Errorf("file changed during edit")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := f.root.Rename(tmp, input.Path); err != nil {
		return nil, err
	}
	dir, err := f.root.Open(filepath.Dir(input.Path))
	if err == nil {
		err = dir.Sync()
		_ = dir.Close()
	}
	if err != nil {
		return nil, err
	}
	version := digest([]byte(input.Content))
	f.mu.Lock()
	f.reads[input.Path] = version
	f.mu.Unlock()
	return json.Marshal(struct {
		Path    string `json:"path"`
		Version string `json:"version"`
	}{input.Path, version})
}

func (f *Files) write(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var input struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(raw, &input); err != nil || !validPath(input.Path) || len(input.Content) > maxFile {
		return nil, fmt.Errorf("invalid file write")
	}
	f.gate.Lock()
	defer f.gate.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	tmp := filepath.Join(filepath.Dir(input.Path), ".harness-write-"+hex.EncodeToString(nonce[:]))
	file, err := f.root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	defer f.root.Remove(tmp)
	if _, err = io.WriteString(file, input.Content); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Linking is atomic and fails if the destination already exists. Rename
	// could silently replace another file created after admission.
	if err := f.root.Link(tmp, input.Path); err != nil {
		return nil, fmt.Errorf("file already exists or path unavailable: %w", err)
	}
	dir, err := f.root.Open(filepath.Dir(input.Path))
	if err == nil {
		err = dir.Sync()
		_ = dir.Close()
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Path    string `json:"path"`
		Version string `json:"version"`
	}{input.Path, digest([]byte(input.Content))})
}

func (f *Files) search(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var input struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal(raw, &input); err != nil || strings.TrimSpace(input.Query) == "" || len(input.Query) > 256 {
		return nil, fmt.Errorf("invalid file search")
	}
	f.gate.RLock()
	defer f.gate.RUnlock()
	query := []byte(strings.ToLower(input.Query))
	paths := make([]string, 0)
	visited, truncated := 0, false
	stop := fmt.Errorf("file search limit reached")
	err := fs.WalkDir(f.root.FS(), ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		visited++
		if visited > 1000 {
			truncated = true
			return stop
		}
		if !entry.Type().IsRegular() {
			return nil // WalkDir does not follow directory symlinks.
		}
		info, err := entry.Info()
		if err != nil || info.Size() > maxFile {
			return err
		}
		data, _, err := f.load(path)
		if err != nil {
			return nil // A changed, non-text or inaccessible file is not a match.
		}
		if strings.Contains(strings.ToLower(path), string(query)) || strings.Contains(strings.ToLower(string(data)), string(query)) {
			paths = append(paths, path)
			if len(paths) == 20 {
				truncated = true
				return stop
			}
		}
		return nil
	})
	if err != nil && err != stop {
		return nil, err
	}
	return json.Marshal(struct {
		Paths     []string `json:"paths"`
		Truncated bool     `json:"truncated"`
	}{paths, truncated})
}

func (f *Files) load(path string) ([]byte, os.FileMode, error) {
	info, err := f.root.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxFile {
		return nil, 0, fmt.Errorf("workspace file unavailable or too large")
	}
	file, err := f.root.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return nil, 0, fmt.Errorf("workspace file changed during open")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxFile+1))
	if err != nil || len(data) > maxFile || !utf8.Valid(data) {
		return nil, 0, fmt.Errorf("workspace file unreadable or too large")
	}
	return data, opened.Mode(), nil
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
