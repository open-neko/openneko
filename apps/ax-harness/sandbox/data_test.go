package sandbox

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// scriptSandbox plays a script that reads one data key per query: it
// answers with the rows once the answer file is there, else it leaves a
// request and exits 75.
type scriptSandbox struct {
	mu     sync.Mutex
	keys   []string
	files  map[string][]byte
	bodies []string
	execs  int
}

func (s *scriptSandbox) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodDelete {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var body execBody
	raw, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bodies = append(s.bodies, string(raw))
	_ = json.Unmarshal(raw, &body)
	s.execs++
	for _, f := range body.Put {
		s.files[f.Path] = f.Data
	}
	for _, name := range body.Delete {
		delete(s.files, name)
	}
	var out []string
	for _, key := range s.keys {
		answer, ok := s.files["runs/r1/.neko-data/answers/"+key+".json"]
		if !ok {
			req := "runs/r1/.neko-data/requests/" + key + ".json"
			s.files[req] = []byte(`{"query":"query { q` + key[:4] + ` { id } }"}`)
			_ = json.NewEncoder(w).Encode(map[string]any{"exit_code": 75, "output": "waiting\n",
				"changed": []map[string]any{{"path": req, "mode": 0o644, "data": base64.StdEncoding.EncodeToString(s.files[req])}}})
			return
		}
		out = append(out, string(answer))
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"exit_code": 0, "output": strings.Join(out, "\n")})
}

func dataKeyOf(i int) string { return fmt.Sprintf("%064x", i+1) }

func dataClient(t *testing.T, keys int, broker DataBroker, limits DataLimits) (*Client, *scriptSandbox, string) {
	t.Helper()
	fake := &scriptSandbox{files: map[string][]byte{}}
	for i := range keys {
		fake.keys = append(fake.keys, dataKeyOf(i))
	}
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	workspace := t.TempDir()
	runRoot := filepath.Join(workspace, "runs", "r1")
	if err := os.MkdirAll(runRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	c, err := NewClient(Config{URL: server.URL}, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SetData(runRoot, broker, limits); err != nil {
		t.Fatal(err)
	}
	return c, fake, runRoot
}

type countingBroker struct {
	mu    sync.Mutex
	calls []string
	reply func(q DataQuery) (json.RawMessage, error)
}

func (b *countingBroker) broker(_ context.Context, q DataQuery) (json.RawMessage, error) {
	b.mu.Lock()
	b.calls = append(b.calls, q.Query)
	b.mu.Unlock()
	if b.reply != nil {
		return b.reply(q)
	}
	return json.RawMessage(`{"orders":[{"id":1},{"id":2}]}`), nil
}

func TestScriptDataRunsTheQueryAndTheScriptAgain(t *testing.T) {
	b := &countingBroker{}
	c, fake, runRoot := dataClient(t, 2, b.broker, DefaultDataLimits)
	res, err := c.Exec(context.Background(), "python3 report.py", "", nil, 60, 50_000)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"data":{"orders":[{"id":1},{"id":2}]}}`
	if res.ExitCode != 0 || res.Output != want+"\n"+want || fake.execs != 3 || len(b.calls) != 2 {
		t.Fatalf("result %+v, %d runs, calls %v", res, fake.execs, b.calls)
	}
	if _, err := os.Stat(filepath.Join(runRoot, ".neko-data", "requests")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("requests left: %v", err)
	}
	record, err := os.ReadFile(filepath.Join(runRoot, ".openneko", "script-data.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(record)), "\n")
	var first DataRecord
	if len(lines) != 2 || json.Unmarshal([]byte(lines[0]), &first) != nil || first.Sequence != 1 || first.RowCount != 2 ||
		first.Operation != "execute_graphql" || first.Key != dataKeyOf(0) || string(first.Data) != `{"orders":[{"id":1},{"id":2}]}` {
		t.Fatalf("record: %s", record)
	}
	for _, body := range fake.bodies {
		if strings.Contains(body, "script-data.jsonl") {
			t.Fatal("the record went to the sandbox")
		}
	}

	again, err := c.Exec(context.Background(), "python3 report.py", "", nil, 60, 50_000)
	if err != nil || again.Output != res.Output || len(b.calls) != 2 || fake.execs != 4 {
		t.Fatalf("second command: %+v %v, calls %v, runs %d", again, err, b.calls, fake.execs)
	}

	next, err := NewClient(Config{URL: "http://127.0.0.1:1"}, filepath.Dir(filepath.Dir(runRoot)))
	if err != nil {
		t.Fatal(err)
	}
	if err := next.SetData(runRoot, b.broker, DefaultDataLimits); err != nil || len(next.data.records) != 2 || next.data.order != 2 {
		t.Fatalf("a new client reads the record: %v %+v", err, next.data)
	}
}

func TestScriptDataGivesTheScriptARefusal(t *testing.T) {
	b := &countingBroker{reply: func(DataQuery) (json.RawMessage, error) {
		return nil, errors.New("No data source is available to this run.")
	}}
	c, _, runRoot := dataClient(t, 1, b.broker, DefaultDataLimits)
	res, err := c.Exec(context.Background(), "x", "", nil, 60, 50_000)
	if err != nil || res.Output != `{"error":"No data source is available to this run."}` {
		t.Fatalf("%+v %v", res, err)
	}
	raw, _ := os.ReadFile(filepath.Join(runRoot, ".openneko", "script-data.jsonl"))
	if !strings.Contains(string(raw), `"error":"No data source is available to this run."`) || strings.Contains(string(raw), `"data"`) {
		t.Fatalf("record: %s", raw)
	}
}

func TestScriptDataLimits(t *testing.T) {
	rows := func(n int) json.RawMessage {
		items := make([]string, n)
		for i := range items {
			items[i] = `{"id":1}`
		}
		return json.RawMessage(`{"orders":[` + strings.Join(items, ",") + `],"total":{"n":1}}`)
	}
	limits := DataLimits{MaxRows: 3, MaxBytes: 1_000, MaxQueries: 2, MaxTime: time.Minute}

	b := &countingBroker{reply: func(DataQuery) (json.RawMessage, error) { return rows(2), nil }}
	c, _, _ := dataClient(t, 1, b.broker, limits)
	if res, _ := c.Exec(context.Background(), "x", "", nil, 60, 50_000); !strings.HasPrefix(res.Output, `{"data":{"orders"`) {
		t.Fatalf("two rows and one object are three rows: %+v", res)
	}

	b = &countingBroker{reply: func(DataQuery) (json.RawMessage, error) { return rows(3), nil }}
	c, _, _ = dataClient(t, 1, b.broker, limits)
	if res, _ := c.Exec(context.Background(), "x", "", nil, 60, 50_000); !strings.Contains(res.Output, "returned 4 rows, more than the 3 rows") {
		t.Fatalf("rows: %+v", res)
	}

	b = &countingBroker{reply: func(DataQuery) (json.RawMessage, error) {
		return json.RawMessage(`{"a":"` + strings.Repeat("x", 1_000) + `"}`), nil
	}}
	c, _, _ = dataClient(t, 1, b.broker, limits)
	if res, _ := c.Exec(context.Background(), "x", "", nil, 60, 50_000); !strings.Contains(res.Output, "returned 1008 bytes, more than the 1000 bytes") {
		t.Fatalf("bytes: %+v", res)
	}

	b = &countingBroker{}
	c, fake, _ := dataClient(t, 3, b.broker, limits)
	res, err := c.Exec(context.Background(), "x", "", nil, 60, 50_000)
	if err != nil || res.ExitCode != 75 || !strings.HasSuffix(res.Output, "[script data stopped: the run reached its limit of 2 data queries]") ||
		len(b.calls) != 2 || fake.execs != 3 {
		t.Fatalf("query count: %+v %v, calls %d, runs %d", res, err, len(b.calls), fake.execs)
	}

	slow := DataLimits{MaxRows: 10, MaxBytes: 1_000, MaxQueries: 10, MaxTime: 50 * time.Millisecond}
	b = &countingBroker{reply: func(DataQuery) (json.RawMessage, error) {
		time.Sleep(30 * time.Millisecond)
		return json.RawMessage(`{"a":1}`), nil
	}}
	c, _, _ = dataClient(t, 3, b.broker, slow)
	res, err = c.Exec(context.Background(), "x", "", nil, 60, 50_000)
	if err != nil || !strings.HasSuffix(res.Output, "[script data stopped: the run reached its script data time limit of 50ms]") || len(b.calls) != 2 {
		t.Fatalf("time: %+v %v, calls %d", res, err, len(b.calls))
	}

	c, _, _ = dataClient(t, 1, func(ctx context.Context, q DataQuery) (json.RawMessage, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}, DataLimits{MaxRows: 10, MaxBytes: 1_000, MaxQueries: 10, MaxTime: 20 * time.Millisecond})
	if res, _ := c.Exec(context.Background(), "x", "", nil, 60, 50_000); !strings.Contains(res.Output, "the script data time limit ran out during this query") {
		t.Fatalf("a query that outlives the limit: %+v", res)
	}
}

func TestScriptDataSetup(t *testing.T) {
	workspace := t.TempDir()
	c, err := NewClient(Config{URL: "http://127.0.0.1:1"}, workspace)
	if err != nil {
		t.Fatal(err)
	}
	broker := func(context.Context, DataQuery) (json.RawMessage, error) { return nil, nil }
	for _, c2 := range []struct {
		run    string
		broker DataBroker
		limits DataLimits
	}{
		{filepath.Join(workspace, "runs", "r"), nil, DefaultDataLimits},
		{workspace, broker, DefaultDataLimits},
		{t.TempDir(), broker, DefaultDataLimits},
		{filepath.Join(workspace, "runs", "r"), broker, DataLimits{}},
	} {
		if err := c.SetData(c2.run, c2.broker, c2.limits); err == nil {
			t.Errorf("accepted %+v", c2)
		}
	}
	run := filepath.Join(workspace, "runs", "r")
	write(t, filepath.Join(run, ".openneko", "script-data.jsonl"), "{\"key\":\"../x\"}\n")
	if err := c.SetData(run, broker, DefaultDataLimits); err == nil || !strings.Contains(err.Error(), "damaged") {
		t.Fatalf("a damaged record: %v", err)
	}
}

func TestGraphQLData(t *testing.T) {
	for _, c := range []struct{ in, want, err string }{
		{`{"data":{"a":1}}`, `{"a":1}`, ""},
		{`{"data":{"a":1},"errors":[]}`, `{"a":1}`, ""},
		{`"{\"data\":{\"a\":1}}"`, `{"a":1}`, ""},
		{`{"content":[{"type":"text","text":"{\"data\":{\"b\":2}}"}]}`, `{"b":2}`, ""},
		{`{"content":[],"structuredContent":{"data":{"c":3}}}`, `{"c":3}`, ""},
		{`{"result":"{\"data\":{\"d\":4}}"}`, `{"d":4}`, ""},
		{`{"data":null,"errors":[{"message":"no selectors found"}]}`, "", `[{"message":"no selectors found"}]`},
		{`{"data":[1]}`, "", "no data object"},
		{`"not json"`, "", "not JSON"},
		{`[1]`, "", "no JSON object"},
		{`{"other":1}`, "", "no data object"},
	} {
		got, err := GraphQLData(json.RawMessage(c.in))
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%s: %s %v", c.in, got, err)
			}
			continue
		}
		if err != nil || string(got) != c.want {
			t.Errorf("%s: %s %v", c.in, got, err)
		}
	}
}
