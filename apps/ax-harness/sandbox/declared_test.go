package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const ordersSidecar = `{"queries": {
  "orders": {"query": "query ($since: String!, $limit: Int) { orders(where: {created_at: {gte: $since}}, limit: $limit) { id } }",
             "params": {"since": {}, "limit": {"type": "integer", "default": 50}}},
  "stock": {"query": "query { stock { sku } }", "namespace": "shop"}
}}`

func declaredSkill(t *testing.T, runRoot, sidecar string) string {
	t.Helper()
	skill := filepath.Join(filepath.Dir(filepath.Dir(runRoot)), "skills", "stock")
	write(t, filepath.Join(skill, "report.py"), "print('report')\n")
	if sidecar != "" {
		write(t, filepath.Join(skill, "report.py"+DeclaredSuffix), sidecar)
	}
	return skill
}

func queryFiles(fake *scriptSandbox) map[string]string {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	out := map[string]string{}
	for p, data := range fake.files {
		if name, ok := strings.CutPrefix(p, "runs/r1/.neko-data/queries/"); ok {
			out[name] = string(data)
		}
	}
	return out
}

func TestCommandWords(t *testing.T) {
	vars := map[string]string{"SKILL_DIR": "/ws/skills/a b", "RUN_DIR": "/ws/runs/r1"}
	got := commandWords(`X="$(cat '/ws/in.json')" exec 'python3' "$SKILL_DIR/r.py" --since=\$SKILL_DIR '${RUN_DIR}' && ${RUN_DIR}/bin/x|wc -l; echo $OTHER`, vars)
	want := [][]string{
		{"X=$(cat '/ws/in.json')", "exec", "python3", "/ws/skills/a b/r.py", "--since=$SKILL_DIR", "${RUN_DIR}"},
		{"/ws/runs/r1/bin/x"},
		{"wc", "-l"},
		{"echo", "$OTHER"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%q", got)
	}
}

func TestDeclaredRowsArriveAsFiles(t *testing.T) {
	b := &countingBroker{reply: func(q DataQuery) (json.RawMessage, error) {
		if q.Namespace == "shop" {
			return json.RawMessage(`{"stock":[{"sku":"A"}]}`), nil
		}
		return json.RawMessage(`{"orders":[{"id":1},{"id":2}]}`), nil
	}}
	var seen []DataQuery
	broker := func(ctx context.Context, q DataQuery) (json.RawMessage, error) {
		seen = append(seen, q)
		return b.broker(ctx, q)
	}
	c, fake, runRoot := dataClient(t, 0, broker, DefaultDataLimits)
	skill := declaredSkill(t, runRoot, ordersSidecar)

	res, err := c.Exec(context.Background(), `python3 "$SKILL_DIR/report.py" --since 2026-11-01`, skill, map[string]string{"RUN_DIR": runRoot}, 60, 50_000)
	if err != nil || res.ExitCode != 0 || fake.execs != 1 {
		t.Fatalf("%+v %v, %d runs", res, err, fake.execs)
	}
	if len(seen) != 2 || string(seen[0].Variables) != `{"limit":50,"since":"2026-11-01"}` || seen[1].Namespace != "shop" || seen[1].Variables != nil {
		t.Fatalf("queries: %+v", seen)
	}
	files := queryFiles(fake)
	if !reflect.DeepEqual(files, map[string]string{"orders.json": `{"orders":[{"id":1},{"id":2}]}`, "stock.json": `{"stock":[{"sku":"A"}]}`}) {
		t.Fatalf("files in the sandbox: %v", files)
	}
	record, _ := os.ReadFile(filepath.Join(runRoot, ".openneko", "script-data.jsonl"))
	if n := strings.Count(string(record), "\n"); n != 2 || !strings.Contains(string(record), `"rowCount":2`) {
		t.Fatalf("record: %s", record)
	}

	if _, err := c.Exec(context.Background(), "python3 "+filepath.Join(skill, "report.py")+" --since=2026-11-01 --limit 50", "", nil, 60, 50_000); err != nil || len(b.calls) != 2 {
		t.Fatalf("a rerun reads the record: %v, %d calls", err, len(b.calls))
	}
	if _, err := c.Exec(context.Background(), "python3 "+filepath.Join(skill, "report.py")+" --since 2026-10-01 --limit 5", "", nil, 60, 50_000); err != nil || len(b.calls) != 3 {
		t.Fatalf("new parameters run the query: %v, %d calls", err, len(b.calls))
	}
	if string(seen[2].Variables) != `{"limit":5,"since":"2026-10-01"}` {
		t.Fatalf("variables: %s", seen[2].Variables)
	}

	workflow := `OPENNEKO_WORKFLOW_INPUT="$(cat '` + runRoot + `/steps/a.input.json')" exec 'python3' '` + filepath.Join(skill, "report.py") + `' '--since' '2026-11-01'`
	if res, err := c.ExecIn(context.Background(), runRoot, workflow, skill, nil, 60, 50_000); err != nil || res.ExitCode != 0 || len(b.calls) != 3 || len(queryFiles(fake)) != 2 {
		t.Fatalf("a workflow script step: %+v %v, %d calls", res, err, len(b.calls))
	}

	if _, err := c.Exec(context.Background(), "ls", "", nil, 60, 50_000); err != nil || len(queryFiles(fake)) != 0 {
		t.Fatalf("the rows of an earlier script stay: %v %v", err, queryFiles(fake))
	}
}

func TestDeclaredQueryRefused(t *testing.T) {
	b := &countingBroker{reply: func(DataQuery) (json.RawMessage, error) {
		return nil, errors.New("No data source is available to this run.")
	}}
	c, fake, runRoot := dataClient(t, 0, b.broker, DefaultDataLimits)
	skill := declaredSkill(t, runRoot, ordersSidecar)
	res, err := c.Exec(context.Background(), "python3 $SKILL_DIR/report.py --since 2026-11-01", skill, nil, 60, 50_000)
	if err != nil || res.ExitCode != 1 || fake.execs != 0 || len(b.calls) != 1 ||
		res.Output != "skills/stock/report.py did not start: its declared query orders failed: No data source is available to this run.\n" {
		t.Fatalf("%+v %v, %d runs, %d calls", res, err, fake.execs, len(b.calls))
	}
}

func TestDeclaredQueryChecks(t *testing.T) {
	b := &countingBroker{}
	c, fake, runRoot := dataClient(t, 0, b.broker, DefaultDataLimits)
	skill := declaredSkill(t, runRoot, ordersSidecar)
	run := func(command string) string {
		t.Helper()
		res, err := c.Exec(context.Background(), command, skill, nil, 60, 50_000)
		if err != nil || res.ExitCode != 1 {
			t.Fatalf("%q: %+v %v", command, res, err)
		}
		return res.Output
	}
	for command, want := range map[string]string{
		"python3 $SKILL_DIR/report.py":                                 "did not start: the query orders needs --since",
		"python3 $SKILL_DIR/report.py --since x --limit ten":           "did not start: --limit is not an integer",
		"python3 $SKILL_DIR/report.py --since x; $SKILL_DIR/report.py": "more than one script with declared queries",
	} {
		if out := run(command); !strings.Contains(out, want) {
			t.Errorf("%q: %q", command, out)
		}
	}
	for sidecar, want := range map[string]string{
		`{"queries": {}}`: "must list 1 to 20 queries",
		`{"queries": {"Orders": {"query": "{ a }"}}}`:                                                  "lower case",
		`{"queries": {"a": {"query": " "}}}`:                                                           "no query text",
		`{"queries": {"a": {"query": "{ a }", "params": {"x": {"type": "date"}}}}}`:                    "use string, integer, number or boolean",
		`{"queries": {"a": {"query": "{ a }", "params": {"x": {"type": "integer", "default": 1.5}}}}}`: "is not a integer",
		`{"queries": {"a": {"query": "{ a }"}}, "rows": 5}`:                                            "unknown field",
		`{"queries": {"a": {"query": "{ a }", "params": {"x-y": {}}}}}`:                                "GraphQL cannot use",
	} {
		write(t, filepath.Join(skill, "report.py"+DeclaredSuffix), sidecar)
		if out := run("python3 $SKILL_DIR/report.py"); !strings.HasPrefix(out, "skills/stock/report.py.queries.json: ") || !strings.Contains(out, want) {
			t.Errorf("%s: %q", sidecar, out)
		}
	}
	if len(b.calls) != 0 || fake.execs != 0 {
		t.Fatalf("%d calls, %d runs", len(b.calls), fake.execs)
	}

	bare, err := NewClient(Config{URL: "http://127.0.0.1:1"}, filepath.Dir(filepath.Dir(runRoot)))
	if err != nil {
		t.Fatal(err)
	}
	res, err := bare.Exec(context.Background(), "python3 $SKILL_DIR/report.py", skill, nil, 60, 50_000)
	if err != nil || res.ExitCode != 1 || !strings.Contains(res.Output, "this run has no script data") {
		t.Fatalf("a run with no script data: %+v %v", res, err)
	}
}

func TestDeclaredQueryLimits(t *testing.T) {
	b := &countingBroker{}
	c, fake, runRoot := dataClient(t, 0, b.broker, DataLimits{MaxRows: 10, MaxBytes: 1_000, MaxQueries: 1, MaxTime: time.Minute})
	skill := declaredSkill(t, runRoot, ordersSidecar)
	res, err := c.Exec(context.Background(), "python3 $SKILL_DIR/report.py --since x", skill, nil, 60, 50_000)
	if err != nil || res.ExitCode != 1 || fake.execs != 0 || len(b.calls) != 1 ||
		!strings.Contains(res.Output, "did not start: the run reached its limit of 1 data queries") {
		t.Fatalf("query count: %+v %v", res, err)
	}

	c, _, runRoot = dataClient(t, 0, b.broker, DataLimits{MaxRows: 1, MaxBytes: 1_000, MaxQueries: 5, MaxTime: time.Minute})
	skill = declaredSkill(t, runRoot, ordersSidecar)
	res, _ = c.Exec(context.Background(), "python3 $SKILL_DIR/report.py --since x", skill, nil, 60, 50_000)
	if res.ExitCode != 1 || !strings.Contains(res.Output, "its declared query orders failed: the query returned 2 rows, more than the 1 rows") {
		t.Fatalf("rows: %+v", res)
	}
}

func TestScriptWithNoSidecar(t *testing.T) {
	b := &countingBroker{}
	c, fake, runRoot := dataClient(t, 0, b.broker, DefaultDataLimits)
	skill := declaredSkill(t, runRoot, "")
	res, err := c.Exec(context.Background(), "python3 $SKILL_DIR/report.py --since x", skill, nil, 60, 50_000)
	if err != nil || res.ExitCode != 0 || fake.execs != 1 || len(b.calls) != 0 || len(queryFiles(fake)) != 0 {
		t.Fatalf("%+v %v", res, err)
	}
	if _, err := os.Stat(filepath.Join(runRoot, ".neko-data", "queries")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("queries folder: %v", err)
	}
}
