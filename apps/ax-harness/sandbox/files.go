package sandbox

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/open-neko/openneko/apps/ax-harness/internal/localtool"
)

const (
	maxTransfer = 128 << 20
	maxBody     = 192 << 20
)

// privateDir holds the run's job file and runtime state. It never goes to
// the sandbox, and nothing the sandbox returns may land in it.
const privateDir = ".openneko"

type execBody struct {
	Fresh          bool              `json:"fresh"`
	Root           string            `json:"root"`
	Command        string            `json:"command"`
	SkillDir       string            `json:"skill_dir,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	TimeoutSeconds int               `json:"timeout_seconds"`
	MaxOutput      int               `json:"max_output"`
	Dirs           []string          `json:"dirs,omitempty"`
	Put            []fileData        `json:"put,omitempty"`
	Delete         []string          `json:"delete,omitempty"`
}

type execAnswer struct {
	localtool.ExecResult
	Dirs    []string   `json:"dirs"`
	Changed []fileData `json:"changed"`
	Deleted []string   `json:"deleted"`
	Skipped []skipped  `json:"skipped"`
}

type fileData struct {
	Path  string `json:"path"`
	Mode  uint32 `json:"mode"`
	Data  []byte `json:"data"`
	state fileState
}

type skipped struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

type fileState struct {
	size  int64
	mtime int64
	exec  bool
}

func stateOf(info fs.FileInfo) fileState {
	return fileState{size: info.Size(), mtime: info.ModTime().UnixNano(), exec: info.Mode().Perm()&0o111 != 0}
}

func (c *Client) relative(p string) (string, error) {
	rel, err := filepath.Rel(c.workspace, filepath.Clean(p))
	if err != nil || !(rel == "." || filepath.IsLocal(rel)) {
		return "", errors.New("outside the workspace")
	}
	return filepath.ToSlash(rel), nil
}

func private(rel string) bool {
	for _, part := range strings.Split(rel, "/") {
		if part == privateDir {
			return true
		}
	}
	return false
}

// changes lists the folders the sandbox has not seen, the regular files that
// differ from what it holds, and the files removed here since the last command.
func (c *Client) changes() ([]string, []fileData, []string, error) {
	var dirs []string
	var put []fileData
	seen := map[string]bool{}
	total := 0
	err := filepath.WalkDir(c.workspace, func(p string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if p == c.workspace {
				return nil
			}
			if entry.Name() == privateDir {
				return filepath.SkipDir
			}
			if rel, err := c.relative(p); err == nil && !c.dirs[rel] {
				dirs = append(dirs, rel)
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		rel, err := c.relative(p)
		if err != nil || private(rel) {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		seen[rel] = true
		state := stateOf(info)
		if old, ok := c.synced[rel]; ok && old == state {
			return nil
		}
		total += int(info.Size())
		if total > maxTransfer {
			return fmt.Errorf("the workspace changed by more than %d MiB since the last command", maxTransfer>>20)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		mode := uint32(0o644)
		if state.exec {
			mode = 0o755
		}
		put = append(put, fileData{Path: rel, Mode: mode, Data: data, state: state})
		return nil
	})
	if err != nil {
		return nil, nil, nil, err
	}
	var remove []string
	for rel := range c.synced {
		if !seen[rel] {
			remove = append(remove, rel)
		}
	}
	return dirs, put, remove, nil
}

// apply writes what the command changed into the workspace.
func (c *Client) apply(answer execAnswer) error {
	root, err := os.OpenRoot(c.workspace)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, name := range answer.Dirs {
		rel, err := sandboxPath(name)
		if err != nil {
			return err
		}
		if err := root.MkdirAll(filepath.FromSlash(rel), 0o770); err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
		c.dirs[rel] = true
	}
	for _, file := range answer.Changed {
		rel, err := sandboxPath(file.Path)
		if err != nil {
			return err
		}
		local := filepath.FromSlash(rel)
		if dir := filepath.Dir(local); dir != "." {
			if err := root.MkdirAll(dir, 0o770); err != nil {
				return fmt.Errorf("%s: %w", rel, err)
			}
			for part := path.Dir(rel); part != "."; part = path.Dir(part) {
				c.dirs[part] = true
			}
		}
		if info, err := root.Lstat(local); err == nil && !info.Mode().IsRegular() {
			if err := root.RemoveAll(local); err != nil {
				return fmt.Errorf("%s: %w", rel, err)
			}
		}
		perm := os.FileMode(0o660)
		if file.Mode&0o111 != 0 {
			perm = 0o770
		}
		if err := root.WriteFile(local, file.Data, perm); err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
		if err := root.Chmod(local, perm); err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
		info, err := root.Lstat(local)
		if err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
		c.synced[rel] = stateOf(info)
	}
	for _, name := range answer.Deleted {
		rel, err := sandboxPath(name)
		if err != nil {
			return err
		}
		if err := root.Remove(filepath.FromSlash(rel)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("%s: %w", rel, err)
		}
		delete(c.synced, rel)
	}
	return nil
}

func sandboxPath(name string) (string, error) {
	clean := path.Clean(name)
	if clean == "." || strings.ContainsRune(name, '\\') || !filepath.IsLocal(filepath.FromSlash(clean)) || private(clean) {
		return "", fmt.Errorf("the sandbox returned a path outside the workspace: %q", name)
	}
	return clean, nil
}

func skippedNote(files []skipped) string {
	if len(files) == 0 {
		return ""
	}
	var parts []string
	for i, file := range files {
		if i == 10 {
			parts = append(parts, fmt.Sprintf("and %d more", len(files)-10))
			break
		}
		parts = append(parts, fmt.Sprintf("%s (%s)", file.Path, file.Reason))
	}
	return "\n[these files stay in the sandbox: " + strings.Join(parts, ", ") + "]"
}
