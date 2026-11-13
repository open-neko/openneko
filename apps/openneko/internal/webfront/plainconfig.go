package webfront

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

const (
	teamBranch     = "refs/heads/main"
	memberRefSpace = "refs/heads/user/"
	preplainSuffix = ".pre-plain"
)

type PlainAction struct {
	Kind string `json:"kind"`
	Path string `json:"path"`
	Note string `json:"note,omitempty"`
}

type PlainResult struct {
	OrgID   string        `json:"orgId"`
	Repo    string        `json:"repo"`
	Moved   string        `json:"moved,omitempty"`
	Actions []PlainAction `json:"actions"`
}

type treeEntry struct{ mode, sha string }

type gitRepo struct{ dir string }

func (g gitRepo) run(args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"-c", "safe.directory=*", "--git-dir", g.dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_TERMINAL_PROMPT=0")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func (g gitRepo) resolve(ref string) string {
	out, err := g.run("rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func (g gitRepo) tree(commit string) (map[string]treeEntry, error) {
	out, err := g.run("ls-tree", "-r", "-z", "--full-tree", commit)
	if err != nil {
		return nil, err
	}
	tree := map[string]treeEntry{}
	for _, rec := range bytes.Split(out, []byte{0}) {
		meta, p, ok := bytes.Cut(rec, []byte{'\t'})
		if !ok {
			continue
		}
		f := strings.Fields(string(meta))
		if len(f) != 3 || f[1] != "blob" || (f[0] != "100644" && f[0] != "100755") || !filepath.IsLocal(string(p)) {
			continue
		}
		tree[string(p)] = treeEntry{mode: f[0], sha: f[2]}
	}
	return tree, nil
}

func (g gitRepo) blob(sha string) ([]byte, error) { return g.run("cat-file", "blob", sha) }

func (g gitRepo) memberRefs() ([]string, error) {
	out, err := g.run("for-each-ref", "--format=%(refname)", memberRefSpace)
	if err != nil {
		return nil, err
	}
	return strings.Fields(string(out)), nil
}

func blobID(data []byte, long bool) string {
	head := "blob " + strconv.Itoa(len(data)) + "\x00"
	if long {
		sum := sha256.Sum256(append([]byte(head), data...))
		return hex.EncodeToString(sum[:])
	}
	sum := sha1.Sum(append([]byte(head), data...))
	return hex.EncodeToString(sum[:])
}

func entryMode(e treeEntry) os.FileMode {
	if e.mode == "100755" {
		return 0o755
	}
	return 0o644
}

type plainRun struct {
	api    *API
	ctx    context.Context
	root   string
	repo   gitRepo
	long   bool
	dryRun bool
	team   map[string]treeEntry
	staged map[string]treeEntry
	res    *PlainResult
}

func (p *plainRun) note(kind, rel, note string) {
	p.res.Actions = append(p.res.Actions, PlainAction{Kind: kind, Path: filepath.ToSlash(rel), Note: note})
}

func (p *plainRun) diskID(abs string) (string, bool) {
	st, err := os.Lstat(abs)
	if err != nil {
		return "", false
	}
	if !st.Mode().IsRegular() {
		return "-", true
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return "-", true
	}
	return blobID(data, p.long), true
}

func (p *plainRun) write(rel string, e treeEntry) error {
	if p.dryRun {
		return nil
	}
	data, err := p.repo.blob(e.sha)
	if err != nil {
		return err
	}
	abs := filepath.Join(p.root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	return atomicWrite(abs, data, entryMode(e))
}

func repoSkillPath(rel string) (string, string, bool) {
	rest, ok := strings.CutPrefix(rel, "skills/")
	if !ok {
		return "", "", false
	}
	name, file, ok := strings.Cut(rest, "/")
	if !ok || name == "" || file == "" {
		return "", "", false
	}
	for _, part := range strings.Split(file, "/") {
		if ignoredSkillPart(part) {
			return "", "", false
		}
	}
	return name, file, file != skillOriginFile
}

// teamFiles writes each team file of the head that the folder lacks. A file
// on disk that differs is newer than the head: the old code wrote the file
// first and committed after, and a failed commit left the file as it was.
func (p *plainRun) teamFiles() error {
	p.staged = map[string]treeEntry{}
	for _, rel := range slices.Sorted(maps.Keys(p.team)) {
		e := p.team[rel]
		if !strings.HasPrefix(rel, "skills/") && !strings.HasPrefix(rel, "skill-overlays/") {
			continue
		}
		if name, _, ok := repoSkillPath(rel); ok && slices.Contains(retiredBuiltinSkill, name) {
			continue
		}
		id, ok := p.diskID(filepath.Join(p.root, filepath.FromSlash(rel)))
		switch {
		case !ok:
			if err := p.write(rel, e); err != nil {
				return err
			}
			p.staged[rel] = e
			p.note("write", rel, "team file from the repository")
		case id != e.sha:
			p.note("keep", rel, "the folder copy is newer than the repository")
		}
	}
	return nil
}

func (p *plainRun) skillIDs(dir, stagedPrefix string) map[string]string {
	ids := map[string]string{}
	if _, err := os.Lstat(dir); err == nil {
		for _, f := range listSkillFiles(dir) {
			if !validStagedFile(f.Path) {
				continue
			}
			if id, ok := p.diskID(filepath.Join(dir, filepath.FromSlash(f.Path))); ok {
				ids[f.Path] = id
			}
		}
	}
	if stagedPrefix != "" {
		for rel, e := range p.staged {
			if file, ok := strings.CutPrefix(rel, stagedPrefix); ok {
				ids[file] = e.sha
			}
		}
	}
	return ids
}

func validStagedFile(file string) bool {
	if file == skillOriginFile {
		return false
	}
	for _, part := range strings.Split(file, "/") {
		if ignoredSkillPart(part) {
			return false
		}
	}
	return true
}

func (p *plainRun) teamSkillExists(name string) bool {
	if _, err := os.Lstat(filepath.Join(p.root, "skills", name)); err == nil {
		return true
	}
	for rel := range p.staged {
		if strings.HasPrefix(rel, "skills/"+name+"/") {
			return true
		}
	}
	return slices.Contains(bundledSkillNames(), name)
}

type memberSkill struct {
	files map[string]treeEntry
	ids   map[string]string
}

func (p *plainRun) renamedIDs(s memberSkill, name string) (map[string]string, error) {
	ids := maps.Clone(s.ids)
	if e, ok := s.files["SKILL.md"]; ok {
		data, err := p.repo.blob(e.sha)
		if err != nil {
			return nil, err
		}
		ids["SKILL.md"] = blobID(renameSkillMarkdown(data, name), p.long)
	}
	return ids, nil
}

// writeSkill takes the run bit of a script from the team skill: a member
// ref stored each file with mode 644.
func (p *plainRun) writeSkill(ownRoot, name, teamDir string, s memberSkill) error {
	if p.dryRun {
		return nil
	}
	if err := os.MkdirAll(ownRoot, 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(ownRoot, ".copy-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	for file, e := range s.files {
		data, err := p.repo.blob(e.sha)
		if err != nil {
			return err
		}
		if file == "SKILL.md" {
			data = renameSkillMarkdown(data, name)
		}
		abs := filepath.Join(tmp, filepath.FromSlash(file))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return err
		}
		mode := entryMode(e)
		if st, err := os.Lstat(filepath.Join(teamDir, filepath.FromSlash(file))); err == nil && st.Mode().IsRegular() && st.Mode().Perm()&0o100 != 0 {
			mode = 0o755
		}
		if err := os.WriteFile(abs, data, mode); err != nil {
			return err
		}
	}
	return os.Rename(tmp, filepath.Join(ownRoot, name))
}

// member finds a copy that a cut-off earlier run wrote by its content, so
// a second run writes no second copy.
func (p *plainRun) member(ref string) (int, int, error) {
	slug := strings.TrimPrefix(ref, memberRefSpace)
	if !validSkillName(slug) {
		p.note("skip", ref, "the member ref name is not a folder name")
		return 0, 0, nil
	}
	commit := p.repo.resolve(ref)
	if commit == "" {
		return 0, 0, nil
	}
	tree, err := p.repo.tree(commit)
	if err != nil {
		return 0, 0, err
	}
	userID := p.memberUser(slug)
	handle := p.api.memberHandleFor(p.ctx, p.res.OrgID, userID)
	memberRoot := filepath.Join(p.root, "members", slug)
	ownRoot := filepath.Join(memberRoot, "skills")
	skills := map[string]memberSkill{}
	files := 0
	for _, rel := range slices.Sorted(maps.Keys(tree)) {
		e := tree[rel]
		if name, file, ok := repoSkillPath(rel); ok {
			s, seen := skills[name]
			if !seen {
				s = memberSkill{files: map[string]treeEntry{}, ids: map[string]string{}}
				skills[name] = s
			}
			s.files[file] = e
			s.ids[file] = e.sha
			continue
		}
		if strings.HasPrefix(rel, "skills/") {
			continue
		}
		if t, ok := p.team[rel]; ok && t.sha == e.sha {
			continue
		}
		dst := filepath.Join("members", slug, filepath.FromSlash(rel))
		id, exists := p.diskID(filepath.Join(p.root, dst))
		switch {
		case !exists:
			if err := p.write(filepath.ToSlash(dst), e); err != nil {
				return 0, 0, err
			}
			p.note("member-file", dst, "")
			files++
		case id != e.sha:
			p.note("keep", dst, "a different file is already there")
		}
	}
	copies := 0
	for _, name := range slices.Sorted(maps.Keys(skills)) {
		s := skills[name]
		if !validSkillName(name) {
			p.note("skip", "skills/"+name, "not a valid skill name")
			continue
		}
		team := p.teamSkillExists(name)
		if team && maps.Equal(s.ids, p.skillIDs(filepath.Join(p.root, "skills", name), "skills/"+name+"/")) {
			continue
		}
		var done, target string
		var check error
		matches := func(n string) bool {
			ids, err := p.renamedIDs(s, n)
			if err != nil {
				check = err
				return false
			}
			return maps.Equal(ids, p.skillIDs(filepath.Join(ownRoot, n), ""))
		}
		ownTaken := func(n string) bool {
			_, err := os.Lstat(filepath.Join(ownRoot, n))
			return err == nil
		}
		if !team && !ownTaken(name) {
			target = name
		} else if !team && matches(name) {
			done = name
		} else {
			target = privateCopyName(name, handle, func(n string) bool {
				if done != "" {
					return false
				}
				if ownTaken(n) && matches(n) {
					done = n
					return false
				}
				return ownTaken(n) || p.teamSkillExists(n)
			})
		}
		if check != nil {
			return 0, 0, check
		}
		rel := filepath.Join("members", slug, "skills", cmp.Or(done, target))
		if done != "" {
			p.note("keep", rel, "this copy is already there")
			continue
		}
		if err := p.writeSkill(ownRoot, target, filepath.Join(p.root, "skills", name), s); err != nil {
			return 0, 0, err
		}
		note := "a skill the team does not have"
		if team {
			note = "the member's changed copy of team skill " + name
		}
		p.note("private-skill", rel, note)
		copies++
	}
	return copies, files, nil
}

func (p *plainRun) memberUser(slug string) string {
	if p.api == nil || p.api.DB == nil {
		return slug
	}
	rows, err := p.api.DB.Query(p.ctx, `select id::text from app_user where org_id = $1`, p.res.OrgID)
	if err != nil {
		return slug
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil && memberFolderName(id) == slug {
			return id
		}
	}
	return slug
}

func freeName(base string) string {
	name := base
	for n := 2; ; n++ {
		if _, err := os.Lstat(name); errors.Is(err, fs.ErrNotExist) {
			return name
		}
		name = base + "-" + strconv.Itoa(n)
	}
}

// ConvertConfigRepo moves one org folder off its config repository. With
// dryRun it writes nothing and lists what it would write.
func (a *API) ConvertConfigRepo(ctx context.Context, orgID, root string, dryRun bool) (*PlainResult, error) {
	gitDir := filepath.Join(root, ".git")
	if st, err := os.Lstat(gitDir); err != nil || !st.IsDir() {
		return nil, nil
	}
	if _, err := exec.LookPath("git"); err != nil {
		return nil, fmt.Errorf("%s holds a config repository and git is not installed: install git, then run openneko migrate config-repo", root)
	}
	p := &plainRun{api: a, ctx: ctx, root: root, repo: gitRepo{dir: gitDir}, dryRun: dryRun,
		res: &PlainResult{OrgID: orgID, Repo: gitDir, Actions: []PlainAction{}}}
	head := p.repo.resolve(teamBranch)
	if head == "" {
		head = p.repo.resolve("HEAD")
	}
	p.long = len(head) == 64
	p.team = map[string]treeEntry{}
	if head != "" {
		tree, err := p.repo.tree(head)
		if err != nil {
			return nil, err
		}
		p.team = tree
	}
	if err := p.teamFiles(); err != nil {
		return nil, err
	}
	refs, err := p.repo.memberRefs()
	if err != nil {
		return nil, err
	}
	copies, files := 0, 0
	for _, ref := range refs {
		c, f, err := p.member(ref)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", ref, err)
		}
		copies += c
		files += f
	}
	moved := freeName(gitDir + preplainSuffix)
	p.res.Moved = moved
	p.note("rename", ".git", "kept as "+filepath.Base(moved))
	if dryRun {
		return p.res, nil
	}
	if err := os.Rename(gitDir, moved); err != nil {
		return nil, err
	}
	if a.DB != nil && orgID != "" {
		var known bool
		_ = a.DB.QueryRow(ctx, `select exists(select 1 from organization where id::text = $1)`, orgID).Scan(&known)
		if known {
			a.insertConfigChange(ctx, configChange{OrgID: orgID, Kind: "config", Ref: ".git",
				Summary: fmt.Sprintf("Moved the config repository to plain folders: %d private skills and %d member files. The old repository is at %s.", copies, files, moved)})
		}
	}
	return p.res, nil
}

func (a *API) ConvertConfigRepos(ctx context.Context, dryRun bool) ([]PlainResult, error) {
	orgs := filepath.Dir(orgAgentRoot("org"))
	entries, err := os.ReadDir(orgs)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []PlainResult
	var errs []error
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		res, err := a.ConvertConfigRepo(ctx, e.Name(), filepath.Join(orgs, e.Name()), dryRun)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if res != nil {
			out = append(out, *res)
		}
	}
	return out, errors.Join(errs...)
}
