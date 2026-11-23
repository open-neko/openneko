package webfront

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func TestRunTelemetryMatchesNode(t *testing.T) {
	raw, err := os.ReadFile("testdata/runtelemetry.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		Cases []struct {
			Name    string            `json:"name"`
			RunID   string            `json:"runId"`
			Events  []json.RawMessage `json:"events"`
			Summary any               `json:"summary"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	if len(fx.Cases) < 11 {
		t.Fatalf("%d cases", len(fx.Cases))
	}
	for _, c := range fx.Cases {
		tel := &runTelemetry{}
		emitted := 0
		emit := tel.wrap(func(context.Context, any) error { emitted++; return nil })
		for _, e := range c.Events {
			event, err := jsParse(e)
			if err != nil {
				t.Fatal(err)
			}
			if err := emit(context.Background(), event); err != nil {
				t.Fatal(err)
			}
		}
		if emitted != len(c.Events) {
			t.Errorf("%s: %d of %d events passed on", c.Name, emitted, len(c.Events))
		}
		var got any
		if err := json.Unmarshal([]byte(jsStringify(tel.summary(c.RunID), false)), &got); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, c.Summary) {
			t.Errorf("%s:\n got %v\nwant %v", c.Name, got, c.Summary)
		}
	}
}

func TestRunTelemetryCountsTheCardRepairs(t *testing.T) {
	tel := &runTelemetry{}
	emit := tel.wrap(func(context.Context, any) error { return nil })
	for _, e := range []any{
		jsObjOf("type", "usage", "source", "outer", "usage", jsObjOf("totalTokens", 10.0)),
		jsObjOf("type", "usage", "source", cardRepairUsageSource, "usage", jsObjOf("totalTokens", 3.0)),
		jsObjOf("type", "usage", "source", cardRepairUsageSource, "usage", jsObjOf("totalTokens", 4.0)),
		cardNotShownNotice(),
	} {
		if err := emit(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
	summary := tel.summary("run-1")
	if got := jsStringify(summary.vals["cards"], false); got != `{"repairs":2,"notShown":1}` || jsGet(jsGet(summary, "usage"), "totalTokens") != 10.0 {
		t.Fatalf("summary %s", jsStringify(summary, false))
	}
	if _, ok := (&runTelemetry{}).summary("run-2").vals["cards"]; ok {
		t.Fatal("a run with no card repair reports cards")
	}
}
