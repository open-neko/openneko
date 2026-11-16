package webfront

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestToolStepLabelPerFamily(t *testing.T) {
	q := func(query string) any { return jsObjOf("query", query) }
	cases := []struct {
		name  string
		input any
		want  string
	}{
		{"mcp_neko_graphjin_execute_graphql", q(`query { sales_orders(where: { order_date: { gte: "2024-01-01" } }, order_by: { order_date: asc }) { id total } }`), "Reading sales orders, filtered by order date"},
		{"mcp_neko_graphjin_execute_graphql", q(`{ customers(where: { and: [{ country: { eq: "DE" } }, { not: { is_active: { eq: false } } }] }) { id } products { id } }`), "Reading customers and products, filtered by country and is active"},
		{"mcp_neko_graphjin_execute_graphql", q(`{ a { id } b { id } c { id } d { id } e { id } }`), "Reading a, b, c and 2 more"},
		{"mcp_neko_graphjin_execute_graphql", q(`{ __schema { types { name } } }`), "Checking the shape of the data"},
		{"mcp_neko_graphjin_execute_graphql", q(`mutation { orders(update: { status: "x" }, where: { id: { eq: 1 } }) { id } }`), "Changing orders, filtered by id"},
		{"mcp_neko_graphjin_execute_graphql", q(`{ broken(`), "Reading data"},
		{"mcp_neko_graphjin_execute_graphql", nil, "Reading data"},
		{"graphjin_query", q(`{ sales }`), "Reading sales"},
		{"mcp_neko_graphjin_execute_saved_query", jsObjOf("name", "monthly_revenue"), "Running the saved query “monthly_revenue”"},
		{"mcp_neko_graphjin_graphql_help", nil, "Checking how to query the data"},
		{"mcp_neko_graphjin_query_catalog", nil, "Looking through the data catalog"},
		{"mcp_neko_graphjin_validate_where_clause", nil, "Checking a query filter"},
		{"mcp_neko_source_config_manager_describe_source_graph", nil, "Checking the connected data sources"},
		{"mcp_neko_source_config_manager_describe_source_config", nil, "Reading the data source settings"},
		{"mcp_neko_source_config_manager_request_source_config_change", nil, "Preparing a source config change for approval"},
		{"mcp_neko_data_source_manager_list_data_sources", nil, "Listing data sources"},
		{"mcp_neko_user_manager_request_item_grant", nil, "Preparing an item grant request"},
		{"mcp_neko_files_search", jsObjOf("query", "freight   invoices Q3"), "Searching your files for “freight invoices Q3”"},
		{"mcp_neko_files_read_text", jsObjOf("path", "uploads/u1/Budget 2025.xlsx"), "Reading Budget 2025.xlsx"},
		{"mcp_neko_files_read_text", nil, "Reading one of your files"},
		{"mcp_neko_files_query_tables", jsObjOf("sql", "select * from budget"), "Querying your spreadsheet tables"},
		{"upload_search", jsObjOf("query", "contract"), "Searching your uploads for “contract”"},
		{"mcp_neko_memory_search", jsObjOf("query", "x"), "Searching saved memories"},
		{"memory_save", nil, "Saving a memory"},
		{"mcp_neko_guides_find", jsObjOf("query", "x"), "Looking for a skill guide"},
		{"skill_view", jsObjOf("path", "/srv/neko/org/skills/xlsx/SKILL.md"), "Opening the xlsx skill"},
		{"skill_used", jsObjOf("path", "/srv/neko/org/skills/xlsx/SKILL.md"), "Using the xlsx skill"},
		{"mcp_neko_interaction_ask_user_question", nil, "Preparing a question for you"},
		{"mcp_neko_workflow_builder_create_workflow", nil, "Drafting a workflow"},
		{"mcp_neko_workflow_builder_list_workflows", nil, "Listing workflows"},
		{"mcp_neko_rule_builder_save_rule", nil, "Drafting a rule"},
		{"mcp_neko_audit_audit_trail", nil, "Reading the audit trail"},
		{"mcp_neko_action_request", nil, "Preparing an action for approval"},
		{"mcp_neko_pack_actions_magento_reindex", nil, "Preparing the magento reindex action"},
		{"terminal", jsObjOf("command", "cat /etc/passwd"), "Running a command in the sandbox"},
		{"web_fetch", jsObjOf("url", "https://example.com/a?b=c"), "Reading a web page on example.com"},
		{"file_read", jsObjOf("path", "out/report.md"), "Reading report.md"},
		{"file_read", jsObjOf("path", "skills/graphjin-config/SKILL.md"), "Reading the graphjin-config skill"},
		{"upload_search", jsObjOf("glob", "*"), "Searching your uploads"},
		{"mcp_neko_graphjin_execute_graphql", q(`{ y2022: salesorderheader(where: { and: [{ orderdate: { gte: "2022-01-01" } }, { orderdate: { lt: "2023-01-01" } }] }) { sum_totaldue } y2023: salesorderheader { sum_totaldue } }`), "Reading salesorderheader, filtered by orderdate"},
		{"file_write", jsObjOf("path", "out/report.md"), "Writing report.md"},
		{"file_search", nil, "Searching the workspace"},
		{"mcp_neko_something_new", nil, "Working on the next step"},
		{"", nil, "Working on the next step"},
	}
	for _, c := range cases {
		if got := toolStepLabel(c.name, c.input); got != c.want {
			t.Errorf("toolStepLabel(%q) = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestToolStepLabelCarriesNoQueryText(t *testing.T) {
	query := `query secretOp($v: String) { orders(where: { note: { eq: "Acme overdue 40,000" }, owner: { eq: $v } }) { id note } }`
	got := toolStepLabel("mcp_neko_graphjin_execute_graphql", jsObjOf("query", query, "variables", jsObjOf("v", "pat@example.com")))
	for _, leak := range []string{"Acme", "40,000", "pat@", "secretOp", "{", "eq"} {
		if strings.Contains(got, leak) {
			t.Fatalf("label %q holds %q from the query", got, leak)
		}
	}
	if got != "Reading orders, filtered by note and owner" {
		t.Fatalf("label = %q", got)
	}
}

func TestToolStepLabelIsOneShortLine(t *testing.T) {
	long := strings.Repeat("quarterly freight invoices ", 20)
	got := toolStepLabel("mcp_neko_files_search", jsObjOf("query", long+"\nsecond line"))
	if strings.ContainsAny(got, "\n\r") || len([]rune(got)) > stepLabelMax {
		t.Fatalf("label %q: %d runes, want one line of %d or fewer", got, len([]rune(got)), stepLabelMax)
	}
	got = toolStepLabel("mcp_neko_graphjin_execute_graphql", jsObjOf("query", "{ "+strings.Repeat("a_very_long_table_name_here ", 3)+"{ id } }"))
	if len([]rune(got)) > stepLabelMax {
		t.Fatalf("label %q is longer than %d", got, stepLabelMax)
	}
}

func TestToolFailLabel(t *testing.T) {
	for name, want := range map[string]string{
		"mcp_neko_graphjin_execute_graphql": "That query failed; trying another way",
		"mcp_neko_files_query_tables":       "That query failed; trying another way",
		"mcp_neko_files_read_text":          "Could not read that file; trying another way",
		"mcp_neko_memory_save":              "That step failed; trying another way",
	} {
		if got := toolFailLabel(name); got != want {
			t.Errorf("toolFailLabel(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestToolStepLabelsPassTheSecretScrub(t *testing.T) {
	scrub := func(s string) string { return strings.ReplaceAll(s, "sk-live-123456", "[REDACTED]") }
	start := jsObjOf("type", "tool_start", "id", "ax-op-1", "name", "mcp_neko_files_search",
		"label", toolStepLabel("mcp_neko_files_search", jsObjOf("query", "sk-live-123456")))
	end := jsObjOf("type", "tool_end", "id", "ax-op-1", "failed", true, "label", "failed for sk-live-123456")
	for _, ev := range []*jsObj{start, end} {
		out := jsStringify(scrubRunEvent(scrub, ev), false)
		if strings.Contains(out, "sk-live-123456") || !strings.Contains(out, "[REDACTED]") {
			t.Fatalf("scrubbed event %s still holds the secret", out)
		}
	}
}

func TestAxMapperLabelsToolSteps(t *testing.T) {
	var got []*jsObj
	m := &axEventMapper{emit: func(e *jsObj) { got = append(got, e) }}
	op := func(n float64) *float64 { return &n }
	name := "mcp_neko_graphjin_execute_graphql"
	other := "mcp_neko_memory_search"
	m.handle(axEvent{Type: "tool.started", OperationID: op(1), Name: &name, Data: json.RawMessage(`{"query":"{ sales_orders(where:{year:{eq:2024}}) { id } }"}`)})
	m.handle(axEvent{Type: "tool.finished", OperationID: op(1), Data: json.RawMessage(`{"content":[{"type":"text","text":"{\"errors\":[{\"message\":\"column not found\"}]}"}]}`)})
	m.handle(axEvent{Type: "tool.started", OperationID: op(2), Name: &other, Data: json.RawMessage(`{"query":"x"}`)})
	m.handle(axEvent{Type: "tool.finished", OperationID: op(2), Data: json.RawMessage(`{"content":[{"type":"text","text":"[]"}]}`)})
	if len(got) != 4 {
		t.Fatalf("got %d events", len(got))
	}
	if l := got[0].vals["label"]; l != "Reading sales orders, filtered by year" {
		t.Fatalf("start label = %v", l)
	}
	if got[1].vals["failed"] != true || got[1].vals["label"] != "That query failed; trying another way" {
		t.Fatalf("failed end = %s", jsStringify(got[1], false))
	}
	if _, ok := got[3].vals["label"]; ok {
		t.Fatalf("a call that worked has a failure label: %s", jsStringify(got[3], false))
	}
	if got[2].vals["label"] != "Searching saved memories" {
		t.Fatalf("second start = %s", jsStringify(got[2], false))
	}
}
