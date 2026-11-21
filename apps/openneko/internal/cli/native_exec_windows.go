package cli

import "github.com/open-neko/neko/apps/openneko/internal/native"

// becomeSupervisor keeps the supervisor in this process: Windows has no
// agent confinement yet, and no exec that replaces a process.
func becomeSupervisor(native.Layout, native.Options) error { return nil }
