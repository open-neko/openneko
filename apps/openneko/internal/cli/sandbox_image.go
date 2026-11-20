package cli

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

const sandboxImageRepo = "ghcr.io/open-neko/neko-sandbox"

var listNativeSandboxImages = func(ctx context.Context) ([]string, error) {
	out, err := exec.CommandContext(ctx, "docker", "ps",
		"--filter", "label=com.docker.compose.project=openneko-sandbox",
		"--format", "{{.Image}}").Output()
	if err != nil {
		return nil, err
	}
	return uniqueLines(string(out)), nil
}

// staleSandboxImages returns the published sandbox images that run at a tag
// other than target. A source build under another name is the operator's.
func staleSandboxImages(images []string, target string) []string {
	var out []string
	for _, ref := range images {
		repo, tag, ok := strings.Cut(ref, ":")
		if ok && repo == sandboxImageRepo && tag != target {
			out = append(out, ref)
		}
	}
	return out
}

// warnStaleNativeSandbox names a sandbox that the operator runs with
// compose.native.yml at an older release. Without Docker it says nothing.
func warnStaleNativeSandbox(ctx context.Context, errOut io.Writer, target string) {
	images, err := listNativeSandboxImages(ctx)
	if err != nil {
		return
	}
	for _, ref := range staleSandboxImages(images, target) {
		fmt.Fprintf(errOut, "warning: the sandbox runs %s. Run its compose command again with OPENNEKO_VERSION=%s (recipes/sandbox/README.md, \"Upgrade the sandbox\").\n", ref, target)
	}
}
