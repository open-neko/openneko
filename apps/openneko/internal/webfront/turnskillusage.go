package webfront

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
)

var (
	skillUseName  = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	skillUsePath  = regexp.MustCompile(`(?i)(?:^|/)skills/([a-z0-9]+(?:-[a-z0-9]+)*)/SKILL\.md(?:$|[?#])`)
	learnedFile   = regexp.MustCompile(`^---\s*\n([\s\S]*?)\n---\s*\n?([\s\S]*)$`)
	learnedStatus = regexp.MustCompile(`(?m)^status:[ \t\n\f\r\v]*(.+)$`)
	learnedBase   = regexp.MustCompile(`(?m)^base_hash:[ \t\n\f\r\v]*(.+)$`)
)

// detectSkillUse is detectSkillUse() in packages/llm/src/work/skill-usage.ts:
// a tool call that names a skill's SKILL.md reads that skill.
func detectSkillUse(event any) string {
	if jsGet(event, "type") != "tool_start" {
		return ""
	}
	for _, value := range skillUseStrings(jsGet(event, "input"), 0) {
		m := skillUsePath.FindStringSubmatch(strings.ReplaceAll(value, `\`, "/"))
		if m != nil && skillUseName.MatchString(m[1]) {
			return m[1]
		}
	}
	return ""
}

func skillUseStrings(value any, depth int) []string {
	if depth > 6 || value == nil {
		return nil
	}
	switch t := value.(type) {
	case string:
		return []string{t}
	case []any:
		var out []string
		for _, item := range t {
			out = append(out, skillUseStrings(item, depth+1)...)
		}
		return out
	case *jsObj:
		var out []string
		for _, k := range t.ordered() {
			out = append(out, skillUseStrings(t.vals[k], depth+1)...)
		}
		return out
	}
	return nil
}

func learnedScalar(frontmatter string, re *regexp.Regexp) string {
	m := re.FindStringSubmatch(frontmatter)
	if m == nil {
		return ""
	}
	return jsTrim(m[1])
}

// fingerprintEffectiveSkill is fingerprintEffectiveSkill() in
// packages/llm/src/work/skill-overlay.ts: the base hash, or the base hash
// with the learned guidance that applies to it.
func fingerprintEffectiveSkill(orgRoot, name, baseDir string) (string, error) {
	base, err := fingerprintSkillTree(baseDir)
	if err != nil {
		return "", err
	}
	raw, err := os.ReadFile(filepath.Join(orgRoot, "skill-overlays", name, "LEARNED.md"))
	if err != nil {
		return base, nil
	}
	m := learnedFile.FindStringSubmatch(string(raw))
	if m == nil {
		return base, nil
	}
	status := learnedScalar(m[1], learnedStatus)
	if status == "" {
		status = "applied"
	}
	if status != "applied" || learnedScalar(m[1], learnedBase) != base {
		return base, nil
	}
	h := sha256.New()
	h.Write([]byte(base + "\x00" + jsTrim(m[2])))
	return hex.EncodeToString(h.Sum(nil)), nil
}

func configHead(root string) any {
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		return nil
	}
	out, err := git(root, "rev-parse", "HEAD")
	if sha := strings.TrimSpace(out); err == nil && sha != "" {
		return sha
	}
	return nil
}

// recordSkillUsage is recordSkillUsageFromEvent(): the first read of a skill
// in a run adds one skill_usage row and one skill_used event. A failure is
// logged only.
func (a *API) recordSkillUsage(ctx context.Context, orgID, threadID, runID string, event any, eventID int64) {
	name := detectSkillUse(event)
	if name == "" {
		return
	}
	if err := a.persistSkillUsage(ctx, orgID, threadID, runID, name, eventID); err != nil {
		log.Printf("[skill-usage] failed to record %s for run %s: %v", name, runID, err)
	}
}

func (a *API) persistSkillUsage(ctx context.Context, orgID, threadID, runID, name string, eventID int64) error {
	root := orgAgentRoot(orgID)
	dir := filepath.Join(root, "skills", name)
	var packID, packVersion *string
	if err := noRows(a.DB.QueryRow(ctx, `select i.pack_id, i.version from pack_artifact a
		join pack_install i on a.pack_install_id = i.id
		where a.org_id = $1 and a.artifact_kind = 'skill' and a.target_ref = $2 and i.status <> 'removed' limit 1`, orgID, name).
		Scan(&packID, &packVersion)); err != nil {
		return err
	}
	hash, err := fingerprintEffectiveSkill(root, name, dir)
	if err != nil {
		return nil
	}
	head := configHead(root)
	origin := "custom"
	switch {
	case packID != nil:
		origin = "pack"
	case readSkillOrigin(dir) != nil:
		origin = "builtin"
	}
	var id string
	err = a.DB.QueryRow(ctx, `insert into skill_usage (org_id, run_id, skill_name, content_hash, origin, pack_id, pack_version,
		config_commit_sha, source, first_event_id, attempt) values ($1, $2::uuid, $3, $4, $5, $6, $7, $8, 'read', $9, 1)
		on conflict (run_id, skill_name) do nothing returning id::text`,
		orgID, runID, name, hash, origin, packID, packVersion, head, eventID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	used := jsObjOf("type", "skill_used", "name", name, "source", "read", "contentHash", hash, "origin", origin,
		"firstEventId", float64(eventID))
	if packID != nil {
		used.set("packId", *packID)
		used.set("packVersion", *packVersion)
	}
	used.set("configCommitSha", head)
	_, err = a.insertRunEvent(ctx, orgID, threadID, runID, used)
	return err
}
