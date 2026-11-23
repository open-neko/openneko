package webfront

import (
	"io/fs"
	"os"
)

const stagedOpenFlags = os.O_RDONLY

func singleLink(fs.FileInfo) bool { return true }

// hasHole is false here; the size limit still holds.
func hasHole(*os.File, int64) bool { return false }
