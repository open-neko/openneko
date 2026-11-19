package webfront

import (
	"slices"
	"strings"
	"testing"
)

func TestBusinessSectionStaysInTheCachedPrompt(t *testing.T) {
	profile := "# Acme — Business Profile\n\n## What they are\nA bike maker.\n\n## Questions worth asking\n- Which region grew most last quarter?\n2. Who are the top five resellers?\n\n## Scale\n31,465 orders."
	body, questions := splitProfileQuestions(profile)
	if !slices.Equal(questions, []string{"Which region grew most last quarter?", "Who are the top five resellers?"}) {
		t.Fatalf("questions: %q", questions)
	}
	if strings.Contains(body, "Questions worth asking") || strings.Contains(body, "resellers") || !strings.Contains(body, "31,465 orders.") {
		t.Fatalf("body: %q", body)
	}
	prompt := buildWorkPrompt(workPromptInput{Business: businessSection(profile), OperatorProfile: "<operator-profile>\nx\n</operator-profile>"})
	stable, turn := splitStablePrompt(prompt)
	if !strings.Contains(stable, "<business>") || !strings.Contains(stable, "A bike maker.") || strings.Contains(turn, "<business>") {
		t.Fatalf("the business section is not in the stable part")
	}
	if businessSection("") != "" || businessSection("## Questions worth asking\n- One?") != "" {
		t.Fatal("an empty profile must give no section")
	}
	if !strings.Contains(profilerOutput("Acme", ""), profileQuestionsHeading+"\n") {
		t.Fatal("the profiler does not ask for the questions section")
	}
}
