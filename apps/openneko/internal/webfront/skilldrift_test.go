package webfront

import (
	"bytes"
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOlderBuiltinCopyIsNotAnEdit(t *testing.T) {
	f, db, roots := skillFront(t)
	t.Setenv("OPENNEKO_AGENT_RUNS_DIR", t.TempDir())
	ctx := context.Background()
	a := f.API
	older := filepath.Join(roots.Skills, "demo")
	if err := os.RemoveAll(older); err != nil {
		t.Fatal(err)
	}
	writeTree(t, older, map[string]string{
		"SKILL.md":  "---\nname: demo\ndescription: Demo skill.\n---\nOlder body\n",
		"README.md": "r",
	})
	hash, err := fingerprintSkillTree(older)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(older, skillOriginFile), `{"kind":"builtin","sourceHash":"`+hash+`"}`+"\n")
	before, _ := os.ReadFile(filepath.Join(older, "SKILL.md"))

	seed(t, db, `insert into work_thread (id, org_id) values ('62000000-0000-4000-8000-000000000001', 'org')`)
	seed(t, db, `insert into work_run (id, org_id, thread_id, backend, actor_user_id, actor_role) values
		('72000000-0000-4000-8000-000000000001', 'org', '62000000-0000-4000-8000-000000000001', 'test', 'admin', 'admin'),
		('72000000-0000-4000-8000-000000000002', 'org', '62000000-0000-4000-8000-000000000001', 'test', null, 'service'),
		('72000000-0000-4000-8000-000000000003', 'org', '62000000-0000-4000-8000-000000000001', 'test', 'member', 'member')`)
	adminAct := runActorRow{UserID: "admin", Role: "admin"}
	capture := func(runID string, act runActorRow, edit func(skills string)) *savedSkillEdits {
		t.Helper()
		host, err := turnWorkspace("org", "thread", "run")
		if err != nil {
			t.Fatal(err)
		}
		st, err := stageTurn(host, stageOptions{Actor: act})
		if err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(st.Dir)
		if staged, _ := os.ReadFile(filepath.Join(st.Workspace.SkillsRoot, "demo", "SKILL.md")); !strings.Contains(string(staged), "Body") {
			t.Fatalf("the run did not get the binary's built-in: %q", staged)
		}
		edit(st.Workspace.SkillsRoot)
		saved, err := a.captureSkillEdits(ctx, "org", runID, act, st.baseline, st.staged, st.own, st.Workspace.SkillsRoot)
		if err != nil {
			t.Fatal(err)
		}
		return saved
	}
	untouched := func(string) {}
	for _, c := range []struct {
		name, runID string
		act         runActorRow
	}{
		{"an admin's chat run", "72000000-0000-4000-8000-000000000001", adminAct},
		{"a run with no user", "72000000-0000-4000-8000-000000000002", runActorRow{}},
		{"a member's run", "72000000-0000-4000-8000-000000000003", runActorRow{UserID: "member", Role: "member"}},
	} {
		if saved := capture(c.runID, c.act, untouched); saved != nil {
			t.Errorf("%s that did not touch demo reports a change: %+v %v", c.name, *saved, saved.notices())
		}
	}

	var logs bytes.Buffer
	log.SetOutput(&logs)
	defer log.SetOutput(os.Stderr)
	saved := capture("72000000-0000-4000-8000-000000000001", adminAct, func(skills string) {
		p := filepath.Join(skills, "demo", "SKILL.md")
		data, _ := os.ReadFile(p)
		writeTestFile(t, p, string(data)+"Admin line\n")
	})
	if got := scalar(t, db, `select count(*)::text from team_item_proposal where org_id = 'org'`); got != "0" {
		t.Errorf("the run proposed the binary's built-in as an edit of the org's demo: %s proposals", got)
	}
	if saved == nil || len(saved.Proposed) != 0 || len(saved.Skipped) != 1 || saved.Skipped[0] != "demo/SKILL.md" {
		t.Errorf("saved %+v", saved)
	}
	if !strings.Contains(logs.String(), "demo/SKILL.md") {
		t.Errorf("no log names the path: %s", logs.String())
	}
	if after, _ := os.ReadFile(filepath.Join(older, "SKILL.md")); !bytes.Equal(after, before) {
		t.Error("the org's demo changed")
	}
	service := capture("72000000-0000-4000-8000-000000000002", runActorRow{}, func(skills string) {
		p := filepath.Join(skills, "demo", "SKILL.md")
		data, _ := os.ReadFile(p)
		writeTestFile(t, p, string(data)+"Service line\n")
	})
	for name, s := range map[string]*savedSkillEdits{"an admin's chat run": saved, "a run with no user": service} {
		n := s.notices()
		if len(n) != 1 || jsGet(n[0], "code") != "skill_edit_not_from_team" {
			t.Errorf("%s that edited the built-in copy of demo: notices %v", name, n)
			continue
		}
		if msg := jsGet(n[0], "message").(string); !strings.Contains(msg, "skill demo") || !strings.Contains(msg, "did not save") || !strings.Contains(msg, "built-in copy") {
			t.Errorf("%s: the notice does not name the skill and the reason: %q", name, msg)
		}
	}
}
