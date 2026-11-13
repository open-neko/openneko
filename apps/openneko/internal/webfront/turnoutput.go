package webfront

import (
	"regexp"
	"strings"

	"github.com/google/uuid"
)

// The answer parsing of packages/llm/src/agent-backends/output.ts and
// surface.ts.

const fenceClose = "\n```"

var hiddenFenceOpeners = []string{
	"```neko_a2ui",
	"```neko_cards",
	"```neko_workflow_save",
	"```neko_workflow_output",
	"```neko_action_request",
	"```neko_rule_save",
}

// outsideFenceText is the text outside the hidden fences. A tail that could
// open one is held back until a later chunk decides it.
func outsideFenceText(raw string) string {
	var out strings.Builder
	i := 0
	for i < len(raw) {
		at, opener := -1, ""
		for _, o := range hiddenFenceOpeners {
			if idx := strings.Index(raw[i:], o); idx >= 0 && (at < 0 || i+idx < at) {
				at, opener = i+idx, o
			}
		}
		if at < 0 {
			tail := raw[i:]
			hold := 0
			for _, o := range hiddenFenceOpeners {
				for k := min(len(tail), len(o)-1); k > hold; k-- {
					if strings.HasSuffix(tail, o[:k]) {
						hold = k
						break
					}
				}
			}
			out.WriteString(tail[:len(tail)-hold])
			break
		}
		out.WriteString(raw[i:at])
		end := strings.Index(raw[at+len(opener):], fenceClose)
		if end < 0 {
			break
		}
		i = at + len(opener) + end + len(fenceClose)
	}
	return out.String()
}

func extractMarkdownText(messages any) string {
	var out []string
	var visit func(node any)
	visit = func(node any) {
		switch n := node.(type) {
		case []any:
			for _, item := range n {
				visit(item)
			}
		case *jsObj:
			if n.vals["component"] == "Markdown" {
				if text, ok := n.vals["text"].(string); ok {
					out = append(out, text)
				}
			}
			for _, k := range n.ordered() {
				visit(n.vals[k])
			}
		}
	}
	visit(messages)
	return strings.TrimSpace(strings.Join(out, "\n\n"))
}

var (
	cardFenceRE   = regexp.MustCompile("(?is)```neko_cards\\s*(.*?)\\n?```")
	a2uiFenceRE   = regexp.MustCompile("(?is)```neko_a2ui\\s*(.*?)```")
	jsxTagRE      = regexp.MustCompile(`</?[A-Z][A-Za-z0-9]*\b[^>]*>`)
	blankLinesRE  = regexp.MustCompile(`\n{3,}`)
	cardSurfaceID = func() string { return "answer-" + uuid.NewString() }
)

// extractCardFences takes the render_cards arguments written into an answer
// as neko_cards fences out of the text.
func extractCardFences(raw string, rich bool) (string, []any, []string) {
	messages, issues := []any{}, []string{}
	text := cardFenceRE.ReplaceAllStringFunc(raw, func(m string) string {
		body := cardFenceRE.FindStringSubmatch(m)[1]
		args, err := jsParse([]byte(strings.TrimSpace(body)))
		if err != nil {
			issues = append(issues, "neko_cards fence is not JSON")
			return ""
		}
		msgs, problems := renderCards(args, rich, cardSurfaceID())
		if len(problems) > 0 {
			issues = append(issues, problems...)
		} else {
			messages = append(messages, msgs...)
		}
		return ""
	})
	if len(messages) == 0 && len(issues) == 0 {
		return raw, messages, issues
	}
	return strings.TrimSpace(blankLinesRE.ReplaceAllString(text, "\n\n")), messages, issues
}

// extractSurfaceMessages takes the first neko_a2ui fence out of the text. A
// body that is not a JSON list becomes one Markdown card.
func extractSurfaceMessages(raw string) (string, []any) {
	loc := a2uiFenceRE.FindStringSubmatchIndex(raw)
	if loc == nil {
		return strings.TrimSpace(raw), []any{}
	}
	outside := strings.TrimSpace(raw[:loc[0]] + raw[loc[1]:])
	body := strings.TrimSpace(raw[loc[2]:loc[3]])
	if parsed, err := jsParse([]byte(body)); err == nil {
		if list, ok := parsed.([]any); ok {
			return outside, coerceReadableSurfaceMessages(list)
		}
	}
	prose := strings.TrimSpace(jsxTagRE.ReplaceAllString(body, ""))
	if prose == "" {
		return outside, []any{}
	}
	return outside, []any{jsObjOf("version", "v1.0", "createSurface", jsObjOf("surfaceId", "fallback",
		"catalogId", "urn:openneko:catalog:work:v2", "components", []any{jsObjOf("id", "root", "component", "Markdown", "text", prose)}))}
}

type surfaceField struct {
	name     string
	optional bool
	check    func(any) (any, bool)
}

func surfaceText(v any) (any, bool) {
	s, ok := v.(string)
	return s, ok && s != ""
}

func anyString(v any) (any, bool) {
	s, ok := v.(string)
	return s, ok
}

func recordValue(v any) (any, bool) {
	o, ok := v.(*jsObj)
	return o, ok
}

func boolValue(v any) (any, bool) {
	b, ok := v.(bool)
	return b, ok
}

func anyValue(v any) (any, bool) { return v, true }

func componentList(v any) (any, bool) {
	list, ok := v.([]any)
	if !ok || len(list) == 0 {
		return nil, false
	}
	out := make([]any, len(list))
	for i, item := range list {
		o, ok := item.(*jsObj)
		if !ok {
			return nil, false
		}
		id, okID := surfaceText(o.vals["id"])
		component, okC := surfaceText(o.vals["component"])
		if !okID || !okC {
			return nil, false
		}
		c := jsObjOf("id", id, "component", component)
		for _, k := range o.ordered() {
			if k != "id" && k != "component" {
				c.set(k, o.vals[k])
			}
		}
		out[i] = c
	}
	return out, true
}

func strictObject(v any, fields []surfaceField) (*jsObj, bool) {
	o, ok := v.(*jsObj)
	if !ok {
		return nil, false
	}
	known := map[string]bool{}
	out := &jsObj{}
	for _, f := range fields {
		known[f.name] = true
		val, present := o.vals[f.name]
		if !present {
			if !f.optional {
				return nil, false
			}
			continue
		}
		got, ok := f.check(val)
		if !ok {
			return nil, false
		}
		out.set(f.name, got)
	}
	for _, k := range o.keys {
		if !known[k] {
			return nil, false
		}
	}
	return out, true
}

var surfaceBodies = []struct {
	key    string
	fields []surfaceField
}{
	{"createSurface", []surfaceField{{"surfaceId", false, surfaceText}, {"catalogId", false, surfaceText},
		{"surfaceProperties", true, recordValue}, {"sendDataModel", true, boolValue},
		{"components", true, componentList}, {"dataModel", true, recordValue}}},
	{"updateComponents", []surfaceField{{"surfaceId", false, surfaceText}, {"components", false, componentList}}},
	{"updateDataModel", []surfaceField{{"surfaceId", false, surfaceText}, {"path", true, anyString}, {"value", true, anyValue}}},
	{"deleteSurface", []surfaceField{{"surfaceId", false, surfaceText}}},
}

// coerceReadableSurfaceMessages keeps the A2UI messages that pass the reader
// schema of a2ui-contract.ts, which still reads v0.9 surfaces.
func coerceReadableSurfaceMessages(list []any) []any {
	out := []any{}
	for _, item := range list {
		for _, body := range surfaceBodies {
			msg, ok := strictObject(item, []surfaceField{
				{"version", false, func(v any) (any, bool) { return v, v == "v0.9" || v == "v1.0" }},
				{body.key, false, func(v any) (any, bool) { return strictObject(v, body.fields) }},
			})
			if ok {
				out = append(out, msg)
				break
			}
		}
	}
	return out
}
