package webfront

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/open-neko/neko/apps/openneko/internal/testpg"
)

const skillMarkdown = "---\nname: multi-file-demo\ndescription: Runs a demo using bundled files.\n---\nRead references/guide.md and run scripts/check.py.\n"

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func skillFront(t *testing.T) (*Front, *pgxpool.Pool, orgRoots) {
	db := testpg.Migrated(t)
	seedOrg(t, db)
	t.Setenv("HOME", t.TempDir())
	builtin := t.TempDir()
	writeTree(t, builtin, map[string]string{
		"demo/SKILL.md":            "---\nname: demo\ndescription: Demo skill.\n---\nBody\n",
		"demo/references/Guide.md": "g", "demo/scripts/run.sh": "x", "demo/README.md": "r",
		"xlsx/SKILL.md": "---\nname: xlsx\ndescription: Sheets.\n---\n",
	})
	t.Setenv("OPENNEKO_BUILTIN_SKILLS_ROOT", builtin)
	t.Setenv("OPENNEKO_SANDBOX_SKILLS_ROOT", t.TempDir())
	t.Setenv("OPENNEKO_SANDBOX_URL", "")
	f, _ := newSetupFront(t, `{"provider":{"pluginName":"scalekit"}}`, db)
	roots, err := orgWorkspace("org")
	if err != nil {
		t.Fatal(err)
	}
	writeTree(t, roots.Skills, map[string]string{
		"finance-close/SKILL.md":   "---\nname: finance-close\ndescription: \"Close the books.\"\n---\nOld\n",
		"finance-close/data/a.bin": "a\x00b",
		"sales-playbook/SKILL.md":  "---\nname: sales-playbook\ndescription: Sell.\n---\n",
	})
	if err := os.MkdirAll(filepath.Join(roots.Skills, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTree(t, roots.Memory, map[string]string{"b.md": "b", "a.md": "a", "c.txt": "c"})
	seed(t, db, `delete from item_grant where org_id = 'org' and item_type = 'skill'`)
	seed(t, db, `insert into item_grant (org_id, group_id, item_type, item_id)
		select 'org', id, 'skill', 'finance-close' from user_group where org_id = 'org' and slug = 'everyone'`)
	return f, db, roots
}

func TestWorkSkillReads(t *testing.T) {
	f, _, roots := skillFront(t)
	admin, member := sessionCookie("admin"), sessionCookie("member")

	origin := readSkillOrigin(filepath.Join(roots.Skills, "demo"))
	if origin == nil || origin.SourceHash != "278b494f0fb1cd5030f06ac8f3dc9985202eacc1e64074a19c5e03e8ed0f0b01" {
		t.Fatalf("seeded origin: %+v", origin)
	}
	if _, err := os.Stat(filepath.Join(roots.Skills, "xlsx")); err == nil {
		t.Error("a sandbox skill was seeded with no sandbox")
	}
	if b, _ := os.ReadFile(filepath.Join(roots.Knowledge, "INDEX.md")); !strings.HasSuffix(string(b), "- syntax.json") {
		t.Errorf("knowledge index: %q", b)
	}

	w := call(f, "GET", "/api/v1/work/skills", "", admin)
	if got := titles(t, w.Body.Bytes(), "skills", "name"); w.Code != 200 || got != "demo,finance-close,sales-playbook" || !strings.HasSuffix(w.Body.String(), `"canImport":true}`) {
		t.Errorf("admin list: %d %s", w.Code, w.Body)
	}
	if !strings.Contains(w.Body.String(), `{"name":"finance-close","description":"Close the books.","fileCount":2,"updatedAt":"`) {
		t.Errorf("summary: %s", w.Body)
	}
	w = call(f, "GET", "/api/work/skills", "", member)
	if got := titles(t, w.Body.Bytes(), "skills", "name"); got != "finance-close" || !strings.HasSuffix(w.Body.String(), `"canImport":false}`) {
		t.Errorf("member list: %s", w.Body)
	}
	for _, c := range []struct {
		name string
		who  map[string]string
		want int
	}{
		{"finance-close", member, 200}, {"sales-playbook", member, 404}, {"sales-playbook", admin, 200},
		{"empty", admin, 404}, {"missing", admin, 404}, {"..", admin, 404},
	} {
		if w := call(f, "GET", "/api/v1/work/skills/"+c.name, "", c.who); w.Code != c.want {
			t.Errorf("GET %s: %d %s", c.name, w.Code, w.Body)
		}
	}
	w = call(f, "GET", "/api/v1/work/skills/finance-close", "", member)
	if !strings.Contains(w.Body.String(), `"path":"`+filepath.Join(roots.Skills, "finance-close")+`","skillMarkdown":"---\nname: finance-close`) ||
		!strings.HasSuffix(w.Body.String(), `"files":[{"path":"data/a.bin","bytes":3},{"path":"SKILL.md","bytes":64}]}}`) {
		t.Errorf("detail: %s", w.Body)
	}

	w = call(f, "GET", "/api/v1/work/assets", "", member)
	if w.Body.String() != `{"skills":[{"name":"demo","path":"/api/v1/work/files/skills/demo/SKILL.md"},{"name":"empty","path":"/api/v1/work/files/skills/empty/SKILL.md"},`+
		`{"name":"finance-close","path":"/api/v1/work/files/skills/finance-close/SKILL.md"},{"name":"sales-playbook","path":"/api/v1/work/files/skills/sales-playbook/SKILL.md"}],`+
		`"memory":[{"name":"a.md","path":"/api/v1/work/files/memory/a.md"},{"name":"b.md","path":"/api/v1/work/files/memory/b.md"}]}` {
		t.Errorf("assets: %s", w.Body)
	}
}

func TestWorkSkillFiles(t *testing.T) {
	f, db, roots := skillFront(t)
	admin, member := sessionCookie("admin"), sessionCookie("member")
	base := "/api/v1/work/skills/finance-close/file?path="

	w := call(f, "GET", base+"SKILL.md", "", member)
	if w.Body.String() != `{"file":{"path":"SKILL.md","bytes":64,"binary":false,"editable":true,"truncated":false,"text":"---\nname: finance-close\ndescription: \"Close the books.\"\n---\nOld\n"}}` {
		t.Errorf("text file: %s", w.Body)
	}
	if w := call(f, "GET", base+"data/a.bin", "", member); w.Body.String() != `{"file":{"path":"data/a.bin","bytes":3,"binary":true,"editable":false,"truncated":false,"text":null}}` {
		t.Errorf("binary file: %s", w.Body)
	}
	big := strings.Repeat("é", maxSkillEditSize/2) + "x"
	writeTree(t, roots.Skills, map[string]string{"finance-close/big.md": big})
	var got struct{ File map[string]any }
	_ = json.Unmarshal(call(f, "GET", base+"big.md", "", admin).Body.Bytes(), &got)
	if got.File["truncated"] != true || got.File["editable"] != false || len(got.File["text"].(string)) != maxSkillEditSize {
		t.Errorf("large file: %v %v %d", got.File["truncated"], got.File["editable"], len(got.File["text"].(string)))
	}
	if err := os.Symlink("/etc/hostname", filepath.Join(roots.Skills, "finance-close", "link")); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		target string
		who    map[string]string
		want   int
	}{
		{"/api/v1/work/skills/finance-close/file", member, 400}, {base, member, 400}, {base + "../sales-playbook/SKILL.md", admin, 404},
		{base + "link", admin, 404}, {base + "nope.md", admin, 404}, {"/api/v1/work/skills/sales-playbook/file?path=SKILL.md", member, 404},
	} {
		if w := call(f, "GET", c.target, "", c.who); w.Code != c.want {
			t.Errorf("GET %s: %d %s", c.target, w.Code, w.Body)
		}
	}

	for _, c := range []struct {
		target, body string
		want         int
		msg          string
	}{
		{base + "SKILL.md", `nope`, 400, "Invalid request body"},
		{base + "SKILL.md", `{"content":1}`, 400, "content must be a string"},
		{base + "SKILL.md", `{"content":"` + strings.Repeat("a", maxSkillEditSize+1) + `"}`, 413, "File is too large to save from here."},
		{base + "a/../b", `{"content":"x"}`, 400, "Invalid file path"},
		{base + "new.md", `{"content":"x"}`, 404, "File not found"},
		{base + "data/a.bin", `{"content":"x"}`, 415, "This file is not a text file and cannot be edited here."},
		{"/api/v1/work/skills/sales-playbook/file?path=SKILL.md", `{"content":"x"}`, 404, "File not found"},
	} {
		w := call(f, "PUT", c.target, c.body, member)
		if w.Code != c.want || w.Body.String() != `{"error":"`+c.msg+`"}` {
			t.Errorf("PUT %s: %d %s", c.target, w.Code, w.Body)
		}
	}
	newText := "---\nname: finance-close\ndescription: Close.\n---\nNew\n"
	body, _ := json.Marshal(map[string]string{"content": newText})
	if w := call(f, "PUT", base+"SKILL.md", string(body), member); w.Code != 200 || w.Body.String() != `{"ok":true,"bytes":`+strconv.Itoa(len(newText))+`}` {
		t.Fatalf("PUT: %d %s", w.Code, w.Body)
	}
	gitOut := func(args ...string) string {
		out, err := exec.Command("git", append([]string{"-C", roots.Org}, args...)...).Output()
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(out))
	}
	if gitOut("log", "-1", "--format=%s") != "Updated skill: finance-close" || !strings.Contains(gitOut("show", "HEAD:skills/finance-close/SKILL.md"), "New") {
		t.Errorf("git: %s", gitOut("log", "--format=%s"))
	}
	var kind, ref, summary, status string
	var actorID *string
	if err := db.QueryRow(context.Background(), `select artifact_kind, artifact_ref, summary, status, actor_user_id from config_change where org_id = 'org' order by created_at desc limit 1`).
		Scan(&kind, &ref, &summary, &status, &actorID); err != nil || kind != "skill" || ref != "finance-close" || summary != "Updated skill: finance-close" || status != "recorded" || actorID != nil {
		t.Errorf("config_change: %v %s %s %s %s %v", err, kind, ref, summary, status, actorID)
	}
	var teamRef string
	_ = db.QueryRow(context.Background(), `select commit_sha from config_ref where org_id = 'org' and scope = 'team'`).Scan(&teamRef)
	if teamRef != gitOut("rev-parse", "HEAD") {
		t.Errorf("team ref %s", teamRef)
	}

	if w := call(f, "DELETE", "/api/v1/work/skills/..", "", admin); w.Code != 404 {
		t.Errorf("delete ..: %d", w.Code)
	}
	if _, err := os.Stat(roots.Memory); err != nil {
		t.Errorf("org folder after delete ..: %v", err)
	}
	if w := call(f, "DELETE", "/api/v1/work/skills/sales-playbook", "", member); w.Code != 404 {
		t.Errorf("member delete: %d", w.Code)
	}
	if w := call(f, "DELETE", "/api/v1/work/skills/sales-playbook", "", admin); w.Code != 200 || w.Body.String() != `{"ok":true}` {
		t.Errorf("delete: %d %s", w.Code, w.Body)
	}
	if w := call(f, "DELETE", "/api/v1/work/skills/sales-playbook", "", admin); w.Code != 404 {
		t.Errorf("second delete: %d", w.Code)
	}
}

func TestConfigHistoryRestore(t *testing.T) {
	f, db, roots := skillFront(t)
	admin, member := sessionCookie("admin"), sessionCookie("member")
	if w := call(f, "GET", "/api/v1/work/config/history", "", member); w.Body.String() != `{"history":[]}` {
		t.Errorf("empty history: %s", w.Body)
	}
	base := "/api/v1/work/skills/finance-close/file?path=SKILL.md"
	for _, text := range []string{"one", "two"} {
		if w := call(f, "PUT", base, `{"content":"`+text+`"}`, admin); w.Code != 200 {
			t.Fatal(w.Body)
		}
	}
	var h struct{ History []configCommit }
	_ = json.Unmarshal(call(f, "GET", "/api/v1/work/config/history?path=skills/finance-close&limit=10", "", member).Body.Bytes(), &h)
	if len(h.History) != 2 || h.History[0].Message != "Updated skill: finance-close" || len(h.History[1].SHA) != 40 || h.History[0].Date == "" {
		t.Fatalf("history: %+v", h.History)
	}
	w := call(f, "GET", "/api/v1/work/config/history?limit=1", "", member)
	if want := `{"history":[{"sha":"` + h.History[0].SHA + `","date":"` + h.History[0].Date + `","message":"Updated skill: finance-close"}]}`; w.Body.String() != want {
		t.Errorf("history body: %s", w.Body)
	}
	_ = json.Unmarshal(w.Body.Bytes(), &h)
	if len(h.History) != 1 {
		t.Errorf("limit: %+v", h.History)
	}
	first := h.History[0].SHA
	_ = json.Unmarshal(call(f, "GET", "/api/v1/work/config/history?path=skills/finance-close", "", member).Body.Bytes(), &h)
	first = h.History[1].SHA

	for _, c := range []struct {
		who  map[string]string
		body string
		want int
	}{
		{member, `{"sha":"` + first + `","path":"skills/finance-close"}`, 403},
		{admin, `{"sha":"xyz","path":"skills/finance-close"}`, 400},
		{admin, `{"sha":"` + first + `","path":"skills/../x"}`, 400},
		{admin, `not json`, 400},
		{admin, `{"sha":"` + first + `","path":"memory"}`, 500},
	} {
		if w := call(f, "POST", "/api/v1/work/config/restore", c.body, c.who); w.Code != c.want {
			t.Errorf("restore %s: %d %s", c.body, w.Code, w.Body)
		}
	}
	w = call(f, "POST", "/api/v1/work/config/restore", `{"sha":"`+first+`","path":"skills/finance-close"}`, admin)
	var out struct {
		OK  bool
		SHA string
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if w.Code != 200 || !out.OK || len(out.SHA) != 40 {
		t.Fatalf("restore: %d %s", w.Code, w.Body)
	}
	if b, _ := os.ReadFile(filepath.Join(roots.Skills, "finance-close", "SKILL.md")); string(b) != "one" {
		t.Errorf("restored file: %q", b)
	}
	var summary, status, actorID, sha string
	if err := db.QueryRow(context.Background(), `select summary, status, actor_user_id, commit_sha from config_change where org_id = 'org' and artifact_kind = 'config'`).
		Scan(&summary, &status, &actorID, &sha); err != nil || summary != "Restored skills/finance-close to "+first[:8] || status != "restored" || actorID != "admin" || sha != out.SHA {
		t.Errorf("config_change: %v %s %s %s", err, summary, status, actorID)
	}
	if w := call(f, "POST", "/api/v1/work/config/restore", `{"sha":"`+first+`","path":"skills/finance-close"}`, admin); w.Code != 404 {
		t.Errorf("second restore: %d %s", w.Code, w.Body)
	}
	if w := call(f, "POST", "/api/v1/work/config/restore", `{"sha":"deadbeef","path":"skills/finance-close"}`, admin); w.Code != 500 {
		t.Errorf("unknown sha: %d %s", w.Code, w.Body)
	}
}

type zipEntry struct {
	name, data string
	mode       uint32
}

func zipArchive(t *testing.T, entries []zipEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate, CreatorVersion: 3 << 8}
		if e.mode != 0 {
			h.ExternalAttrs = e.mode << 16
		}
		fw, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fw.Write([]byte(e.data))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func uploadSkill(f *Front, archive []byte, filename string, who map[string]string) *httptest.ResponseRecorder {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", filename)
	_, _ = fw.Write(archive)
	_ = mw.Close()
	header := map[string]string{"Content-Type": mw.FormDataContentType()}
	for k, v := range who {
		header[k] = v
	}
	return call(f, "POST", "/api/v1/work/skills", body.String(), header)
}

func TestSkillArchiveImport(t *testing.T) {
	f, db, roots := skillFront(t)
	admin, member := sessionCookie("admin"), sessionCookie("member")
	archive := zipArchive(t, []zipEntry{
		{name: "multi-file-demo/"},
		{name: "multi-file-demo/SKILL.md", data: skillMarkdown},
		{name: "multi-file-demo/scripts/check.py", data: "print('ok')\n", mode: 0o100755},
		{name: "multi-file-demo/references/guide.md", data: "# Guide\n"},
		{name: "multi-file-demo/assets/icon.png", data: "\x89PNG"},
	})
	if w := uploadSkill(f, archive, "demo.skill", member); w.Code != 403 {
		t.Errorf("member import: %d", w.Code)
	}
	w := uploadSkill(f, archive, "demo.SKILL", admin)
	if w.Code != 201 || w.Body.String() != `{"name":"multi-file-demo"}` {
		t.Fatalf("import: %d %s", w.Code, w.Body)
	}
	dir := filepath.Join(roots.Skills, "multi-file-demo")
	if b, _ := os.ReadFile(filepath.Join(dir, "SKILL.md")); string(b) != skillMarkdown {
		t.Errorf("SKILL.md: %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "assets/icon.png")); string(b) != "\x89PNG" {
		t.Errorf("icon: %q", b)
	}
	if st, err := os.Stat(filepath.Join(dir, "scripts/check.py")); err != nil || st.Mode()&0o111 == 0 {
		t.Errorf("script mode: %v", err)
	}
	if st, _ := os.Stat(filepath.Join(dir, "references/guide.md")); st.Mode()&0o111 != 0 {
		t.Errorf("guide mode: %v", st.Mode())
	}
	var actorID, summary string
	if err := db.QueryRow(context.Background(), `select actor_user_id, summary from config_change where org_id = 'org' and artifact_ref = 'multi-file-demo'`).Scan(&actorID, &summary); err != nil ||
		actorID != "admin" || summary != "Imported skill: multi-file-demo" {
		t.Errorf("config_change: %v %s %s", err, actorID, summary)
	}
	if w := uploadSkill(f, archive, "demo.zip", admin); w.Code != 409 || w.Body.String() != `{"error":"A skill with this name already exists."}` {
		t.Errorf("duplicate: %d %s", w.Code, w.Body)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "SKILL.md")); string(b) != skillMarkdown {
		t.Errorf("SKILL.md after duplicate: %q", b)
	}
	if w := uploadSkill(f, archive, "demo.tar", admin); w.Code != 400 || w.Body.String() != `{"error":"Choose a .skill or ZIP archive."}` {
		t.Errorf("wrong extension: %d %s", w.Code, w.Body)
	}
	if w := uploadSkill(f, nil, "demo.zip", admin); w.Code != 413 {
		t.Errorf("empty: %d %s", w.Code, w.Body)
	}

	bad := strings.Replace(skillMarkdown, "multi-file-demo", "bad-skill", 1)
	for _, c := range []struct {
		entries []zipEntry
		msg     string
	}{
		{[]zipEntry{{name: "../escape/SKILL.md", data: skillMarkdown}}, "Skill archive is invalid or corrupt."},
		{[]zipEntry{{name: "another-skill/SKILL.md", data: skillMarkdown}}, "SKILL.md name must match its folder, with a description of at most 1024 characters."},
		{[]zipEntry{{name: "multi-file-demo/SKILL.md", data: skillMarkdown}, {name: "other-skill/file.md", data: "x"}}, "Skill archive must contain one skill directory."},
		{[]zipEntry{{name: "bad-skill/SKILL.md", data: "# Missing frontmatter"}}, "SKILL.md needs YAML frontmatter with a name and description."},
		{[]zipEntry{{name: "bad-skill/SKILL.md", data: "---\nname: different\ndescription: Wrong name\n---\n"}}, "SKILL.md name must match its folder, with a description of at most 1024 characters."},
		{[]zipEntry{{name: "bad-skill/SKILL.md", data: bad}, {name: "bad-skill/link", data: "../../escape", mode: 0o120777}}, "Skill archive may contain only files and directories."},
		{[]zipEntry{{name: "bad-skill/SKILL.md", data: bad}, {name: "bad-skill/Guide.md", data: "a"}, {name: "bad-skill/guide.md", data: "b"}}, "Skill archive contains duplicate paths."},
		{[]zipEntry{{name: "bad-skill/SKILL.md", data: bad}, {name: "bad-skill/Docs/a.md", data: "a"}, {name: "bad-skill/docs/b.md", data: "b"}}, "Skill archive contains case-ambiguous paths."},
		{[]zipEntry{{name: "bad-skill/SKILL.md", data: "---\nname: bad-skill\nname: bad-skill\ndescription: x\n---\n"}}, "SKILL.md frontmatter is invalid YAML."},
		{[]zipEntry{{name: "Bad/SKILL.md", data: bad}}, "Put one skill directory at the archive root."},
		{[]zipEntry{{name: "bad-skill/README.md", data: "x"}}, "Skill archive needs SKILL.md at the skill directory root."},
	} {
		w := uploadSkill(f, zipArchive(t, c.entries), "bad.zip", admin)
		if w.Code != 400 || w.Body.String() != `{"error":"`+c.msg+`"}` {
			t.Errorf("%v: %d %s", c.entries[0].name, w.Code, w.Body)
		}
	}
	if w := uploadSkill(f, []byte("not a zip"), "bad.zip", admin); w.Code != 400 || w.Body.String() != `{"error":"Choose a valid .skill or ZIP archive."}` {
		t.Errorf("not a zip: %d %s", w.Code, w.Body)
	}
	entries, _ := os.ReadDir(roots.Skills)
	for _, e := range entries {
		if e.Name() == "bad-skill" || e.Name() == "another-skill" {
			t.Errorf("installed %s", e.Name())
		}
	}
	if left, _ := filepath.Glob(filepath.Join(roots.Org, ".skill-import-*")); len(left) != 0 {
		t.Errorf("staging left: %v", left)
	}
}
