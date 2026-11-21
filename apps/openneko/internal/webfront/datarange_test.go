package webfront

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDateRangesReadTheDateColumnsOfTheCatalog(t *testing.T) {
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body struct{ Query string }
		_ = json.Unmarshal(raw, &body)
		queries = append(queries, body.Query)
		switch {
		case strings.Contains(body.Query, "gj_catalog"):
			_, _ = io.WriteString(w, `{"data":{"gj_catalog":[
				{"table_name":"salesorderheader","column_name":"orderdate","summary":"timestamp without time zone, not null"},
				{"table_name":"salesorderheader","column_name":"subtotal","summary":"numeric, not null"},
				{"table_name":"employee","column_name":"birthdate","summary":"date, sensitive:pii"},
				{"table_name":"workorder","column_name":"startdate","summary":"timestamp without time zone"},
				{"table_name":"broken","column_name":"day","summary":"date"}]}}`)
		case strings.Contains(body.Query, "salesorderheader"):
			_, _ = io.WriteString(w, `{"data":{"salesorderheader":[{"min_orderdate":"2022-05-30T00:00:00","max_orderdate":"2025-06-29T00:00:00"}]}}`)
		case strings.Contains(body.Query, "workorder"):
			_, _ = io.WriteString(w, `{"data":{"workorder":{"min_startdate":"2022-06-02T00:00:00","max_startdate":"2025-06-01T08:30:00"}}}`)
		default:
			_, _ = io.WriteString(w, `{"errors":[{"message":"table not found"}]}`)
		}
	}))
	defer srv.Close()
	text, err := dateRanges(t.Context(), srv.URL, "token", time.Date(2026, 11, 21, 9, 5, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"read at 2026-11-21 09:05 UTC", "- salesorderheader.orderdate: 2022-05-30 to 2025-06-29",
		"- workorder.startdate: 2022-06-02 to 2025-06-01 08:30"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in\n%s", want, text)
		}
	}
	if strings.Contains(text, "birthdate") || strings.Contains(text, "broken") || strings.Contains(text, "subtotal") {
		t.Errorf("a sensitive, failed or non-date column is listed:\n%s", text)
	}
	if !strings.Contains(strings.Join(queries, "\n"), "query { salesorderheader { min_orderdate max_orderdate } }") {
		t.Errorf("queries: %v", queries)
	}
}

func TestDateRangesSectionStaysOutOfThePromptWhenEmpty(t *testing.T) {
	block := dataAccessSection(knowledgePack{Mode: "agentic"}, true, false)
	if strings.Contains(block, "Date ranges") {
		t.Fatal("an empty range list must add no block")
	}
	block = dataAccessSection(knowledgePack{Mode: "agentic", DateRanges: section("date_ranges", "at", "x", "ranges", "- a.b: 1 to 2")}, true, false)
	if !strings.Contains(block, "- a.b: 1 to 2") {
		t.Fatal("the ranges are missing from the data access block")
	}
}
