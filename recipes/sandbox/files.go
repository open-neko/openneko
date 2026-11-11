package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const (
	maxBody  = 192 << 20
	maxFiles = 50_000
)

var maxTransfer = 128 << 20

type fileData struct {
	Path string `json:"path"`
	Mode uint32 `json:"mode"`
	Data []byte `json:"data"`
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

func checkTransfer(dirs []string, put []fileData, remove []string) error {
	if len(dirs)+len(put)+len(remove) > maxFiles {
		return fmt.Errorf("a command carries at most %d files", maxFiles)
	}
	for _, name := range dirs {
		if _, ok := runPath("/", name); !ok || path.Clean(name) == "." {
			return fmt.Errorf("invalid folder path %q", name)
		}
	}
	total := 0
	for _, file := range put {
		if _, ok := runPath("/", file.Path); !ok || path.Clean(file.Path) == "." {
			return fmt.Errorf("invalid file path %q", file.Path)
		}
		total += len(file.Data)
	}
	if total > maxTransfer {
		return fmt.Errorf("a command carries at most %d MiB of files", maxTransfer>>20)
	}
	for _, name := range remove {
		if _, ok := runPath("/", name); !ok || path.Clean(name) == "." {
			return fmt.Errorf("invalid file path %q", name)
		}
	}
	return nil
}

// receive applies the caller's deletions and files to the run's folder.
// The run's own processes are stopped, so nothing changes under it.
func (s *Server) receive(r *run, dirs []string, put []fileData, remove []string) error {
	root, err := os.OpenRoot(r.dir)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, name := range remove {
		name = path.Clean(name)
		if err := root.RemoveAll(filepath.FromSlash(name)); err != nil {
			return fmt.Errorf("could not remove %s", name)
		}
		for synced := range r.synced {
			if synced == name || strings.HasPrefix(synced, name+"/") {
				delete(r.synced, synced)
			}
		}
		for dir := range r.dirs {
			if dir == name || strings.HasPrefix(dir, name+"/") {
				delete(r.dirs, dir)
			}
		}
	}
	for _, dir := range dirs {
		if err := s.mkdirIn(root, r, path.Clean(dir)); err != nil {
			return fmt.Errorf("could not make %s", dir)
		}
	}
	for _, file := range put {
		name := path.Clean(file.Path)
		local := filepath.FromSlash(name)
		if err := s.mkdirIn(root, r, path.Dir(name)); err != nil {
			return fmt.Errorf("could not write %s", name)
		}
		if info, err := root.Lstat(local); err == nil && !info.Mode().IsRegular() {
			if err := root.RemoveAll(local); err != nil {
				return fmt.Errorf("could not write %s", name)
			}
		}
		perm := os.FileMode(0o600)
		if file.Mode&0o111 != 0 {
			perm = 0o700
		}
		if err := root.WriteFile(local, file.Data, perm); err != nil {
			return fmt.Errorf("could not write %s", name)
		}
		if s.SwitchUsers {
			if err := root.Lchown(local, r.uid, r.uid); err != nil {
				return fmt.Errorf("could not write %s", name)
			}
		}
		if err := root.Chmod(local, perm); err != nil {
			return fmt.Errorf("could not write %s", name)
		}
		info, err := root.Lstat(local)
		if err != nil {
			return fmt.Errorf("could not write %s", name)
		}
		r.synced[name] = stateOf(info)
	}
	return nil
}

func (s *Server) mkdirOwned(r *run, dir string) error {
	rel, err := filepath.Rel(r.dir, dir)
	if err != nil || !(rel == "." || filepath.IsLocal(rel)) {
		return errors.New("outside the run")
	}
	root, err := os.OpenRoot(r.dir)
	if err != nil {
		return err
	}
	defer root.Close()
	return s.mkdirIn(root, r, filepath.ToSlash(rel))
}

func (s *Server) mkdirIn(root *os.Root, r *run, dir string) error {
	if dir == "." || dir == "" {
		return nil
	}
	if err := root.MkdirAll(filepath.FromSlash(dir), 0o700); err != nil {
		return err
	}
	for part := dir; part != "." && part != "/"; part = path.Dir(part) {
		r.dirs[part] = true
		if !s.SwitchUsers {
			continue
		}
		if err := root.Lchown(filepath.FromSlash(part), r.uid, r.uid); err != nil {
			return err
		}
	}
	return nil
}

// collect returns the files the command wrote or removed since the last
// exchange. A file over the transfer limit stays unsent until a later
// command has room for it.
func collect(r *run) ([]string, []fileData, []string, []skipped, error) {
	synced, told := r.synced, r.told
	root, err := os.OpenRoot(r.dir)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	defer root.Close()
	dirs, changed, skips := []string{}, []fileData{}, []skipped{}
	seen := map[string]bool{}
	total := 0
	err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if name == "." {
			return nil
		}
		if entry.IsDir() {
			if !r.dirs[name] {
				r.dirs[name] = true
				dirs = append(dirs, name)
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			if !told[name] {
				told[name] = true
				skips = append(skips, skipped{Path: name, Reason: "not a regular file"})
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		state := stateOf(info)
		if old, ok := synced[name]; ok {
			seen[name] = true
			if old == state {
				return nil
			}
		}
		if len(changed) >= maxFiles || total+int(info.Size()) > maxTransfer {
			skips = append(skips, skipped{Path: name, Reason: "over the transfer limit"})
			return nil
		}
		file, err := root.Open(filepath.FromSlash(name))
		if err != nil {
			return err
		}
		data, err := io.ReadAll(io.LimitReader(file, int64(maxTransfer-total)+1))
		file.Close()
		if err != nil {
			return err
		}
		if total+len(data) > maxTransfer {
			skips = append(skips, skipped{Path: name, Reason: "over the transfer limit"})
			return nil
		}
		total += len(data)
		mode := uint32(0o644)
		if state.exec {
			mode = 0o755
		}
		changed = append(changed, fileData{Path: name, Mode: mode, Data: data})
		seen[name] = true
		synced[name] = state
		return nil
	})
	if err != nil {
		return nil, nil, nil, nil, err
	}
	deleted := []string{}
	for name := range synced {
		if !seen[name] {
			deleted = append(deleted, name)
			delete(synced, name)
		}
	}
	return dirs, changed, deleted, skips, nil
}
