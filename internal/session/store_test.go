package session

import (
	"context"
	"encoding/json"
	"errors"
	ax "github.com/ax-llm/ax/packages/go"
	"github.com/open-neko/harness/internal/agent"
	"os"
	"testing"
)

func TestInterruptedAdmissionCannotBeReplayed(t *testing.T) {
	root := t.TempDir()
	spec := agent.Spec{Version: 1, RunID: "r", InputID: "i", Prompt: "q"}
	client := ax.NewOpenAICompatibleClient(ax.Object("base_url", "http://127.0.0.1:1", "api_key", "synthetic", "model", "fixture"))
	called := false
	lookup := func(context.Context, string) (json.RawMessage, error) { called = true; return nil, nil }
	_, err := Run(context.Background(), root, spec, client, lookup, func(agent.Event) error { return errors.New("sink failed") })
	if err == nil || called {
		t.Fatal("failure did not stop admission")
	}
	_, err = Run(context.Background(), root, spec, client, lookup, func(agent.Event) error { t.Fatal("interrupted run replayed"); return nil })
	if err == nil {
		t.Fatal("interrupted run accepted")
	}
	spec.Prompt = "changed"
	if _, err = Run(context.Background(), root, spec, client, lookup, func(agent.Event) error { return nil }); err == nil {
		t.Fatal("conflicting input accepted")
	}
}

func TestInvalidInputIsNotPersisted(t *testing.T) {
	root := t.TempDir()
	_, err := Run(context.Background(), root, agent.Spec{Version: 1, RunID: "r", InputID: "i", Prompt: "   "}, nil, nil, func(agent.Event) error { return nil })
	if err == nil {
		t.Fatal("blank prompt accepted")
	}
	files, err := os.ReadDir(root)
	if err != nil || len(files) != 0 {
		t.Fatal("invalid input created durable state")
	}
}
