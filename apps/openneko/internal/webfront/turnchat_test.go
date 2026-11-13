package webfront

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func chatFixture(t *testing.T) *jsObj {
	t.Helper()
	raw, err := os.ReadFile("testdata/chatturn.json")
	if err != nil {
		t.Fatal(err)
	}
	v, err := jsParse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return v.(*jsObj)
}

func payloadJS(p *jsObj) any {
	if p == nil {
		return nil
	}
	return p
}

// sameReasons compares fence errors: a zod reason must match byte for byte;
// a JSON.parse reason is V8's text, which Go gives as "invalid JSON".
func sameReasons(t *testing.T, label string, got []string, want any) {
	t.Helper()
	list := jsArray(want)
	if len(got) != len(list) {
		t.Errorf("%s: %d errors, Node has %d: %q", label, len(got), len(list), got)
		return
	}
	for i, w := range list {
		reason := w.(string)
		if strings.HasPrefix(reason, "[") {
			if got[i] != reason {
				t.Errorf("%s error %d:\n%s\nNode:\n%s", label, i, got[i], reason)
			}
		} else if got[i] != "invalid JSON" {
			t.Errorf("%s error %d: %q for a JSON error", label, i, got[i])
		}
	}
}

func TestChatTurnHelpersMatchNode(t *testing.T) {
	f := chatFixture(t)
	same := func(label string, got, want any) {
		t.Helper()
		if g, w := jsStringify(got, false), jsStringify(want, false); g != w {
			t.Errorf("%s:\n got %s\nNode %s", label, g, w)
		}
	}
	for i, c := range jsArray(f.vals["fences"]) {
		raw := jsGet(c, "raw").(string)
		label := func(kind string) string { return kind + " " + string(rune('A'+i)) }
		check := func(kind string, got fenceResult, single bool, first bool) {
			want := jsGet(c, kind)
			same(label(kind)+" text", got.Text, jsGet(want, "text"))
			if single {
				p := got.last()
				if first {
					p = got.first()
				}
				same(label(kind)+" payload", payloadJS(p), jsGet(want, "payload"))
			} else {
				list := make([]any, len(got.Payloads))
				for i, p := range got.Payloads {
					list[i] = p
				}
				same(label(kind)+" payloads", list, jsGet(want, "payloads"))
			}
			sameReasons(t, label(kind), got.Errors, jsGet(want, "errors"))
		}
		check("workflow", extractFence(raw, workflowSaveFence, workflowSaveShape, true), true, true)
		check("actions", extractFence(raw, actionRequestFence, actionRequestShape, false), false, false)
		check("rule", extractFence(raw, ruleSaveFence, policySaveShape, true), true, true)
		check("value", extractFence(raw, valueFence, valueEstimateShape, false), true, false)
		check("followups", extractFence(raw, followupsFence, followupsShape, false), true, false)
		check("vitals", extractFence(raw, vitalsFence, vitalsShape, false), true, false)
		text, ops := extractMemoryFences(raw)
		list := []any{}
		for _, op := range ops {
			var scope, pinned any
			if op.Scope != "" {
				scope = op.Scope
			}
			if op.Pinned != nil {
				pinned = *op.Pinned
			}
			list = append(list, jsObjOf("text", op.Text, "scope", scope, "pinned", pinned))
		}
		same(label("memory"), jsObjOf("text", text, "ops", list), jsGet(c, "memory"))
		same(label("display"), displayText(raw), jsGet(c, "display"))
	}
	for _, c := range jsArray(f.vals["dangling"]) {
		same("dangling", stripDanglingToolCalls(jsGet(c, "text").(string)), jsGet(c, "out"))
	}
	for _, c := range jsArray(f.vals["questions"]) {
		same("question", needsInputText(jsGet(c, "event")), jsGet(c, "out"))
	}
	for _, c := range jsArray(f.vals["denials"]) {
		var got any
		if d := networkPolicyDenial(jsGet(c, "event")); d != nil {
			got = d
		}
		same("denial "+jsStringOf(jsGet(jsGet(c, "event"), "id")), got, jsGet(c, "out"))
	}
	for i, c := range jsArray(f.vals["summaries"]) {
		vitals, _ := jsGet(c, "vitals").(*jsObj)
		var notes []string
		for _, n := range jsArray(jsGet(c, "notes")) {
			notes = append(notes, n.(string))
		}
		same("summary "+string(rune('A'+i)), structuredTurnSummary(vitals, jsArray(jsGet(c, "surfaces")), notes), jsGet(c, "out"))
	}
	for _, c := range jsArray(f.vals["minutes"]) {
		value := jsGet(c, "value")
		if value == "NaN" {
			value = math.NaN()
		}
		same("minutes", clampAnalysisMinutes(value), jsGet(c, "out"))
	}
}

func TestDiscoverRunArtifacts(t *testing.T) {
	org := t.TempDir()
	ws := agentWorkspace{OrgRoot: org, ArtifactRoot: filepath.Join(org, "runs", "r1", "artifacts")}
	outside := filepath.Join(org, "secret.txt")
	for path, body := range map[string]string{
		filepath.Join(ws.ArtifactRoot, "b.csv"):           "b",
		filepath.Join(ws.ArtifactRoot, "A.md"):            "a",
		filepath.Join(ws.ArtifactRoot, "charts", "c.png"): "c",
		outside: "s",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(ws.ArtifactRoot, "link.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(org, filepath.Join(ws.ArtifactRoot, "orglink")); err != nil {
		t.Fatal(err)
	}
	got := discoverRunArtifacts(ws)
	want := []runArtifact{{"runs/r1/artifacts/A.md", "A.md"}, {"runs/r1/artifacts/b.csv", "b.csv"}, {"runs/r1/artifacts/charts/c.png", "c.png"}}
	if len(got) != len(want) {
		t.Fatalf("artifacts %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("artifacts %+v", got)
		}
	}
	if got := discoverRunArtifacts(agentWorkspace{OrgRoot: org, ArtifactRoot: filepath.Join(org, "none")}); got != nil {
		t.Fatalf("a missing folder has no artifacts: %+v", got)
	}
}
