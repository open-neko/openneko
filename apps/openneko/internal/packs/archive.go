package packs

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"
)

const (
	limitCompressed = 16 << 20
	limitExtracted  = 64 << 20
	limitFile       = 8 << 20
	limitEntries    = 1000
	limitDepth      = 16
	limitRatio      = 100
)

var allowedUploadExt = []string{".yaml", ".yml", ".json", ".md", ".gql", ".graphql"}

// StageArchive is stagePackArchive(): it extracts into a new private folder
// under stagingParent. The caller owns cleanup.
func StageArchive(ctx context.Context, data []byte, stagingParent string, reservedIDs []string) (*Bundle, func(), error) {
	if len(data) > limitCompressed {
		return nil, nil, errors.New("pack ZIP exceeds 16 MiB")
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, nil, fmt.Errorf("invalid pack ZIP: %w", err)
	}
	if err := os.MkdirAll(stagingParent, 0o700); err != nil {
		return nil, nil, err
	}
	temporary, err := os.MkdirTemp(stagingParent, ".upload-")
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { _ = os.RemoveAll(temporary) }
	bundle, err := extractArchive(ctx, reader, temporary, reservedIDs)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	return bundle, cleanup, nil
}

func hasLinkField(extra []byte) bool {
	for len(extra) >= 4 {
		id := binary.LittleEndian.Uint16(extra)
		size := int(binary.LittleEndian.Uint16(extra[2:]))
		if 4+size > len(extra) {
			return false
		}
		if (id == 0x000d && size > 12) || id == 0x756e {
			return true
		}
		extra = extra[4+size:]
	}
	return false
}

func extractArchive(ctx context.Context, reader *zip.Reader, temporary string, reservedIDs []string) (*Bundle, error) {
	var root string
	var total uint64
	paths := map[string]bool{}
	spellings := map[string]string{}
	for count, entry := range reader.File {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if count+1 > limitEntries {
			return nil, errors.New("pack ZIP has too many entries")
		}
		directory := strings.HasSuffix(entry.Name, "/")
		name := strings.TrimSuffix(entry.Name, "/")
		parts := strings.Split(name, "/")
		if len(parts) > limitDepth || slices.ContainsFunc(parts, func(p string) bool { return !zipPartRe.MatchString(p) || p == "." || p == ".." }) {
			return nil, errors.New("pack ZIP contains an invalid or overly deep path")
		}
		if !PackIDPattern.MatchString(parts[0]) {
			return nil, errors.New("invalid pack directory name")
		}
		if root == "" {
			root = parts[0]
		}
		if parts[0] != root || (!directory && len(parts) < 2) {
			return nil, errors.New("pack ZIP must contain one top-level directory")
		}
		if slices.Contains(reservedIDs, root) {
			return nil, errors.New("pack id is reserved for a first-party pack")
		}
		normalized := strings.ToLower(name)
		if paths[normalized] {
			return nil, errors.New("pack ZIP contains duplicate paths")
		}
		paths[normalized] = true
		for depth := 1; depth <= len(parts); depth++ {
			prefix := strings.Join(parts[:depth], "/")
			if prior, ok := spellings[strings.ToLower(prefix)]; ok && prior != prefix {
				return nil, errors.New("pack ZIP contains case-ambiguous paths")
			}
			spellings[strings.ToLower(prefix)] = prefix
		}
		mode := entry.ExternalAttrs >> 16
		kind := mode & 0o170000
		want := uint32(0o100000)
		if directory {
			want = 0o040000
		}
		if (kind != 0 && kind != want) || (!directory && mode&0o111 != 0) {
			return nil, errors.New("pack ZIP may contain only non-executable regular files and directories")
		}
		if hasLinkField(entry.Extra) {
			return nil, errors.New("pack ZIP cannot contain links")
		}
		if entry.Flags&0x1 != 0 {
			return nil, errors.New("encrypted pack ZIP entries are unsupported")
		}
		if entry.UncompressedSize64 > limitFile || entry.UncompressedSize64 > max(1, entry.CompressedSize64)*limitRatio {
			return nil, errors.New("pack ZIP entry exceeds size or compression ratio limits")
		}
		total += entry.UncompressedSize64
		if total > limitExtracted {
			return nil, errors.New("pack ZIP extracted size exceeds 64 MiB")
		}
		target := filepath.Join(temporary, filepath.FromSlash(name))
		if directory {
			if entry.UncompressedSize64 != 0 {
				return nil, errors.New("pack ZIP directory has data")
			}
			if err := os.MkdirAll(target, 0o700); err != nil {
				return nil, err
			}
			continue
		}
		if !slices.Contains(allowedUploadExt, strings.ToLower(filepath.Ext(name))) {
			return nil, errors.New("pack ZIP may contain declarative YAML, JSON, GraphQL and Markdown files only")
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return nil, err
		}
		content, err := readEntry(entry)
		if err != nil {
			return nil, err
		}
		if !utf8.Valid(content) || bytes.IndexByte(content, 0) >= 0 || bytes.HasPrefix(content, []byte("PK")) || bytes.HasPrefix(content, []byte("#!")) {
			return nil, errors.New("pack ZIP cannot contain binary, executable, or nested archive content")
		}
		file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return nil, err
		}
		_, err = file.Write(content)
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return nil, err
		}
	}
	if root == "" {
		return nil, errors.New("pack ZIP is empty")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	bundle, err := LoadBundle(filepath.Join(temporary, root))
	if err != nil {
		return nil, err
	}
	if bundle.Manifest.Metadata.ID != root {
		return nil, errors.New("pack directory and metadata.id must match")
	}
	return bundle, nil
}

func readEntry(entry *zip.File) ([]byte, error) {
	rc, err := entry.OpenRaw()
	if err != nil {
		return nil, err
	}
	var src io.Reader = rc
	switch entry.Method {
	case zip.Store:
	case zip.Deflate:
		src = flate.NewReader(rc)
	default:
		return nil, errors.New("pack ZIP entry uses an unsupported compression method")
	}
	limit := min(entry.UncompressedSize64, limitFile)
	content, err := io.ReadAll(io.LimitReader(src, int64(limit)+1))
	if err != nil {
		return nil, errors.New("pack ZIP entry is corrupt")
	}
	if uint64(len(content)) > limit {
		return nil, errors.New("pack ZIP entry exceeds declared size")
	}
	if uint64(len(content)) != entry.UncompressedSize64 || crc32.ChecksumIEEE(content) != entry.CRC32 {
		return nil, errors.New("pack ZIP entry is corrupt")
	}
	return content, nil
}

// HashFiles is hashPackFiles(): every file under root, including files the
// manifest does not name.
func HashFiles(root string) (string, error) {
	files := [][2]string{}
	var bytesSeen int64
	var walk func(path string, depth int) error
	walk = func(path string, depth int) error {
		if depth > limitDepth {
			return errors.New("pack tree is too deep")
		}
		entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			return err
		}
		for _, e := range entries {
			if len(files) >= limitEntries {
				return errors.New("pack tree has too many entries")
			}
			name := e.Name()
			if path != "" {
				name = path + "/" + e.Name()
			}
			switch {
			case e.IsDir():
				files = append(files, [2]string{name, "directory"})
				if err := walk(name, depth+1); err != nil {
					return err
				}
			case e.Type().IsRegular():
				info, err := e.Info()
				if err != nil {
					return err
				}
				if info.Size() > limitFile || bytesSeen+info.Size() > limitExtracted {
					return errors.New("pack tree exceeds size limits")
				}
				content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
				if err != nil {
					return err
				}
				bytesSeen += int64(len(content))
				if len(content) > limitFile || bytesSeen > limitExtracted {
					return errors.New("pack tree exceeds size limits")
				}
				files = append(files, [2]string{name, SHA256(content)})
			default:
				return errors.New("pack tree contains a link or special file")
			}
		}
		return nil
	}
	if err := walk("", 0); err != nil {
		return "", err
	}
	slices.SortStableFunc(files, func(a, b [2]string) int { return utf16Less(a[0], b[0]) })
	list := make([]any, len(files))
	for i, f := range files {
		list[i] = []any{f[0], f[1]}
	}
	return CanonicalHash(list)
}
