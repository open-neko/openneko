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
	"strconv"
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
// built-in origin mark and cache files stay behind. With a name, SKILL.md
// takes it.
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
		if p == "SKILL.md" && name != "" {
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
	a.recordConfigChange(r.Context(), act.OrgID, "Shared skill: "+s.Name, "skill", s.Name, act.UserID)
	sendJSON(w, http.StatusOK, map[string]string{"name": s.Name, "scope": skillScopeTeam})
}

func (a *API) recordOwnSkillChange(ctx context.Context, act actor, name, summary string) {
	a.insertConfigChange(ctx, configChange{OrgID: act.OrgID, Kind: "skill", Ref: name, Actor: act.UserID,
		Summary: summary, Scope: "user", UserID: act.UserID})
}

// recordSkillChange adds the audit row for a change to a skill, with the
// user scope for a member's own skill.
func (a *API) recordSkillChange(ctx context.Context, act actor, s visibleSkill, summary string) {
	if s.Scope == skillScopeOwn {
		a.recordOwnSkillChange(ctx, act, s.Name, summary)
		return
	}
	a.recordConfigChange(ctx, act.OrgID, summary, "skill", s.Name, act.UserID)
}

// ownRunSkills are the skills in the member's own folder that a run can
// load, with the description from SKILL.md when it has one.
func ownRunSkills(orgRoot, userID string) []installedSkill {
	root := memberSkillsRoot(orgRoot, userID)
	described := map[string]string{}
	for _, s := range listInstalledSkills(root) {
		described[s.Name] = s.Description
	}
	var out []installedSkill
	for _, name := range ownSkillNames(orgRoot, userID) {
		out = append(out, installedSkill{Name: name, Description: described[name]})
	}
	return out
}

// runSkillsWithOwn adds the member's own skills to a run's skills and
// scope. An own skill takes the place of a team skill of the same name.
// A nil scope stays nil: the run may load every skill.
func runSkillsWithOwn(team []installedSkill, allowed []string, own []installedSkill, admin, terminal bool) ([]installedSkill, []string, []string) {
	var ownNames []string
	for _, s := range own {
		if !admin && s.Name == "graphjin-config" || sandboxSkills[s.Name] && !terminal {
			continue
		}
		ownNames = append(ownNames, s.Name)
	}
	var skills []installedSkill
	for _, s := range team {
		if !slices.Contains(ownNames, s.Name) {
			skills = append(skills, s)
		}
	}
	for _, s := range own {
		if slices.Contains(ownNames, s.Name) {
			skills = append(skills, s)
		}
	}
	slices.SortFunc(skills, func(a, b installedSkill) int { return strings.Compare(a.Name, b.Name) })
	if allowed == nil {
		return skills, nil, ownNames
	}
	scope := slices.Clone(allowed)
	for _, name := range ownNames {
		if !slices.Contains(scope, name) {
			scope = append(scope, name)
		}
	}
	return skills, scope, ownNames
}

var (
	handleUnsafe = regexp.MustCompile(`[^a-z0-9]+`)
	handleEdges  = regexp.MustCompile(`^-+|-+$`)
)

// memberHandle is the part of a private copy's name that names the
// member: the local part of the email, else the start of the user id.
func memberHandle(email, userID string) string {
	local, _, _ := strings.Cut(strings.ToLower(email), "@")
	handle := handleEdges.ReplaceAllString(handleUnsafe.ReplaceAllString(local, "-"), "")
	if handle == "" {
		handle = handleEdges.ReplaceAllString(handleUnsafe.ReplaceAllString(strings.ToLower(userID), "-"), "")
	}
	if len(handle) > 16 {
		handle = strings.TrimRight(handle[:16], "-")
	}
	if handle == "" {
		handle = "member"
	}
	return handle
}

// privateCopyName is the name of a member's copy of a team skill that a
// run changed: <skill>-<handle>, then -2, -3 and on while the name is in
// use.
func privateCopyName(skill, handle string, taken func(string) bool) string {
	base := handleEdges.ReplaceAllString(handleUnsafe.ReplaceAllString(strings.ToLower(skill), "-"), "")
	for n := 1; ; n++ {
		tail := "-" + handle
		if n > 1 {
			tail += "-" + strconv.Itoa(n)
		}
		head := base
		if len(head)+len(tail) > maxSkillName {
			head = strings.TrimRight(head[:maxSkillName-len(tail)], "-")
		}
		name := strings.TrimLeft(head+tail, "-")
		if !taken(name) {
			return name
		}
	}
}

func (a *API) memberHandleFor(ctx context.Context, orgID, userID string) string {
	var email *string
	if a.DB != nil {
		_ = a.DB.QueryRow(ctx, `select email from app_user where org_id = $1 and id = $2`, orgID, userID).Scan(&email)
	}
	return memberHandle(deref(email), userID)
}

var ownSkillStopWords = map[string]bool{"the": true, "and": true, "for": true, "you": true, "with": true, "from": true, "that": true, "this": true}

// matchOwnSkills finds the member's own skills for a skill search: the
// share of the query's words that the name or description holds. The
// org skill index does not hold own skills.
func matchOwnSkills(own []installedSkill, query string) []installedSkill {
	var words []string
	for _, w := range strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	}) {
		if len(w) >= 3 && !ownSkillStopWords[w] && !slices.Contains(words, w) {
			words = append(words, w)
		}
	}
	type scored struct {
		skill installedSkill
		hits  int
	}
	var found []scored
	for _, s := range own {
		text := strings.ToLower(s.Name + " " + s.Description)
		hits := 0
		for _, w := range words {
			if strings.Contains(text, w) {
				hits++
			}
		}
		if hits > 0 {
			found = append(found, scored{s, hits})
		}
	}
	slices.SortStableFunc(found, func(a, b scored) int { return b.hits - a.hits })
	out := make([]installedSkill, len(found))
	for i, f := range found {
		out[i] = f.skill
	}
	return out
}
