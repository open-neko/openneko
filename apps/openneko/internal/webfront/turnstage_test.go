package webfront

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestSkillStageMatchesNode(t *testing.T) {
	raw, err := os.ReadFile("testdata/skillstage.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		Cwd       string   `json:"cwd"`
		Roots     []string `json:"roots"`
		Decisions []struct {
			ToolCalls []struct {
				Name  string          `json:"name"`
				Input json.RawMessage `json:"input"`
			} `json:"toolCalls"`
			Capture bool   `json:"capture"`
			Reason  string `json:"reason"`
		} `json:"decisions"`
		Diffs []struct {
			Baseline skillFileHashes `json:"baseline"`
			Box      skillFileHashes `json:"box"`
			Changed  []string        `json:"changed"`
			Removed  []string        `json:"removed"`
		} `json:"diffs"`
		TreeFiles map[string]string `json:"treeFiles"`
		Tree      skillFileHashes   `json:"tree"`
		Stages    []struct {
			Steps []struct {
				Builtin string `json:"builtin"`
				Origin  bool   `json:"origin"`
				Write   string `json:"write"`
				Overlay string `json:"overlay"`
				Text    string `json:"text"`
			} `json:"steps"`
			Allowed []string        `json:"allowed"`
			Tree    skillFileHashes `json:"tree"`
		} `json:"stages"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	for i, c := range fx.Decisions {
		var evidence *skillEvidence
		if c.ToolCalls != nil {
			evidence = &skillEvidence{Cwd: fx.Cwd, SkillsRoots: fx.Roots, ToolCalls: []skillToolCall{}}
			for _, call := range c.ToolCalls {
				var input any
				if call.Input != nil {
					if input, err = jsParse(call.Input); err != nil {
						t.Fatal(err)
					}
				}
				evidence.ToolCalls = append(evidence.ToolCalls, skillToolCall{Name: call.Name, Input: input})
			}
		}
		if capture, reason := skillCaptureDecision(evidence); capture != c.Capture || reason != c.Reason {
			t.Errorf("decision %d: %v %s, Node %v %s", i, capture, reason, c.Capture, c.Reason)
		}
	}
	for i, c := range fx.Diffs {
		changed, removed := diffSkillTrees(c.Baseline, c.Box)
		if !slices.Equal(changed, c.Changed) || !slices.Equal(removed, c.Removed) {
			t.Errorf("diff %d: %v %v, Node %v %v", i, changed, removed, c.Changed, c.Removed)
		}
	}
	tree := t.TempDir()
	for p, text := range fx.TreeFiles {
		writeTestFile(t, filepath.Join(tree, p), text)
	}
	if got := hashSkillTree(tree); !mapsEqual(got, fx.Tree) {
		t.Errorf("tree %v, Node %v", got, fx.Tree)
	}

	t.Setenv("OPENNEKO_AGENT_RUNS_DIR", t.TempDir())
	t.Setenv("OPENNEKO_SANDBOX_URL", "")
	for i, c := range fx.Stages {
		t.Setenv("HOME", t.TempDir())
		host, err := turnWorkspace("org", "thread", "run")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.RemoveAll(host.SkillsRoot); err != nil {
			t.Fatal(err)
		}
		for _, step := range c.Steps {
			switch {
			case step.Builtin != "":
				if err := copyTree(bundledSkillDir(step.Builtin), filepath.Join(host.SkillsRoot, step.Builtin)); err != nil {
					t.Fatal(err)
				}
				if step.Origin {
					hash, _ := fingerprintSkillTree(bundledSkillDir(step.Builtin))
					writeTestFile(t, filepath.Join(host.SkillsRoot, step.Builtin, skillOriginFile), `{"kind":"builtin","sourceHash":"`+hash+`"}`+"\n")
				}
			case step.Write != "":
				writeTestFile(t, filepath.Join(host.SkillsRoot, step.Write), step.Text)
			default:
				hash, _ := fingerprintSkillTree(filepath.Join(host.SkillsRoot, step.Overlay))
				writeTestFile(t, filepath.Join(host.OrgRoot, "skill-overlays", step.Overlay, "LEARNED.md"), strings.Replace(step.Text, "BASE", hash, 1))
			}
		}
		st, err := stageTurn(host, stageOptions{Allowed: c.Allowed})
		if err != nil {
			t.Fatal(err)
		}
		if got := hashSkillTree(st.Workspace.SkillsRoot); !mapsEqual(got, c.Tree) {
			t.Errorf("stage %d: %v\nNode %v", i, got, c.Tree)
		}
		os.RemoveAll(st.Dir)
	}
}

func mapsEqual(a, b skillFileHashes) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

func writeTestFile(t *testing.T, p, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestStageTurn(t *testing.T) {
	runs := t.TempDir()
	t.Setenv("OPENNEKO_AGENT_RUNS_DIR", runs)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("OPENNEKO_SANDBOX_URL", "")
	host, err := turnWorkspace("org", "thread-1", "run-1")
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(host.MemoryRoot, "secret.md"), "memory")
	writeTestFile(t, filepath.Join(host.UploadsRoot, "thread-2", "other.csv"), "other thread")
	writeTestFile(t, filepath.Join(host.RunsRoot, "run-0", "old.txt"), "other run")
	writeTestFile(t, filepath.Join(host.ThreadUploadsRoot, "mine.csv"), "a,b\n")
	writeTestFile(t, filepath.Join(host.RunRoot, "notes.txt"), "run file")
	writeTestFile(t, filepath.Join(host.KnowledgeRoot, "extra.md"), "loose knowledge")
	writeTestFile(t, filepath.Join(host.SkillsRoot, "report", "SKILL.md"), "# Team report\n")
	writeTestFile(t, filepath.Join(host.SkillsRoot, "report", "scripts", "run.py"), "print(1)\n")
	writeTestFile(t, filepath.Join(host.SkillsRoot, "hidden", "SKILL.md"), "# Hidden\n")
	if _, err := commitUserRef(host.OrgRoot, "member-1", []configFile{{path: "skills/report/SKILL.md", content: "# My report\n"},
		{path: "skills/hidden/SKILL.md", content: "# Mine too\n"}}, "mine", true); err != nil {
		t.Fatal(err)
	}

	st, err := stageTurn(host, stageOptions{Allowed: []string{"report", "graphjin-config"}, Actor: runActorRow{UserID: "member-1", Role: "member"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(st.Dir, filepath.Join(runs, "openneko-turn."+stageBootID+".")) {
		t.Fatalf("stage dir %s", st.Dir)
	}
	ws := st.Workspace
	if ws.OrgRoot != filepath.Join(st.Dir, "workspace", "org") || ws.ArtifactRoot != filepath.Join(ws.OrgRoot, "runs", "run-1", "artifacts") {
		t.Fatalf("workspace %+v", ws)
	}
	read := func(p string) string {
		data, err := os.ReadFile(p)
		if err != nil {
			return "<none>"
		}
		return string(data)
	}
	for p, want := range map[string]string{
		filepath.Join(ws.MemoryRoot, "secret.md"):                   "<none>",
		filepath.Join(ws.UploadsRoot, "thread-2", "other.csv"):      "<none>",
		filepath.Join(ws.RunsRoot, "run-0", "old.txt"):              "<none>",
		filepath.Join(ws.ThreadUploadsRoot, "mine.csv"):             "a,b\n",
		filepath.Join(ws.RunRoot, "notes.txt"):                      "run file",
		filepath.Join(ws.KnowledgeRoot, "extra.md"):                 "loose knowledge",
		filepath.Join(ws.SkillsRoot, "report", "SKILL.md"):          "# My report\n",
		filepath.Join(ws.SkillsRoot, "report", "scripts", "run.py"): "print(1)\n",
		filepath.Join(ws.SkillsRoot, "hidden", "SKILL.md"):          "# Mine too\n",
	} {
		if got := read(p); got != want {
			t.Errorf("%s: %q, want %q", p, got, want)
		}
	}
	if _, err := os.Stat(filepath.Join(ws.SkillsRoot, "graphjin-config", "SKILL.md")); err != nil {
		t.Errorf("the built-in skill is not staged: %v", err)
	}
	if st.baseline["report/SKILL.md"] != skillFileHash([]byte("# My report\n")) || st.baseline["report/scripts/run.py"] != skillFileHash([]byte("print(1)\n")) {
		t.Errorf("baseline %v", st.baseline)
	}
	for _, p := range []string{st.Dir, filepath.Join(ws.SkillsRoot, "report", "SKILL.md")} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if mode := info.Mode().Perm(); mode&0o007 != 0 || mode&0o060 != 0o060 || info.IsDir() && mode&0o010 == 0 {
			t.Errorf("%s mode %v", p, mode)
		}
	}

	writeTestFile(t, filepath.Join(ws.ArtifactRoot, "chart.png"), "png")
	writeTestFile(t, filepath.Join(ws.ArtifactRoot, "deep", "table.csv"), "x")
	if err := os.Symlink("/etc/passwd", filepath.Join(ws.ArtifactRoot, "link")); err != nil {
		t.Fatal(err)
	}
	if err := st.copyBack(); err != nil {
		t.Fatal(err)
	}
	if read(filepath.Join(host.ArtifactRoot, "chart.png")) != "png" || read(filepath.Join(host.ArtifactRoot, "deep", "table.csv")) != "x" {
		t.Error("the artifacts did not come back")
	}
	if _, err := os.Lstat(filepath.Join(host.ArtifactRoot, "link")); err == nil {
		t.Error("a link came back")
	}

	t.Run("knowledge snapshot", func(t *testing.T) {
		files := map[string]string{}
		for _, name := range knowledgeSnapshotFiles {
			files[name] = "{}"
			if !strings.HasSuffix(name, ".json") {
				files[name] = "# " + name
			}
		}
		if err := publishKnowledgeSnapshot(host.KnowledgeRoot, knowledgeSnapshot{Source: "s", Revision: "r", Files: files}, nil); err != nil {
			t.Fatal(err)
		}
		st2, err := stageTurn(host, stageOptions{})
		if err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(st2.Dir)
		entries, _ := os.ReadDir(st2.Workspace.KnowledgeRoot)
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		slices.Sort(names)
		want := slices.Clone(knowledgeSnapshotFiles)
		slices.Sort(want)
		if !slices.Equal(names, want) {
			t.Fatalf("staged knowledge %v", names)
		}
	})

	t.Run("an aborted turn leaves the artifacts behind and removes the stage", func(t *testing.T) {
		st3, err := stageTurn(host, stageOptions{})
		if err != nil {
			t.Fatal(err)
		}
		writeTestFile(t, filepath.Join(st3.Workspace.ArtifactRoot, "late.csv"), "x")
		st3.finish(context.Background(), &API{}, "org", "run-1", true, &skillEvidence{})
		if _, err := os.Stat(filepath.Join(host.ArtifactRoot, "late.csv")); err == nil {
			t.Error("an aborted turn copied its artifacts back")
		}
		if _, err := os.Stat(st3.Dir); err == nil {
			t.Error("the stage is still there")
		}
	})
}

func TestReadPersonalSkillFiles(t *testing.T) {
	root := t.TempDir()
	if got := readPersonalSkillFiles(root, "member-1"); got != nil {
		t.Fatalf("no repo: %v", got)
	}
	if err := ensureConfigRepo(root); err != nil {
		t.Fatal(err)
	}
	if got := readPersonalSkillFiles(root, "member-1"); got != nil {
		t.Fatalf("no ref: %v", got)
	}
	if _, err := commitUserRef(root, "member-1", []configFile{{path: "skills/a/SKILL.md", content: "A"},
		{path: "skills/top.md", content: "top"}, {path: "skills/a/__pycache__/x.pyc", content: "x"},
		{path: "memory/rule.md", content: "rule"}}, "mine", false); err != nil {
		t.Fatal(err)
	}
	got := readPersonalSkillFiles(root, "member-1")
	if len(got) != 1 || got[0].Path != "a/SKILL.md" || string(got[0].Content) != "A" {
		t.Fatalf("%+v", got)
	}
}

func TestSweepStages(t *testing.T) {
	runs := t.TempDir()
	t.Setenv("OPENNEKO_AGENT_RUNS_DIR", runs)
	for _, name := range []string{"openneko-turn.earlier.a", "openneko-turn." + stageBootID + ".b", "openneko.earlier.c", "openneko-turnx.earlier.d"} {
		if err := os.MkdirAll(filepath.Join(runs, name, "workspace"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	gone, err := SweepStages()
	if err != nil || !slices.Equal(gone, []string{"openneko-turn.earlier.a"}) {
		t.Fatalf("%v %v", gone, err)
	}
	entries, _ := os.ReadDir(runs)
	if len(entries) != 3 {
		t.Fatalf("left %d", len(entries))
	}
	t.Setenv("OPENNEKO_AGENT_RUNS_DIR", filepath.Join(runs, "missing"))
	if gone, err := SweepStages(); err != nil || gone != nil {
		t.Fatalf("missing folder: %v %v", gone, err)
	}
}
