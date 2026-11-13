package webfront

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
	head := func() string {
		out, _ := exec.Command("git", "-C", roots.Org, "rev-parse", "HEAD").Output()
		return strings.TrimSpace(string(out))
	}
	copyTo := func(src, name string, who map[string]string) (int, string) {
		w := call(f, "POST", "/api/v1/work/skills/"+src+"/copy", `{"name":"`+name+`"}`, who)
		return w.Code, w.Body.String()
	}

	writeTree(t, roots.Skills, map[string]string{"finance-close/" + skillOriginFile: "{}"})
	if err := os.Symlink("/etc/hostname", filepath.Join(roots.Skills, "finance-close", "link")); err != nil {
		t.Fatal(err)
	}
	before := head()
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
	if head() != before {
		t.Error("a member's own skill reached the config repo")
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
	if out, _ := exec.Command("git", "-C", roots.Org, "log", "-1", "--format=%s").Output(); strings.TrimSpace(string(out)) != "Shared skill: admin-demo" {
		t.Errorf("share commit: %s", out)
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
