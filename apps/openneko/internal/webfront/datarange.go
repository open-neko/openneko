package webfront

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

// The date ranges of a source go into the prompt so the agent finds the
// anchor of a relative period without a query. They are read again after
// dateRangeTTL, because live data moves the newest date.
const (
	dateRangeTTL      = 15 * time.Minute
	dateRangeTables   = 25
	dateRangeColumns  = 3
	dateRangeParallel = 6
	dateRangeMax      = 3000
)

type dateRangeEntry struct {
	at   time.Time
	text string
}

var (
	dateRangeMu    sync.Mutex
	dateRangeCache = map[string]dateRangeEntry{}
	dateColumnType = regexp.MustCompile(`^(date|timestamp)`)
)

type dateColumn struct{ table, column string }

// dateRangesText is the date ranges block of the org's default source, or
// "" when the source has none or does not answer.
func (a *API) dateRangesText(ctx context.Context, orgID string) string {
	dateRangeMu.Lock()
	entry, ok := dateRangeCache[orgID]
	dateRangeMu.Unlock()
	if ok && time.Since(entry.at) < dateRangeTTL {
		return entry.text
	}
	text, err := a.readDateRanges(ctx, orgID)
	if err != nil {
		log.Printf("[date-ranges] org=%s: %v", orgID, err)
	}
	dateRangeMu.Lock()
	dateRangeCache[orgID] = dateRangeEntry{at: time.Now(), text: text}
	dateRangeMu.Unlock()
	return text
}

func (a *API) readDateRanges(ctx context.Context, orgID string) (string, error) {
	var authMode, graphqlURL string
	var mcpURL *string
	err := a.DB.QueryRow(ctx, `select auth_mode, graphql_url, mcp_url from data_source
		where org_id = $1 and enabled = true order by is_default desc, created_at limit 1`, orgID).Scan(&authMode, &graphqlURL, &mcpURL)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && authMode != "jwt" {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	endpoint := graphqlURL
	if endpoint == "" && mcpURL != nil {
		endpoint = mcpPathSuffix.ReplaceAllString(*mcpURL, "/graphql")
	}
	token, err := a.graphjinToken(orgID, "service", 120*time.Second)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	return dateRanges(ctx, endpoint, token, time.Now())
}

// dateRanges reads the date columns from the catalog, then the smallest and
// largest value of each with one aggregate query per table.
func dateRanges(ctx context.Context, endpoint, token string, at time.Time) (string, error) {
	rows, err := catalogRows(ctx, endpoint, token, listQuery(kindWhere("column"), 5000, "table_name column_name summary"))
	if err != nil {
		return "", err
	}
	var tables []string
	columns := map[string][]string{}
	for _, row := range rows {
		table := jsValueString(objectField(row, "table_name"))
		column := jsValueString(objectField(row, "column_name"))
		summary := strings.ToLower(jsValueString(objectField(row, "summary")))
		if !dateColumnType.MatchString(summary) || strings.Contains(summary, "sensitive") ||
			!graphqlName.MatchString(table) || !graphqlName.MatchString(column) {
			continue
		}
		if _, seen := columns[table]; !seen {
			if len(tables) >= dateRangeTables {
				continue
			}
			tables = append(tables, table)
		}
		if len(columns[table]) < dateRangeColumns {
			columns[table] = append(columns[table], column)
		}
	}
	lines := make([][]string, len(tables))
	sem := make(chan struct{}, dateRangeParallel)
	var wg sync.WaitGroup
	for i, table := range tables {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			lines[i] = tableDateRanges(ctx, endpoint, token, table, columns[table])
		})
	}
	wg.Wait()
	var out strings.Builder
	for _, group := range lines {
		for _, line := range group {
			if out.Len()+len(line) > dateRangeMax {
				break
			}
			out.WriteString(line + "\n")
		}
	}
	if out.Len() == 0 {
		return "", nil
	}
	return section("date_ranges", "at", at.UTC().Format("2006-01-02 15:04"), "ranges", strings.TrimRight(out.String(), "\n")), nil
}

func tableDateRanges(ctx context.Context, endpoint, token, table string, cols []string) []string {
	fields := make([]string, 0, 2*len(cols))
	for _, c := range cols {
		fields = append(fields, "min_"+c, "max_"+c)
	}
	status, raw, err := knowledgePost(ctx, endpoint, token, fmt.Sprintf("query { %s { %s } }", table, strings.Join(fields, " ")))
	if err != nil || status < 200 || status > 299 {
		return nil
	}
	body, err := jsParse(bytes.TrimSpace(raw))
	if err != nil {
		return nil
	}
	if errs, _ := jsField(body, "errors"); len(jsArray(errs)) > 0 {
		return nil
	}
	data, _ := jsField(body, "data")
	got, _ := jsField(data, table)
	if list := jsArray(got); len(list) > 0 {
		got = list[0]
	}
	var out []string
	for _, c := range cols {
		lo, hi := dateText(objectField(got, "min_"+c)), dateText(objectField(got, "max_"+c))
		if lo != "" && hi != "" {
			out = append(out, fmt.Sprintf("- %s.%s: %s to %s", table, c, lo, hi))
		}
	}
	return out
}

func dateRangesBlock(text string) string {
	if text = strings.TrimSpace(text); text == "" {
		return ""
	}
	return "\n\n" + text
}

// dateText shows a date with its time only when the time is not midnight.
func dateText(v any) string {
	s, ok := v.(string)
	if !ok {
		return ""
	}
	t, ok := parseCompileTime(s)
	if !ok {
		return ""
	}
	if t.Hour() == 0 && t.Minute() == 0 && t.Second() == 0 {
		return t.Format("2006-01-02")
	}
	return t.Format("2006-01-02 15:04")
}
