package config

import (
	"path/filepath"

	"github.com/open-neko/neko/apps/openneko/internal/appdir"
	"github.com/open-neko/neko/apps/openneko/internal/instance"
)

const AppDirName = "openneko"

func Dir(override string) string {
	if override != "" {
		return override
	}
	return instance.ScopeConfigDir(RootDir())
}

// RootDir returns the unscoped OpenNeko config directory. Most callers should
// use Dir; inventory commands use RootDir to discover named installations.
func RootDir() string {
	return appdir.Config()
}

func File(override, name string) string {
	return filepath.Join(Dir(override), name)
}
