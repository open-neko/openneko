package webfront

import (
	"math"
	"testing"
	"time"

	"github.com/open-neko/neko/apps/openneko/internal/llm"
)

func TestAStoppedStreamCountsOnceInTheRunCost(t *testing.T) {
	record := &llm.AxModelRoute{Provider: "google-gemini", Model: "gemini-3.8-flash", Price: &llm.Price{InputUSDPer1M: 1, OutputUSDPer1M: 1}}
	unknown := &llm.AxModelRoute{Provider: "no-such-provider", Model: "no-such-model"}
	early := `"early":{"requests":1,"input_tokens":1000,"output_tokens":500,"total_tokens":1500,"cost_micros":1500}`
	cases := []struct {
		name  string
		usage string
		route *llm.AxModelRoute
		want  float64
	}{
		{"every other call reported", `{"requests":3,"reported":2,"input_tokens":3000,"output_tokens":1500,"total_tokens":4500,"coverage":"partial",` + early + `}`, record, 0.0045},
		{"one call with no usage", `{"requests":4,"reported":2,"input_tokens":3000,"output_tokens":1500,"total_tokens":4500,"coverage":"partial",` + early + `}`, record, 0.006},
		{"only the stopped stream", `{"requests":1,"input_tokens":1000,"output_tokens":500,"total_tokens":1500,"coverage":"partial",` + early + `}`, record, 0.0015},
		{"harness catalog price", `{"requests":3,"reported":2,"input_tokens":3000,"output_tokens":1500,"total_tokens":4500,"coverage":"partial","cost_micros":4500,"priced_by":"catalog","catalog_version":"v1",` + early + `}`, unknown, 0.0045},
		{"no stopped stream", `{"requests":4,"reported":2,"input_tokens":2000,"output_tokens":1000,"total_tokens":3000,"coverage":"partial"}`, record, 0.006},
	}
	for _, c := range cases {
		v, err := jsParse([]byte(c.usage))
		if err != nil {
			t.Fatal(err)
		}
		u, _ := v.(*jsObj)
		out := normalizeAxUsage(u, c.route, time.Now())
		got, ok := out.vals["estimatedCostUsd"].(float64)
		if !ok || math.Abs(got-c.want) > 1e-12 {
			t.Errorf("%s: cost %v (%v), want %v; usage %s", c.name, got, ok, c.want, jsStringify(out, false))
		}
	}
}
