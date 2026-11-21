package cli

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
	ext := "tar.gz"
	if goos == "windows" {
		ext = "zip"
	}
	return fmt.Sprintf("openneko-native_%s_%s_%s.%s", strings.TrimPrefix(target, "v"), goos, goarch, ext)
}

func nativeExeName(goos string) string {
	if goos == "windows" {
		return "openneko.exe"
	}
	return "openneko"
}

// nativeReleaseBase is the folder of one release. OPENNEKO_RELEASE_URL
// points at a mirror.
func nativeReleaseBase(target string) (string, error) {
	base := openNekoReleaseBaseURL
	if mirror := strings.TrimSpace(os.Getenv("OPENNEKO_RELEASE_URL")); mirror != "" {
		base = mirror
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return "", fmt.Errorf("the release URL %q is not an https URL; a release downloads over https only", base)
	}
	return strings.TrimSuffix(base, "/") + "/" + target, nil
}

var nativeReleaseClient = &http.Client{CheckRedirect: httpsOnlyRedirect}

func httpsOnlyRedirect(req *http.Request, via []*http.Request) error {
	if req.URL.Scheme != "https" {
		return fmt.Errorf("refused a redirect to %s: a release downloads over https only", req.URL.Redacted())
	}
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	return nil
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
	switch runtime.GOOS {
	case "linux", "darwin", "windows":
	default:
		return "", fmt.Errorf("no native release download for %s; use --from <release folder>", runtime.GOOS)
	}
	base, err := nativeReleaseBase(target)
	if err != nil {
		return "", err
	}
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
	if err := extractNativeArchive(archive, release, nativeExeName(runtime.GOOS)); err != nil {
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
	res, err := nativeReleaseClient.Do(req)
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

// extractNativeArchive takes bin/<exe> and the files under share/openneko
// from a .tar.gz or .zip release archive, so that a later release can add
// a folder.
func extractNativeArchive(archive, dst, exe string) error {
	x := &nativeExtract{dst: dst, exe: "bin/" + exe}
	var err error
	if strings.HasSuffix(archive, ".zip") {
		err = x.zip(archive)
	} else {
		err = x.tarGz(archive)
	}
	if err != nil {
		return err
	}
	return x.finish()
}

type nativeExtract struct {
	dst, exe                                string
	sawExe, sawPack, sawBuiltin, sawSandbox bool
}

func (x *nativeExtract) tarGz(archive string) error {
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
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read the release archive: %w", err)
		}
		if hdr.Typeflag != tar.TypeDir && hdr.Typeflag != tar.TypeReg {
			return fmt.Errorf("the release archive entry %q is not a file or a folder", hdr.Name)
		}
		if err := x.entry(hdr.Name, hdr.Typeflag == tar.TypeDir, hdr.Size, hdr.Mode&0o111 != 0, tr); err != nil {
			return err
		}
	}
}

func (x *nativeExtract) zip(archive string) error {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return fmt.Errorf("open the release archive: %w", err)
	}
	defer zr.Close()
	for _, f := range zr.File {
		mode := f.Mode()
		dir := mode.IsDir()
		if !dir && !mode.IsRegular() {
			return fmt.Errorf("the release archive entry %q is not a file or a folder", f.Name)
		}
		if f.UncompressedSize64 > maxNativeEntryBytes {
			return fmt.Errorf("the release archive entry %q is too large", f.Name)
		}
		var r io.ReadCloser = io.NopCloser(strings.NewReader(""))
		if !dir {
			if r, err = f.Open(); err != nil {
				return fmt.Errorf("read the release archive: %w", err)
			}
		}
		err = x.entry(f.Name, dir, int64(f.UncompressedSize64), mode&0o111 != 0, r)
		if cerr := r.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (x *nativeExtract) entry(raw string, dir bool, size int64, executable bool, r io.Reader) error {
	name := path.Clean(strings.TrimPrefix(raw, "./"))
	if name == "." || strings.HasPrefix(name, "/") || name == ".." || strings.HasPrefix(name, "../") ||
		strings.ContainsAny(name, `\:`) || !filepath.IsLocal(filepath.FromSlash(name)) {
		return fmt.Errorf("the release archive holds the unsafe path %q", raw)
	}
	exe := name == x.exe
	if !exe && !nativeReleasePath(name) {
		return fmt.Errorf("the release archive holds the unexpected path %q", raw)
	}
	target := filepath.Join(x.dst, filepath.FromSlash(name))
	if dir {
		if exe {
			return fmt.Errorf("%s in the release archive is a folder", x.exe)
		}
		return os.MkdirAll(target, 0o755)
	}
	if size < 0 || size > maxNativeEntryBytes {
		return fmt.Errorf("the release archive entry %q is too large", raw)
	}
	mode := os.FileMode(0o644)
	if exe || executable {
		mode = 0o755
	}
	x.sawExe = x.sawExe || exe
	for pattern, saw := range map[string]*bool{
		"share/openneko/packs/*/pack.yaml":         &x.sawPack,
		"share/openneko/skills/builtin/*/SKILL.md": &x.sawBuiltin,
		"share/openneko/skills/sandbox/*/SKILL.md": &x.sawSandbox,
	} {
		if m, _ := path.Match(pattern, name); m {
			*saw = true
		}
	}
	return writeArchiveEntry(target, r, size, mode)
}

func (x *nativeExtract) finish() error {
	if !x.sawExe {
		return fmt.Errorf("the release archive holds no %s", x.exe)
	}
	if !x.sawPack {
		return errors.New("the release archive holds no built-in pack (share/openneko/packs/*/pack.yaml)")
	}
	if !x.sawBuiltin || !x.sawSandbox {
		return errors.New("the release archive holds no bundled skills (share/openneko/skills/builtin/*/SKILL.md and share/openneko/skills/sandbox/*/SKILL.md)")
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

func nativeReleasePath(name string) bool {
	switch name {
	case "bin", "share", "share/openneko":
		return true
	}
	return strings.HasPrefix(name, "share/openneko/")
}
