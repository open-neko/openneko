package host

import (
	"errors"
	"io/fs"
	"os"
	"runtime"
)

type Result struct {
	Supported bool
	Triple    string
	Reason    string
}

// Check reports whether microsandbox is expected to run on this host. Mirrors
// the gating in apps/openneko-cli/src/host-check.ts and worker/src/plugins/
// microsandbox-sdk.ts; keep in sync if microsandbox adds targets.
func Check() Result {
	return checkWith(runtime.GOOS, runtime.GOARCH, hasKVM)
}

func checkWith(goos, goarch string, kvm func() bool) Result {
	switch goos {
	case "darwin":
		if goarch != "arm64" {
			return Result{
				Supported: false,
				Triple:    goos + "-" + goarch,
				Reason:    "macOS x86_64 is not supported — microsandbox bundles only arm64. Run OpenNeko on Apple Silicon, or on a Linux host with KVM.",
			}
		}
		return Result{Supported: true, Triple: "darwin-arm64"}
	case "linux":
		triple := "linux-x64-gnu"
		if goarch == "arm64" {
			triple = "linux-arm64-gnu"
		} else if goarch != "amd64" {
			return Result{
				Supported: false,
				Triple:    goos + "-" + goarch,
				Reason:    "Linux " + goarch + " not supported by the bundled microsandbox runtime.",
			}
		}
		if !kvm() {
			return Result{
				Supported: false,
				Triple:    triple,
				Reason:    "/dev/kvm not found — microsandbox needs KVM (nested virtualization in cloud VMs may need the host enabled).",
			}
		}
		return Result{Supported: true, Triple: triple}
	}
	return Result{
		Supported: false,
		Triple:    goos + "-" + goarch,
		Reason:    "Windows is not currently supported. WSL2 viability is being evaluated.",
	}
}

func hasKVM() bool {
	_, err := os.Stat("/dev/kvm")
	return !errors.Is(err, fs.ErrNotExist)
}
