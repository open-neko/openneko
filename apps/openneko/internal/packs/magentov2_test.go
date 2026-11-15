package packs

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"strconv"
	"testing"
	"time"
)

type orderedTestObject struct {
	keys []string
	vals map[string]any
}

func (o *orderedTestObject) ObjectKeys() []string       { return o.keys }
func (o *orderedTestObject) ObjectValue(key string) any { return o.vals[key] }

func decodeOrdered(t *testing.T, dec *json.Decoder) any {
	t.Helper()
	tok, err := dec.Token()
	if err != nil {
		t.Fatal(err)
	}
	switch v := tok.(type) {
	case json.Delim:
		if v == '[' {
			list := []any{}
			for dec.More() {
				list = append(list, decodeOrdered(t, dec))
			}
			dec.Token()
			return list
		}
		obj := &orderedTestObject{vals: map[string]any{}}
		for dec.More() {
			k, _ := dec.Token()
			obj.keys = append(obj.keys, k.(string))
			obj.vals[k.(string)] = decodeOrdered(t, dec)
		}
		dec.Token()
		return obj
	case json.Number:
		f, _ := strconv.ParseFloat(string(v), 64)
		return f
	}
	return tok
}

func orderedField(v any, key string) any {
	if o, ok := v.(*orderedTestObject); ok {
		return o.vals[key]
	}
	return nil
}

func roundTrip(t *testing.T, v any) any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

type magentoParityFixture struct {
	Now  string `json:"now"`
	Caps []struct {
		Name   string          `json:"name"`
		Input  json.RawMessage `json:"input"`
		Output map[string]any  `json:"output"`
	} `json:"caps"`
	Classify []struct {
		Name   string          `json:"name"`
		Input  json.RawMessage `json:"input"`
		Output map[string]any  `json:"output"`
	} `json:"classify"`
	Handoff []struct {
		OperationID string `json:"operationId"`
		HandoffOnly bool   `json:"handoffOnly"`
	} `json:"handoff"`
}

func TestMagentoV2MatchesNode(t *testing.T) {
	raw, err := os.ReadFile("testdata/magento_v2_caps_parity.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture magentoParityFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	now, _ := time.Parse(time.RFC3339, fixture.Now)
	parse := func(raw json.RawMessage) any {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		return decodeOrdered(t, dec)
	}

	for _, c := range fixture.Caps {
		t.Run("caps/"+c.Name, func(t *testing.T) {
			in := parse(c.Input)
			input := EvaluateMagentoCapsInput{Now: now}
			input.Domain = MagentoDomain(orderedField(in, "domain").(string))
			input.OperationID, _ = orderedField(in, "operationId").(string)
			input.RiskClass = MagentoRiskClass(orderedField(in, "riskClass").(float64))
			for _, row := range orderedField(in, "rows").([]any) {
				input.Rows = append(input.Rows, MagentoChangeRow{BeforeImage: orderedField(row, "beforeImage"), AfterImage: orderedField(row, "afterImage")})
			}
			if caps, ok := orderedField(in, "caps").(*orderedTestObject); ok {
				input.Caps = map[string]float64{}
				for _, k := range caps.keys {
					input.Caps[k] = caps.vals[k].(float64)
				}
			}
			if v, ok := orderedField(in, "projectedExposure").(float64); ok {
				input.ProjectedExposure = &v
			}
			input.AutoExecution, _ = orderedField(in, "autoExecution").(bool)
			if v, ok := orderedField(in, "autoActionsToday").(float64); ok {
				input.AutoActionsToday = int(v)
			}
			if list, ok := orderedField(in, "cooldownBreaches").([]any); ok {
				for _, item := range list {
					input.CooldownBreaches = append(input.CooldownBreaches, item.(string))
				}
			}
			got := roundTrip(t, EvaluateMagentoCaps(input))
			if !reflect.DeepEqual(got, roundTrip(t, c.Output)) {
				t.Fatalf("Go result differs from Node\n got: %v\nwant: %v", got, c.Output)
			}
		})
	}

	for _, c := range fixture.Classify {
		t.Run("classify/"+c.Name, func(t *testing.T) {
			in := parse(c.Input)
			var stored []MagentoAttributeClassification
			if list, ok := orderedField(in, "classifications").([]any); ok {
				stored = []MagentoAttributeClassification{}
				for _, item := range list {
					raw, _ := json.Marshal(roundTrip(t, item.(*orderedTestObject).vals))
					var entry MagentoAttributeClassification
					if err := json.Unmarshal(raw, &entry); err != nil {
						t.Fatal(err)
					}
					stored = append(stored, entry)
				}
			}
			got := roundTrip(t, ClassifyMagentoChange(ClassifyMagentoInput{
				Domain:          MagentoDomain(orderedField(in, "domain").(string)),
				EntityType:      orderedField(in, "entityType").(string),
				OperationID:     orderedField(in, "operationId").(string),
				DefaultClass:    MagentoRiskClass(orderedField(in, "defaultClass").(float64)),
				Body:            orderedField(in, "body"),
				Classifications: stored,
			}))
			if !reflect.DeepEqual(got, roundTrip(t, c.Output)) {
				t.Fatalf("Go result differs from Node\n got: %v\nwant: %v", got, c.Output)
			}
		})
	}

	for _, c := range fixture.Handoff {
		if got := IsMagentoHandoffOnlyOperation(c.OperationID); got != c.HandoffOnly {
			t.Errorf("%s: handoff-only %v, Node %v", c.OperationID, got, c.HandoffOnly)
		}
	}
}

func TestMagentoCapsReadPlainMaps(t *testing.T) {
	result := EvaluateMagentoCaps(EvaluateMagentoCapsInput{
		Domain:    MagentoCatalog,
		RiskClass: 2,
		Rows:      []MagentoChangeRow{{BeforeImage: map[string]any{"price": 100.0}, AfterImage: map[string]any{"product": map[string]any{"price": 125.0}}}},
	})
	if !result.Allowed || result.RiskClass != 1 || len(result.Escalations) != 1 {
		t.Fatalf("got %+v", result)
	}
}
