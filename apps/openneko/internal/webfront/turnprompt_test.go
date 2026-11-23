package webfront

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type promptFixture struct {
	Name         string          `json:"name"`
	FilesEnabled bool            `json:"filesEnabled"`
	Input        workPromptInput `json:"input"`
	Bytes        int             `json:"bytes"`
	SHA256       string          `json:"sha256"`
}

func TestWorkPromptMatchesNode(t *testing.T) {
	raw, err := os.ReadFile("testdata/workprompt.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []promptFixture
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 6 {
		t.Fatalf("fixture holds %d cases", len(cases))
	}
	dump := os.Getenv("PROMPT_DUMP_DIR")
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			in := c.Input
			in.FilesEnabled = c.FilesEnabled
			got := buildWorkPrompt(in)
			sum := sha256.Sum256([]byte(got))
			if dump != "" {
				_ = os.WriteFile(filepath.Join(dump, strings.ReplaceAll(c.Name, " ", "_")+".go.txt"), []byte(got), 0o644)
			}
			if len(got) != c.Bytes || hex.EncodeToString(sum[:]) != c.SHA256 {
				t.Fatalf("prompt differs from Node: %d bytes, want %d", len(got), c.Bytes)
			}
		})
	}
}

func TestTurnSectionsParse(t *testing.T) {
	for _, name := range []string{"cards", "rich_card_examples", "memory_fence", "ax_delegation", "transcript"} {
		if turnSections[name] == "" {
			t.Fatalf("section %s is empty", name)
		}
	}
	if !strings.HasPrefix(turnSections["rich_card_examples"], "\nA comparison:") ||
		!strings.HasSuffix(turnSections["rich_card_examples"], "step\"}\n") {
		t.Fatalf("rich_card_examples lost its edge newlines: %q", turnSections["rich_card_examples"])
	}
}

func TestThePromptStatesTheClosingBlockLimitsOfTheSchema(t *testing.T) {
	prompt := buildWorkPrompt(workPromptInput{IncludeUxMetadata: true})
	for _, want := range []string{
		"Keep `basis` to 160 characters.",
		"Keep each `label` to 28 characters, each `value` to\n   18 characters, and each `sub` to 36 characters.",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the prompt lacks %q", want)
		}
	}
	if strings.Contains(prompt, "${") {
		t.Error("the prompt holds a placeholder")
	}
	if flow := buildWorkflowPrompt(workflowPromptInput{IncludeUxMetadata: true}); !strings.Contains(flow, "Keep `basis` to 160 characters.") || strings.Contains(flow, "${") {
		t.Error("the workflow prompt lacks the basis limit")
	}
	old := vitalsShape.fields[0].typ.(*zArr).elem.(*zObj).fields[0].typ.(*zStr).max
	t.Cleanup(func() { vitalsShape.fields[0].typ.(*zArr).elem.(*zObj).fields[0].typ.(*zStr).max = old })
	vitalsShape.fields[0].typ.(*zArr).elem.(*zObj).fields[0].typ.(*zStr).max = num(30)
	if !strings.Contains(buildWorkPrompt(workPromptInput{IncludeUxMetadata: true}), "Keep each `label` to 30 characters") {
		t.Error("the prompt does not follow the schema")
	}
}

func TestEveryPromptCopyHoldsTheFailedStepRule(t *testing.T) {
	rule := section("step_failure_rule")
	for _, want := range []string{"the last run of the skill's own script", "the step failed and why",
		"Never edit a\n  result so that it passes a check", `"no exact match"`, "Never reuse an output"} {
		if !strings.Contains(rule, want) {
			t.Errorf("the rule lacks %q", want)
		}
	}
	if !strings.Contains(buildWorkPrompt(workPromptInput{}), rule) {
		t.Error("the chat prompt lacks the rule")
	}
	if !strings.Contains(buildWorkflowPrompt(workflowPromptInput{}), rule) {
		t.Error("the workflow prompt lacks the rule")
	}
	for _, path := range []string{"../../../../packages/llm/src/work/prompt.ts", "../../../../packages/llm/src/workflows/runner-prompt.ts"} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), "${GRAPHJIN_DATE_RULE}\n"+rule+"\n") {
			t.Errorf("%s lacks the rule after the date rule", path)
		}
	}
}
