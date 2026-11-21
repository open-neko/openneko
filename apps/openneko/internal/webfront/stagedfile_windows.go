package webfront

import (
	"io/fs"
	"os"
)

const stagedOpenFlags = os.O_RDONLY

func singleLink(fs.FileInfo) bool { return true }
