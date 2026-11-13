package webfront

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf16"
)

const (
	a2uiVersion   = "v1.0"
	a2uiCatalogID = "urn:openneko:catalog:work:v2"
)

var askUserShape = &zObj{fields: []zField{
	{name: "reason", typ: zText(1, 500), optional: true},
	{name: "questions", typ: &zArr{min: num(1), max: num(3), elem: &zObj{strict: true, fields: []zField{
		{name: "header", typ: zText(1, 40), optional: true},
		{name: "question", typ: zText(1, 500)},
		{name: "options", optional: true, typ: &zArr{min: num(2), max: num(5), elem: &zObj{strict: true, fields: []zField{
			{name: "label", typ: zText(1, 80)},
			{name: "description", typ: zText(1, 240), optional: true},
		}}}},
	}}}},
}}

func interactionTools(b turnBinding, be toolBackend) []toolDef {
	return []toolDef{{server: "neko_interaction", name: "ask_user_question",
		description: joinWords(
			"Pause this Work-chat turn and ask the operator for missing information.",
			"Use only when uncertainty is material to correctness, scope, cost, timing, authorization, or an irreversible decision and available tools cannot resolve it.",
			"Ask one to three focused questions. After this call, stop: do not render an answer, provide totals or recommendations, emit vitals/follow-ups, or call another tool."),
		shape: askUserShape,
		call: func(ctx context.Context, args *jsObj) (toolResult, error) {
			var questions []*jsObj
			for i, q := range args.vals["questions"].([]any) {
				in := q.(*jsObj)
				out := jsObjOf("id", "q"+jsNumberText(float64(i+1)))
				for _, k := range []string{"header", "question", "options"} {
					if v, ok := in.vals[k]; ok {
						out.set(k, v)
					}
				}
				questions = append(questions, out)
			}
			reason, _ := args.vals["reason"].(string)
			surfaceID := "clarification-" + b.RunID
			if b.WantsCards {
				if err := be.emitEvent(ctx, jsObjOf("type", "surface", "messages", clarificationSurface(b.RunID, questions, reason))); err != nil {
					return toolResult{}, err
				}
			}
			event := jsObjOf("type", "needs_input")
			if len(questions) == 1 {
				event.set("question", questions[0].vals["question"])
				if options, ok := questions[0].vals["options"].([]any); ok {
					labels := make([]any, len(options))
					for i, o := range options {
						labels[i] = o.(*jsObj).vals["label"]
					}
					event.set("options", labels)
				}
			} else {
				event.set("question", "I need "+jsNumberText(float64(len(questions)))+" details before I can continue.")
			}
			list := make([]any, len(questions))
			for i, q := range questions {
				list[i] = q
			}
			event.set("questions", list)
			if reason != "" {
				event.set("reason", reason)
			}
			if b.WantsCards {
				event.set("surfaceId", surfaceID)
			}
			if err := be.emitEvent(ctx, event); err != nil {
				return toolResult{}, err
			}
			return toolText(jsObjOf("ok", true, "status", "needs_input",
				"instruction", "The clarification was presented to the operator. End this turn now without any further tools, answer content, vitals, or follow-ups.")), nil
		}}}
}

// clarificationSurface is the host-owned form for the questions, so a
// question cannot become malformed or unsubmittable generated UI.
func clarificationSurface(runID string, questions []*jsObj, reason string) []any {
	var components, children, requirements, asked []any
	answers := &jsObj{}
	for _, q := range questions {
		id := q.vals["id"].(string)
		inputID := id + "-input"
		children = append(children, inputID)
		label := q.vals["question"].(string)
		if header, ok := q.vals["header"].(string); ok {
			label = header + ": " + label
		}
		if options, ok := q.vals["options"].([]any); ok && len(options) > 0 {
			answers.set(id, []any{})
			requirements = append(requirements, jsObjOf("path", "/answers/"+id+"/0"))
			choices := make([]any, len(options))
			for i, o := range options {
				opt := o.(*jsObj)
				text := opt.vals["label"].(string)
				if d, ok := opt.vals["description"].(string); ok {
					text += " — " + d
				}
				choices[i] = jsObjOf("label", text, "value", opt.vals["label"])
			}
			components = append(components, jsObjOf("id", inputID, "component", "ChoicePicker", "label", label,
				"options", choices, "value", jsObjOf("path", "/answers/"+id), "variant", "mutuallyExclusive"))
		} else {
			answers.set(id, "")
			requirements = append(requirements, jsObjOf("path", "/answers/"+id))
			components = append(components, jsObjOf("id", inputID, "component", "TextField", "label", label,
				"value", jsObjOf("path", "/answers/"+id), "variant", "longText"))
		}
		ask := jsObjOf("id", id)
		if header, ok := q.vals["header"]; ok {
			ask.set("header", header)
		}
		ask.set("question", q.vals["question"])
		asked = append(asked, ask)
	}
	children = append(children, "submit")
	title := "A few details before I continue"
	if len(questions) == 1 {
		title = "One detail before I continue"
	}
	root := jsObjOf("id", "root", "component", "Answer", "eyebrow", "Clarification", "title", title)
	if reason != "" {
		root.set("subtitle", reason)
	}
	root.set("children", children)
	components = append([]any{root}, components...)
	components = append(components,
		jsObjOf("id", "submit-label", "component", "Text", "text", "Continue", "variant", "body"),
		jsObjOf("id", "submit", "component", "Button", "child", "submit-label", "variant", "primary", "requires", requirements,
			"action", jsObjOf("event", jsObjOf("name", "submit_clarification", "context", jsObjOf(
				"prompt", "Continue the previous request using these operator-supplied answers. Re-check any remaining external facts and do not infer omitted values.",
				"questions", asked,
				"answers", jsObjOf("path", "/answers"))))))
	return []any{jsObjOf("version", a2uiVersion, "createSurface", jsObjOf("surfaceId", "clarification-"+runID,
		"catalogId", a2uiCatalogID, "components", components, "dataModel", jsObjOf("answers", answers)))}
}

var createSkillShape = &zObj{fields: []zField{
	{name: "name", typ: zText(1, 64)},
	{name: "description", typ: zText(1, 1024)},
	{name: "body", typ: &zStr{min: num(1)}},
	{name: "license", typ: zText(1, 200), optional: true},
	{name: "compatibility", typ: zText(1, 500), optional: true},
	{name: "metadata", typ: &zRecord{key: zText(1, 128), val: &zStr{max: num(2048)}}, optional: true},
	{name: "allowedTools", typ: zText(1, 1000), optional: true},
	{name: "files", optional: true, typ: &zArr{max: num(30), elem: &zObj{fields: []zField{
		{name: "path", typ: zText(1, 240)},
		{name: "content", typ: &zStr{max: num(128 * 1024)}},
	}}}},
}}

func skillTools(b turnBinding) []toolDef {
	return []toolDef{{server: "neko_skills", name: "create_skill",
		description: "Create or update an agentskills.io-style skill in Neko's shared skills directory.",
		shape:       createSkillShape,
		call: func(_ context.Context, args *jsObj) (toolResult, error) {
			name, skillPath, err := writeSkillFiles(b.SkillsRoot, args)
			if err != nil {
				return toolResult{}, err
			}
			return toolText(jsObjOf("ok", true, "name", name, "skillFile", skillPath+"/SKILL.md")), nil
		}}}
}

var (
	skillNameRe  = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	skillNameRun = regexp.MustCompile(`[^a-z0-9]+`)
)

func normalizeSkillName(input string) string {
	s := strings.ToLower(strings.ReplaceAll(strings.TrimFunc(input, isJSSpace), "İ", "i̇"))
	s = strings.Trim(skillNameRun.ReplaceAllString(s, "-"), "-")
	if len(s) > 64 {
		s = s[:64]
	}
	return strings.TrimSuffix(s, "-")
}

// writeSkillFiles is writeWorkSkillFiles(): SKILL.md first, then each file,
// so a bad file path leaves the files before it in place, as in Node.
func writeSkillFiles(root string, args *jsObj) (string, string, error) {
	if root == "" {
		return "", "", errors.New("The skills folder is not set for this run.")
	}
	name := normalizeSkillName(args.vals["name"].(string))
	if name == "" || !skillNameRe.MatchString(name) {
		return "", "", errors.New("Skill name must use lowercase letters, numbers, and hyphens only.")
	}
	description := strings.TrimFunc(args.vals["description"].(string), isJSSpace)
	body := strings.TrimFunc(args.vals["body"].(string), isJSSpace)
	if description == "" {
		return "", "", errors.New("Skill description is required.")
	}
	if body == "" {
		return "", "", errors.New("Skill body is required.")
	}
	base, err := filepath.Abs(root)
	if err != nil {
		return "", "", err
	}
	skillPath := filepath.Join(base, name)
	if err := os.MkdirAll(skillPath, 0o755); err != nil {
		return "", "", err
	}
	if err := writeFileKeepMode(filepath.Join(skillPath, "SKILL.md"), []byte(skillFileText(name, description, body, args))); err != nil {
		return "", "", err
	}
	files, _ := args.vals["files"].([]any)
	for _, f := range files {
		file := f.(*jsObj)
		rel, err := skillRelativePath(file.vals["path"].(string))
		if err != nil {
			return "", "", err
		}
		full := filepath.Join(skillPath, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return "", "", err
		}
		if err := writeFileKeepMode(full, []byte(file.vals["content"].(string))); err != nil {
			return "", "", err
		}
	}
	return name, skillPath, nil
}

func skillFileText(name, description, body string, args *jsObj) string {
	lines := []string{"---", "name: " + name, "description: " + jsStringify(description, false)}
	optional := func(field, key string) {
		if v, ok := args.vals[field].(string); ok {
			if v = strings.TrimFunc(v, isJSSpace); v != "" {
				lines = append(lines, key+": "+jsStringify(v, false))
			}
		}
	}
	optional("license", "license")
	optional("compatibility", "compatibility")
	if meta, ok := args.vals["metadata"].(*jsObj); ok && len(meta.keys) > 0 {
		lines = append(lines, "metadata:")
		for _, k := range meta.ordered() {
			lines = append(lines, "  "+skillYAMLKey(k)+": "+jsStringify(meta.vals[k], false))
		}
	}
	optional("allowedTools", "allowed-tools")
	return strings.Join(lines, "\n") + "\n---\n\n" + body + "\n"
}

// skillYAMLKey replaces each UTF-16 unit outside [a-zA-Z0-9_-] with "_".
func skillYAMLKey(k string) string {
	var b strings.Builder
	for _, r := range k {
		if r < 0x80 && (r == '_' || r == '-' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			b.WriteRune(r)
			continue
		}
		b.WriteString(strings.Repeat("_", utf16.RuneLen(r)))
	}
	return b.String()
}

func skillRelativePath(input string) (string, error) {
	rel := strings.TrimLeft(strings.ReplaceAll(strings.TrimFunc(input, isJSSpace), `\`, "/"), "/")
	if rel == "" {
		return "", errors.New("Skill file path is required.")
	}
	for _, part := range strings.Split(rel, "/") {
		if part == "" || part == "." || part == ".." {
			return "", errors.New("Invalid skill file path: " + input)
		}
	}
	return rel, nil
}
