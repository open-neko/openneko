package cli

import "io/fs"

func fileOwner(fs.FileInfo) (int, int, bool) { return 0, 0, false }
