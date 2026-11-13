package webfront

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

const (
	skillScopeTeam = "team"
	skillScopeOwn  = "own"
	maxSkillName   = 64
)

var (
	memberUnsafe       = regexp.MustCompile(`[^a-zA-Z0-9_-]`)
	memberDashes       = regexp.MustCompile(`^-+|-+$`)
	skillFrontmatterNm = regexp.MustCompile(`(?m)^name:[^\r\n]*`)
)

func memberFolderName(userID string) string {
	slug := memberDashes.ReplaceAllString(memberUnsafe.ReplaceAllString(userID, "-"), "")
	if slug != userID {
		sum := sha256.Sum256([]byte(userID))
		slug = slug + "-" + hex.EncodeToString(sum[:])[:8]
	}
	if slug == "" {
		slug = "anon"
	}
	return slug
}

// memberSkillsRoot is the folder of one member's own skills. The config
// repo ignores the members folder, so a member's skills never reach it.
func memberSkillsRoot(orgRoot, userID string) string {
	return filepath.Join(orgRoot, "members", memberFolderName(userID), "skills")
}

type visibleSkill struct {
	Name, Dir, Scope string
}

func hasSkillFiles(dir string) bool {
	st, err := os.Lstat(dir)
	return err == nil && st.IsDir() && len(listSkillFiles(dir)) > 0
}

func ownSkillNames(orgRoot, userID string) []string {
	if userID == "" {
		return nil
	}
	root := memberSkillsRoot(orgRoot, userID)
	entries, _ := os.ReadDir(root)
	var out []string
	for _, e := range entries {
		if e.IsDir() && validSkillName(e.Name()) && hasSkillFiles(filepath.Join(root, e.Name())) {
			out = append(out, e.Name())
		}
	}
	return out
}

// findSkill resolves a name for the actor: the actor's own skill first,
// then a team skill the actor holds.
func findSkill(roots orgRoots, act actor, held heldSet, name string) (visibleSkill, bool) {
	if !validSkillName(name) {
		return visibleSkill{}, false
	}
	if act.UserID != "" {
		dir := filepath.Join(memberSkillsRoot(roots.Org, act.UserID), name)
		if hasSkillFiles(dir) {
			return visibleSkill{name, dir, skillScopeOwn}, true
		}
	}
	dir := filepath.Join(roots.Skills, name)
	if held.has(name) && hasSkillFiles(dir) {
		return visibleSkill{name, dir, skillScopeTeam}, true
	}
	return visibleSkill{}, false
}

func canChangeSkill(act actor, s visibleSkill) bool {
	return s.Scope == skillScopeOwn || act.Admin
}

func skillFlags(act actor, s visibleSkill) skillSummary {
	return skillSummary{Scope: s.Scope, CanEdit: canChangeSkill(act, s), CanShare: s.Scope == skillScopeOwn && act.Admin}
}

// skillTarget reads the {name} segment, decoded a second time as the Node
// route did, and finds the skill the actor sees under that name.
func (a *API) skillTarget(w http.ResponseWriter, r *http.Request, notFound string) (actor, orgRoots, visibleSkill, bool) {
	act, err := a.currentActor(r.Context(), r)
	if err != nil {
		a.fail(w, r, err)
		return act, orgRoots{}, visibleSkill{}, false
	}
	name, err := url.PathUnescape(r.PathValue("name"))
	if err != nil {
		sendError(w, http.StatusNotFound, notFound)
		return act, orgRoots{}, visibleSkill{}, false
	}
	held, err := a.heldItems(r.Context(), act, "skill")
	if err != nil {
		a.fail(w, r, err)
		return act, orgRoots{}, visibleSkill{}, false
	}
	roots, err := orgWorkspace(act.OrgID)
	if err != nil {
		a.fail(w, r, err)
		return act, roots, visibleSkill{}, false
	}
	s, ok := findSkill(roots, act, held, name)
	if !ok {
		sendError(w, http.StatusNotFound, notFound)
		return act, roots, s, false
	}
	return act, roots, s, true
}

const teamSkillChange = "Only an admin can change a team skill. Copy it to make your own."

func validNewSkillName(name string) bool {
	return len(name) <= maxSkillName && archiveSkillName.MatchString(name)
}

func skillNameTaken(roots orgRoots, userID, name string) bool {
	for _, dir := range []string{filepath.Join(roots.Skills, name), filepath.Join(memberSkillsRoot(roots.Org, userID), name)} {
		if _, err := os.Lstat(dir); err == nil {
			return true
		}
	}
	return false
}

// copySkillFolder copies the regular files of a skill. Links, the
// built-in origin mark and cache files stay behind, and SKILL.md takes
// the new name.
func copySkillFolder(src, dst, name string) error {
	root, err := os.OpenRoot(src)
	if err != nil {
		return err
	}
	defer root.Close()
	return fs.WalkDir(root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p != "." && ignoredSkillPart(d.Name()) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		target := filepath.Join(dst, filepath.FromSlash(p))
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return os.MkdirAll(target, 0o755)
		case !d.Type().IsRegular() || p == skillOriginFile:
			return nil
		}
		data, err := fs.ReadFile(root.FS(), p)
		if err != nil {
			return err
		}
		if p == "SKILL.md" {
			data = renameSkillMarkdown(data, name)
		}
		return os.WriteFile(target, data, info.Mode().Perm()&0o755|0o644)
	})
}

func renameSkillMarkdown(data []byte, name string) []byte {
	loc := skillFrontmatter.FindSubmatchIndex(data)
	if loc == nil {
		return data
	}
	front := data[loc[2]:loc[3]]
	renamed := skillFrontmatterNm.ReplaceAllLiteral(front, []byte("name: "+name))
	return slices.Concat(data[:loc[2]], renamed, data[loc[3]:])
}

// copySkillTo writes a copy of the skill into the member's own folder
// through a temporary folder, so a failed copy leaves nothing behind.
func copySkillTo(src, ownRoot, name string) error {
	if err := os.MkdirAll(ownRoot, 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(ownRoot, ".copy-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if err := copySkillFolder(src, tmp, name); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(ownRoot, name)); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return errSkillExists
		}
		return err
	}
	return nil
}

var errSkillExists = errors.New("skill exists")

func skillExistsMessage(name string) string {
	return "A skill named " + name + " already exists. Choose another name."
}

func (a *API) copySkill(w http.ResponseWriter, r *http.Request) {
	act, roots, s, ok := a.skillTarget(w, r, "Skill not found")
	if !ok {
		return
	}
	if act.UserID == "" {
		sendError(w, http.StatusForbidden, "Sign in as a person to copy a skill.")
		return
	}
	body, _ := readBody(r)
	name, _ := fieldString(body, "name")
	name = strings.TrimSpace(name)
	if !validNewSkillName(name) {
		sendError(w, http.StatusBadRequest, "Use lowercase letters, digits and single hyphens for the name, up to 64 characters.")
		return
	}
	if skillNameTaken(roots, act.UserID, name) {
		sendError(w, http.StatusConflict, skillExistsMessage(name))
		return
	}
	err := copySkillTo(s.Dir, memberSkillsRoot(roots.Org, act.UserID), name)
	if errors.Is(err, errSkillExists) {
		sendError(w, http.StatusConflict, skillExistsMessage(name))
		return
	}
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.recordOwnSkillChange(r.Context(), act, name, "Copied skill "+s.Name+" to "+name)
	sendJSON(w, http.StatusCreated, map[string]string{"name": name, "scope": skillScopeOwn})
}

func (a *API) shareSkill(w http.ResponseWriter, r *http.Request) {
	act, roots, s, ok := a.skillTarget(w, r, "Skill not found")
	if !ok {
		return
	}
	if !act.Admin {
		sendError(w, http.StatusForbidden, "Only an admin can share a skill with the team.")
		return
	}
	if s.Scope != skillScopeOwn {
		sendError(w, http.StatusBadRequest, "This skill is already a team skill.")
		return
	}
	target := filepath.Join(roots.Skills, s.Name)
	if _, err := os.Lstat(target); err == nil {
		sendError(w, http.StatusConflict, "The team already has a skill named "+s.Name+". Copy your skill to a new name, then share the copy.")
		return
	}
	if err := os.Rename(s.Dir, target); err != nil {
		a.fail(w, r, err)
		return
	}
	a.recordConfigChange(r.Context(), act.OrgID, "skills/"+s.Name, "Shared skill: "+s.Name, "skill", s.Name, act.UserID)
	sendJSON(w, http.StatusOK, map[string]string{"name": s.Name, "scope": skillScopeTeam})
}

func (a *API) recordOwnSkillChange(ctx context.Context, act actor, name, summary string) {
	a.insertConfigChange(ctx, configChange{OrgID: act.OrgID, Kind: "skill", Ref: name, Actor: act.UserID,
		Summary: summary, Scope: "user", UserID: act.UserID})
}

// recordSkillChange adds the audit row for a change to a skill: a team
// skill is also committed to the config repo, a member's own is not.
func (a *API) recordSkillChange(ctx context.Context, act actor, s visibleSkill, summary string) {
	if s.Scope == skillScopeOwn {
		a.recordOwnSkillChange(ctx, act, s.Name, summary)
		return
	}
	a.recordConfigChange(ctx, act.OrgID, "skills/"+s.Name, summary, "skill", s.Name, act.UserID)
}
