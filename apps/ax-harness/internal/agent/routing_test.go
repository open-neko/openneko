package agent

import (
	"context"
	"errors"
	"testing"

	ax "github.com/ax-llm/ax/packages/go"
)

type routeStreams struct {
	ax.AIClient
	streams map[string]func() (ax.AxChatStream, error)
	opened  []string
}

func (c *routeStreams) StreamEvents(_ context.Context, request, _ map[string]ax.Value) (ax.AxChatStream, error) {
	model, _ := request["model"].(string)
	c.opened = append(c.opened, model)
	return c.streams[model]()
}

func outage() error {
	return ax.AxError{Category: "network", Type: "AxAIServiceStatusError", Status: 503, Retryable: true, Message: "outage"}
}

func drainAll(stream ax.AxChatStream) ([]ax.Value, error) {
	var values []ax.Value
	for stream.Next() {
		values = append(values, stream.Value())
	}
	return values, stream.Err()
}

func TestStreamFallbackOnlyBeforeTheFirstValue(t *testing.T) {
	spare := func() (ax.AxChatStream, error) { return &valueStream{values: []ax.Value{"s1", "s2"}}, nil }
	cases := []struct {
		name     string
		primary  func() (ax.AxChatStream, error)
		opened   string
		values   int
		fails    bool
		recorded int
	}{
		{"open fails", func() (ax.AxChatStream, error) { return nil, outage() }, "primary,spare", 2, false, 1},
		{"first read fails", func() (ax.AxChatStream, error) { return failedStream{err: outage()}, nil }, "primary,spare", 2, false, 1},
		{"fails after content", func() (ax.AxChatStream, error) {
			return &thenFail{values: []ax.Value{"p1"}, err: outage()}, nil
		}, "primary", 1, true, 0},
		{"refused", func() (ax.AxChatStream, error) {
			return nil, ax.AxError{Category: "auth", Status: 401, Message: "refused"}
		}, "primary", 0, true, 0},
		{"empty", func() (ax.AxChatStream, error) { return &valueStream{}, nil }, "primary", 0, false, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			inner := &routeStreams{streams: map[string]func() (ax.AxChatStream, error){"primary": c.primary, "spare": spare}}
			recorded := 0
			client := &transientRouteFallback{AIClient: inner, fallbacks: map[string]string{"primary": "spare"},
				before: func(from, to string) error { recorded++; return nil }}
			stream, err := client.StreamEvents(context.Background(), map[string]ax.Value{"model": "primary"}, nil)
			var values []ax.Value
			if err == nil {
				values, err = drainAll(stream)
			}
			opened := ""
			for i, model := range inner.opened {
				if i > 0 {
					opened += ","
				}
				opened += model
			}
			if opened != c.opened || len(values) != c.values || (err != nil) != c.fails || recorded != c.recorded {
				t.Fatalf("opened=%s values=%v err=%v recorded=%d", opened, values, err, recorded)
			}
		})
	}
	inner := &routeStreams{streams: map[string]func() (ax.AxChatStream, error){"primary": func() (ax.AxChatStream, error) { return nil, outage() }, "spare": spare}}
	client := &transientRouteFallback{AIClient: inner, fallbacks: map[string]string{"primary": "spare"},
		before: func(string, string) error { return errors.New("not recorded") }}
	if _, err := client.StreamEvents(context.Background(), map[string]ax.Value{"model": "primary"}, nil); err == nil || len(inner.opened) != 1 {
		t.Fatalf("an unrecorded fallback opened the spare: err=%v opened=%v", err, inner.opened)
	}
}
