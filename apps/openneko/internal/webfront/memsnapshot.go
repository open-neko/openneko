package webfront

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const configRepoIgnore = `# config-vcs (CV0): version config artifacts only.
*
!.gitignore
!index.md
!skills/
!skills/**
!workflows/
!workflows/**
!memory/
!memory/**
!skill-overlays/
!skill-overlays/**
`

const configRepoIndex = `---
okf_version: "0.2"
title: "OpenNeko org knowledge"
---

# OpenNeko org knowledge

- [skills/](skills/) — what the assistant knows how to do
- [workflows/](workflows/) — scheduled and saved workflows
- [memory/](memory/) — durable team memories
- [skill-overlays/](skill-overlays/) — additive learned guidance for skills
`

var (
	configRepoMu  sync.Mutex
	unsafeSegment = regexp.MustCompile(`[^a-zA-Z0-9._-]`)
)

// orgAgentRoot is getOrgAgentRoot() in packages/llm/src/work/workspace.ts.
func orgAgentRoot(orgID string) string {
	home := ""
	if os.Getenv("OPENNEKO_HOST_WEB_DEV") == "1" && os.Getenv("NODE_ENV") == "development" &&
		os.Getenv("OPENNEKO_STACK_MODE") != "demo" && os.Getenv("NEXT_PUBLIC_DEMO") != "true" && os.Getenv("DEMO") != "true" {
		home = strings.TrimSpace(os.Getenv("OPENNEKO_AGENT_HOME"))
	}
	if home == "" {
		home = os.Getenv("HOME")
	}
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	return filepath.Join(home, ".config", "openneko", "agents", "orgs", unsafeSegment.ReplaceAllString(orgID, "_"))
}

// snapshotTeamMemories is snapshotDurableMemories() in
// packages/llm/src/config-vcs/snapshot.ts. A failure is logged and the
// promote stands.
func (a *API) snapshotTeamMemories(ctx context.Context, orgID string) {
	if err := a.writeMemorySnapshot(ctx, orgID); err != nil {
		log.Printf("[config-vcs] memory snapshot failed: %v", err)
	}
}

func (a *API) writeMemorySnapshot(ctx context.Context, orgID string) error {
	rows, err := a.DB.Query(ctx, `select id::text, kind, text, pinned, updated_at from work_memory
		where org_id = $1 and kind in ('business_rule', 'preference', 'metric_definition', 'company_context')
		  and user_id is null and archived_at is null`, orgID)
	if err != nil {
		return err
	}
	type snap struct {
		id, kind, text string
		pinned         bool
		updated        time.Time
	}
	var snaps []snap
	for rows.Next() {
		var s snap
		if err := rows.Scan(&s.id, &s.kind, &s.text, &s.pinned, &s.updated); err != nil {
			rows.Close()
			return err
		}
		snaps = append(snaps, s)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	root := orgAgentRoot(orgID)
	configRepoMu.Lock()
	defer configRepoMu.Unlock()
	memoryRoot := filepath.Join(root, "memory")
	if err := os.RemoveAll(memoryRoot); err != nil {
		return err
	}
	for _, s := range snaps {
		dir := filepath.Join(memoryRoot, s.kind)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		body := strings.Join([]string{"---", "type: " + s.kind, "id: " + s.id, "kind: " + s.kind,
			fmt.Sprintf("pinned: %t", s.pinned), "updated_at: " + jsTime(s.updated), "---", "", s.text, ""}, "\n")
		if err := os.WriteFile(filepath.Join(dir, s.id+".md"), []byte(body), 0o644); err != nil {
			return err
		}
	}
	sha, err := commitConfigRepo(root, "memory", "Memory checkpoint")
	if err != nil {
		return fmt.Errorf("versioning failed (write succeeded): %w", err)
	}
	if sha == "" {
		return nil
	}
	_, err = a.DB.Exec(ctx, `insert into config_ref (org_id, scope, commit_sha) values ($1, 'team', $2)
		on conflict (org_id, scope, user_id) do update set commit_sha = excluded.commit_sha, updated_at = now()`, orgID, sha)
	return err
}

func git(root string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=OpenNeko", "GIT_AUTHOR_EMAIL=auto@openneko.local",
		"GIT_COMMITTER_NAME=OpenNeko", "GIT_COMMITTER_EMAIL=auto@openneko.local",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s failed: %s", strings.Join(args, " "), msg)
	}
	return out.String(), nil
}

// commitConfigRepo is commitConfigChange() in packages/llm/src/config-vcs:
// the new HEAD, or "" when nothing changed.
func commitConfigRepo(root, path, message string) (string, error) {
	if _, err := os.Stat(filepath.Join(root, ".git")); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(root, 0o755); err != nil {
			return "", err
		}
		if _, err := git(root, "init", "--initial-branch=main"); err != nil {
			return "", err
		}
	}
	ignore, ignoreErr := os.ReadFile(filepath.Join(root, ".gitignore"))
	index, _ := os.ReadFile(filepath.Join(root, "index.md"))
	if ignoreErr != nil || string(ignore) != configRepoIgnore || string(index) != configRepoIndex {
		if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte(configRepoIgnore), 0o644); err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(root, "index.md"), []byte(configRepoIndex), 0o644); err != nil {
			return "", err
		}
		message := "Update tracked config paths"
		if ignoreErr != nil {
			message = "Initialize config versioning"
		}
		if _, err := git(root, "add", ".gitignore", "index.md"); err != nil {
			return "", err
		}
		if _, err := git(root, "commit", "-m", message); err != nil {
			return "", err
		}
	}
	if _, err := git(root, "add", "-A", "--", path); err != nil {
		return "", err
	}
	status, err := git(root, "status", "--porcelain", "--", path)
	if err != nil || strings.TrimSpace(status) == "" {
		return "", err
	}
	if _, err := git(root, "commit", "-m", message); err != nil {
		return "", err
	}
	head, err := git(root, "rev-parse", "HEAD")
	return strings.TrimSpace(head), err
}
