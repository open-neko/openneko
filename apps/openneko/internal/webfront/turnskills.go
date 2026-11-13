package webfront

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

type savedSkillEdits struct {
	Scope   string
	SHA     string
	Skills  []string
	Skipped []string
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

// readPersonalSkillFiles is readPersonalSkillFiles(): the member's own skill
// files from their config ref, laid over the company skills for their runs.
func readPersonalSkillFiles(orgRoot, userID string) []stagedSkillFile {
	if _, err := os.Stat(filepath.Join(orgRoot, ".git")); err != nil {
		return nil
	}
	ref := userConfigRef(userID)
	configRepoMu.Lock()
	defer configRepoMu.Unlock()
	listed, err := git(orgRoot, "ls-tree", "-r", "--name-only", ref, "--", "skills")
	if err != nil {
		return nil
	}
	var files []stagedSkillFile
	for _, p := range strings.Split(listed, "\n") {
		rel, ok := strings.CutPrefix(p, "skills/")
		if !ok || !safeSkillPath(rel) {
			continue
		}
		content, err := git(orgRoot, "show", ref+":"+p)
		if err != nil {
			return nil
		}
		files = append(files, stagedSkillFile{Path: rel, Content: []byte(content)})
	}
	return files
}

func skillEditMessage(runID string, skills []string) string {
	noun := "skills"
	if len(skills) == 1 {
		noun = "skill"
	}
	return "Updated " + noun + " in run " + jsSlice(runID, 8) + ": " + strings.Join(skills, ", ")
}

// saveSkillEdits is saveSkillEdits(): on the company skills for an admin or
// an unattended run, else on the member's own ref. A file another writer
// changed on the host during the turn stays as it is.
func (a *API) saveSkillEdits(ctx context.Context, orgID, runID string, act runActorRow, baseline skillFileHashes,
	changed []stagedSkillFile, removed []string) savedSkillEdits {
	orgRoot := orgAgentRoot(orgID)
	names := map[string]bool{}
	for _, f := range changed {
		names[skillOf(f.Path)] = true
	}
	for _, p := range removed {
		names[skillOf(p)] = true
	}
	skills := make([]string, 0, len(names))
	for n := range names {
		skills = append(skills, n)
	}
	slices.Sort(skills)
	message := skillEditMessage(runID, skills)
	ref := strings.Join(skills, ", ")

	if act.UserID != "" && act.Role != "admin" {
		files := make([]configFile, len(changed))
		for i, f := range changed {
			files[i] = configFile{path: "skills/" + f.Path, content: string(f.Content)}
		}
		configRepoMu.Lock()
		sha, err := commitUserRef(orgRoot, act.UserID, files, message, true)
		configRepoMu.Unlock()
		if err != nil {
			log.Printf("[agent-run] personal skill save failed for %s: %v", runID, err)
			sha = ""
		}
		if sha != "" {
			a.insertConfigChange(ctx, configChange{OrgID: orgID, Kind: "skill", Ref: ref, Actor: act.UserID, SHA: sha,
				Summary: message, Scope: "user", UserID: act.UserID})
		}
		return savedSkillEdits{Scope: "user", SHA: sha, Skills: skills, Skipped: []string{}}
	}

	skillsRoot := filepath.Join(orgRoot, "skills")
	current := hashSkillTree(skillsRoot)
	unchanged := func(p string) bool { return current[p] == baseline[p] }
	skipped := []string{}
	for _, f := range changed {
		if !unchanged(f.Path) {
			skipped = append(skipped, f.Path)
			continue
		}
		target := filepath.Join(skillsRoot, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			log.Printf("[agent-run] skill save failed for %s: %v", runID, err)
			continue
		}
		if err := os.WriteFile(target, f.Content, 0o644); err != nil {
			log.Printf("[agent-run] skill save failed for %s: %v", runID, err)
		}
	}
	for _, p := range removed {
		if !unchanged(p) {
			skipped = append(skipped, p)
			continue
		}
		if err := os.Remove(filepath.Join(skillsRoot, filepath.FromSlash(p))); err != nil && !os.IsNotExist(err) {
			log.Printf("[agent-run] skill removal failed for %s: %v", runID, err)
		}
	}
	paths := make([]string, len(skills))
	for i, s := range skills {
		paths[i] = "skills/" + s
	}
	slices.Sort(skipped)
	sha := a.recordConfigPaths(ctx, orgID, paths, message, "skill", ref, act.UserID)
	return savedSkillEdits{Scope: "team", SHA: sha, Skills: skills, Skipped: skipped}
}

// captureSkillEdits is captureSandboxSkillEdits() for a stage on this host:
// it compares the run's skills folder with the tree the run received and
// saves what changed. It returns nil when nothing changed.
func (a *API) captureSkillEdits(ctx context.Context, orgID, runID string, act runActorRow, baseline skillFileHashes, stagedSkills string) (*savedSkillEdits, error) {
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
	saved := a.saveSkillEdits(ctx, orgID, runID, act, baseline, files, removed)
	return &saved, nil
}
