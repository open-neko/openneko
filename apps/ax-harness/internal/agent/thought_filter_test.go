package agent

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestPlainContractNamesInAPlanLine(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Write the answer with neko_cards and neko_followups", "Write the answer with charts and tables and follow-up questions"},
		{"Fill `neko_vitals` and `neko_value`", "Fill key figures and time saved"},
		{"Build the neko_a2ui table", "Build the charts and tables table"},
		{"Query sales by territory", "Query sales by territory"},
		{"Read neko_cardsets", "Read neko_cardsets"},
	}
	for _, c := range cases {
		if got := plainContractNames(c.in); got != c.want {
			t.Errorf("%q gave %q, want %q", c.in, got, c.want)
		}
		if strings.Contains(plainContractNames(c.in), "neko_cards ") {
			t.Errorf("%q kept a contract name", c.in)
		}
	}
}

func TestThoughtFilterDropsProgramHeadings(t *testing.T) {
	cases := []struct {
		name   string
		chunks []string
		want   string
	}{
		{"plain headings stay", []string{"**Counting the orders**\n\nThe query returned one row.", "\n\n**Writing the reply**\n\nOne sentence."},
			"**Counting the orders**\n\nThe query returned one row.\n\n**Writing the reply**\n\nOne sentence."},
		{"a code heading goes", []string{"**Determining `answer` Formatting**\n\nI keep it short."}, "\nI keep it short."},
		{"a program word heading goes", []string{"**Confirming DSPy ", "Parsing**\n\nThe fields line up."}, "\nThe fields line up."},
		{"a markdown heading goes", []string{"## Checking the JSON schema\nDone."}, "Done."},
		{"a contract name split over chunks", []string{"I will add neko_", "vit", "als now."}, "I will add key figures now."},
		{"a backtick contract name", []string{"Then `neko_followups` last."}, "Then follow-up questions last."},
		{"a heading at the end", []string{"Some text.\n**Writing the Final Output**"}, "Some text.\n"},
		{"a bold span inside a line is body", []string{"The top region is **Northwest** today."}, "The top region is **Northwest** today."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &thoughtFilter{}
			var out strings.Builder
			for _, chunk := range c.chunks {
				out.WriteString(f.add(chunk))
			}
			out.WriteString(f.end())
			if out.String() != c.want {
				t.Fatalf("got %q, want %q", out.String(), c.want)
			}
		})
	}
}

func TestThoughtFilterSendsBodyTextAtOnce(t *testing.T) {
	f := &thoughtFilter{}
	if got := f.add("**Comparing the regions**\n\nThe Northwest "); got != "**Comparing the regions**\n\nThe Northwest " {
		t.Fatalf("got %q", got)
	}
	if got := f.add("leads with"); got != "leads " {
		t.Fatalf("got %q", got)
	}
	if got := f.add(" 4,200 orders."); got != "with 4,200 orders." {
		t.Fatalf("got %q", got)
	}
	if got := f.add("\n**Checking"); got != "\n" {
		t.Fatalf("a part heading was sent: %q", got)
	}
}

func TestRunKeepsProgramTermsOffThePlanAndTheThinking(t *testing.T) {
	raw, err := os.ReadFile("testdata/thinking/google-gemini.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture thinkingFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	saved := thinkingActorReply
	thinkingActorReply = `{"javascriptCode":"final('Write neko_cards and neko_followups',{})"}`
	t.Cleanup(func() { thinkingActorReply = saved })
	part := func(text string) string {
		return `data: {"candidates": [{"content": {"role": "model", "parts": [{"text": ` + jsonString(text) + `, "thought": true}]}}], "modelVersion": "gemini-3.8-flash"}`
	}
	fixture.Stream = []string{
		part("**Confirming DSPy "), "",
		part("Parsing**\n\nI add the neko_"), "",
		part("vitals block.\n\n**Writing the reply**\n\nShort."), "",
		`data: {"candidates": [{"content": {"role": "model", "parts": [{"text": "Answer: done"}]}, "finishReason": "STOP"}], "modelVersion": "gemini-3.8-flash"}`, "",
	}
	var streamed []string
	server := thinkingServer(t, fixture, &streamed)
	var thoughts, steps strings.Builder
	result, err := RunWithTools(context.Background(), Spec{Version: 1, RunID: "plain-terms", InputID: "i", Prompt: "How many orders?",
		ReasoningEffort: "low", StreamResponses: true}, thinkingClient(fixture, server.URL), Tools{}, func(e Event) error {
		var data struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(e.Data, &data)
		switch e.Type {
		case "thought.delta":
			thoughts.WriteString(data.Text)
		case "actor.step":
			steps.WriteString(data.Text)
		}
		return nil
	})
	if err != nil || result.Status != "completed" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if steps.String() != "Write charts and tables and follow-up questions" {
		t.Fatalf("plan line %q", steps.String())
	}
	if want := "\nI add the key figures block.\n\n**Writing the reply**\n\nShort."; thoughts.String() != want {
		t.Fatalf("thinking %q, want %q", thoughts.String(), want)
	}
}
