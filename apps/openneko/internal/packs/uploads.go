package packs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
)

// Upload is the provenance record of an uploaded pack version (upload.json).
type Upload struct {
	OrgID       string  `json:"orgId"`
	PackID      string  `json:"packId"`
	Version     string  `json:"version"`
	BundleHash  string  `json:"bundleHash"`
	ContentHash string  `json:"contentHash"`
	ArchiveHash string  `json:"archiveHash"`
	UploadedAt  string  `json:"uploadedAt"`
	UploadedBy  *string `json:"uploadedBy"`
}

func regular(path string, directory bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if (directory && !info.IsDir()) || (!directory && !info.Mode().IsRegular()) {
		return errors.New("uploaded pack storage contains a link or special file")
	}
	return nil
}

func readJSONFile(path string, out any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

// LoadUploaded is loadUploadedPack(). An empty version reads the candidate.
func LoadUploaded(root, orgID, packID, version string) (*Bundle, error) {
	if !PackIDPattern.MatchString(packID) {
		return nil, errors.New("invalid uploaded pack id")
	}
	packRoot := filepath.Join(root, packID)
	if err := regular(packRoot, true); err != nil {
		return nil, err
	}
	if version == "" {
		candidate := filepath.Join(packRoot, "candidate.json")
		if err := regular(candidate, false); err != nil {
			return nil, err
		}
		var pointer struct {
			Version any `json:"version"`
		}
		if err := readJSONFile(candidate, &pointer); err != nil {
			return nil, err
		}
		version, _ = pointer.Version.(string)
	}
	if !versionPattern.MatchString(version) {
		return nil, errors.New("invalid uploaded pack version")
	}
	directory := filepath.Join(packRoot, "versions", version)
	for _, p := range []string{filepath.Join(packRoot, "versions"), directory, filepath.Join(directory, "bundle")} {
		if err := regular(p, true); err != nil {
			return nil, err
		}
	}
	if err := regular(filepath.Join(directory, "upload.json"), false); err != nil {
		return nil, err
	}
	var upload Upload
	if err := readJSONFile(filepath.Join(directory, "upload.json"), &upload); err != nil {
		return nil, err
	}
	if upload.OrgID != orgID || upload.PackID != packID || upload.Version != version {
		return nil, errors.New("uploaded pack provenance does not match this organization/version")
	}
	contentRoot := filepath.Join(directory, "bundle")
	contentHash, err := HashFiles(contentRoot)
	if err != nil {
		return nil, err
	}
	if contentHash != upload.ContentHash {
		return nil, errors.New("uploaded pack contents changed after upload")
	}
	bundle, err := LoadBundle(contentRoot)
	if err != nil {
		return nil, err
	}
	meta := bundle.Manifest.Metadata
	if meta.ID != packID || meta.Version != version || bundle.BundleHash != upload.BundleHash {
		return nil, errors.New("uploaded pack manifest does not match its provenance")
	}
	bundle.Upload = &upload
	return bundle, nil
}

// ListUploaded is listUploadedPacks(): a pack whose files are gone is skipped.
func ListUploaded(root, orgID string) ([]*Bundle, error) {
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []*Bundle
	for _, e := range entries {
		if !e.IsDir() || !PackIDPattern.MatchString(e.Name()) {
			continue
		}
		b, err := LoadUploaded(root, orgID, e.Name(), "")
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}

func isoMillis(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

func writeNew(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}

// StoreUpload is storePackUpload(). A version is immutable: the same bytes
// again return the stored record, changed bytes fail.
func StoreUpload(ctx context.Context, root, orgID string, data []byte, reservedIDs []string, actorUserID *string) (*Upload, error) {
	bundle, cleanup, err := StageArchive(ctx, data, filepath.Join(root, ".staging"), reservedIDs)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	meta := bundle.Manifest.Metadata
	contentHash, err := HashFiles(bundle.Root)
	if err != nil {
		return nil, err
	}
	upload := Upload{OrgID: orgID, PackID: meta.ID, Version: meta.Version, BundleHash: bundle.BundleHash,
		ContentHash: contentHash, ArchiveHash: SHA256(data), UploadedAt: isoMillis(time.Now()), UploadedBy: actorUserID}
	packRoot := filepath.Join(root, meta.ID)
	directory := filepath.Join(packRoot, "versions", meta.Version)
	temporary := filepath.Dir(bundle.Root)
	record, err := json.Marshal(upload)
	if err != nil {
		return nil, err
	}
	if err := writeNew(filepath.Join(temporary, "upload.json"), record); err != nil {
		return nil, err
	}
	if err := os.Rename(bundle.Root, filepath.Join(temporary, "bundle")); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(directory), 0o700); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	stored, duplicate := &upload, false
	if err := os.Rename(temporary, directory); err != nil {
		if _, statErr := os.Stat(directory); statErr != nil {
			return nil, err
		}
		existing, loadErr := LoadUploaded(root, orgID, meta.ID, meta.Version)
		if loadErr != nil {
			return nil, loadErr
		}
		if existing.Upload.ContentHash != upload.ContentHash {
			return nil, errors.New("uploaded pack versions are immutable; use a new version for changed contents")
		}
		stored, duplicate = existing.Upload, true
	}
	candidate := filepath.Join(packRoot, "candidate.json")
	_, statErr := os.Lstat(candidate)
	if statErr != nil && !errors.Is(statErr, fs.ErrNotExist) {
		return nil, statErr
	}
	if !duplicate || statErr != nil {
		pointer := filepath.Join(packRoot, fmt.Sprintf(".candidate-%s.json", uuid.NewString()))
		body, _ := json.Marshal(map[string]string{"version": meta.Version})
		if err := writeNew(pointer, body); err != nil {
			return nil, err
		}
		if err := os.Rename(pointer, candidate); err != nil {
			return nil, err
		}
	}
	return stored, nil
}
