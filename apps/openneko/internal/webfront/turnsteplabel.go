package webfront

import (
	"net/url"
	"path"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
)

const stepLabelMax = 90

// toolFamily strips the transport prefixes: "mcp_neko_graphjin_execute_graphql"
// and "graphjin_execute_graphql" both give "graphjin_execute_graphql".
func toolFamily(name string) string {
	name = strings.TrimPrefix(name, "mcp_")
	name = strings.TrimPrefix(name, "_")
	name = strings.TrimPrefix(name, "neko_")
	return strings.TrimPrefix(name, "_")
}

func plainWords(s string) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "__", "_"), "_", " ")
	return strings.Join(strings.Fields(s), " ")
}

func clipLabel(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= stepLabelMax {
		return s
	}
	r := []rune(s)
	return strings.TrimRight(string(r[:stepLabelMax-1]), " ,;:") + "…"
}

func quotedArg(input any, key string, max int) string {
	s, ok := jsNonEmptyString(jsGet(input, key))
	if !ok {
		return ""
	}
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > max {
		s = strings.TrimRight(string(r[:max-1]), " ") + "…"
	}
	return "“" + s + "”"
}

func forQuery(input any) string {
	if q := quotedArg(input, "query", 48); q != "" {
		return "for " + q
	}
	return ""
}

func baseName(input any, key string) string {
	p, ok := jsNonEmptyString(jsGet(input, key))
	if !ok {
		return ""
	}
	b := path.Base(strings.ReplaceAll(p, "\\", "/"))
	if b == "." || b == "/" {
		return ""
	}
	return b
}

func skillName(input any) string {
	p, ok := jsNonEmptyString(jsGet(input, "path"))
	if !ok {
		return ""
	}
	return path.Base(path.Dir(strings.ReplaceAll(p, "\\", "/")))
}

// toolStepLabel says in plain words what a tool call does and on what. It
// never carries query text; the caller scrubs it as run text.
func toolStepLabel(name string, input any) string {
	family := toolFamily(name)
	with := func(prefix, detail string) string {
		if detail == "" {
			return prefix
		}
		return prefix + " " + detail
	}
	var label string
	switch family {
	case "graphjin_execute_graphql", "graphjin_query":
		q, _ := jsNonEmptyString(jsGet(input, "query"))
		label = graphqlStepLabel(q)
	case "graphjin_execute_saved_query":
		label = with("Running the saved query", quotedArg(input, "name", 40))
	case "graphjin_graphql_help":
		label = "Checking how to query the data"
	case "graphjin_query_catalog":
		label = "Looking through the data catalog"
	case "graphjin_validate_where_clause":
		label = "Checking a query filter"
	case "source_config_manager_describe_source_graph":
		label = "Checking the connected data sources"
	case "source_config_manager_describe_source_config":
		label = "Reading the data source settings"
	case "source_config_manager_check_source":
		label = "Testing a data source connection"
	case "files_search":
		label = with("Searching your files", forQuery(input))
	case "files_read_text":
		label = with("Reading", baseName(input, "path"))
		if label == "Reading" {
			label = "Reading one of your files"
		}
	case "files_query_tables":
		label = "Querying your spreadsheet tables"
	case "upload_search":
		label = with("Searching your uploads", forQuery(input))
	case "memory_search":
		label = "Searching saved memories"
	case "memory_save":
		label = "Saving a memory"
	case "memory_update":
		label = "Updating a memory"
	case "guides_find":
		label = "Looking for a skill guide"
	case "skill_view":
		label = with("Opening the", skillName(input)) + " skill"
	case "skill_used":
		label = with("Using the", skillName(input)) + " skill"
	case "skills_create_skill":
		label = "Writing a new skill"
	case "interaction_ask_user_question":
		label = "Preparing a question for you"
	case "workflow_builder_create_workflow":
		label = "Drafting a workflow"
	case "workflow_builder_delete_workflow":
		label = "Preparing to delete a workflow"
	case "rule_builder_save_rule":
		label = "Drafting a rule"
	case "audit_audit_trail":
		label = "Reading the audit trail"
	case "action_request":
		label = "Preparing an action for approval"
	case "workflow_output_emit":
		label = "Writing the routine's output"
	case "terminal":
		label = "Running a command in the sandbox"
	case "web_fetch":
		label = "Reading a web page"
		if raw, ok := jsNonEmptyString(jsGet(input, "url")); ok {
			if u, err := url.Parse(raw); err == nil && u.Hostname() != "" {
				label += " on " + u.Hostname()
			}
		}
	case "file_read":
		label = with("Reading", baseName(input, "path"))
		if strings.EqualFold(baseName(input, "path"), "SKILL.md") {
			label = with("Reading the", skillName(input)) + " skill"
		}
	case "file_write":
		label = with("Writing", baseName(input, "path"))
	case "file_edit":
		label = with("Editing", baseName(input, "path"))
	case "file_search":
		label = "Searching the workspace"
	default:
		label = genericStepLabel(family)
	}
	return clipLabel(label)
}

func genericStepLabel(family string) string {
	if strings.HasPrefix(family, "pack_actions_") {
		return "Preparing the " + plainWords(strings.TrimPrefix(family, "pack_actions_")) + " action"
	}
	verb := family
	for _, server := range []string{"user_manager_", "channel_manager_", "data_source_manager_", "source_config_manager_", "workflow_builder_", "rule_builder_", "graphjin_"} {
		verb = strings.TrimPrefix(verb, server)
	}
	switch {
	case strings.HasPrefix(verb, "list_"):
		return "Listing " + plainWords(strings.TrimPrefix(verb, "list_"))
	case strings.HasPrefix(verb, "request_") && strings.HasSuffix(verb, "_change"):
		return "Preparing " + withArticle(plainWords(strings.TrimSuffix(strings.TrimPrefix(verb, "request_"), "_change"))) + " change for approval"
	case strings.HasPrefix(verb, "request_"):
		return "Preparing " + withArticle(plainWords(strings.TrimPrefix(verb, "request_"))) + " request"
	case strings.HasPrefix(verb, "present_"):
		return "Preparing " + withArticle(plainWords(strings.TrimPrefix(verb, "present_")))
	case strings.HasPrefix(verb, "import_"):
		return "Importing " + plainWords(strings.TrimPrefix(verb, "import_"))
	}
	return "Working on the next step"
}

func withArticle(noun string) string {
	if noun != "" && strings.ContainsRune("aeiou", rune(noun[0])) {
		return "an " + noun
	}
	return "a " + noun
}

// graphqlStepLabel names the tables a query reads and the fields it filters
// on, with no values.
func graphqlStepLabel(query string) string {
	doc, err := parser.ParseQuery(&ast.Source{Input: query})
	if err != nil || len(doc.Operations) == 0 {
		return "Reading data"
	}
	var tables, filters []string
	seenTable, seenFilter := map[string]bool{}, map[string]bool{}
	schemaOnly := true
	mutation := false
	for _, op := range doc.Operations {
		if op.Operation == ast.Mutation {
			mutation = true
		}
		for _, sel := range op.SelectionSet {
			f, ok := sel.(*ast.Field)
			if !ok {
				continue
			}
			if strings.HasPrefix(f.Name, "__") {
				continue
			}
			schemaOnly = false
			if t := plainWords(f.Name); t != "" && !seenTable[t] {
				seenTable[t] = true
				tables = append(tables, t)
			}
			for _, arg := range f.Arguments {
				if arg.Name != "where" {
					continue
				}
				for _, name := range whereFields(arg.Value) {
					if w := plainWords(name); w != "" && !seenFilter[w] {
						seenFilter[w] = true
						filters = append(filters, w)
					}
				}
			}
		}
	}
	if schemaOnly {
		return "Checking the shape of the data"
	}
	label := "Reading " + joinPlain(tables, 3)
	if mutation {
		label = "Changing " + joinPlain(tables, 3)
	}
	if len(filters) > 0 {
		label += ", filtered by " + joinPlain(filters, 3)
	}
	return label
}

func whereFields(v *ast.Value) []string {
	if v == nil || v.Kind != ast.ObjectValue {
		return nil
	}
	var out []string
	for _, c := range v.Children {
		switch strings.ToLower(c.Name) {
		case "and", "or", "not":
			if c.Value != nil && c.Value.Kind == ast.ListValue {
				for _, item := range c.Value.Children {
					out = append(out, whereFields(item.Value)...)
				}
			} else {
				out = append(out, whereFields(c.Value)...)
			}
		default:
			out = append(out, c.Name)
		}
	}
	return out
}

func joinPlain(items []string, max int) string {
	if len(items) == 0 {
		return "data"
	}
	more := 0
	if len(items) > max {
		more = len(items) - max
		items = items[:max]
	}
	if more > 0 {
		return strings.Join(items, ", ") + " and " + strconv.Itoa(more) + " more"
	}
	switch len(items) {
	case 1:
		return items[0]
	case 2:
		return items[0] + " and " + items[1]
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}

// toolFailLabel says a failed call in plain words.
func toolFailLabel(name string) string {
	switch family := toolFamily(name); {
	case strings.HasPrefix(family, "graphjin_execute"), family == "graphjin_query", family == "files_query_tables":
		return "That query failed; trying another way"
	case strings.HasPrefix(family, "files_"), family == "upload_search", strings.HasPrefix(family, "file_"):
		return "Could not read that file; trying another way"
	}
	return "That step failed; trying another way"
}
