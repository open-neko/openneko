package webfront

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"log"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// skillFileHashes maps "<skill>/<file>" inside a skills folder to the file's
// sha256, as SkillFileHashes in packages/llm/src/config-vcs/skill-capture.ts.
type skillFileHashes map[string]string

type stagedSkillFile struct {
	Path    string
	Content []byte
}

// savedSkillEdits names the skills a run changed: team skills saved in
// place, own skills saved in place, and team skills a member changed,
// saved as a private copy (team name to copy name).
type savedSkillEdits struct {
	Team    []string
	Own     []string
	Copies  map[string]string
	SHA     string
	Skipped []string
}

func (s savedSkillEdits) copyList() []string {
	var out []string
	for from, to := range s.Copies {
		out = append(out, from+" -> "+to)
	}
	slices.Sort(out)
	return out
}

func ignoredSkillPart(part string) bool {
	return part == "__pycache__" || part == ".DS_Store" || strings.HasPrefix(part, "._") || strings.HasSuffix(part, ".pyc")
}

func safeSkillPath(p string) bool {
	parts := strings.Split(p, "/")
	if len(parts) < 2 {
		return false
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || ignoredSkillPart(part) {
			return false
		}
	}
	return true
}

func skillFileHash(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// hashSkillTree is hashSkillTree(): regular files under each skill folder;
// files at the top and links are left out.
func hashSkillTree(root string) skillFileHashes {
	out := skillFileHashes{}
	var walk func(dir, prefix string)
	walk = func(dir, prefix string) {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if ignoredSkillPart(e.Name()) {
				continue
			}
			rel := e.Name()
			if prefix != "" {
				rel = prefix + "/" + e.Name()
			}
			switch {
			case e.IsDir():
				walk(filepath.Join(dir, e.Name()), rel)
			case e.Type().IsRegular() && prefix != "":
				if data, err := os.ReadFile(filepath.Join(dir, e.Name())); err == nil {
					out[rel] = skillFileHash(data)
				}
			}
		}
	}
	walk(root, "")
	return out
}

func skillOf(p string) string {
	name, _, _ := strings.Cut(p, "/")
	return name
}

// diffSkillTrees is diffSkillTrees(): a skill the run never held is not
// treated as removed.
func diffSkillTrees(baseline, box skillFileHashes) (changed, removed []string) {
	held := map[string]bool{}
	for p, hash := range box {
		held[skillOf(p)] = true
		if safeSkillPath(p) {
			if old, ok := baseline[p]; !ok || old != hash {
				changed = append(changed, p)
			}
		}
	}
	for p := range baseline {
		if _, ok := box[p]; !ok && held[skillOf(p)] {
			removed = append(removed, p)
		}
	}
	slices.Sort(changed)
	slices.Sort(removed)
	return changed, removed
}

var (
	skillShellTools = map[string]bool{"terminal": true, "process": true, "execute_code": true, "execute": true, "delegate_task": true}
	skillWriteTools = map[string]bool{"file_write": true, "file_edit": true, "write_file": true, "patch": true, "edit": true, "delete": true, "move": true}
	skillToolName   = regexp.MustCompile(`(^|[^a-z0-9])(create_skill|skill_manage)$`)
)

type skillToolCall struct {
	Name  string
	Input any
}

type skillEvidence struct {
	ToolCalls   []skillToolCall
	Cwd         string
	SkillsRoots []string
}

func jsRecord(v any) map[string]any {
	switch x := v.(type) {
	case *jsObj:
		if x != nil {
			return x.vals
		}
	case map[string]any:
		return x
	}
	return nil
}

func skillToolPaths(input any) []string {
	top := jsRecord(input)
	candidates := []any{top["path"], jsRecord(top["rawInput"])["path"]}
	if locations, ok := top["locations"].([]any); ok {
		for _, l := range locations {
			candidates = append(candidates, jsRecord(l)["path"])
		}
	}
	var out []string
	for _, c := range candidates {
		if s, ok := c.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

func posixResolve(cwd, p string) string {
	if strings.HasPrefix(p, "/") {
		return path.Clean(p)
	}
	return path.Clean(cwd + "/" + p)
}

func posixWithin(root, target string) bool {
	root = path.Clean(root)
	if target == root {
		return true
	}
	prefix := strings.TrimSuffix(root, "/") + "/"
	return strings.HasPrefix(target, prefix) && !strings.HasPrefix(target[len(prefix):], "..")
}

func touchesSkills(p, cwd string, roots []string) bool {
	if strings.HasPrefix(p, "~") {
		return true
	}
	target := posixResolve(cwd, p)
	if slices.Contains(strings.Split(target, "/"), "skills") {
		return true
	}
	for _, root := range roots {
		if posixWithin(root, target) {
			return true
		}
	}
	return false
}

// skillCaptureDecision is skillCaptureDecision(): whether the run's skills
// folder can have changed. A shell can write anywhere, and missing evidence
// means capture.
func skillCaptureDecision(evidence *skillEvidence) (bool, string) {
	if evidence == nil {
		return true, "evidence_missing"
	}
	for _, call := range evidence.ToolCalls {
		if skillToolName.MatchString(call.Name) {
			return true, "skill_tool"
		}
		if skillShellTools[call.Name] {
			return true, "shell"
		}
		if !skillWriteTools[call.Name] {
			continue
		}
		if title, ok := jsRecord(call.Input)["title"].(string); ok && strings.HasPrefix(title, "skill ") {
			return true, "skill_tool"
		}
		paths := skillToolPaths(call.Input)
		if len(paths) == 0 {
			return true, "write_path_unknown"
		}
		for _, p := range paths {
			if touchesSkills(p, evidence.Cwd, evidence.SkillsRoots) {
				return true, "skill_write"
			}
		}
	}
	return false, "no_skill_writes"
}

func skillEditMessage(runID string, skills []string) string {
	noun := "skills"
	if len(skills) == 1 {
		noun = "skill"
	}
	return "Updated " + noun + " in run " + jsSlice(runID, 8) + ": " + strings.Join(skills, ", ")
}

// saveSkillEdits saves a run's skill edits. An own skill the run staged
// is saved in place. A team skill is saved in place for an admin or an
// unattended run; a member's edit becomes a private copy and the team
// skill stays as it is. A new skill from a member is their own skill. A
// file another writer changed on the host during the turn stays as it is.
func (a *API) saveSkillEdits(ctx context.Context, orgID, runID string, act runActorRow, baseline skillFileHashes,
	own map[string]bool, changed []stagedSkillFile, removed []string, stagedSkills string) savedSkillEdits {
	orgRoot := orgAgentRoot(orgID)
	teamRoot := filepath.Join(orgRoot, "skills")
	ownRoot := memberSkillsRoot(orgRoot, act.UserID)
	member := act.UserID != "" && act.Role != "admin"
	names := map[string]bool{}
	for _, f := range changed {
		names[skillOf(f.Path)] = true
	}
	for _, p := range removed {
		names[skillOf(p)] = true
	}
	out := savedSkillEdits{Copies: map[string]string{}, Skipped: []string{}}
	var team, mine, copies []string
	for n := range names {
		switch {
		case own[n] && act.UserID != "":
			mine = append(mine, n)
		case !member:
			team = append(team, n)
		case isTeamSkill(teamRoot, baseline, n):
			copies = append(copies, n)
		case validSkillName(n) && !skillNameTaken(orgRoots{Org: orgRoot, Skills: teamRoot}, act.UserID, n):
			mine = append(mine, n)
		default:
			copies = append(copies, n)
		}
	}
	slices.Sort(team)
	slices.Sort(mine)
	slices.Sort(copies)

	save := func(root string, skills []string) {
		current := hashSkillTree(root)
		unchanged := func(p string) bool { return current[p] == baseline[p] }
		for _, f := range changed {
			if !slices.Contains(skills, skillOf(f.Path)) {
				continue
			}
			if !unchanged(f.Path) {
				out.Skipped = append(out.Skipped, f.Path)
				continue
			}
			target := filepath.Join(root, filepath.FromSlash(f.Path))
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				log.Printf("[agent-run] skill save failed for %s: %v", runID, err)
				continue
			}
			if err := os.WriteFile(target, f.Content, 0o644); err != nil {
				log.Printf("[agent-run] skill save failed for %s: %v", runID, err)
			}
		}
		for _, p := range removed {
			if !slices.Contains(skills, skillOf(p)) {
				continue
			}
			if !unchanged(p) {
				out.Skipped = append(out.Skipped, p)
				continue
			}
			if err := os.Remove(filepath.Join(root, filepath.FromSlash(p))); err != nil && !os.IsNotExist(err) {
				log.Printf("[agent-run] skill removal failed for %s: %v", runID, err)
			}
		}
	}

	if len(team) > 0 {
		save(teamRoot, team)
		paths := make([]string, len(team))
		for i, s := range team {
			paths[i] = "skills/" + s
		}
		out.SHA = a.recordConfigPaths(ctx, orgID, paths, skillEditMessage(runID, team), "skill", strings.Join(team, ", "), act.UserID)
		out.Team = team
	}
	if len(mine) > 0 {
		save(ownRoot, mine)
		a.insertConfigChange(ctx, configChange{OrgID: orgID, Kind: "skill", Ref: strings.Join(mine, ", "), Actor: act.UserID,
			Summary: skillEditMessage(runID, mine), Scope: "user", UserID: act.UserID})
		out.Own = mine
	}
	if len(copies) > 0 {
		handle := a.memberHandleFor(ctx, orgID, act.UserID)
		roots := orgRoots{Org: orgRoot, Skills: teamRoot}
		for _, n := range copies {
			name := privateCopyName(n, handle, func(name string) bool { return skillNameTaken(roots, act.UserID, name) })
			if err := copyStagedSkill(stagedSkills, n, ownRoot, name); err != nil {
				log.Printf("[agent-run] private skill copy failed for %s: %v", runID, err)
				continue
			}
			out.Copies[n] = name
			a.insertConfigChange(ctx, configChange{OrgID: orgID, Kind: "skill", Ref: name, Actor: act.UserID,
				Summary: "Saved the changes to skill " + n + " in run " + jsSlice(runID, 8) + " as " + name,
				Scope:   "user", UserID: act.UserID})
		}
	}
	slices.Sort(out.Skipped)
	return out
}

// isTeamSkill reports whether the run received the skill from the team or
// from the image, or the team folder now holds it.
func isTeamSkill(teamRoot string, baseline skillFileHashes, name string) bool {
	for p := range baseline {
		if skillOf(p) == name {
			return true
		}
	}
	if _, err := os.Lstat(filepath.Join(teamRoot, name)); err == nil {
		return true
	}
	if dir := bundledSkillDir(name); dir != "" {
		if _, err := os.Stat(filepath.Join(dir, "SKILL.md")); err == nil {
			return true
		}
	}
	return false
}

func copyStagedSkill(stagedSkills, skill, ownRoot, name string) error {
	src := filepath.Join(stagedSkills, skill)
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("staged skill is not a folder")
	}
	return copySkillTo(src, ownRoot, name)
}

// captureSkillEdits is captureSandboxSkillEdits() for a stage on this host:
// it compares the run's skills folder with the tree the run received and
// saves what changed. It returns nil when nothing changed.
func (a *API) captureSkillEdits(ctx context.Context, orgID, runID string, act runActorRow, baseline skillFileHashes,
	own map[string]bool, stagedSkills string) (*savedSkillEdits, error) {
	changed, removed := diffSkillTrees(baseline, hashSkillTree(stagedSkills))
	if len(changed) == 0 && len(removed) == 0 {
		return nil, nil
	}
	root, err := os.OpenRoot(stagedSkills)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	var files []stagedSkillFile
	for _, p := range changed {
		info, err := root.Lstat(filepath.FromSlash(p))
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		data, err := fs.ReadFile(root.FS(), p)
		if err != nil {
			continue
		}
		files = append(files, stagedSkillFile{Path: p, Content: data})
	}
	saved := a.saveSkillEdits(ctx, orgID, runID, act, baseline, own, files, removed, stagedSkills)
	return &saved, nil
}
