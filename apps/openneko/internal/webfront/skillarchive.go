package webfront

import (
	"archive/zip"
	"bytes"
	"errors"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"unicode/utf16"

	"gopkg.in/yaml.v3"
)

const (
	archiveExtracted = 64 << 20
	archiveFile      = 8 << 20
	archiveEntries   = 500
	archiveDepth     = 12
	archiveRatio     = 200
)

var (
	archiveSkillName   = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	archiveBadPart     = regexp.MustCompile(`[\\\x00-\x1f\x7f]`)
	archiveFrontmatter = regexp.MustCompile(`(?s)^---\r?\n(.*?)\r?\n---(?:\r?\n|$)`)
	archiveDrive       = regexp.MustCompile(`^[a-zA-Z]:`)
	archiveParent      = regexp.MustCompile(`(^|/)\.\.(/|$)`)
)

var importMu sync.Mutex

type skillArchiveError struct {
	msg    string
	status int
}

func (e *skillArchiveError) Error() string { return e.msg }

func invalidArchive(msg string) error { return &skillArchiveError{msg: msg, status: 400} }

func jsLength(s string) int { return len(utf16.Encode([]rune(s))) }

// importSkillArchive is importWorkSkillArchive() in
// apps/web/src/lib/skill-archive.ts: one Agent Skills folder from a .skill
// or ZIP archive, checked in a staging folder before it moves into place.
func importSkillArchive(roots orgRoots, data []byte) (string, error) {
	if len(data) == 0 || len(data) > skillArchiveMax {
		return "", invalidArchive("Skill archive must be at most 16 MB.")
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", invalidArchive("Choose a valid .skill or ZIP archive.")
	}
	staging, err := os.MkdirTemp(roots.Org, ".skill-import-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(staging)

	root := ""
	total := uint64(0)
	paths := map[string]bool{}
	spellings := map[string]string{}
	for i, entry := range zr.File {
		if i+1 > archiveEntries {
			return "", invalidArchive("Skill archive has too many files.")
		}
		if strings.Contains(entry.Name, "\\") || strings.HasPrefix(entry.Name, "/") || archiveDrive.MatchString(entry.Name) ||
			archiveParent.MatchString(entry.Name) {
			return "", invalidArchive("Skill archive is invalid or corrupt.")
		}
		directory := strings.HasSuffix(entry.Name, "/")
		name := entry.Name
		if directory {
			name = name[:len(name)-1]
		}
		parts := strings.Split(name, "/")
		if jsLength(name) > 240 || len(parts) > archiveDepth {
			return "", invalidArchive("Skill archive contains an invalid path.")
		}
		for _, p := range parts {
			if p == "" || p == "." || p == ".." || archiveBadPart.MatchString(p) {
				return "", invalidArchive("Skill archive contains an invalid path.")
			}
		}
		if !archiveSkillName.MatchString(parts[0]) || len(parts[0]) > 64 || (!directory && len(parts) < 2) {
			return "", invalidArchive("Put one skill directory at the archive root.")
		}
		if root == "" {
			root = parts[0]
		}
		if parts[0] != root {
			return "", invalidArchive("Skill archive must contain one skill directory.")
		}
		folded := strings.ToLower(name)
		if paths[folded] {
			return "", invalidArchive("Skill archive contains duplicate paths.")
		}
		paths[folded] = true
		for depth := 1; depth <= len(parts); depth++ {
			prefix := strings.Join(parts[:depth], "/")
			if prior, ok := spellings[strings.ToLower(prefix)]; ok && prior != prefix {
				return "", invalidArchive("Skill archive contains case-ambiguous paths.")
			}
			spellings[strings.ToLower(prefix)] = prefix
		}
		mode := entry.ExternalAttrs >> 16
		kind := mode & 0o170000
		want := uint32(0o100000)
		if directory {
			want = 0o040000
		}
		if kind != 0 && kind != want {
			return "", invalidArchive("Skill archive may contain only files and directories.")
		}
		if entry.Flags&0x1 != 0 {
			return "", invalidArchive("Encrypted skill archives are unsupported.")
		}
		if entry.UncompressedSize64 > archiveFile || entry.UncompressedSize64 > max(1, entry.CompressedSize64)*archiveRatio {
			return "", invalidArchive("Skill archive has a file over the size or compression limit.")
		}
		total += entry.UncompressedSize64
		if total > archiveExtracted {
			return "", invalidArchive("Skill archive exceeds 64 MB when extracted.")
		}
		target := filepath.Join(staging, filepath.FromSlash(name))
		if directory {
			if entry.UncompressedSize64 != 0 {
				return "", invalidArchive("Skill archive contains an invalid directory.")
			}
			if err := os.MkdirAll(target, 0o755); err != nil {
				return "", err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return "", err
		}
		perm := os.FileMode(0o644)
		if mode&0o111 != 0 {
			perm = 0o755
		}
		if err := extractArchiveFile(entry, target, perm); err != nil {
			return "", err
		}
	}
	if root == "" {
		return "", invalidArchive("Skill archive is empty.")
	}
	markdown, err := os.ReadFile(filepath.Join(staging, root, "SKILL.md"))
	if err != nil {
		return "", invalidArchive("Skill archive needs SKILL.md at the skill directory root.")
	}
	m := archiveFrontmatter.FindSubmatch(markdown)
	if m == nil {
		return "", invalidArchive("SKILL.md needs YAML frontmatter with a name and description.")
	}
	var front any
	if err := yaml.Unmarshal(m[1], &front); err != nil {
		return "", invalidArchive("SKILL.md frontmatter is invalid YAML.")
	}
	fields, _ := front.(map[string]any)
	name, _ := fields["name"].(string)
	description, isString := fields["description"].(string)
	if fields == nil || name != root || !isString || strings.TrimSpace(description) == "" || jsLength(description) > 1024 {
		return "", invalidArchive("SKILL.md name must match its folder, with a description of at most 1024 characters.")
	}
	destination := filepath.Join(roots.Skills, root)
	importMu.Lock()
	defer importMu.Unlock()
	if err := os.Mkdir(destination, 0o755); err != nil {
		if errors.Is(err, os.ErrExist) {
			return "", &skillArchiveError{msg: "A skill with this name already exists.", status: 409}
		}
		return "", err
	}
	if err := os.Remove(destination); err != nil {
		return "", err
	}
	if err := os.Rename(filepath.Join(staging, root), destination); err != nil {
		os.RemoveAll(destination)
		return "", err
	}
	return root, nil
}

func extractArchiveFile(entry *zip.File, target string, perm os.FileMode) error {
	corrupt := invalidArchive("Skill archive contains a corrupt file.")
	rc, err := entry.Open()
	if err != nil {
		return invalidArchive("Skill archive is invalid or corrupt.")
	}
	defer rc.Close()
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	defer out.Close()
	limit := min(entry.UncompressedSize64, archiveFile)
	sum := crc32.NewIEEE()
	n, err := io.Copy(io.MultiWriter(out, sum), io.LimitReader(rc, int64(limit)+1))
	if uint64(n) > limit {
		return invalidArchive("Skill archive is invalid or corrupt.")
	}
	if err != nil {
		if errors.Is(err, zip.ErrChecksum) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, zip.ErrFormat) {
			return corrupt
		}
		return invalidArchive("Skill archive is invalid or corrupt.")
	}
	if uint64(n) != entry.UncompressedSize64 || sum.Sum32() != entry.CRC32 {
		return corrupt
	}
	return out.Close()
}
