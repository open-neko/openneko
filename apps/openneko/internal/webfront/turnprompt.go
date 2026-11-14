package webfront

import (
	_ "embed"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"
)

//go:embed turnprompt.txt
var turnPromptText string

var turnSections = parseTurnSections(turnPromptText)

func parseTurnSections(text string) map[string]string {
	out := map[string]string{}
	marks := regexp.MustCompile(`(?m)^@@ (\S+)\n`).FindAllStringSubmatchIndex(text, -1)
	for i, m := range marks {
		end := len(text)
		if i+1 < len(marks) {
			end = marks[i+1][0]
		}
		out[text[m[2]:m[3]]] = strings.TrimSuffix(text[m[1]:end], "\n")
	}
	return out
}

func section(name string, vars ...string) string {
	text, ok := turnSections[name]
	if !ok {
		panic("turnprompt.txt has no section " + name)
	}
	if len(vars) == 0 {
		return text
	}
	pairs := make([]string, len(vars))
	for i := 0; i < len(vars); i += 2 {
		pairs[i], pairs[i+1] = "${"+vars[i]+"}", vars[i+1]
	}
	return strings.NewReplacer(pairs...).Replace(text)
}

type agentWorkspace struct {
	OrgRoot           string `json:"orgRoot"`
	SkillsRoot        string `json:"skillsRoot"`
	MemoryRoot        string `json:"memoryRoot"`
	KnowledgeRoot     string `json:"knowledgeRoot"`
	UploadsRoot       string `json:"uploadsRoot"`
	RunsRoot          string `json:"runsRoot"`
	ThreadUploadsRoot string `json:"threadUploadsRoot"`
	RunRoot           string `json:"runRoot"`
	ArtifactRoot      string `json:"artifactRoot"`
	BinRoot           string `json:"binRoot"`
}

type knowledgePack struct {
	Mode       string `json:"mode"`
	Tables     string `json:"tables"`
	Namespaces string `json:"namespaces"`
	Insights   string `json:"insights"`
	Syntax     string `json:"syntax"`
}

type promptMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type installedSkill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type threadBot struct {
	Name         string `json:"name"`
	Instructions string `json:"instructions"`
}

// workPromptInput holds the arguments of buildWorkPrompt() in
// packages/llm/src/work/prompt.ts, with every default made explicit.
// FilesEnabled is filesEnabled() at the time of the call.
type workPromptInput struct {
	Backend                   string           `json:"backend"`
	Workspace                 agentWorkspace   `json:"workspace"`
	Knowledge                 knowledgePack    `json:"knowledge"`
	Messages                  []promptMessage  `json:"messages"`
	CurrentUserMessage        string           `json:"currentUserMessage"`
	PriorSummary              string           `json:"priorSummary"`
	MemoryContext             string           `json:"memoryContext"`
	OperatorProfile           string           `json:"operatorProfile"`
	InstalledSkills           []installedSkill `json:"installedSkills"`
	WantsCards                bool             `json:"wantsCards"`
	IncludeUxMetadata         bool             `json:"includeUxMetadata"`
	SupportsCardTool          bool             `json:"supportsCardTool"`
	RichCards                 bool             `json:"richCards"`
	SupportsSkillTool         bool             `json:"supportsSkillTool"`
	SupportsMemoryTool        bool             `json:"supportsMemoryTool"`
	SupportsWorkflowTool      bool             `json:"supportsWorkflowTool"`
	SupportsPolicyTool        bool             `json:"supportsPolicyTool"`
	SupportsSourceConfigTool  bool             `json:"supportsSourceConfigTool"`
	SupportsClarificationTool bool             `json:"supportsClarificationTool"`
	SupportsNativeDelegation  bool             `json:"supportsNativeDelegation"`
	InlineTranscript          bool             `json:"inlineTranscript"`
	Bot                       *threadBot       `json:"bot"`
	FilesEnabled              bool             `json:"filesEnabled"`
	ScriptData                bool             `json:"scriptData"`
}

const graphjinExecuteTool = "mcp_neko_graphjin_execute_graphql"

func pick(on bool, a, b string) string {
	if on {
		return a
	}
	return b
}

func buildWorkPrompt(in workPromptInput) string {
	ws := in.Workspace
	const shellTool = "terminal"
	cards := ""
	if in.WantsCards {
		cards = cardsSection(in.SupportsCardTool, in.RichCards, in.Backend == "ax")
	}
	delegation := ""
	if in.SupportsNativeDelegation && in.Backend == "ax" {
		delegation = section("ax_delegation")
	}
	sourceConfig := ""
	if in.SupportsSourceConfigTool {
		sourceConfig = section("source_config", "graphjinSkill", ws.SkillsRoot+"/graphjin-config/SKILL.md")
	}
	closing := ""
	if in.IncludeUxMetadata {
		closing = section("closing", "value", section("value_estimate"))
	}
	bot := ""
	if in.Bot != nil {
		bot = botSection(*in.Bot)
	}
	parts := []string{
		section("role", "backend", in.Backend),
		section("conversation"),
		pick(in.SupportsClarificationTool, section("clarification_tool"), section("clarification_plain")),
		section("selected_mentions", "skillsRoot", ws.SkillsRoot),
		cards,
		skillsSection(in.SupportsSkillTool, ws, in.InstalledSkills, pick(in.Backend == "ax", "search", "list")),
		pick(in.SupportsWorkflowTool, section("workflows_tool", "trigger", section("trigger_discovery")),
			section("workflows_fence", "trigger", section("trigger_discovery"))),
		pick(in.SupportsPolicyTool, section("rules_tool"), section("rules_fence")),
		sourceConfig,
		delegation,
		dataAccessSection(in.Knowledge, false, in.ScriptData),
		section("conduct", "date_rule", section("date_rule")),
		closing,
		in.OperatorProfile,
		bot,
		section("workspace", "orgRoot", ws.OrgRoot, "skillsRoot", ws.SkillsRoot, "memoryRoot", ws.MemoryRoot,
			"knowledgeRoot", ws.KnowledgeRoot, "threadUploadsRoot", ws.ThreadUploadsRoot,
			"artifactRoot", ws.ArtifactRoot, "shellTool", shellTool),
		memorySection(in.SupportsMemoryTool, in.FilesEnabled, in.MemoryContext),
	}
	kept := parts[:0]
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	if in.InlineTranscript {
		summary := ""
		if in.PriorSummary != "" {
			summary = "[Earlier conversation summary]\n" + in.PriorSummary + "\n\n"
		}
		kept = append(kept, section("transcript", "summaryBlock", summary,
			"transcript_lines", formatTranscript(in.Messages), "currentUserMessage", in.CurrentUserMessage))
	}
	return strings.Join(kept, "\n\n")
}

func formatTranscript(messages []promptMessage) string {
	if len(messages) == 0 {
		return "No prior messages."
	}
	lines := make([]string, len(messages))
	for i, m := range messages {
		lines[i] = strconv.Itoa(i+1) + ". " + pick(m.Role == "user", "User", "Assistant") + ": " + m.Content
	}
	return strings.Join(lines, "\n\n")
}

func cardsSection(cardTool, rich, answerFence bool) string {
	richPart := func(name string) string { return pick(rich, section(name), "") }
	return section("cards",
		"tool", pick(cardTool, "mcp__neko__ui_render_cards", "render_cards"),
		"choices", richPart("rich_card_choices"),
		"examples", richPart("rich_card_examples"),
		"fence", pick(answerFence, section("answer_fence_cards"), ""),
		"stages", richPart("rich_card_stages"))
}

func skillsSection(skillTool bool, ws agentWorkspace, skills []installedSkill, catalog string) string {
	creation := pick(skillTool, section("skill_create_tool"), section("skill_create_shell", "skillsRoot", ws.SkillsRoot))
	if catalog == "search" {
		return section("skills_search", "libraries", section("libraries"), "creationGuidance", creation)
	}
	list := "(none installed; check " + ws.SkillsRoot + ")"
	if len(skills) > 0 {
		lines := make([]string, len(skills))
		for i, s := range skills {
			lines[i] = "- " + s.Name + " — " + pick(s.Description != "", s.Description, "Capability instructions") +
				" — " + ws.SkillsRoot + "/" + s.Name + "/SKILL.md"
		}
		list = strings.Join(lines, "\n")
	}
	return section("skills_list", "libraries", section("libraries"), "skillList", list, "creationGuidance", creation)
}

func botSection(b threadBot) string {
	job := "You have no job description yet. Help with whatever the operator asks."
	if instructions := jsTrim(b.Instructions); instructions != "" {
		job = section("bot_job", "instructions", instructions)
	}
	return section("bot", "name", jsStringify(b.Name, false), "job", job)
}

func memorySection(searchTool, files bool, memoryContext string) string {
	loaded := jsTrim(memoryContext)
	if loaded == "" {
		loaded = "No memories are currently saved for this workspace."
	}
	var usage []string
	if searchTool {
		usage = append(usage, section("memory_search"))
		if files {
			usage = append(usage, section("memory_files"))
		}
		usage = append(usage, section("memory_tool"))
	} else {
		usage = append(usage, section("memory_fence"))
	}
	return section("memory", "loaded", loaded, "application", section("memory_apply"),
		"usage", "\n\n"+strings.Join(usage, "\n\n"))
}

// readOnlyMemorySection is buildMemorySection() with saveMode "none".
func readOnlyMemorySection(searchTool, files bool, memoryContext string) string {
	loaded := jsTrim(memoryContext)
	if loaded == "" {
		loaded = "No memories are currently saved for this workspace."
	}
	var usage []string
	if searchTool {
		usage = append(usage, section("memory_search"))
		if files {
			usage = append(usage, section("memory_files"))
		}
	}
	joined := ""
	if len(usage) > 0 {
		joined = "\n\n" + strings.Join(usage, "\n\n")
	}
	return section("memory", "loaded", loaded, "application", section("memory_apply"), "usage", joined)
}

func dataAccessSection(k knowledgePack, readOnly, scripts bool) string {
	return dataAccessSectionAs(k, readOnly, scripts, "actor credential")
}

func dataAccessSectionAs(k knowledgePack, readOnly, scripts bool, credential string) string {
	tool := func(name string) string { return strings.TrimSuffix(graphjinExecuteTool, "execute_graphql") + name }
	agentic := k.Mode == "agentic"
	var block string
	if agentic {
		block = section("knowledge_agentic", "tables_digest", compactTableDigest(k.Tables),
			"api_digest", compactAPIOperationDigest(k.Insights), "insights_digest", compactInsightsDigest(k.Insights),
			"help_index", compactHelpCardIndex(k.Insights), "syntax", k.Syntax)
	} else {
		block = section("knowledge_legacy", "tables", k.Tables, "namespaces", k.Namespaces,
			"insights", k.Insights, "syntax", k.Syntax)
	}
	return section("data_access",
		"credential", credential,
		"catalogTool", tool("query_catalog"),
		"helpTool", tool("graphql_help"),
		"validateTool", tool("validate_where_clause"),
		"savedQueryTool", tool("execute_saved_query"),
		"queryTool", graphjinExecuteTool,
		"scripts", pick(scripts, section("scripts_on", "queryTool", graphjinExecuteTool), section("scripts_off")),
		"writes", pick(readOnly, section("read_only"), section("writes")),
		"discovery", pick(agentic, section("agentic_discovery"), section("inline_discovery")),
		"date_rule", section("date_rule"),
		"fanout_rule", section("fanout_rule"),
		"aggregate_rule", section("aggregate_rule"),
		"knowledgeBlock", block)
}

func utf16Len(s string) int { return len(utf16.Encode([]rune(s))) }

func jsTruthy(v any) bool {
	switch x := v.(type) {
	case nil, jsUndefined:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case float64:
		return x != 0 && x == x
	}
	return true
}

func jsCollapseSpace(s string) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		if jsSpace(r) {
			space = true
			continue
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteRune(r)
	}
	if space {
		b.WriteByte(' ')
	}
	return b.String()
}

func parsedField(raw, key string) (any, bool) {
	v, err := jsParse([]byte(raw))
	if err != nil || v == nil {
		return nil, false
	}
	if obj, ok := v.(*jsObj); ok {
		return obj.vals[key], true
	}
	return nil, true
}

func objectField(v any, key string) any {
	if obj, ok := v.(*jsObj); ok {
		return obj.vals[key]
	}
	return nil
}

func jsArrayHead(v any, n int) []any {
	switch x := v.(type) {
	case []any:
		return x[:min(n, len(x))]
	case string:
		var out []any
		for _, r := range jsSlice(x, n) {
			out = append(out, string(r))
		}
		return out
	}
	return nil
}

// compactInsightsDigest is compactInsightsDigest() in
// packages/llm/src/prompts/sections.ts.
func compactInsightsDigest(raw string) string {
	field, ok := parsedField(raw, "hub_tables")
	hubs, _ := field.([]any)
	if !ok || len(hubs) == 0 {
		return ""
	}
	out := ""
	for _, hub := range hubs {
		name := "?"
		if n := objectField(hub, "name"); n != nil {
			name = jsDisplay(n)
		}
		block := "## " + name
		if s := objectField(hub, "summary"); jsTruthy(s) {
			block += " — " + jsSlice(jsDisplay(s), 110)
		}
		block += "\n"
		for _, path := range jsArrayHead(objectField(hub, "join_paths"), 6) {
			block += "  join: " + jsSlice(jsDisplay(path), 140) + "\n"
		}
		for _, ex := range jsArrayHead(objectField(hub, "examples"), 2) {
			q, isString := ex.(string)
			if !isString {
				if query := objectField(ex, "query"); query != nil {
					q = jsDisplay(query)
				} else {
					q = jsStringify(ex, false)
				}
			}
			block += "  template: " + jsSlice(jsCollapseSpace(q), 300) + "\n"
		}
		if utf16Len(out)+utf16Len(block) > 6000 {
			break
		}
		out += block
	}
	return strings.TrimRightFunc(out, jsSpace)
}

func namedIndex(raw, key, nameKey string, width, limit int) string {
	field, _ := parsedField(raw, key)
	items, _ := field.([]any)
	out := ""
	for _, item := range items {
		name := objectField(item, nameKey)
		if !jsTruthy(name) {
			continue
		}
		line := "- " + jsDisplay(name)
		if s := objectField(item, "summary"); jsTruthy(s) {
			line += " — " + jsSlice(jsDisplay(s), width)
		}
		line += "\n"
		if utf16Len(out)+utf16Len(line) > limit {
			break
		}
		out += line
	}
	return strings.TrimRightFunc(out, jsSpace)
}

func compactAPIOperationDigest(raw string) string {
	return namedIndex(raw, "api_operations", "name", 90, 4000)
}

func compactHelpCardIndex(raw string) string {
	return namedIndex(raw, "help_cards", "id", 90, 2000)
}

func tableDigestLines(raw string) ([]string, bool) {
	field, ok := parsedField(raw, "tables")
	tables, _ := field.([]any)
	if !ok || len(tables) == 0 {
		return nil, false
	}
	lines := make([]string, len(tables))
	for i, t := range tables {
		if t == nil {
			return nil, false
		}
		var parts []string
		for _, key := range []string{"schema", "name"} {
			if v := objectField(t, key); jsTruthy(v) {
				parts = append(parts, jsDisplay(v))
			}
		}
		name := strings.Join(parts, ".")
		if name == "" {
			name = "?"
			if id := objectField(t, "id"); jsTruthy(id) {
				name = jsDisplay(id)
			}
		}
		extra := ""
		if s := objectField(t, "summary"); jsTruthy(s) {
			extra = " — " + jsSlice(jsDisplay(s), 60)
		} else if c := objectField(t, "column_count"); c != nil {
			extra = " (" + jsDisplay(c) + " cols)"
		}
		lines[i] = "- " + name + extra
	}
	return lines, true
}

func compactTableDigest(raw string) string {
	lines, ok := tableDigestLines(raw)
	if !ok {
		lines = nil
		for _, l := range strings.Split(raw, "\n") {
			if jsTrim(l) != "" {
				lines = append(lines, l)
			}
		}
	}
	out := ""
	dropped := 0
	for _, line := range lines {
		if utf16Len(out)+utf16Len(line)+1 > 4000 {
			kept := 0
			for _, l := range strings.Split(out, "\n") {
				if l != "" {
					kept++
				}
			}
			dropped = len(lines) - kept
			break
		}
		out += line + "\n"
	}
	if dropped > 0 {
		out += "… " + strconv.Itoa(dropped) + " more — list the rest via gj_catalog (kind: \"table\").\n"
	}
	return strings.TrimRightFunc(out, jsSpace)
}
