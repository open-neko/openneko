package webfront

import (
	"context"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

type configCommit struct {
	SHA     string `json:"sha"`
	Date    string `json:"date"`
	Message string `json:"message"`
}

// configHistory is GET /api/v1/work/config/history.
func (a *API) configHistory(w http.ResponseWriter, r *http.Request) {
	orgID, err := a.OrgID(r.Context())
	if err != nil {
		a.fail(w, r, err)
		return
	}
	q := r.URL.Query()
	limit, err := strconv.ParseFloat(strings.TrimSpace(q.Get("limit")), 64)
	if err != nil || math.IsNaN(limit) || limit == 0 {
		limit = 50
	}
	limit = math.Min(limit, 200)
	history := []configCommit{}
	root := orgAgentRoot(orgID)
	if _, err := os.Stat(filepath.Join(root, ".git")); err == nil {
		args := []string{"log", "--max-count=" + strconv.FormatFloat(limit, 'f', -1, 64), "--pretty=format:%H%x09%cI%x09%s"}
		if path := q.Get("path"); path != "" {
			args = append(args, "--", path)
		}
		configRepoMu.Lock()
		out, _ := git(root, args...)
		configRepoMu.Unlock()
		for _, line := range strings.Split(out, "\n") {
			if line == "" {
				continue
			}
			parts := strings.SplitN(line, "\t", 3)
			for len(parts) < 3 {
				parts = append(parts, "")
			}
			history = append(history, configCommit{SHA: parts[0], Date: parts[1], Message: parts[2]})
		}
	}
	sendJSON(w, http.StatusOK, map[string]any{"history": history})
}

var commitSHA = regexp.MustCompile(`^[0-9a-fA-F]{7,40}$`)

func fieldString(body any, name string) (string, bool) {
	v, _ := field(body, name)
	s, ok := v.(string)
	return s, ok
}

// configRestore is POST /api/v1/work/config/restore: a forward-only restore
// of one path to its state at an earlier commit.
func (a *API) configRestore(w http.ResponseWriter, r *http.Request) {
	act, err := a.currentActor(r.Context(), r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if !act.Admin {
		sendError(w, http.StatusForbidden, "admin only")
		return
	}
	body, _ := readBody(r)
	sha, _ := fieldString(body, "sha")
	path, _ := fieldString(body, "path")
	if !commitSHA.MatchString(sha) || path == "" || strings.Contains(path, "..") {
		sendError(w, http.StatusBadRequest, "provide sha and path")
		return
	}
	root := orgAgentRoot(act.OrgID)
	newSHA := ""
	if _, err := os.Stat(filepath.Join(root, ".git")); err == nil {
		if newSHA, err = restoreConfigPath(root, sha, path); err != nil {
			a.fail(w, r, err)
			return
		}
	}
	if newSHA == "" {
		sendError(w, http.StatusNotFound, "nothing to restore (unknown path or already at that version)")
		return
	}
	a.insertConfigChange(r.Context(), configChange{OrgID: act.OrgID, Kind: "config", Ref: path, Actor: act.UserID,
		SHA: newSHA, Summary: "Restored " + path + " to " + sha[:8], Status: "restored"})
	sendJSON(w, http.StatusOK, struct {
		OK  bool   `json:"ok"`
		SHA string `json:"sha"`
	}{true, newSHA})
}

func restoreConfigPath(root, sha, path string) (string, error) {
	configRepoMu.Lock()
	defer configRepoMu.Unlock()
	if _, err := git(root, "checkout", sha, "--", path); err != nil {
		return "", err
	}
	if _, err := git(root, "add", "-A", "--", path); err != nil {
		return "", err
	}
	status, err := git(root, "status", "--porcelain", "--", path)
	if err != nil || strings.TrimSpace(status) == "" {
		return "", err
	}
	if _, err := git(root, "commit", "-m", "Restored "+filepath.Base(path)+" to "+sha[:8]); err != nil {
		return "", err
	}
	head, err := git(root, "rev-parse", "HEAD")
	return strings.TrimSpace(head), err
}

type configChange struct {
	OrgID, Kind, Ref, Actor, SHA, Summary, Status string
}

// insertConfigChange is insertConfigChangeRow(): a failure is logged only.
func (a *API) insertConfigChange(ctx context.Context, c configChange) {
	if c.Status == "" {
		c.Status = "recorded"
	}
	_, err := a.DB.Exec(ctx, `insert into config_change (org_id, artifact_kind, artifact_ref, scope, user_id, actor_user_id, commit_sha, summary, status)
		values ($1, $2, $3, 'team', '', nullif($4, ''), nullif($5, ''), $6, $7)`,
		c.OrgID, c.Kind, c.Ref, c.Actor, c.SHA, c.Summary, c.Status)
	if err != nil {
		log.Printf("[config-vcs] config_change insert failed: %v", err)
	}
}

// recordConfigChange is recordConfigChange() in packages/llm/src/config-vcs:
// commit, move the team ref and add the config_change row. A versioning
// failure must not fail the write that called it.
func (a *API) recordConfigChange(ctx context.Context, orgID, path, message, kind, ref, actorID string) {
	configRepoMu.Lock()
	sha, err := commitConfigRepo(orgAgentRoot(orgID), path, message)
	configRepoMu.Unlock()
	if err != nil {
		log.Printf("[config-vcs] versioning failed (write succeeded): %v", err)
		return
	}
	if sha == "" {
		return
	}
	if _, err := a.DB.Exec(ctx, `insert into config_ref (org_id, scope, commit_sha) values ($1, 'team', $2)
		on conflict (org_id, scope, user_id) do update set commit_sha = excluded.commit_sha, updated_at = now()`, orgID, sha); err != nil {
		log.Printf("[config-vcs] config_ref update failed (commit persisted): %v", err)
	}
	a.insertConfigChange(ctx, configChange{OrgID: orgID, Kind: kind, Ref: ref, Actor: actorID, SHA: sha, Summary: message})
}
