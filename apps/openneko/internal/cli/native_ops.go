package cli

import (
	"archive/tar"
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/open-neko/neko/apps/openneko/assets"
	"github.com/open-neko/neko/apps/openneko/internal/compose"
	"github.com/open-neko/neko/apps/openneko/internal/config"
	"github.com/open-neko/neko/apps/openneko/internal/host"
	"github.com/open-neko/neko/apps/openneko/internal/installation"
	"github.com/open-neko/neko/apps/openneko/internal/native"
	"github.com/open-neko/neko/apps/openneko/internal/preflight"
	"github.com/open-neko/neko/apps/openneko/internal/version"
	"github.com/open-neko/neko/apps/openneko/internal/webfront"
)

const nativeBackupProject = "openneko-native"

// A nativeFolder is one backup volume. place gives the host path of each
// top-level archive name it takes; tops lists the names present on the host.
type nativeFolder struct {
	volume string
	place  func(top string) (string, bool)
	tops   func() ([]string, error)
}

func folderUnder(root string, include func(top string) bool) nativeFolder {
	return nativeFolder{
		place: func(top string) (string, bool) {
			if !include(top) {
				return "", false
			}
			return filepath.Join(root, top), true
		},
		tops: func() ([]string, error) {
			entries, err := os.ReadDir(root)
			if errors.Is(err, fs.ErrNotExist) {
				return nil, nil
			}
			var names []string
			for _, e := range entries {
				if include(e.Name()) {
					names = append(names, e.Name())
				}
			}
			return names, err
		},
	}
}

func folderOf(paths map[string]string) nativeFolder {
	return nativeFolder{
		place: func(top string) (string, bool) { p, ok := paths[top]; return p, ok },
		tops: func() ([]string, error) {
			names := slices.Collect(maps.Keys(paths))
			slices.Sort(names)
			return names, nil
		},
	}
}

// installation.json and server.json name host paths, and instances/ holds
// other installs.
func nativeFolders() []nativeFolder {
	cfg := folderUnder(config.Dir(""), func(top string) bool {
		return top != "installation.json" && top != native.ServerFile && top != "instances"
	})
	cfg.volume = "native-config"
	data := folderOf(nativeLayout().DataFolders())
	data.volume = "native-data"
	return []nativeFolder{cfg, data}
}

func isNativeBackupVolume(name string) bool {
	for _, f := range nativeFolders() {
		if f.volume == name {
			return true
		}
	}
	return false
}

func nativeDatabaseURL() (string, error) {
	local, _ := config.ReadLocal("")
	url, err := databaseURL(local)
	if err != nil {
		return "", err
	}
	if url == "" {
		return "", errNativeNoDatabase
	}
	return url, nil
}

func createNativeBackup(ctx context.Context, destination string, out io.Writer) (string, error) {
	if err := requireServerRoot("backup"); err != nil {
		return "", err
	}
	dsn, err := nativeDatabaseURL()
	if err != nil {
		return "", err
	}
	sup := compose.New(assets.ComposeFS)
	key, keyPath, err := loadBackupKey(sup, true)
	if err != nil {
		return "", err
	}
	dir, err := backupDirectory(sup, nativeBackupProject)
	if err != nil {
		return "", err
	}
	now := time.Now().UTC()
	target, err := resolveBackupDestination(destination, dir, nativeBackupProject, now)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return "", err
	}
	identity, err := databaseIdentityOf(ctx, dsn)
	if err != nil {
		return "", fmt.Errorf("inspect OpenNeko's database: %w", err)
	}
	parts := strings.Split(identity, "|")
	if len(parts) != 3 {
		return "", fmt.Errorf("inspect OpenNeko's database: unexpected response %q", identity)
	}
	manifest := backupManifest{Format: backupManifestFormat, CreatedAt: now, Project: nativeBackupProject, OpenNekoVersion: version.Version}
	manifest.Database.User, manifest.Database.Name, manifest.Database.ServerVersion = parts[0], parts[1], parts[2]
	manifest.Database.External = true
	folders := map[string]nativeFolder{}
	for _, f := range nativeFolders() {
		manifest.Volumes = append(manifest.Volumes, f.volume)
		folders[f.volume] = f
	}
	return writeBackupFile(target, key, keyPath, manifest, backupSource{
		volume: func(name string, w io.Writer) error {
			return archiveFolder(folders[name], w)
		},
		dump: func(w io.Writer) error { return dumpDatabaseTo(ctx, dsn, w) },
	}, out)
}

// archiveFolder writes the folders and regular files of each top-level name
// of f. Links and devices are left out.
func archiveFolder(f nativeFolder, w io.Writer) error {
	tw := tar.NewWriter(w)
	tops, err := f.tops()
	if err != nil {
		return err
	}
	for _, top := range tops {
		root, _ := f.place(top)
		if err := archiveTop(tw, top, root); err != nil {
			return err
		}
	}
	return tw.Close()
}

func archiveTop(tw *tar.Writer, top, root string) error {
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == root && errors.Is(err, fs.ErrNotExist) {
				return filepath.SkipAll
			}
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		name := top
		if rel != "." {
			name = top + "/" + filepath.ToSlash(rel)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !d.IsDir() && !info.Mode().IsRegular() {
			return nil
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name, hdr.Uid, hdr.Gid, hdr.Uname, hdr.Gname = name, 0, 0, "", ""
		if d.IsDir() {
			hdr.Name += "/"
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		file, err := os.Open(p)
		if err != nil {
			return err
		}
		defer file.Close()
		_, err = io.CopyN(tw, file, hdr.Size)
		return err
	})
}

// restoreFolder empties each top-level name of f, then writes the archive's
// folders and regular files to the place of their top-level name.
func restoreFolder(f nativeFolder, r io.Reader) error {
	tops, err := f.tops()
	if err != nil {
		return err
	}
	for _, top := range tops {
		p, _ := f.place(top)
		if err := os.RemoveAll(p); err != nil {
			return err
		}
	}
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		name := path.Clean(strings.TrimPrefix(hdr.Name, "./"))
		if name == "." {
			continue
		}
		if path.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") {
			return fmt.Errorf("archive entry %q leaves the folder", hdr.Name)
		}
		top, rest, _ := strings.Cut(name, "/")
		root, ok := f.place(top)
		if !ok {
			continue
		}
		target := filepath.Join(root, filepath.FromSlash(rest))
		mode := hdr.FileInfo().Mode().Perm() | 0o700
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
			if err := os.Chmod(target, mode); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, hdr.FileInfo().Mode().Perm()|0o600)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
		}
	}
	_, err = io.Copy(io.Discard, r)
	return err
}

func runNativeRestore(ctx context.Context, cmd *cobra.Command, path, keyFile string, dropForeign bool) error {
	out := cmd.OutOrStdout()
	if err := requireServerRoot("restore"); err != nil {
		return err
	}
	key, err := readRestoreKey(keyFile)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Verifying %s...\n", path)
	verified, err := verifyBackupFile(path, key)
	if err != nil {
		var mismatch *backupKeyMismatchError
		if errors.As(err, &mismatch) {
			return fmt.Errorf("%w; pass --key-file with the key that made it (nothing was changed)", err)
		}
		return fmt.Errorf("backup verification failed; nothing was changed: %w", err)
	}
	manifest := verified.Manifest
	fmt.Fprintf(out, "Backup of %s taken %s by openneko %s verified.\n",
		manifest.Project, manifest.CreatedAt.Format("2006-01-02 15:04:05 UTC"), manifest.OpenNekoVersion)
	dsn, err := nativeDatabaseURL()
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Restoring into %s, from OpenNeko's database URL.\n", describeDatabaseURL(dsn))
	foreign, err := backupDump(path, key, func(dump io.Reader) ([]string, error) { return listForeignObjects(ctx, dsn, dump) })
	if err != nil {
		return fmt.Errorf("check database %s before the restore; nothing was changed: %w", databaseNameOf(dsn), err)
	}
	drop, err := confirmForeignDrop(cmd.InOrStdin(), out, foreign, databaseNameOf(dsn), dropForeign)
	if err != nil {
		return err
	}
	local, _ := config.ReadLocal("")

	wasRunning := nativeRunning(ctx)
	if wasRunning {
		fmt.Fprintln(out, "Stopping OpenNeko...")
		if err := nativeStop(ctx, out); err != nil {
			return err
		}
	}
	failed := func(step string, err error) error {
		if activeServer != nil {
			if ownErr := ownServerData(*activeServer); ownErr != nil {
				err = errors.Join(err, fmt.Errorf("set the owners of the restored files: %w", ownErr))
			}
		}
		return fmt.Errorf("restore failed while %s; OpenNeko is stopped and may hold partly restored data. Run the restore again once the cause is fixed: %w", step, err)
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	plain, err := newBackupDecrypter(file, key)
	if err != nil {
		return err
	}
	folders := map[string]nativeFolder{}
	for _, f := range nativeFolders() {
		folders[f.volume] = f
	}
	bundle := newBackupBundleReader(plain)
	databaseRestored := false
	for {
		name, entry, err := bundle.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return failed("reading the backup", err)
		}
		switch {
		case strings.HasPrefix(name, backupVolumeEntryPrefix):
			volume := strings.TrimSuffix(strings.TrimPrefix(name, backupVolumeEntryPrefix), backupVolumeEntrySuffix)
			f, ok := folders[volume]
			if !ok {
				fmt.Fprintf(out, "Skipping volume %s: a native install does not use it.\n", volume)
				continue
			}
			fmt.Fprintf(out, "Restoring %s...\n", volume)
			if err := restoreFolder(f, entry); err != nil {
				return failed("restoring "+volume, err)
			}
		case name == backupDatabaseEntry:
			fmt.Fprintln(out, "Restoring the database...")
			if err := restoreExternalDatabase(ctx, dsn, entry, out, drop); err != nil {
				return failed("restoring the database", err)
			}
			databaseRestored = true
		}
	}
	if !databaseRestored {
		return failed("reading the backup", errors.New("the database dump is missing"))
	}
	if url := strings.TrimSpace(local.DatabaseURL); url != "" {
		if err := config.WriteLocalDatabaseURL("", url); err != nil {
			return failed("keeping the database URL", err)
		}
	}
	if activeServer != nil {
		if err := ownServerData(*activeServer); err != nil {
			return failed("setting the owners of the restored files", err)
		}
	}
	if !wasRunning {
		fmt.Fprintf(out, "Restore complete from %s. `openneko start` starts OpenNeko and applies the migrations.\n", path)
		return nil
	}
	fmt.Fprintln(out, "Starting OpenNeko...")
	if err := nativeStartAgain(ctx, out, cmd.ErrOrStderr(), ""); err != nil {
		return fmt.Errorf("restore completed but OpenNeko did not start: %w", err)
	}
	fmt.Fprintf(out, "Restore complete from %s.\n", path)
	return nil
}

func backupDump[T any](path, key string, use func(io.Reader) (T, error)) (T, error) {
	var zero T
	file, err := os.Open(path)
	if err != nil {
		return zero, err
	}
	defer file.Close()
	plain, err := newBackupDecrypter(file, key)
	if err != nil {
		return zero, err
	}
	bundle := newBackupBundleReader(plain)
	for {
		name, entry, err := bundle.Next()
		if errors.Is(err, io.EOF) {
			return zero, errors.New("the database dump is missing")
		}
		if err != nil {
			return zero, err
		}
		if name == backupDatabaseEntry {
			return use(bufio.NewReaderSize(entry, 1<<20))
		}
	}
}

func runSelf(ctx context.Context, stdout, stderr io.Writer, exe string, args ...string) error {
	if exe == "" {
		var err error
		if exe, err = os.Executable(); err != nil {
			return err
		}
	}
	c := exec.CommandContext(ctx, exe, args...)
	c.Stdout, c.Stderr = stdout, stderr
	return c.Run()
}

var (
	postgresMajor = regexp.MustCompile(`(\d+)(?:\.\d+)?`)
	toolVersionRe = regexp.MustCompile(`\(PostgreSQL\)\s+(\S+)`)
)

func toolVersionOf(out string) string {
	if m := toolVersionRe.FindStringSubmatch(out); m != nil {
		return m[1]
	}
	return strings.TrimSpace(out)
}

func majorOf(v string) string {
	if m := postgresMajor.FindStringSubmatch(v); m != nil {
		return m[1]
	}
	return ""
}

// postgresToolLine reports pg_dump or pg_restore. Each must be at least the
// server's major version to read and write its archives.
func postgresToolLine(ctx context.Context, tool, serverVersion string) (string, bool) {
	raw, err := exec.CommandContext(ctx, tool, "--version").Output()
	if err != nil {
		return fmt.Sprintf("%s: FAIL, not found; install the PostgreSQL %s client tools so that backup and restore work", tool, majorOf(serverVersion)), false
	}
	toolVersion := toolVersionOf(string(raw))
	if serverVersion != "" && olderVersion(majorOf(toolVersion), majorOf(serverVersion)) {
		return fmt.Sprintf("%s: FAIL, %s is older than the server (%s); install the PostgreSQL %s client tools", tool, toolVersion, serverVersion, majorOf(serverVersion)), false
	}
	return fmt.Sprintf("%s: %s", tool, toolVersion), true
}

func runNativeDoctor(ctx context.Context, out io.Writer) error {
	failed := false
	h := host.Platform()
	if h.Supported {
		fmt.Fprintf(out, "host: %s (supported)\n", h.Triple)
	} else {
		fmt.Fprintf(out, "host: %s (UNSUPPORTED)\n", h.Triple)
		if h.Reason != "" {
			fmt.Fprintf(out, "  reason: %s\n", h.Reason)
		}
		failed = true
	}
	fmt.Fprintln(out, "runtime: native")
	if activeServer != nil {
		failed = serverDoctorLines(out) || failed
	}
	if settings, ok := nativeInstall(); ok {
		if err := webfront.Check(settings.WebDir); err != nil {
			fmt.Fprintf(out, "web app: FAIL, %v\n", err)
			failed = true
		} else {
			fmt.Fprintf(out, "web app: %s\n", settings.WebDir)
		}
	}
	serverVersion := ""
	dsn, err := nativeDatabaseURL()
	if err != nil {
		fmt.Fprintf(out, "database: FAIL, %v\n", err)
		failed = true
	} else if err := checkDatabase(ctx, dsn, out); err != nil {
		fmt.Fprintf(out, "database: FAIL, %v\n", err)
		failed = true
	} else if identity, err := databaseIdentityOf(ctx, dsn); err == nil {
		if parts := strings.Split(identity, "|"); len(parts) == 3 {
			serverVersion = parts[2]
		}
	}
	for _, tool := range []string{"pg_dump", "pg_restore"} {
		line, ok := postgresToolLine(ctx, tool, serverVersion)
		fmt.Fprintln(out, line)
		failed = failed || !ok
	}
	sup := compose.New(assets.ComposeFS)
	if dir, err := backupDirectory(sup, nativeBackupProject); err != nil {
		fmt.Fprintf(out, "backup: warning, %v\n", err)
	} else if latest, err := latestBackup(dir); err != nil {
		fmt.Fprintf(out, "backup: warning, cannot read %s: %v\n", dir, err)
	} else {
		fmt.Fprintf(out, "backup: %s\n", backupDoctorLine(latest, dir, time.Now()))
	}
	docker := preflight.Docker()
	fmt.Fprintf(out, "docker: %s (a native install needs Docker only for the external services that you run in it)\n", docker.Detail)
	_ = statusNative(ctx, out)
	if failed {
		return WithExit(1, nil)
	}
	return nil
}

func runNativeUpgrade(ctx context.Context, cmd *cobra.Command, settings installation.Settings, from string, skipBackup bool) error {
	out, errOut := cmd.OutOrStdout(), cmd.ErrOrStderr()
	if err := requireServerRoot("upgrade"); err != nil {
		return err
	}
	from, err := filepath.Abs(from)
	if err != nil {
		return err
	}
	name := "openneko"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	newExe := filepath.Join(from, "bin", name)
	newWeb := filepath.Join(from, "share", "openneko", "web")
	if err := webfront.Check(newWeb); err != nil {
		return fmt.Errorf("the release web app in %s: %w (nothing was changed)", newWeb, err)
	}
	raw, err := exec.CommandContext(ctx, newExe, "version").Output()
	if err != nil {
		return fmt.Errorf("run %s version: %w (nothing was changed)", newExe, err)
	}
	fmt.Fprintf(out, "Upgrading to %s", raw)
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if activeServer != nil {
		exe = activeServer.Exe
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	if skipBackup {
		fmt.Fprintln(errOut, "warning: --skip-backup given; upgrading without a backup.")
	} else {
		fmt.Fprintln(out, "Taking a backup before the upgrade...")
		if _, err := createNativeBackup(ctx, "", out); err != nil {
			return fmt.Errorf("the backup before the upgrade failed, so nothing was changed (--skip-backup upgrades without one): %w", err)
		}
	}
	wasRunning := nativeRunning(ctx)
	if wasRunning {
		fmt.Fprintln(out, "Stopping OpenNeko...")
		if err := nativeStop(ctx, out); err != nil {
			return err
		}
	}
	if !sameFile(newExe, exe) {
		if err := replaceFile(newExe, exe); err != nil {
			return fmt.Errorf("replace %s: %w", exe, err)
		}
	}
	if !sameFile(newWeb, settings.WebDir) {
		if err := replaceFolder(newWeb, settings.WebDir); err != nil {
			return fmt.Errorf("replace %s: %w", settings.WebDir, err)
		}
	}
	if !wasRunning {
		fmt.Fprintln(out, "Upgrade complete. `openneko start` starts OpenNeko and applies the migrations.")
		return nil
	}
	fmt.Fprintln(out, "Starting OpenNeko...")
	if err := nativeStartAgain(ctx, out, errOut, exe); err != nil {
		return fmt.Errorf("the files are upgraded but OpenNeko did not start: %w", err)
	}
	fmt.Fprintln(out, "Upgrade complete.")
	return nil
}

func sameFile(a, b string) bool {
	ai, err := os.Stat(a)
	if err != nil {
		return false
	}
	bi, err := os.Stat(b)
	return err == nil && os.SameFile(ai, bi)
}

func replaceFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dst), "."+filepath.Base(dst)+".new-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dst)
}

func replaceFolder(src, dst string) error {
	staged, err := os.MkdirTemp(filepath.Dir(dst), "."+filepath.Base(dst)+".new-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staged)
	if err := os.CopyFS(staged, os.DirFS(src)); err != nil {
		return err
	}
	if err := os.Chmod(staged, 0o755); err != nil {
		return err
	}
	old := dst + ".old"
	if err := os.RemoveAll(old); err != nil {
		return err
	}
	if err := os.Rename(dst, old); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.Rename(staged, dst); err != nil {
		_ = os.Rename(old, dst)
		return err
	}
	return os.RemoveAll(old)
}
