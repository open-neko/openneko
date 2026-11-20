package host

import (
	"os/exec"
	"runtime"
)

type Result struct {
	Supported bool
	Triple    string
	Reason    string
}

// Check reports whether the OpenNeko stack is expected to run on this host.
// Every service, the agent runtime included, runs as a container, so the
// requirement is Docker — not KVM, which was a microsandbox-era constraint.
func Check() Result {
	return checkWith(runtime.GOOS, runtime.GOARCH, hasDocker)
}

// Platform reports OS/arch support independent of whether docker is present.
// preflight checks docker separately so it can give a sharper "daemon not
// running" remediation, so it wants the OS/arch verdict on its own.
func Platform() Result {
	return checkWith(runtime.GOOS, runtime.GOARCH, func() bool { return true })
}

func checkWith(goos, goarch string, docker func() bool) Result {
	triple := goos + "-" + goarch
	switch goos {
	case "darwin", "linux":
		if goarch != "arm64" && goarch != "amd64" {
			return Result{
				Supported: false,
				Triple:    triple,
				Reason:    goos + " " + goarch + " is not supported — the OpenNeko images ship for amd64 and arm64.",
			}
		}
		if !docker() {
			return Result{
				Supported: false,
				Triple:    triple,
				Reason:    "docker not found on PATH — the OpenNeko stack and its agent runtime run as containers.",
			}
		}
		return Result{Supported: true, Triple: triple}
	}
	return Result{
		Supported: false,
		Triple:    triple,
		Reason:    "the Docker install runs on Linux and macOS; on Windows, install the native release with install-native.ps1.",
	}
}

// Native reports whether the native release, which needs no Docker, runs on
// this host.
func Native() Result {
	return nativeCheck(runtime.GOOS, runtime.GOARCH)
}

func nativeCheck(goos, goarch string) Result {
	triple := goos + "-" + goarch
	switch goos {
	case "linux", "darwin", "windows":
	default:
		return Result{Triple: triple, Reason: "the native release is for Linux, macOS and Windows."}
	}
	if goarch != "amd64" && goarch != "arm64" {
		return Result{Triple: triple, Reason: "the native release is for amd64 and arm64."}
	}
	return Result{Supported: true, Triple: triple}
}

func hasDocker() bool {
	_, err := exec.LookPath("docker")
	return err == nil
}
