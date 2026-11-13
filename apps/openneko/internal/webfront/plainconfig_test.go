package webfront

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func gitT(t *testing.T, dir string, env []string, stdin string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "init.defaultBranch=main"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func memberRef(t *testing.T, root, slug string, files map[string]string) {
	t.Helper()
	env := []string{"GIT_INDEX_FILE=" + filepath.Join(t.TempDir(), "index")}
	gitT(t, root, env, "", "read-tree", "--empty")
	for p, body := range files {
		blob := gitT(t, root, env, body, "hash-object", "-w", "--stdin")
		gitT(t, root, env, "", "update-index", "--add", "--cacheinfo", "100644,"+blob+","+p)
	}
	tree := gitT(t, root, env, "", "write-tree")
	commit := gitT(t, root, env, "", "commit-tree", tree, "-m", "snapshot")
	gitT(t, root, nil, "", "update-ref", "refs/heads/user/"+slug, commit)
}

func skillMD(name, body string) string {
	return "---\nname: " + name + "\ndescription: D.\n---\n" + body + "\n"
}

func repoFixture(t *testing.T, root string, member string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	writeTree(t, root, map[string]string{
		".gitignore":               "*\n!.gitignore\n!skills/\n!skills/**\n!memory/\n!memory/**\n",
		"skills/report/SKILL.md":   skillMD("report", "Team"),
		"skills/report/run.sh":     "echo team\n",
		"skills/finance/SKILL.md":  skillMD("finance", "Team"),
		"skills/clash/SKILL.md":    skillMD("clash", "Team"),
		"skills/missing/SKILL.md":  skillMD("missing", "Team"),
		"skills/missing/run.sh":    "echo hi\n",
		"memory/team.md":           "team note",
		"skills/finance/notes.txt": "n",
	})
	for _, script := range []string{"skills/missing/run.sh", "skills/report/run.sh"} {
		if err := os.Chmod(filepath.Join(root, script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	gitT(t, root, nil, "", "init")
	gitT(t, root, nil, "", "add", "-A")
	gitT(t, root, nil, "", "commit", "-m", "team")
	if err := os.RemoveAll(filepath.Join(root, "skills/missing")); err != nil {
		t.Fatal(err)
	}
	writeTree(t, root, map[string]string{"skills/finance/SKILL.md": skillMD("finance", "Newer on disk")})
	memberRef(t, root, member, map[string]string{
		"skills/report/SKILL.md":   skillMD("report", "Mine"),
		"skills/report/run.sh":     "echo team\n",
		"skills/finance/SKILL.md":  skillMD("finance", "Newer on disk"),
		"skills/finance/notes.txt": "n",
		"skills/clash/SKILL.md":    skillMD("clash", "Mine"),
		"skills/solo/SKILL.md":     skillMD("solo", "Only mine"),
		"memory/note.md":           "my note",
		"memory/team.md":           "team note",
	})
	memberRef(t, root, "bob", map[string]string{"skills/report/SKILL.md": skillMD("report", "Bob's")})
	writeTree(t, filepath.Join(root, "members", member, "skills"), map[string]string{"clash-" + member + "/SKILL.md": skillMD("clash-"+member, "Other")})
}

func readFileT(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func plainTreeFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && strings.HasPrefix(d.Name(), ".git") {
			return filepath.SkipDir
		}
		if !d.IsDir() {
			rel, _ := filepath.Rel(root, p)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	slices.Sort(out)
	return out
}

func TestConvertConfigRepo(t *testing.T) {
	t.Setenv("OPENNEKO_BUILTIN_SKILLS_ROOT", t.TempDir())
	root := t.TempDir()
	repoFixture(t, root, "ann")
	before := plainTreeFiles(t, root)
	api := &API{}
	ctx := context.Background()

	dry, err := api.ConvertConfigRepo(ctx, "org", root, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := plainTreeFiles(t, root); !slices.Equal(got, before) {
		t.Fatalf("the dry run wrote files: %v", got)
	}
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		t.Fatal("the dry run moved the repository")
	}
	listed := map[string]string{}
	for _, a := range dry.Actions {
		listed[a.Path] = a.Kind
	}
	want := map[string]string{
		"skills/missing/SKILL.md":        "write",
		"skills/missing/run.sh":          "write",
		"skills/finance/SKILL.md":        "keep",
		"members/ann/skills/report-ann":  "private-skill",
		"members/ann/skills/clash-ann-2": "private-skill",
		"members/ann/skills/solo":        "private-skill",
		"members/ann/memory/note.md":     "member-file",
		"members/bob/skills/report-bob":  "private-skill",
		".git":                           "rename",
	}
	for p, kind := range want {
		if listed[p] != kind {
			t.Errorf("dry run %s: %q, want %q (all: %v)", p, listed[p], kind, dry.Actions)
		}
	}
	if len(listed) != len(want) {
		t.Errorf("dry run lists %d actions, want %d: %v", len(listed), len(want), dry.Actions)
	}

	res, err := api.ConvertConfigRepo(ctx, "org", root, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Moved != filepath.Join(root, ".git.pre-plain") {
		t.Errorf("moved to %s", res.Moved)
	}
	if _, err := os.Stat(filepath.Join(root, ".git")); err == nil {
		t.Error(".git is still there")
	}
	if st, err := os.Stat(filepath.Join(root, "skills/missing/run.sh")); err != nil || st.Mode().Perm() != 0o755 {
		t.Errorf("run.sh: %v %v", st, err)
	}
	if got := readFileT(t, filepath.Join(root, "skills/finance/SKILL.md")); got != skillMD("finance", "Newer on disk") {
		t.Errorf("finance on disk: %q", got)
	}
	own := filepath.Join(root, "members", "ann", "skills")
	for name, body := range map[string]string{"report-ann": "Mine", "clash-ann-2": "Mine", "solo": "Only mine", "clash-ann": "Other"} {
		if got := readFileT(t, filepath.Join(own, name, "SKILL.md")); got != skillMD(name, body) {
			t.Errorf("%s: %q", name, got)
		}
	}
	if st, err := os.Stat(filepath.Join(own, "report-ann", "run.sh")); err != nil || st.Mode().Perm() != 0o755 {
		t.Errorf("report-ann run.sh: %v %v", st, err)
	}
	if got := readFileT(t, filepath.Join(root, "members/bob/skills/report-bob/SKILL.md")); got != skillMD("report-bob", "Bob's") {
		t.Errorf("report-bob: %q", got)
	}
	if got := readFileT(t, filepath.Join(root, "members/ann/memory/note.md")); got != "my note" {
		t.Errorf("note: %q", got)
	}
	for _, absent := range []string{"members/ann/skills/finance", "members/ann/skills/finance-ann", "members/ann/memory/team.md"} {
		if _, err := os.Lstat(filepath.Join(root, absent)); err == nil {
			t.Errorf("%s was written", absent)
		}
	}
	if got := readFileT(t, filepath.Join(root, "skills/report/SKILL.md")); got != skillMD("report", "Team") {
		t.Errorf("team report changed: %q", got)
	}
	after := plainTreeFiles(t, root)

	if again, err := api.ConvertConfigRepo(ctx, "org", root, false); err != nil || again != nil {
		t.Fatalf("second run: %v %v", again, err)
	}

	if err := os.Rename(res.Moved, filepath.Join(root, ".git")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(res.Moved, 0o755); err != nil {
		t.Fatal(err)
	}
	cut, err := api.ConvertConfigRepo(ctx, "org", root, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := plainTreeFiles(t, root); !slices.Equal(got, after) {
		t.Errorf("a run after a cut-off run wrote more files:\n%v\n%v", got, after)
	}
	for _, a := range cut.Actions {
		if a.Kind == "private-skill" || a.Kind == "write" || a.Kind == "member-file" {
			t.Errorf("a run after a cut-off run wrote %v", a)
		}
	}
	if cut.Moved != filepath.Join(root, ".git.pre-plain-2") {
		t.Errorf("second rename: %s", cut.Moved)
	}
}

func TestConvertConfigRepoNoGit(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	_, err := (&API{}).ConvertConfigRepo(context.Background(), "org", root, false)
	if err == nil || !strings.Contains(err.Error(), "git is not installed") {
		t.Fatalf("error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		t.Error("the repository moved")
	}
}

func TestConvertConfigReposDatabase(t *testing.T) {
	f, db, roots := skillFront(t)
	_ = f
	repoFixture(t, roots.Org, "member")
	api := &API{DB: db}
	ctx := context.Background()
	results, err := api.ConvertConfigRepos(ctx, false)
	if err != nil || len(results) != 1 || results[0].OrgID != "org" {
		t.Fatalf("results: %v %v", results, err)
	}
	if got := readFileT(t, filepath.Join(memberSkillsRoot(roots.Org, "member"), "report-m", "SKILL.md")); got != skillMD("report-m", "Mine") {
		t.Errorf("report-m: %q", got)
	}
	var summary string
	var n int
	if err := db.QueryRow(ctx, `select count(*), max(summary) from config_change where org_id = 'org' and artifact_ref = '.git'`).Scan(&n, &summary); err != nil ||
		n != 1 || !strings.Contains(summary, "4 private skills and 1 member files") || !strings.Contains(summary, ".git.pre-plain") {
		t.Errorf("config_change: %v %d %q", err, n, summary)
	}
	if again, err := api.ConvertConfigRepos(ctx, false); err != nil || len(again) != 0 {
		t.Errorf("second run: %v %v", again, err)
	}
	if err := db.QueryRow(ctx, `select count(*) from config_change where org_id = 'org' and artifact_ref = '.git'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("rows after the second run: %d %v", n, err)
	}
}
