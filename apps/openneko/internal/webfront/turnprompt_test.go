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
