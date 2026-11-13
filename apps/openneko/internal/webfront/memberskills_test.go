package webfront

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

type listedSkill struct {
	Name     string
	Scope    string
	CanEdit  bool
	CanShare bool
}

func listedSkills(t *testing.T, f *Front, who map[string]string) map[string]listedSkill {
	t.Helper()
	var body struct{ Skills []listedSkill }
	w := call(f, "GET", "/api/v1/work/skills", "", who)
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != 200 {
		t.Fatalf("list: %d %s", w.Code, w.Body)
	}
	out := map[string]listedSkill{}
	for _, s := range body.Skills {
		out[s.Name] = s
	}
	return out
}

func TestMemberSkillCopyAndShare(t *testing.T) {
	f, db, roots := skillFront(t)
	admin, member := sessionCookie("admin"), sessionCookie("member")
	ctx := context.Background()
	copyTo := func(src, name string, who map[string]string) (int, string) {
		w := call(f, "POST", "/api/v1/work/skills/"+src+"/copy", `{"name":"`+name+`"}`, who)
		return w.Code, w.Body.String()
	}

	writeTree(t, roots.Skills, map[string]string{"finance-close/" + skillOriginFile: "{}"})
	if err := os.Symlink("/etc/hostname", filepath.Join(roots.Skills, "finance-close", "link")); err != nil {
		t.Fatal(err)
	}
	if code, body := copyTo("finance-close", "my-close", member); code != 201 || body != `{"name":"my-close","scope":"own"}` {
		t.Fatalf("copy: %d %s", code, body)
	}
	own := filepath.Join(memberSkillsRoot(roots.Org, "member"), "my-close")
	if b, _ := os.ReadFile(filepath.Join(own, "SKILL.md")); string(b) != "---\nname: my-close\ndescription: \"Close the books.\"\n---\nOld\n" {
		t.Errorf("copied SKILL.md: %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(own, "data", "a.bin")); string(b) != "a\x00b" {
		t.Errorf("copied data: %q", b)
	}
	for _, left := range []string{"link", skillOriginFile} {
		if _, err := os.Lstat(filepath.Join(own, left)); err == nil {
			t.Errorf("copy holds %s", left)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(roots.Skills, "finance-close", "SKILL.md")); !strings.Contains(string(b), "name: finance-close") {
		t.Errorf("team skill changed: %q", b)
	}
	var scope, userID, actorID, summary string
	var sha *string
	if err := db.QueryRow(ctx, `select scope, user_id, actor_user_id, summary, commit_sha from config_change
		where org_id = 'org' and artifact_ref = 'my-close'`).Scan(&scope, &userID, &actorID, &summary, &sha); err != nil ||
		scope != "user" || userID != "member" || actorID != "member" || summary != "Copied skill finance-close to my-close" || sha != nil {
		t.Errorf("config_change: %v %s %s %s %s %v", err, scope, userID, actorID, summary, sha)
	}

	got := listedSkills(t, f, member)
	if len(got) != 2 || got["finance-close"] != (listedSkill{"finance-close", "team", false, false}) ||
		got["my-close"] != (listedSkill{"my-close", "own", true, false}) {
		t.Errorf("member list: %+v", got)
	}
	if _, seen := listedSkills(t, f, admin)["my-close"]; seen {
		t.Error("an admin sees a member's own skill")
	}
	if w := call(f, "GET", "/api/v1/work/skills/my-close", "", admin); w.Code != 404 {
		t.Errorf("admin reads a member's skill: %d", w.Code)
	}
	w := call(f, "GET", "/api/v1/work/skills/my-close", "", member)
	if !strings.Contains(w.Body.String(), `"scope":"own","canEdit":true,"canShare":false,"path":"`+own+`"`) {
		t.Errorf("own detail: %s", w.Body)
	}
	if w := call(f, "PUT", "/api/v1/work/skills/my-close/file?path=SKILL.md", `{"content":"mine"}`, member); w.Code != 200 {
		t.Errorf("edit own: %d %s", w.Code, w.Body)
	}
	if b, _ := os.ReadFile(filepath.Join(own, "SKILL.md")); string(b) != "mine" {
		t.Errorf("own edit: %q", b)
	}
	if _, err := os.Stat(filepath.Join(roots.Org, ".git")); err == nil {
		t.Error("a skill change made a git repo")
	}

	for _, c := range []struct {
		src, name string
		want      int
		msg       string
	}{
		{"finance-close", "my-close", 409, skillExistsMessage("my-close")},
		{"finance-close", "sales-playbook", 409, skillExistsMessage("sales-playbook")},
		{"finance-close", "Bad Name", 400, "Use lowercase letters, digits and single hyphens for the name, up to 64 characters."},
		{"finance-close", "..", 400, "Use lowercase letters, digits and single hyphens for the name, up to 64 characters."},
		{"finance-close", strings.Repeat("a", 65), 400, "Use lowercase letters, digits and single hyphens for the name, up to 64 characters."},
		{"sales-playbook", "mine", 404, "Skill not found"},
	} {
		if code, body := copyTo(c.src, c.name, member); code != c.want || body != `{"error":"`+c.msg+`"}` {
			t.Errorf("copy %s to %s: %d %s", c.src, c.name, code, body)
		}
	}
	if w := call(f, "POST", "/api/v1/work/skills/my-close/share", "", member); w.Code != 403 {
		t.Errorf("member share: %d %s", w.Code, w.Body)
	}

	writeTree(t, memberSkillsRoot(roots.Org, "member"), map[string]string{"finance-close/SKILL.md": "own version"})
	w = call(f, "GET", "/api/v1/work/skills/finance-close/file?path=SKILL.md", "", member)
	if !strings.Contains(w.Body.String(), `"text":"own version"`) {
		t.Errorf("own skill over team skill: %s", w.Body)
	}
	if w := call(f, "DELETE", "/api/v1/work/skills/finance-close", "", member); w.Code != 200 {
		t.Errorf("delete own: %d %s", w.Code, w.Body)
	}
	if _, err := os.Stat(filepath.Join(roots.Skills, "finance-close", "SKILL.md")); err != nil {
		t.Errorf("own delete removed the team skill: %v", err)
	}
	if w := call(f, "DELETE", "/api/v1/work/skills/finance-close", "", member); w.Code != 403 {
		t.Errorf("member delete of the team skill: %d %s", w.Code, w.Body)
	}

	if code, body := copyTo("demo", "admin-demo", admin); code != 201 {
		t.Fatalf("admin copy: %d %s", code, body)
	}
	if s := listedSkills(t, f, admin)["admin-demo"]; s != (listedSkill{"admin-demo", "own", true, true}) {
		t.Errorf("admin own skill: %+v", s)
	}
	if w := call(f, "POST", "/api/v1/work/skills/demo/share", "", admin); w.Code != 400 {
		t.Errorf("share a team skill: %d %s", w.Code, w.Body)
	}
	w = call(f, "POST", "/api/v1/work/skills/admin-demo/share", "", admin)
	if w.Code != 200 || w.Body.String() != `{"name":"admin-demo","scope":"team"}` {
		t.Fatalf("share: %d %s", w.Code, w.Body)
	}
	if b, _ := os.ReadFile(filepath.Join(roots.Skills, "admin-demo", "SKILL.md")); !strings.Contains(string(b), "name: admin-demo") {
		t.Errorf("shared SKILL.md: %q", b)
	}
	if _, err := os.Stat(filepath.Join(memberSkillsRoot(roots.Org, "admin"), "admin-demo")); err == nil {
		t.Error("the shared skill stayed in the admin's folder")
	}
	if s := listedSkills(t, f, admin)["admin-demo"]; s.Scope != "team" {
		t.Errorf("shared skill: %+v", s)
	}
	if got := queryText(t, db, `select scope || ' ' || coalesce(actor_user_id, '-') || ' ' || coalesce(commit_sha, 'no-commit') from config_change
		where org_id = 'org' and summary = 'Shared skill: admin-demo'`); got != "team admin no-commit" {
		t.Errorf("share row: %s", got)
	}

	writeTree(t, memberSkillsRoot(roots.Org, "admin"), map[string]string{"sales-playbook/SKILL.md": "---\nname: sales-playbook\n---\n"})
	w = call(f, "POST", "/api/v1/work/skills/sales-playbook/share", "", admin)
	if w.Code != 409 || w.Body.String() != `{"error":"The team already has a skill named sales-playbook. Copy your skill to a new name, then share the copy."}` {
		t.Errorf("share clash: %d %s", w.Code, w.Body)
	}
	if b, _ := os.ReadFile(filepath.Join(roots.Skills, "sales-playbook", "SKILL.md")); !strings.Contains(string(b), "Sell.") {
		t.Errorf("share clash changed the team skill: %q", b)
	}
}

func TestSkillRoutesRefuseParentNames(t *testing.T) {
	f, _, roots := skillFront(t)
	admin := sessionCookie("admin")
	writeTree(t, memberSkillsRoot(roots.Org, "admin"), map[string]string{"mine/SKILL.md": "x"})
	for _, name := range []string{"..", "%2E%2E", "%252E%252E", "%252e%252e", "mine%252F..", "..%252F..%252Fskills"} {
		for _, c := range []struct{ method, suffix, body string }{
			{"GET", "", ""}, {"DELETE", "", ""}, {"GET", "/file?path=SKILL.md", ""},
			{"PUT", "/file?path=SKILL.md", `{"content":"x"}`}, {"POST", "/copy", `{"name":"escape"}`}, {"POST", "/share", ""},
		} {
			w := call(f, c.method, "/api/v1/work/skills/"+name+c.suffix, c.body, admin)
			if w.Code != 404 {
				t.Errorf("%s %s%s: %d %s", c.method, name, c.suffix, w.Code, w.Body)
			}
		}
	}
	for _, p := range []string{roots.Memory, filepath.Join(roots.Skills, "finance-close", "SKILL.md"), filepath.Join(memberSkillsRoot(roots.Org, "admin"), "mine", "SKILL.md")} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s after the requests: %v", p, err)
		}
	}
	if _, err := os.Stat(filepath.Join(memberSkillsRoot(roots.Org, "admin"), "escape")); err == nil {
		t.Error("a copy was made from a parent name")
	}
}

func TestMemberFolderName(t *testing.T) {
	for id, want := range map[string]string{"u-123": "u-123", "a b@example.com": "a-b-example-com-"} {
		if got := memberFolderName(id); !strings.HasPrefix(got, want) || strings.ContainsAny(got, "/.@ ") {
			t.Errorf("%q: %q", id, got)
		}
	}
	if memberFolderName("../x") == memberFolderName("x") || memberFolderName("") == "" {
		t.Error("folder names clash or are empty")
	}
}

func TestRunSkillCapture(t *testing.T) {
	f, db, roots := skillFront(t)
	t.Setenv("OPENNEKO_AGENT_RUNS_DIR", t.TempDir())
	ctx := context.Background()
	a := f.API
	member := runActorRow{UserID: "member", Role: "member"}
	adminAct := runActorRow{UserID: "admin", Role: "admin"}
	mine := memberSkillsRoot(roots.Org, "member")
	writeTree(t, mine, map[string]string{"my-notes/SKILL.md": "---\nname: my-notes\ndescription: Notes.\n---\nOld\n"})
	writeTree(t, memberSkillsRoot(roots.Org, "admin"), map[string]string{"admin-own/SKILL.md": "---\nname: admin-own\n---\nOld\n"})

	run := func(act runActorRow, allowed []string, edit func(skills string)) savedSkillEdits {
		t.Helper()
		host, err := turnWorkspace("org", "thread", "run")
		if err != nil {
			t.Fatal(err)
		}
		_, scope, own := runSkillsWithOwn(nil, allowed, ownRunSkills(roots.Org, act.UserID), false, false)
		st, err := stageTurn(host, stageOptions{Allowed: scope, Own: own, Actor: act})
		if err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(st.Dir)
		edit(st.Workspace.SkillsRoot)
		saved, err := a.captureSkillEdits(ctx, "org", "run-12345678-aaaa", act, st.baseline, st.own, st.Workspace.SkillsRoot)
		if err != nil || saved == nil {
			t.Fatalf("capture: %v %v", saved, err)
		}
		return *saved
	}
	read := func(p string) string {
		b, err := os.ReadFile(p)
		if err != nil {
			return "<none>"
		}
		return string(b)
	}
	rows := func(ref string) []string {
		r, err := db.Query(ctx, `select scope || ' ' || coalesce(user_id, '') || ' ' || coalesce(actor_user_id, '') || ' ' ||
			coalesce(commit_sha, 'no-commit') || ' ' || summary from config_change where org_id = 'org' and artifact_ref = $1 order by id`, ref)
		if err != nil {
			t.Fatal(err)
		}
		out, err := pgx.CollectRows(r, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	teamClose := read(filepath.Join(roots.Skills, "finance-close", "SKILL.md"))

	saved := run(member, []string{"finance-close"}, func(skills string) {
		writeTestFile(t, filepath.Join(skills, "finance-close", "SKILL.md"), "---\nname: finance-close\n---\nMine\n")
		os.Remove(filepath.Join(skills, "finance-close", "data", "a.bin"))
		writeTestFile(t, filepath.Join(skills, "my-notes", "SKILL.md"), "---\nname: my-notes\n---\nNew\n")
		writeTestFile(t, filepath.Join(skills, "fresh", "SKILL.md"), "---\nname: fresh\n---\n")
		writeTestFile(t, filepath.Join(skills, "sales-playbook", "SKILL.md"), "---\nname: sales-playbook\n---\nMine\n")
	})
	if !slices.Equal(saved.Own, []string{"fresh", "my-notes"}) || len(saved.Team) != 0 ||
		saved.Copies["finance-close"] != "finance-close-m" || saved.Copies["sales-playbook"] != "sales-playbook-m" || len(saved.Copies) != 2 {
		t.Fatalf("saved %+v", saved)
	}
	if read(filepath.Join(roots.Skills, "finance-close", "SKILL.md")) != teamClose ||
		read(filepath.Join(roots.Skills, "finance-close", "data", "a.bin")) != "a\x00b" ||
		read(filepath.Join(roots.Skills, "sales-playbook", "SKILL.md")) != "---\nname: sales-playbook\ndescription: Sell.\n---\n" {
		t.Error("a member's run changed a team skill")
	}
	if got := read(filepath.Join(mine, "finance-close-m", "SKILL.md")); got != "---\nname: finance-close-m\n---\nMine\n" {
		t.Errorf("copy SKILL.md %q", got)
	}
	if read(filepath.Join(mine, "finance-close-m", "data", "a.bin")) != "<none>" {
		t.Error("the copy holds a file the run removed")
	}
	if read(filepath.Join(mine, "my-notes", "SKILL.md")) != "---\nname: my-notes\n---\nNew\n" || read(filepath.Join(mine, "fresh", "SKILL.md")) != "---\nname: fresh\n---\n" {
		t.Error("own skills not saved in place")
	}
	if got := rows("finance-close-m"); !slices.Equal(got, []string{"user member member no-commit Saved the changes to skill finance-close in run run-1234 as finance-close-m"}) {
		t.Errorf("copy rows %q", got)
	}
	if got := rows("fresh, my-notes"); !slices.Equal(got, []string{"user member member no-commit Updated skills in run run-1234: fresh, my-notes"}) {
		t.Errorf("own rows %q", got)
	}

	saved = run(member, []string{"finance-close"}, func(skills string) {
		writeTestFile(t, filepath.Join(skills, "finance-close", "SKILL.md"), "---\nname: finance-close\n---\nAgain\n")
		writeTestFile(t, filepath.Join(skills, "finance-close-m", "SKILL.md"), "---\nname: finance-close-m\n---\nEdited copy\n")
	})
	if saved.Copies["finance-close"] != "finance-close-m-2" || !slices.Equal(saved.Own, []string{"finance-close-m"}) {
		t.Fatalf("second run %+v", saved)
	}
	if read(filepath.Join(mine, "finance-close-m", "SKILL.md")) != "---\nname: finance-close-m\n---\nEdited copy\n" {
		t.Error("the own copy was not edited in place")
	}

	saved = run(adminAct, nil, func(skills string) {
		writeTestFile(t, filepath.Join(skills, "finance-close", "SKILL.md"), "---\nname: finance-close\n---\nAdmin\n")
		writeTestFile(t, filepath.Join(skills, "admin-own", "SKILL.md"), "---\nname: admin-own\n---\nNew\n")
	})
	if !slices.Equal(saved.Team, []string{"finance-close"}) || !slices.Equal(saved.Own, []string{"admin-own"}) || len(saved.Copies) != 0 {
		t.Fatalf("admin %+v", saved)
	}
	if read(filepath.Join(roots.Skills, "finance-close", "SKILL.md")) != "---\nname: finance-close\n---\nAdmin\n" ||
		read(filepath.Join(memberSkillsRoot(roots.Org, "admin"), "admin-own", "SKILL.md")) != "---\nname: admin-own\n---\nNew\n" {
		t.Error("admin edits not saved in place")
	}
	if _, err := os.Stat(filepath.Join(roots.Skills, "admin-own")); err == nil {
		t.Error("an admin's own skill reached the team folder")
	}

	saved = run(runActorRow{}, nil, func(skills string) {
		writeTestFile(t, filepath.Join(skills, "sales-playbook", "SKILL.md"), "---\nname: sales-playbook\n---\nJob\n")
	})
	if !slices.Equal(saved.Team, []string{"sales-playbook"}) || len(saved.Own)+len(saved.Copies) != 0 ||
		read(filepath.Join(roots.Skills, "sales-playbook", "SKILL.md")) != "---\nname: sales-playbook\n---\nJob\n" {
		t.Fatalf("unattended %+v", saved)
	}

	seed(t, db, `insert into work_thread (id, org_id) values ('61000000-0000-4000-8000-000000000001', 'org')`)
	seed(t, db, `insert into work_run (id, org_id, thread_id, backend, actor_user_id, actor_role) values
		('71000000-0000-4000-8000-000000000001', 'org', '61000000-0000-4000-8000-000000000001', 'test', 'member', 'member'),
		('71000000-0000-4000-8000-000000000002', 'org', '61000000-0000-4000-8000-000000000001', 'test', null, 'service')`)
	search := func(runID, query string) []string {
		t.Helper()
		res, err := a.runTools(turnBinding{}).searchSkills(ctx, skillSearchIn{OrgID: "org", RunID: runID, Query: query})
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, r := range res.([]any) {
			names = append(names, jsStringOrEmpty(jsGet(r, "name")))
		}
		return names
	}
	if got := search("71000000-0000-4000-8000-000000000001", "edited copy finance close notes"); len(got) == 0 ||
		!slices.Contains(got, "finance-close-m") || slices.Contains(got, "admin-own") {
		t.Errorf("member search %v", got)
	}
	if got := search("71000000-0000-4000-8000-000000000002", "finance close notes"); slices.Contains(got, "finance-close-m") || slices.Contains(got, "my-notes") {
		t.Errorf("a run with no person found own skills: %v", got)
	}
}
