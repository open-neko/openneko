package webfront

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/open-neko/openneko/apps/ax-harness/sandbox"
)

type workflowFixture struct {
	Prompts []struct {
		Name  string `json:"name"`
		Input struct {
			workflowPromptInput
			Steps json.RawMessage `json:"steps"`
		} `json:"input"`
		Bytes  int    `json:"bytes"`
		SHA256 string `json:"sha256"`
	} `json:"prompts"`
	Seeds []struct {
		Name, TriggerKind, UserMessage, Now, Text string
		Payload                                   json.RawMessage
	} `json:"seeds"`
	Expands []struct {
		Arg           string            `json:"arg"`
		Input         json.RawMessage   `json:"input"`
		Vars          map[string]string `json:"vars"`
		Result, Error *string
	} `json:"expands"`
	StepResults []struct {
		Results []scriptStepResult `json:"results"`
		Text    string             `json:"text"`
	} `json:"stepResults"`
	Servers []struct {
		FilesEnabled bool     `json:"filesEnabled"`
		Servers      []string `json:"servers"`
	} `json:"servers"`
	Continuations []struct {
		Name             string `json:"name"`
		MaxContinuations int    `json:"maxContinuations"`
		Turns            []struct {
			Events []json.RawMessage `json:"events"`
			Result axResultFixture   `json:"result"`
		} `json:"turns"`
		Prompts []string          `json:"prompts"`
		Events  []json.RawMessage `json:"events"`
		Result  axResultFixture   `json:"result"`
	} `json:"continuations"`
	Steps []stepScenarioFixture `json:"steps"`
}

type axResultFixture struct {
	FinalText string `json:"finalText"`
	Status    string `json:"status"`
	Error     string `json:"error"`
	TimedOut  bool   `json:"timedOut"`
}

type sandboxAnswerFixture struct {
	ExitCode int    `json:"exit_code"`
	Output   string `json:"output"`
	TimedOut bool   `json:"timed_out,omitempty"`
	Changed  []struct {
		Path string `json:"path"`
		Mode int    `json:"mode"`
		Data string `json:"data"`
	} `json:"changed,omitempty"`
}

type seenRequest struct {
	Command        string            `json:"command"`
	Dir            string            `json:"dir,omitempty"`
	SkillDir       string            `json:"skill_dir,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	TimeoutSeconds int               `json:"timeout_seconds"`
	MaxOutput      int               `json:"max_output"`
	Fresh          bool              `json:"fresh"`
	Put            []string          `json:"put"`
}

type stepScenarioFixture struct {
	Name     string                 `json:"name"`
	Sandbox  bool                   `json:"sandbox"`
	Allowed  []string               `json:"allowed"`
	Input    json.RawMessage        `json:"input"`
	Steps    json.RawMessage        `json:"steps"`
	Answers  []sandboxAnswerFixture `json:"answers"`
	Results  []scriptStepResult     `json:"results"`
	Events   []json.RawMessage      `json:"events"`
	Requests []seenRequest          `json:"requests"`
	Ended    int                    `json:"ended"`
	Files    []string               `json:"files"`
}

func loadWorkflowFixture(t *testing.T) workflowFixture {
	t.Helper()
	raw, err := os.ReadFile("testdata/workflowturn.json")
	if err != nil {
		t.Fatal(err)
	}
	var f workflowFixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Prompts) < 4 || len(f.Seeds) < 9 || len(f.Expands) < 14 || len(f.Continuations) < 5 || len(f.Steps) < 6 {
		t.Fatalf("fixture is short: %d prompts, %d seeds, %d expands, %d continuations, %d step scenarios",
			len(f.Prompts), len(f.Seeds), len(f.Expands), len(f.Continuations), len(f.Steps))
	}
	return f
}

func mustJS(t *testing.T, raw []byte) any {
	t.Helper()
	v, err := jsParse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestWorkflowPromptMatchesNode(t *testing.T) {
	f := loadWorkflowFixture(t)
	dump := os.Getenv("PROMPT_DUMP_DIR")
	for _, c := range f.Prompts {
		t.Run(c.Name, func(t *testing.T) {
			in := c.Input.workflowPromptInput
			in.Steps = jsArray(mustJS(t, c.Input.Steps))
			got := buildWorkflowPrompt(in)
			sum := sha256.Sum256([]byte(got))
			if dump != "" {
				_ = os.WriteFile(filepath.Join(dump, strings.ReplaceAll(c.Name, " ", "_")+".go.txt"), []byte(got), 0o644)
			}
			if len(got) != c.Bytes || hex.EncodeToString(sum[:]) != c.SHA256 {
				t.Fatalf("prompt differs from Node: %d bytes, want %d", len(got), c.Bytes)
			}
		})
	}
	for _, c := range f.Seeds {
		now, err := time.Parse(time.RFC3339Nano, c.Now)
		if err != nil {
			t.Fatal(err)
		}
		var payload any
		if string(c.Payload) != "null" {
			payload = mustJS(t, c.Payload)
		}
		if got := workflowSeedMessage(c.Name, c.TriggerKind, c.UserMessage, payload, now); got != c.Text {
			t.Errorf("seed %s %s:\n%q\nwant\n%q", c.TriggerKind, c.Payload, jsSlice(got, 300), jsSlice(c.Text, 300))
		}
	}
	for _, c := range f.Servers {
		if got := workflowCoreServers(c.FilesEnabled); !slices.Equal(got, c.Servers) {
			t.Errorf("servers with files %v = %v, want %v", c.FilesEnabled, got, c.Servers)
		}
	}
}

func TestWorkflowStepTextMatchesNode(t *testing.T) {
	f := loadWorkflowFixture(t)
	for _, c := range f.Expands {
		got, err := expandStepArg(c.Arg, c.Vars, mustJS(t, c.Input))
		switch {
		case c.Error != nil && (err == nil || err.Error() != *c.Error):
			t.Errorf("expand %q: %q, %v; want error %q", c.Arg, got, err, *c.Error)
		case c.Error == nil && (err != nil || got != *c.Result):
			t.Errorf("expand %q: %q, %v; want %q", c.Arg, got, err, *c.Result)
		}
	}
	for i, c := range f.StepResults {
		if got := formatStepResults(c.Results); got != c.Text {
			t.Errorf("step results %d:\n%s\nwant\n%s", i, got, c.Text)
		}
	}
	for id, want := range map[string]string{"pull data/1": "pull_data_1", "": "step", "a😀b": "a__b", strings.Repeat("é", 70): strings.Repeat("_", 60)} {
		if got := safeStepName(id); got != want {
			t.Errorf("safeStepName(%q) = %q, want %q", id, got, want)
		}
	}
}

func fixtureEventsJSON(t *testing.T, raws []json.RawMessage) []string {
	out := make([]string, len(raws))
	for i, raw := range raws {
		out[i] = jsStringify(mustJS(t, raw), false)
	}
	return out
}

func TestWorkflowContinuationsMatchNode(t *testing.T) {
	f := loadWorkflowFixture(t)
	for _, c := range f.Continuations {
		t.Run(c.Name, func(t *testing.T) {
			log := &eventLog{}
			st := &workflowTurnState{emit: log.emit}
			turns := c.Turns
			var prompts []string
			core := func(ctx context.Context, in agentCoreInput) (axRunResult, error) {
				prompts = append(prompts, in.Prompt)
				turn := turns[0]
				turns = turns[1:]
				for _, raw := range turn.Events {
					if err := in.Emit(ctx, mustJS(t, raw)); err != nil {
						return axRunResult{}, err
					}
				}
				r := turn.Result
				return axRunResult{FinalText: r.FinalText, Status: r.Status, Error: r.Error, TimedOut: r.TimedOut}, nil
			}
			ws := agentWorkspace{OrgRoot: "/org"}
			res, err := (&API{}).workflowStagedCore(context.Background(), workflowTurnOptions{runCore: core}, st,
				agentCoreInput{Prompt: "BASE PROMPT", Emit: st.wrapped}, &turnStage{Workspace: ws, host: ws}, []any{}, nil, false, c.MaxContinuations)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(prompts, c.Prompts) {
				t.Fatalf("prompts differ:\n%q\nwant\n%q", prompts, c.Prompts)
			}
			if got, want := log.all(), fixtureEventsJSON(t, c.Events); !slices.Equal(got, want) {
				t.Fatalf("events differ:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
			if got := (axResultFixture{res.FinalText, res.Status, res.Error, res.TimedOut}); got != c.Result {
				t.Fatalf("result %+v, want %+v", got, c.Result)
			}
		})
	}
}

type fakeStepSandbox struct {
	mu      sync.Mutex
	answers []sandboxAnswerFixture
	seen    []seenRequest
	ended   int
}

func (f *fakeStepSandbox) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Method == http.MethodDelete {
		f.ended++
		w.WriteHeader(http.StatusNoContent)
		return
	}
	raw, _ := io.ReadAll(r.Body)
	var body struct {
		seenRequest
		Put []struct{ Path string } `json:"put"`
	}
	_ = json.Unmarshal(raw, &body)
	seen := body.seenRequest
	seen.Put = []string{}
	for _, p := range body.Put {
		seen.Put = append(seen.Put, p.Path)
	}
	sort.Strings(seen.Put)
	f.seen = append(f.seen, seen)
	answer := sandboxAnswerFixture{}
	if len(f.answers) > 0 {
		answer, f.answers = f.answers[0], f.answers[1:]
	}
	_ = json.NewEncoder(w).Encode(answer)
}

func stepTestWorkspace(t *testing.T, root string) agentWorkspace {
	t.Helper()
	org := filepath.Join(root, "org")
	ws := agentWorkspace{OrgRoot: org, SkillsRoot: filepath.Join(org, "skills"), MemoryRoot: filepath.Join(org, "memory"),
		KnowledgeRoot: filepath.Join(org, "knowledge"), UploadsRoot: filepath.Join(org, "uploads"), RunsRoot: filepath.Join(org, "runs"),
		ThreadUploadsRoot: filepath.Join(org, "uploads", "t1"), RunRoot: filepath.Join(org, "runs", "r1"),
		ArtifactRoot: filepath.Join(org, "runs", "r1", "artifacts"), BinRoot: filepath.Join(org, "runs", "r1", "bin")}
	for path, content := range map[string]string{
		filepath.Join(ws.SkillsRoot, "xlsx", "run.py"):     "print('rows')\n",
		filepath.Join(ws.SkillsRoot, "pdf", "SKILL.md"):    "# pdf\n",
		filepath.Join(ws.RunRoot, ".openneko", "job.json"): "{}",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(ws.ArtifactRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	return ws
}

func listTree(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		content, err := os.ReadFile(path)
		rel, _ := filepath.Rel(dir, path)
		out = append(out, filepath.ToSlash(rel)+": "+string(content))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

var stepDuration = regexp.MustCompile(` in \d+s"`)

func TestWorkflowScriptStepsMatchNode(t *testing.T) {
	f := loadWorkflowFixture(t)
	for _, s := range f.Steps {
		t.Run(s.Name, func(t *testing.T) {
			root := t.TempDir()
			ws := stepTestWorkspace(t, root)
			fake := &fakeStepSandbox{answers: s.Answers}
			server := httptest.NewServer(fake)
			defer server.Close()
			var box *sandbox.Config
			if s.Sandbox {
				box = &sandbox.Config{URL: server.URL}
			}
			log := &eventLog{}
			results, err := runScriptSteps(context.Background(), scriptStepsInput{Steps: jsArray(mustJS(t, s.Steps)), Workspace: ws,
				Input: mustJS(t, s.Input), Allowed: s.Allowed, Sandbox: box, Emit: log.emit})
			if err != nil {
				t.Fatal(err)
			}
			norm := func(v any) string {
				raw, err := json.Marshal(v)
				if err != nil {
					t.Fatal(err)
				}
				return strings.ReplaceAll(string(raw), root, "$ROOT")
			}
			for i := range results {
				results[i].DurationMs = 0
			}
			if got, want := norm(results), norm(s.Results); got != want {
				t.Fatalf("results:\n%s\nwant\n%s", got, want)
			}
			var events []string
			for _, e := range log.all() {
				events = append(events, stepDuration.ReplaceAllString(e, `"`))
			}
			var want []string
			for _, e := range fixtureEventsJSON(t, s.Events) {
				want = append(want, stepDuration.ReplaceAllString(e, `"`))
			}
			if !slices.Equal(events, want) {
				t.Fatalf("events:\n%s\nwant\n%s", strings.Join(events, "\n"), strings.Join(want, "\n"))
			}
			fake.mu.Lock()
			seen, ended := fake.seen, fake.ended
			fake.mu.Unlock()
			if seen == nil {
				seen = []seenRequest{}
			}
			if s.Requests == nil {
				s.Requests = []seenRequest{}
			}
			if got, want := norm(seen), norm(s.Requests); got != want {
				t.Fatalf("requests:\n%s\nwant\n%s", got, want)
			}
			if ended != s.Ended {
				t.Fatalf("ended %d runs, want %d", ended, s.Ended)
			}
			if got := listTree(t, ws.RunsRoot); !slices.Equal(got, s.Files) {
				t.Fatalf("files:\n%q\nwant\n%q", got, s.Files)
			}
		})
	}
}
