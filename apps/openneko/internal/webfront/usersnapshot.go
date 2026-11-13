package webfront

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type configFile struct{ path, content string }

var (
	refUnsafe = regexp.MustCompile(`[^a-zA-Z0-9_-]`)
	refDashes = regexp.MustCompile(`^-+|-+$`)
)

// userConfigRef is userConfigRef() in packages/llm/src/config-vcs/forks.ts.
func userConfigRef(userID string) string {
	slug := refDashes.ReplaceAllString(refUnsafe.ReplaceAllString(userID, "-"), "")
	safe := slug
	if slug != userID {
		sum := sha256.Sum256([]byte(userID))
		safe = slug + "-" + hex.EncodeToString(sum[:])[:8]
	}
	if safe == "" {
		safe = "anon"
	}
	return "refs/heads/user/" + safe
}

// commitUserRef is commitToUserRef(): the files become the whole tree of the
// user's ref, or with overlay they replace only their own paths in it. The
// working tree stays the team checkout.
func commitUserRef(root, userID string, files []configFile, message string, overlay bool) (string, error) {
	if err := ensureConfigRepo(root); err != nil {
		return "", err
	}
	ref := userConfigRef(userID)
	parent := ""
	if out, err := git(root, "rev-parse", "--verify", "--quiet", ref); err == nil {
		parent = strings.TrimSpace(out)
	}
	tmp, err := os.MkdirTemp("", "neko-gitidx-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	env := []string{"GIT_INDEX_FILE=" + filepath.Join(tmp, "index")}
	base := []string{"read-tree", "--empty"}
	if overlay && parent != "" {
		base = []string{"read-tree", parent}
	}
	if _, err := gitWith(root, env, nil, base...); err != nil {
		return "", err
	}
	for _, f := range files {
		blob, err := gitWith(root, env, strings.NewReader(f.content), "hash-object", "-w", "--stdin")
		if err != nil {
			return "", err
		}
		if _, err := gitWith(root, env, nil, "update-index", "--add", "--cacheinfo", "100644,"+strings.TrimSpace(blob)+","+f.path); err != nil {
			return "", err
		}
	}
	out, err := gitWith(root, env, nil, "write-tree")
	if err != nil {
		return "", err
	}
	tree := strings.TrimSpace(out)
	args := []string{"commit-tree", tree}
	if parent != "" {
		parentTree, err := git(root, "rev-parse", parent+"^{tree}")
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(parentTree) == tree {
			return "", nil
		}
		args = append(args, "-p", parent)
	}
	out, err = git(root, append(args, "-m", message)...)
	if err != nil {
		return "", err
	}
	sha := strings.TrimSpace(out)
	_, err = git(root, "update-ref", ref, sha)
	return sha, err
}

// snapshotUserConfigs is snapshotUserConfigsForOrg(): each member with
// personal memories or workflows gets a checkpoint on their own ref.
func (a *API) snapshotUserConfigs(ctx context.Context, orgID string) {
	rows, err := a.DB.Query(ctx, `select user_id from work_memory where org_id = $1 and user_id is not null and archived_at is null
		union select owner_user_id from workflow_definition where org_id = $1 and owner_user_id <> ''`, orgID)
	if err != nil {
		log.Printf("[config-vcs] org user snapshots failed: %v", err)
		return
	}
	var owners []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			log.Printf("[config-vcs] org user snapshots failed: %v", err)
			return
		}
		if id != "" {
			owners = append(owners, id)
		}
	}
	if err := rows.Err(); err != nil {
		log.Printf("[config-vcs] org user snapshots failed: %v", err)
		return
	}
	for _, userID := range owners {
		if err := a.snapshotUserConfig(ctx, orgID, userID); err != nil {
			log.Printf("[config-vcs] user snapshot failed: %v", err)
		}
	}
}

// snapshotUserConfig is snapshotUserConfig() in packages/llm/src/config-vcs/forks.ts.
func (a *API) snapshotUserConfig(ctx context.Context, orgID, userID string) error {
	rows, err := a.DB.Query(ctx, `select id::text, kind, text, pinned, suppressed, overrides_origin_id::text, updated_at from work_memory
		where org_id = $1 and user_id = $2 and archived_at is null
		  and kind in ('business_rule', 'preference', 'metric_definition', 'company_context')`, orgID, userID)
	if err != nil {
		return err
	}
	var files []configFile
	for rows.Next() {
		var id, kind, text string
		var pinned, suppressed bool
		var origin *string
		var updated time.Time
		if err := rows.Scan(&id, &kind, &text, &pinned, &suppressed, &origin, &updated); err != nil {
			rows.Close()
			return err
		}
		lines := []string{"---", "type: " + kind, "id: " + id, "kind: " + kind, fmt.Sprintf("pinned: %t", pinned)}
		if origin != nil && *origin != "" {
			lines = append(lines, "overrides_origin_id: "+*origin)
		}
		if suppressed {
			lines = append(lines, "suppressed: true")
		}
		lines = append(lines, "updated_at: "+jsTime(updated), "---", "", text, "")
		files = append(files, configFile{path: "memory/" + kind + "/" + id + ".md", content: strings.Join(lines, "\n")})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	wfRows, err := a.DB.Query(ctx, `select `+workflowRecordColumns+` from workflow_definition where org_id = $1 and owner_user_id = $2`, orgID, userID)
	if err != nil {
		return err
	}
	for wfRows.Next() {
		wf, err := scanWorkflowRecord(wfRows)
		if err != nil {
			wfRows.Close()
			return err
		}
		body, err := workflowMarkdown(wf)
		if err != nil {
			wfRows.Close()
			return err
		}
		files = append(files, configFile{path: "workflows/" + workflowSlug(wf.Name) + ".md", content: body})
	}
	if err := wfRows.Err(); err != nil {
		return err
	}
	configRepoMu.Lock()
	sha, err := commitUserRef(orgAgentRoot(orgID), userID, files, "Personal config checkpoint", false)
	configRepoMu.Unlock()
	if err != nil || sha == "" {
		return err
	}
	if _, err := a.DB.Exec(ctx, `insert into config_ref (org_id, scope, user_id, commit_sha) values ($1, 'user', $2, $3)
		on conflict (org_id, scope, user_id) do update set commit_sha = excluded.commit_sha, updated_at = now()`, orgID, userID, sha); err != nil {
		log.Printf("[config-vcs] user config_ref update failed (commit persisted): %v", err)
	}
	return nil
}
