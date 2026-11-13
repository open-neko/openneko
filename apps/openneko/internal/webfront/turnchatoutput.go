package webfront

import (
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// The answer fences of packages/llm/src/workflows/fence-parsers.ts and
// packages/llm/src/agent-backends/memory-fence.ts.

func fencePattern(name string) *regexp.Regexp {
	return regexp.MustCompile("(?is)```" + name + "[" + jsSpaceClass + "]*(.*?)```")
}

var (
	workflowSaveFence  = fencePattern("neko_workflow_save")
	actionRequestFence = fencePattern("neko_action_request")
	ruleSaveFence      = fencePattern("neko_rule_save")
	valueFence         = fencePattern("neko_value")
	followupsFence     = fencePattern("neko_followups")
	vitalsFence        = fencePattern("neko_vitals")
	memoryFence        = fencePattern("neko_memory")
	blankLines         = regexp.MustCompile(`\n{3,}`)
	danglingToolCall   = jsRe(`\bcall:[A-Za-z0-9_.:-]+\s*\{[^{}]*(?:\}|$)`)
	spaceBeforeNewline = regexp.MustCompile(`[ \t]+\n`)
)

var valueEstimateShape = &zObj{fields: []zField{
	zRequired("minutes_saved", zIntRange(0, 600)),
	zOptional("basis", &zStr{trim: true, max: num(160)}),
}}

var followupsShape = &zObj{fields: []zField{
	zRequired("followups", &zArr{elem: zText(1, 120), max: num(3)}),
}}

var vitalsShape = &zObj{fields: []zField{
	zRequired("vitals", &zArr{max: num(4), elem: &zObj{fields: []zField{
		zRequired("label", zText(1, 28)),
		zRequired("value", zText(1, 18)),
		zOptional("sub", &zStr{trim: true, max: num(36)}),
		zOptional("basis", zEnum("", "observed", "calculated", "estimated")),
		zOptional("asOf", zText(1, 48)),
		zOptional("source", zText(1, 64)),
	}}}),
}}

type fenceResult struct {
	Text     string
	Payloads []*jsObj
	Errors   []string
}

// first is the payload of a fence where the first valid one wins; last where
// the last valid one wins.
func (f fenceResult) first() *jsObj {
	if len(f.Payloads) == 0 {
		return nil
	}
	return f.Payloads[0]
}

func (f fenceResult) last() *jsObj {
	if len(f.Payloads) == 0 {
		return nil
	}
	return f.Payloads[len(f.Payloads)-1]
}

func stripFences(raw string, re *regexp.Regexp) string {
	return jsTrim(blankLines.ReplaceAllString(re.ReplaceAllString(raw, ""), "\n\n"))
}

// extractFence parses every fence of one kind. With stopAtFirst, parsing
// ends at the first valid payload, as extractWorkflowSaveFence() and
// extractRuleSaveFence() do.
func extractFence(raw string, re *regexp.Regexp, shape *zObj, stopAtFirst bool) fenceResult {
	out := fenceResult{Text: stripFences(raw, re)}
	for _, m := range re.FindAllStringSubmatch(raw, -1) {
		if stopAtFirst && len(out.Payloads) > 0 {
			break
		}
		value, err := jsParse([]byte(jsTrim(m[1])))
		if err != nil {
			out.Errors = append(out.Errors, err.Error())
			continue
		}
		issues := []any{}
		clean := shape.parse(value, []any{}, &issues)
		if len(issues) > 0 {
			out.Errors = append(out.Errors, jsStringifyIndent(issues, "  "))
			continue
		}
		out.Payloads = append(out.Payloads, clean.(*jsObj))
	}
	return out
}

type memoryFenceOp struct {
	Text   string
	Scope  string
	Pinned *bool
}

// extractMemoryFences is extractMemoryFences(): each fence leaves the text,
// valid or not.
func extractMemoryFences(raw string) (string, []memoryFenceOp) {
	var ops []memoryFenceOp
	stripped := raw
	for _, m := range memoryFence.FindAllStringSubmatch(raw, -1) {
		if value, err := jsParse([]byte(jsTrim(m[1]))); err == nil {
			items := []any{value}
			if list, ok := value.([]any); ok {
				items = list
			}
			for _, item := range items {
				if op, ok := memorySaveOp(item); ok {
					ops = append(ops, op)
				}
			}
		}
		stripped = strings.Replace(stripped, m[0], "", 1)
	}
	return jsTrim(stripped), ops
}

func memorySaveOp(item any) (memoryFenceOp, bool) {
	save, ok := jsGet(item, "save").(*jsObj)
	if !ok {
		return memoryFenceOp{}, false
	}
	text, ok := save.vals["text"].(string)
	if !ok || utf16Len(jsTrim(text)) < 3 {
		return memoryFenceOp{}, false
	}
	op := memoryFenceOp{Text: jsTrim(text)}
	if scope, _ := save.vals["scope"].(string); scope == "thread" || scope == "global" {
		op.Scope = scope
	}
	if pinned, ok := save.vals["pinned"].(bool); ok {
		op.Pinned = &pinned
	}
	return op, true
}

// displayText is the persisted assistant text: every machine fence and any
// dangling tool call removed from the final text.
func displayText(final string) string {
	text := jsTrim(final)
	for _, re := range []*regexp.Regexp{actionRequestFence, workflowSaveFence, ruleSaveFence, valueFence, followupsFence, vitalsFence} {
		text = stripFences(text, re)
	}
	text, _ = extractMemoryFences(text)
	return stripDanglingToolCalls(text)
}

func stripDanglingToolCalls(text string) string {
	return jsTrim(spaceBeforeNewline.ReplaceAllString(danglingToolCall.ReplaceAllString(text, ""), "\n"))
}

// clampAnalysisMinutes is clampAnalysisMinutes() in packages/llm/src/workflows/value.ts.
func clampAnalysisMinutes(v any) any {
	f, ok := v.(float64)
	if !ok || math.IsNaN(f) || math.IsInf(f, 0) {
		return nil
	}
	if f < 0 {
		return 0.0
	}
	return math.Min(math.Floor(f+0.5), 180)
}

func needsInputText(event any) string {
	questions := jsArray(jsGet(event, "questions"))
	if len(questions) == 0 {
		q := jsObjOf("id", "q1", "question", jsLookup(event, "question"))
		if options, ok := jsGet(event, "options").([]any); ok {
			labels := make([]any, len(options))
			for i, o := range options {
				labels[i] = jsObjOf("label", o)
			}
			q.set("options", labels)
		}
		questions = []any{q}
	}
	var lines []string
	if reason := jsGet(event, "reason"); jsTruthy(reason) {
		lines = append(lines, jsTemplate(reason), "")
	}
	for i, q := range questions {
		prefix := ""
		if len(questions) > 1 {
			prefix = strconv.Itoa(i+1) + ". "
		}
		lines = append(lines, prefix+jsTemplate(jsLookup(q, "question")))
		for _, o := range jsArray(jsGet(q, "options")) {
			lines = append(lines, "- "+jsTemplate(jsLookup(o, "label")))
		}
		if i < len(questions)-1 {
			lines = append(lines, "")
		}
	}
	return jsTrim(strings.Join(lines, "\n"))
}

var (
	allowlistDenial = jsRe(`(?i)\bhost ([a-z0-9.-]+)(?::(\d+))? is not on the web allowlist`)
	policyDenied    = regexp.MustCompile(`(?i)policy_denied|not permitted by policy`)
	requestDenial   = jsRe(`(?i)\b(GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS)\s+([a-z0-9.-]+)(?::(\d+))?(/[^\s"'\\]*)?\s+not permitted by policy`)
)

func eventStrings(v any, depth int) []string {
	if depth > 5 || v == nil {
		return nil
	}
	switch x := v.(type) {
	case string:
		return []string{x}
	case []any:
		var out []string
		for _, item := range x {
			out = append(out, eventStrings(item, depth+1)...)
		}
		return out
	case *jsObj:
		var out []string
		for _, k := range x.ordered() {
			out = append(out, eventStrings(x.vals[k], depth+1)...)
		}
		return out
	}
	return nil
}

// networkPolicyDenial is extractNetworkPolicyDenial(): a tool result that
// names a refused host becomes a capability_denied event.
func networkPolicyDenial(event any) *jsObj {
	if jsGet(event, "type") != "tool_end" {
		return nil
	}
	errText, _ := jsGet(event, "error").(string)
	text := strings.Join(append([]string{errText}, eventStrings(jsGet(event, "result"), 0)...), "\n")
	denied := jsObjOf("type", "capability_denied", "capability", "network_egress", "reason", "policy_denied")
	if m := allowlistDenial.FindStringSubmatch(text); m != nil {
		denied.set("host", strings.ToLower(m[1]))
		if m[2] != "" {
			port, _ := strconv.ParseFloat(m[2], 64)
			denied.set("port", port)
		}
		denied.set("method", "GET")
		return denied
	}
	if !policyDenied.MatchString(text) {
		return nil
	}
	m := requestDenial.FindStringSubmatch(text)
	if m == nil {
		return nil
	}
	denied.set("host", strings.ToLower(m[2]))
	if m[3] != "" {
		port, _ := strconv.ParseFloat(m[3], 64)
		denied.set("port", port)
	}
	denied.set("method", strings.ToUpper(m[1]))
	if m[4] != "" {
		denied.set("path", m[4])
	}
	return denied
}

const structuredSummaryMax = 6000

func surfaceFacts(messages []any) []string {
	var facts []string
	seen := map[string]bool{}
	add := func(v any) {
		s, ok := v.(string)
		if !ok {
			return
		}
		s = jsTrim(s)
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		facts = append(facts, s)
	}
	resolve := func(v any, model any) any {
		obj, ok := v.(*jsObj)
		if !ok {
			return v
		}
		path, ok := obj.vals["path"].(string)
		if !ok || !strings.HasPrefix(path, "/") {
			return v
		}
		node := model
		for _, part := range strings.Split(path[1:], "/") {
			part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
			switch n := node.(type) {
			case *jsObj:
				node = n.vals[part]
			case []any:
				i, err := strconv.Atoi(part)
				if err != nil || i < 0 || i >= len(n) || strconv.Itoa(i) != part {
					node = nil
				} else {
					node = n[i]
				}
			default:
				node = nil
			}
		}
		return node
	}
	isRecord := func(v any) bool { _, ok := v.(*jsObj); return ok }
	text := func(v any) (string, bool) {
		switch x := v.(type) {
		case string:
			return x, true
		case float64:
			return jsNumberString(x), true
		}
		return "", false
	}
	var visit func(node, model any)
	visit = func(node, model any) {
		if list, ok := node.([]any); ok {
			for _, item := range list {
				visit(item, model)
			}
			return
		}
		value, ok := node.(*jsObj)
		if !ok {
			return
		}
		if surface, ok := value.vals["createSurface"].(*jsObj); ok {
			visit(surface.vals["components"], surface.vals["dataModel"])
			return
		}
		if component, ok := value.vals["component"].(string); ok {
			for _, k := range []string{"title", "label", "metric", "text", "detail", "subtitle"} {
				add(value.vals[k])
			}
			if items, ok := value.vals["items"].([]any); ok && component == "KeyFigures" {
				for _, item := range items[:min(len(items), 20)] {
					figure, ok := item.(*jsObj)
					if !ok {
						continue
					}
					label, ok := figure.vals["label"].(string)
					if !ok {
						continue
					}
					shown, ok := text(figure.vals["value"])
					if !ok {
						continue
					}
					var qualifiers []string
					for _, k := range []string{"sub", "basis", "asOf", "source"} {
						if q, ok := figure.vals[k].(string); ok && jsTrim(q) != "" {
							qualifiers = append(qualifiers, q)
						}
					}
					line := label + ": " + shown
					if len(qualifiers) > 0 {
						line += " (" + strings.Join(qualifiers, "; ") + ")"
					}
					add(line)
				}
			}
			if rows, ok := resolve(value.vals["rows"], model).([]any); ok && component == "Table" {
				var keys []string
				for _, c := range jsArray(value.vals["columns"]) {
					if key, ok := jsGet(c, "key").(string); ok {
						keys = append(keys, key)
					}
				}
				for _, r := range rows[:min(len(rows), 20)] {
					row, ok := r.(*jsObj)
					if !ok {
						continue
					}
					names := keys
					if len(names) == 0 {
						names = row.ordered()
					}
					var cells []string
					for _, key := range names[:min(len(names), 12)] {
						if cell, ok := text(row.vals[key]); ok {
							cells = append(cells, key+": "+cell)
						}
					}
					if len(cells) > 0 {
						add(strings.Join(cells, ", "))
					}
				}
			}
			if points, ok := resolve(value.vals["data"], model).([]any); ok && component == "Chart" {
				label := "value"
				if l, ok := value.vals["valueLabel"].(string); ok {
					label = jsTrim(l)
				}
				for _, p := range points[:min(len(points), 60)] {
					point, ok := p.(*jsObj)
					if !ok {
						continue
					}
					d, dok := point.vals["d"].(string)
					v, vok := point.vals["v"].(float64)
					if dok && vok && !math.IsNaN(v) && !math.IsInf(v, 0) {
						add(d + ": " + jsNumberString(v) + " " + label)
					}
				}
			}
		}
		for _, k := range value.ordered() {
			if isRecord(value.vals[k]) || isList(value.vals[k]) {
				visit(value.vals[k], model)
			}
		}
	}
	for _, m := range messages {
		visit(m, nil)
	}
	return facts
}

func isList(v any) bool { _, ok := v.([]any); return ok }

// structuredTurnSummary is structuredTurnSummary() in
// packages/llm/src/work/structured-turn-summary.ts.
func structuredTurnSummary(vitals *jsObj, surfaces []any, notes []string) string {
	var facts []string
	if vitals != nil {
		for _, v := range jsArray(vitals.vals["vitals"]) {
			var qualifiers []string
			if sub, _ := jsGet(v, "sub").(string); sub != "" {
				qualifiers = append(qualifiers, sub)
			}
			if basis, _ := jsGet(v, "basis").(string); basis != "" {
				qualifiers = append(qualifiers, basis)
			}
			if asOf, _ := jsGet(v, "asOf").(string); asOf != "" {
				qualifiers = append(qualifiers, "as of "+asOf)
			}
			if source, _ := jsGet(v, "source").(string); source != "" {
				qualifiers = append(qualifiers, "source: "+source)
			}
			line := "- " + jsTemplate(jsGet(v, "label")) + ": " + jsTemplate(jsGet(v, "value"))
			if len(qualifiers) > 0 {
				line += " (" + strings.Join(qualifiers, "; ") + ")"
			}
			facts = append(facts, line)
		}
	}
	var content []string
	for _, n := range notes {
		if n = jsTrim(n); n != "" && !slices.Contains(content, n) {
			content = append(content, n)
		}
	}
	content = append(content, surfaceFacts(surfaces)...)
	if len(content) == 0 && len(facts) == 0 {
		return ""
	}
	lines := []string{"Structured result from my previous turn:"}
	for _, c := range content {
		lines = append(lines, "- "+c)
	}
	if len(facts) > 0 {
		lines = append(append(lines, "Headline figures:"), facts...)
	}
	summary := strings.Join(lines, "\n")
	if utf16Len(summary) <= structuredSummaryMax {
		return summary
	}
	return strings.TrimRightFunc(jsSlice(summary, structuredSummaryMax), jsSpace) + "\n[Structured result truncated]"
}

type runArtifact struct{ Path, Label string }

func within(root, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	return err == nil && (rel == "." || (!strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel)))
}

// discoverRunArtifacts is discoverRunArtifacts() in
// packages/llm/src/work/artifacts.ts: regular files inside the run's artifact
// folder and the org folder, with no symlink followed.
func discoverRunArtifacts(ws agentWorkspace) []runArtifact {
	root, err := filepath.EvalSymlinks(ws.ArtifactRoot)
	if err != nil {
		return nil
	}
	orgRoot, err := filepath.Abs(ws.OrgRoot)
	if err == nil {
		orgRoot, err = filepath.EvalSymlinks(orgRoot)
	}
	if err != nil {
		return nil
	}
	var out []runArtifact
	var walk func(dir string)
	walk = func(dir string) {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			candidate := filepath.Join(dir, e.Name())
			switch {
			case e.Type()&os.ModeSymlink != 0:
				continue
			case e.IsDir():
				walk(candidate)
				continue
			case !e.Type().IsRegular():
				continue
			}
			physical, err := filepath.EvalSymlinks(candidate)
			if err != nil || !within(root, physical) || !within(orgRoot, physical) {
				continue
			}
			rel, _ := filepath.Rel(orgRoot, physical)
			out = append(out, runArtifact{Path: filepath.ToSlash(rel), Label: filepath.Base(physical)})
		}
	}
	walk(root)
	slices.SortFunc(out, func(x, y runArtifact) int { return localeCompare(x.Path, y.Path) })
	return out
}
