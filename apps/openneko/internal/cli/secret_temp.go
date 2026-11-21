package cli

import (
	"os"
	"runtime"

	"github.com/open-neko/neko/apps/openneko/internal/ownonly"
)

// secretTemp makes a temp file for a database URL or a database archive.
// %TEMP% on Windows can be open to other accounts, so the file goes in the
// data folder's tmp folder, limited to the owner.
func secretTemp(pattern string) (*os.File, error) {
	dir := ""
	if runtime.GOOS == "windows" {
		dir = nativeLayout().Tmp()
		if err := ownonly.Mkdir(dir); err != nil {
			return nil, err
		}
	}
	return os.CreateTemp(dir, pattern)
}
