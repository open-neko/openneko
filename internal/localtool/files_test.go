package localtool

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	ax "github.com/ax-llm/ax/packages/go"
	"github.com/open-neko/harness/internal/agent"
	"github.com/open-neko/harness/internal/session"
)

func TestFileFreshnessAndContainment(t *testing.T) {
	workspace := t.TempDir()
	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "note.txt"), []byte("first"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "secret.txt"), []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	f, err := OpenFiles(workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cap := f.Capabilities()
	read := func(path string) (string, error) {
		input, _ := json.Marshal(map[string]string{"path": path})
		data, err := cap[0].Call(context.Background(), input)
		var output struct{ Version string }
		_ = json.Unmarshal(data, &output)
		return output.Version, err
	}
	edit := func(version, content string) error {
		input, _ := json.Marshal(map[string]string{"path": "note.txt", "version": version, "content": content})
		_, err := cap[1].Call(context.Background(), input)
		return err
	}
	if err := edit(strings.Repeat("0", 64), "bad"); err == nil {
		t.Fatal("edit before read admitted")
	}
	version, err := read("note.txt")
	if err != nil || len(version) != 64 {
		t.Fatalf("read version=%q err=%v", version, err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "note.txt"), []byte("external"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := edit(version, "bad"); err == nil {
		t.Fatal("stale edit admitted")
	}
	version, err = read("note.txt")
	if err != nil || edit(version, "second") != nil {
		t.Fatalf("fresh edit failed: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(workspace, "note.txt"))
	if err != nil || string(data) != "second" {
		t.Fatalf("edited content=%q err=%v", data, err)
	}
	if _, err := read("../" + filepath.Base(other) + "/secret.txt"); err == nil {
		t.Fatal("cross-run traversal admitted")
	}
	if err := os.Symlink(filepath.Join(other, "secret.txt"), filepath.Join(workspace, "link.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := read("link.txt"); err == nil {
		t.Fatal("symlink escape admitted")
	}
}

func TestReadCanOverlapReadButEditWaits(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "note.txt"), []byte("first"), 0600); err != nil {
		t.Fatal(err)
	}
	f, err := OpenFiles(workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	read := f.Capabilities()[0].Call
	edit := f.Capabilities()[1].Call
	input := json.RawMessage(`{"path":"note.txt"}`)
	result, err := read(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	var version struct{ Version string }
	if err := json.Unmarshal(result, &version); err != nil {
		t.Fatal(err)
	}
	editInput, _ := json.Marshal(map[string]string{"path": "note.txt", "version": version.Version, "content": "second"})
	f.gate.RLock() // Simulates an admitted read still in progress.
	if _, err := read(context.Background(), input); err != nil {
		t.Fatalf("second read did not overlap: %v", err)
	}
	finished := make(chan error, 1)
	go func() { _, err := edit(context.Background(), editInput); finished <- err }()
	select {
	case err := <-finished:
		t.Fatalf("edit overlapped read: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	f.gate.RUnlock()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("edit stayed blocked")
	}
}

func TestFileWriteCreatesOnlyInsideWorkspace(t *testing.T) {
	workspace, other := t.TempDir(), t.TempDir()
	f, err := OpenFiles(workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	write := f.Capabilities()[2].Call
	input := json.RawMessage(`{"path":"result.txt","content":"ready"}`)
	result, err := write(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	var receipt struct{ Path, Version string }
	if json.Unmarshal(result, &receipt) != nil || receipt.Path != "result.txt" || receipt.Version != digest([]byte("ready")) {
		t.Fatalf("invalid write receipt: %s", result)
	}
	if _, err := write(context.Background(), json.RawMessage(`{"path":"result.txt","content":"replaced"}`)); err == nil {
		t.Fatal("write replaced an existing file")
	}
	if data, err := os.ReadFile(filepath.Join(workspace, "result.txt")); err != nil || string(data) != "ready" {
		t.Fatalf("existing file changed: %q %v", data, err)
	}
	if _, err := write(context.Background(), json.RawMessage(`{"path":"../escape.txt","content":"no"}`)); err == nil {
		t.Fatal("write escaped workspace")
	}
	if err := os.Symlink(other, filepath.Join(workspace, "outside")); err != nil {
		t.Fatal(err)
	}
	if _, err := write(context.Background(), json.RawMessage(`{"path":"outside/escape.txt","content":"no"}`)); err == nil {
		t.Fatal("write followed external symlink")
	}
	if _, err := os.Stat(filepath.Join(other, "escape.txt")); !os.IsNotExist(err) {
		t.Fatalf("external file created: %v", err)
	}
}

func TestFileSearchStaysInsideWorkspace(t *testing.T) {
	workspace, other := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(workspace, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "nested", "note.txt"), []byte("The answer is here"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "secret.txt"), []byte("The answer is private"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, filepath.Join(workspace, "outside")); err != nil {
		t.Fatal(err)
	}
	f, err := OpenFiles(workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	result, err := f.Capabilities()[3].Call(context.Background(), json.RawMessage(`{"query":"answer"}`))
	if err != nil {
		t.Fatal(err)
	}
	var found struct {
		Paths     []string
		Truncated bool
	}
	if json.Unmarshal(result, &found) != nil || len(found.Paths) != 1 || found.Paths[0] != "nested/note.txt" || found.Truncated {
		t.Fatalf("unexpected search result: %s", result)
	}
}

func TestUploadCapabilitiesAreReadOnly(t *testing.T) {
	uploads := t.TempDir()
	if err := os.WriteFile(filepath.Join(uploads, "invoice.txt"), []byte("approved"), 0600); err != nil {
		t.Fatal(err)
	}
	f, err := OpenFiles(uploads)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	capabilities := f.UploadCapabilities()
	if len(capabilities) != 2 || capabilities[0].Name != "upload_read" || capabilities[1].Name != "upload_search" {
		t.Fatalf("unexpected upload tool catalog: %+v", capabilities)
	}
	for _, capability := range capabilities {
		if capability.Effect != "read" || capability.Origin != "uploads" {
			t.Fatalf("upload mutation admitted: %+v", capability)
		}
	}
	result, err := capabilities[0].Call(context.Background(), json.RawMessage(`{"path":"invoice.txt"}`))
	if err != nil || !strings.Contains(string(result), "approved") {
		t.Fatalf("upload read failed: %s %v", result, err)
	}
}

func TestSkillCapabilitiesAreReadOnlyAndConfined(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "daily"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "daily", "SKILL.md"), []byte("Use the supplied date."), 0600); err != nil {
		t.Fatal(err)
	}
	f, err := OpenFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	capabilities := f.SkillCapabilities()
	if len(capabilities) != 2 || capabilities[0].Name != "skill_read" || capabilities[1].Name != "skill_search" {
		t.Fatalf("unexpected skill catalog: %+v", capabilities)
	}
	for _, capability := range capabilities {
		if capability.Effect != "read" || capability.Origin != "skills" {
			t.Fatalf("skill mutation admitted: %+v", capability)
		}
	}
	if result, err := capabilities[0].Call(context.Background(), json.RawMessage(`{"path":"daily/SKILL.md"}`)); err != nil || !strings.Contains(string(result), "supplied date") {
		t.Fatalf("skill read failed: %s %v", result, err)
	}
	if _, err := capabilities[0].Call(context.Background(), json.RawMessage(`{"path":"../secret"}`)); err == nil {
		t.Fatal("skill read escaped staged root")
	}
}

func TestUploadReadsUseAxJournal(t *testing.T) {
	uploads := t.TempDir()
	if err := os.WriteFile(filepath.Join(uploads, "invoice.txt"), []byte("Invoice approved"), 0600); err != nil {
		t.Fatal(err)
	}
	f, err := OpenFiles(uploads)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	answers := []string{
		`{"javascriptCode":"final('Find the invoice',{})"}`,
		`{"javascriptCode":"const matches=upload_search({query:'invoice'}); const file=upload_read({path:matches.paths[0]}); final('Found the invoice',{matches,file});"}`,
		`{"answer":"Invoice approved"}`,
	}
	calls := 0
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls >= len(answers) {
			t.Errorf("unexpected model call")
			http.Error(w, "unexpected", 400)
			return
		}
		response := answers[calls]
		calls++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ax.Object("choices", ax.Array(ax.Object("message", ax.Object("role", "assistant", "content", response), "finish_reason", "stop"))))
	}))
	defer model.Close()
	client := ax.NewOpenAICompatibleClient(ax.Object("base_url", model.URL, "api_key", "synthetic", "model", "fixture"))
	spec := agent.Spec{Version: 1, RunID: "upload", InputID: "input", Prompt: "Read the uploaded invoice"}
	state := t.TempDir()
	result, err := session.RunWithTools(context.Background(), state, spec, client, agent.Tools{Capabilities: f.UploadCapabilities(), Scope: uploads}, func(agent.Event) error { return nil })
	if err != nil || result.Status != "completed" || result.Answer != "Invoice approved" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	recovery, err := session.Inspect(state, spec)
	if err != nil || len(recovery.Operations) != 2 || recovery.Operations[0].Tool != "upload_search" || recovery.Operations[1].Tool != "upload_read" {
		t.Fatalf("recovery=%+v err=%v", recovery, err)
	}
}

func TestFileCapabilitiesUseAxJournal(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "note.txt"), []byte("first"), 0600); err != nil {
		t.Fatal(err)
	}
	f, err := OpenFiles(workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	answers := []string{
		`{"javascriptCode":"final('Update the note',{})"}`,
		`{"javascriptCode":"const r=file_read({path:'note.txt'}); const e=file_edit({path:'note.txt',version:r.version,content:'done'}); const w=file_write({path:'result.txt',content:'created'}); final('Updated the note',{r,e,w});"}`,
		`{"answer":"Updated the note"}`,
	}
	calls := 0
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls >= len(answers) {
			t.Errorf("unexpected model call")
			http.Error(w, "unexpected", 400)
			return
		}
		response := answers[calls]
		calls++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ax.Object("choices", ax.Array(ax.Object("message", ax.Object("role", "assistant", "content", response), "finish_reason", "stop"))))
	}))
	defer model.Close()
	client := ax.NewOpenAICompatibleClient(ax.Object("base_url", model.URL, "api_key", "synthetic", "model", "fixture"))
	spec := agent.Spec{Version: 1, RunID: "files", InputID: "input", Prompt: "Update the note"}
	state := t.TempDir()
	result, err := session.RunWithTools(context.Background(), state, spec, client, agent.Tools{Capabilities: f.Capabilities()}, func(agent.Event) error { return nil })
	if err != nil || result.Status != "completed" || result.Kind != "answer" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	recovery, err := session.Inspect(state, spec)
	if err != nil || len(recovery.Operations) != 3 || recovery.Operations[0].Tool != "file_read" || recovery.Operations[1].Tool != "file_edit" || recovery.Operations[2].Tool != "file_write" || !recovery.Operations[2].Finished {
		t.Fatalf("recovery=%+v err=%v", recovery, err)
	}
	data, err := os.ReadFile(filepath.Join(workspace, "note.txt"))
	if err != nil || string(data) != "done" {
		t.Fatalf("edited file=%q err=%v", data, err)
	}
	data, err = os.ReadFile(filepath.Join(workspace, "result.txt"))
	if err != nil || string(data) != "created" {
		t.Fatalf("created file=%q err=%v", data, err)
	}
}

func TestReadVersionRestoresAfterRestart(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "note.txt"), []byte("first"), 0600); err != nil {
		t.Fatal(err)
	}
	first, err := OpenFiles(workspace)
	if err != nil {
		t.Fatal(err)
	}
	readInput := json.RawMessage(`{"path":"note.txt"}`)
	result, err := first.Capabilities()[0].Call(context.Background(), readInput)
	if err != nil {
		t.Fatal(err)
	}
	_ = first.Close()
	var read struct{ Version string }
	if err := json.Unmarshal(result, &read); err != nil {
		t.Fatal(err)
	}
	second, err := OpenFiles(workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if err := second.Restore(context.Background(), []agent.SavedOperation{{Tool: "file_read", ID: 1, Instruction: string(readInput), Result: result, Finished: true}}); err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(map[string]string{"path": "note.txt", "version": read.Version, "content": "second"})
	if _, err := second.Capabilities()[1].Call(context.Background(), input); err != nil {
		t.Fatalf("restored read could not support edit: %v", err)
	}
}

func TestFileReadResumeThenEdit(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "note.txt"), []byte("first"), 0600); err != nil {
		t.Fatal(err)
	}
	answers := []string{
		`{"javascriptCode":"final('Read the note',{})"}`,
		`{"javascriptCode":"const r=file_read({path:'note.txt'}); final('Read',{r});"}`,
		`{"javascriptCode":"final('Continue the update',{})"}`,
		`{"javascriptCode":"const r=file_read({path:'note.txt'}); const e=file_edit({path:'note.txt',version:r.version,content:'done'}); final('Updated',{r,e});"}`,
		`{"answer":"Updated"}`,
	}
	calls := 0
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls >= len(answers) {
			t.Errorf("unexpected model call")
			http.Error(w, "unexpected", 400)
			return
		}
		response := answers[calls]
		calls++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ax.Object("choices", ax.Array(ax.Object("message", ax.Object("role", "assistant", "content", response), "finish_reason", "stop"))))
	}))
	defer model.Close()
	client := ax.NewOpenAICompatibleClient(ax.Object("base_url", model.URL, "api_key", "synthetic", "model", "fixture"))
	spec := agent.Spec{Version: 1, RunID: "files-resume", InputID: "input", Prompt: "Update the note"}
	state := t.TempDir()
	first, err := OpenFiles(workspace)
	if err != nil {
		t.Fatal(err)
	}
	_, err = session.RunWithTools(context.Background(), state, spec, client, agent.Tools{Capabilities: first.Capabilities()}, func(e agent.Event) error {
		if e.Type == "tool.finished" && e.Name == "file_read" {
			return errors.New("delivery interrupted")
		}
		return nil
	})
	_ = first.Close()
	if err == nil {
		t.Fatal("expected interrupted delivery")
	}
	second, err := OpenFiles(workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	reused := 0
	result, err := session.ResumeWithTools(context.Background(), state, spec, client, agent.Tools{Capabilities: second.Capabilities(), OnResume: second.Restore}, func(e agent.Event) error {
		if e.Type == "tool.reused" && e.Name == "file_read" {
			reused++
		}
		return nil
	})
	if err != nil || result.Status != "completed" || result.Kind != "answer" || reused != 1 {
		t.Fatalf("result=%+v err=%v reused=%d", result, err, reused)
	}
	data, err := os.ReadFile(filepath.Join(workspace, "note.txt"))
	if err != nil || string(data) != "done" {
		report, _ := session.Inspect(state, spec)
		t.Fatalf("file=%q err=%v result=%+v report=%+v", data, err, result, report)
	}
}
