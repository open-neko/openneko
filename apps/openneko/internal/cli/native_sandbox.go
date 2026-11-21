package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/open-neko/neko/apps/openneko/internal/appdir"
	"github.com/open-neko/neko/apps/openneko/internal/instance"
	"github.com/open-neko/neko/apps/openneko/internal/native"
	"github.com/open-neko/openneko/apps/ax-harness/sandbox"
)

func nativeSandboxDir() string {
	return instance.ScopeConfigDir(appdir.Data() + "-sandbox")
}

// prepareNativeSandbox makes the sandbox socket folder of a one-user native
// install, mode 0700. The sandbox gives its socket to the user's primary
// group, which other users can share, so a sandbox here needs a key and a
// socket folder that only this user can enter.
func prepareNativeSandbox(dir, url, key string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	if url == "" {
		return nil
	}
	if key == "" {
		return fmt.Errorf("OPENNEKO_SANDBOX_URL is set and OPENNEKO_SANDBOX_KEY is not: a sandbox on a one-user install needs a key (INSTALL.md, \"Run without Docker\")")
	}
	path, ok := sandbox.SocketPath(url)
	if !ok || runtime.GOOS == "windows" {
		return nil
	}
	if path == "" {
		return fmt.Errorf("OPENNEKO_SANDBOX_URL %s names no socket path", url)
	}
	if err := native.CheckSocketPath(path); err != nil {
		return err
	}
	folder := filepath.Dir(filepath.Clean(path))
	info, err := os.Stat(folder)
	if err != nil {
		return fmt.Errorf("the sandbox socket folder: %w", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("the sandbox socket folder %s has mode %04o; other users can enter it. Use %s or set the folder to mode 0700", folder, info.Mode().Perm(), dir)
	}
	if uid, _, ok := fileOwner(info); ok && uid != os.Getuid() {
		return fmt.Errorf("the sandbox socket folder %s belongs to user %d, not to you; use %s", folder, uid, dir)
	}
	return nil
}
