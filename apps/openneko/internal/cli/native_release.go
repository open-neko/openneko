package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	nativeChecksumsAsset  = "checksums-native.txt"
	maxNativeArchiveBytes = 512 << 20
	maxNativeEntryBytes   = 384 << 20
)

func nativeArchiveName(target, goos, goarch string) string {
	return fmt.Sprintf("openneko-native_%s_%s_%s.tar.gz", strings.TrimPrefix(target, "v"), goos, goarch)
}

// nativeReleaseBase is the folder of one release. OPENNEKO_RELEASE_URL
// points at a mirror with the same layout: <url>/<tag>/<asset>.
func nativeReleaseBase(target string) string {
	base := openNekoReleaseBaseURL
	if mirror := strings.TrimSpace(os.Getenv("OPENNEKO_RELEASE_URL")); mirror != "" {
		base = mirror
	}
	return strings.TrimSuffix(base, "/") + "/" + target
}

func resolveNativeTarget(ctx context.Context, requested string) (string, error) {
	target := normalizeUpgradeImageVersion(requested)
	if target == "latest" {
		if strings.TrimSpace(os.Getenv("OPENNEKO_RELEASE_URL")) != "" {
			return "", errors.New("OPENNEKO_RELEASE_URL is set, so give the release with --version")
		}
		return resolveLatestReleaseTag(ctx)
	}
	if !releaseTagPattern.MatchString(target) {
		return "", fmt.Errorf("--version needs a released semantic version, got %q", requested)
	}
	return target, nil
}

// downloadNativeRelease puts the verified release of target in a new
// folder under parent and returns that folder.
func downloadNativeRelease(ctx context.Context, target, parent string) (string, error) {
	if runtime.GOOS != "linux" {
		return "", fmt.Errorf("no native release download for %s yet; use --from <release folder>", runtime.GOOS)
	}
	base := nativeReleaseBase(target)
	name := nativeArchiveName(target, runtime.GOOS, runtime.GOARCH)
	sums, err := downloadReleaseAsset(ctx, base+"/"+nativeChecksumsAsset)
	if err != nil {
		return "", fmt.Errorf("download %s: %w", nativeChecksumsAsset, err)
	}
	want, err := checksumForAsset(sums, name)
	if err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp(parent, ".openneko-release-*")
	if err != nil {
		return "", err
	}
	archive := filepath.Join(dir, name)
	if err := downloadToFile(ctx, base+"/"+name, archive, want); err != nil {
		os.RemoveAll(dir)
		return "", fmt.Errorf("download %s: %w", name, err)
	}
	release := filepath.Join(dir, "release")
	if err := extractNativeArchive(archive, release); err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	_ = os.Remove(archive)
	return release, nil
}

func downloadToFile(ctx context.Context, url, dst string, want []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "openneko-upgrade")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		return fmt.Errorf("unexpected HTTP status %s", res.Status)
	}
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(res.Body, maxNativeArchiveBytes+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if n > maxNativeArchiveBytes {
		return errors.New("the archive exceeds the size limit")
	}
	if !bytes.Equal(h.Sum(nil), want) {
		return errors.New("checksum mismatch")
	}
	return nil
}

// extractNativeArchive takes only bin/openneko and the files under
// share/openneko/web from the archive.
func extractNativeArchive(archive, dst string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("open the release archive: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	sawExe := false
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read the release archive: %w", err)
		}
		name := path.Clean(strings.TrimPrefix(hdr.Name, "./"))
		if name == "." || strings.HasPrefix(name, "/") || name == ".." || strings.HasPrefix(name, "../") {
			return fmt.Errorf("the release archive holds the unsafe path %q", hdr.Name)
		}
		exe := name == "bin/openneko"
		if !exe && name != "bin" && name != "share" && name != "share/openneko" && name != "share/openneko/web" && !strings.HasPrefix(name, "share/openneko/web/") {
			return fmt.Errorf("the release archive holds the unexpected path %q", hdr.Name)
		}
		target := filepath.Join(dst, filepath.FromSlash(name))
		switch hdr.Typeflag {
		case tar.TypeDir:
			if exe {
				return errors.New("bin/openneko in the release archive is a folder")
			}
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if hdr.Size < 0 || hdr.Size > maxNativeEntryBytes {
				return fmt.Errorf("the release archive entry %q is too large", hdr.Name)
			}
			mode := os.FileMode(0o644)
			if exe {
				mode, sawExe = 0o755, true
			}
			if err := writeArchiveEntry(target, tr, hdr.Size, mode); err != nil {
				return err
			}
		default:
			return fmt.Errorf("the release archive entry %q is not a file or a folder", hdr.Name)
		}
	}
	if !sawExe {
		return errors.New("the release archive holds no bin/openneko")
	}
	return nil
}

func writeArchiveEntry(target string, r io.Reader, size int64, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	n, err := io.Copy(out, io.LimitReader(r, size))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil && n != size {
		err = fmt.Errorf("%s is truncated", target)
	}
	return err
}
