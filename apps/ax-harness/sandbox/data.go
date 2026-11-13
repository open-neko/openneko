package sandbox

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/open-neko/openneko/apps/ax-harness/internal/localtool"
)

// DataTool is the bridge tool that runs a skill script's query for the
// host. The bridge does not list it, so the model never sees it.
const DataTool = "script_data_query"

// DataQuery is one GraphJin read that a skill script asked for.
type DataQuery struct {
	Query     string          `json:"query"`
	Variables json.RawMessage `json:"variables,omitempty"`
	Namespace string          `json:"namespace,omitempty"`
}

// DataBroker runs q under the run's identity, tool policy, audit and trace,
// and returns the GraphQL data object.
type DataBroker func(ctx context.Context, q DataQuery) (json.RawMessage, error)

// DataLimits cap the script data of one run.
type DataLimits struct {
	MaxRows    int
	MaxBytes   int
	MaxQueries int
	MaxTime    time.Duration
}

var DefaultDataLimits = DataLimits{MaxRows: 10_000, MaxBytes: 4 << 20, MaxQueries: 50, MaxTime: 15 * time.Minute}

// DataRecord is one script query of a run and its answer, as the eval
// recorder keeps a data step.
type DataRecord struct {
	Sequence   int             `json:"sequence"`
	Key        string          `json:"key"`
	Operation  string          `json:"operation"`
	Request    DataQuery       `json:"request"`
	Data       json.RawMessage `json:"data,omitempty"`
	RowCount   int             `json:"rowCount"`
	Bytes      int             `json:"bytes"`
	Error      string          `json:"error,omitempty"`
	DurationMs int64           `json:"durationMs"`
}

const (
	dataDir       = ".neko-data"
	dataRecord    = "script-data.jsonl"
	dataExitCode  = 75
	dataStopLabel = "\n[script data stopped: "
)

var dataKey = regexp.MustCompile(`^[0-9a-f]{64}$`)

type scriptData struct {
	broker  DataBroker
	limits  DataLimits
	runRoot string
	records map[string]DataRecord
	order   int
	spent   time.Duration
}

// SetData lets the scripts of the run in runRoot read data. A script that
// needs rows stops with its request in runRoot/.neko-data/requests; the
// host runs the query, records it, writes the answer, and runs the command
// again. The sandbox gets the rows as files and never a key or a socket.
func (c *Client) SetData(runRoot string, broker DataBroker, limits DataLimits) error {
	if broker == nil {
		return errors.New("script data needs a broker")
	}
	if _, err := c.relative(runRoot); err != nil || filepath.Clean(runRoot) == c.workspace {
		return errors.New("script data needs a run folder inside the workspace")
	}
	if limits.MaxRows < 1 || limits.MaxBytes < 1 || limits.MaxQueries < 1 || limits.MaxTime <= 0 {
		return errors.New("invalid script data limits")
	}
	d := &scriptData{broker: broker, limits: limits, runRoot: filepath.Clean(runRoot), records: map[string]DataRecord{}}
	if err := d.load(); err != nil {
		return err
	}
	c.mu.Lock()
	c.data = d
	c.mu.Unlock()
	return nil
}

func (d *scriptData) recordPath() string {
	return filepath.Join(d.runRoot, privateDir, dataRecord)
}

func (d *scriptData) load() error {
	f, err := os.Open(d.recordPath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 2*d.limits.MaxBytes+64*1024)
	for scanner.Scan() {
		var r DataRecord
		if json.Unmarshal(scanner.Bytes(), &r) != nil || !dataKey.MatchString(r.Key) {
			return errors.New("the script data record is damaged")
		}
		d.records[r.Key] = r
		d.order = max(d.order, r.Sequence)
	}
	return scanner.Err()
}

func (d *scriptData) save(r DataRecord) error {
	if err := os.MkdirAll(filepath.Join(d.runRoot, privateDir), 0o700); err != nil {
		return err
	}
	line, err := json.Marshal(r)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(d.recordPath(), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func answerBody(r DataRecord) []byte {
	if r.Error != "" {
		raw, _ := json.Marshal(map[string]string{"error": r.Error})
		return raw
	}
	return append(append([]byte(`{"data":`), r.Data...), '}')
}

// prepare writes the recorded answers the run folder lacks or that a
// command changed, and removes the requests of an earlier command.
func (d *scriptData) prepare() error {
	answers := filepath.Join(d.runRoot, dataDir, "answers")
	if err := os.RemoveAll(filepath.Join(d.runRoot, dataDir, "requests")); err != nil {
		return err
	}
	if len(d.records) == 0 {
		return nil
	}
	if err := os.MkdirAll(answers, 0o755); err != nil {
		return err
	}
	for key, r := range d.records {
		p := filepath.Join(answers, key+".json")
		body := answerBody(r)
		if old, err := os.ReadFile(p); err == nil && bytes.Equal(old, body) {
			continue
		}
		if err := os.WriteFile(p, body, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func (d *scriptData) requests() (map[string]DataQuery, error) {
	dir := filepath.Join(d.runRoot, dataDir, "requests")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := map[string]DataQuery{}
	for _, entry := range entries {
		key, ok := strings.CutSuffix(entry.Name(), ".json")
		if !ok || !dataKey.MatchString(key) || !entry.Type().IsRegular() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		var q DataQuery
		if json.Unmarshal(raw, &q) != nil || strings.TrimSpace(q.Query) == "" {
			continue
		}
		out[key] = q
	}
	return out, nil
}

// countRows counts the rows of each top-level field of a data object: a
// list counts its items, any other value counts one.
func countRows(data json.RawMessage) int {
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil {
		return 0
	}
	rows := 0
	for _, v := range fields {
		var list []json.RawMessage
		if json.Unmarshal(v, &list) == nil {
			rows += len(list)
		} else if string(v) != "null" {
			rows++
		}
	}
	return rows
}

func (d *scriptData) run(ctx context.Context, key string, q DataQuery) (DataRecord, error) {
	d.order++
	r := DataRecord{Sequence: d.order, Key: key, Operation: "execute_graphql", Request: q}
	qctx, cancel := context.WithTimeout(ctx, d.limits.MaxTime-d.spent)
	started := time.Now()
	data, err := d.broker(qctx, q)
	expired := errors.Is(qctx.Err(), context.DeadlineExceeded)
	cancel()
	took := time.Since(started)
	d.spent += took
	r.DurationMs = took.Milliseconds()
	switch {
	case ctx.Err() != nil:
		return r, ctx.Err()
	case err != nil && expired:
		r.Error = "the script data time limit ran out during this query"
	case err != nil:
		r.Error = err.Error()
	case !json.Valid(data) || !bytes.HasPrefix(bytes.TrimSpace(data), []byte("{")):
		r.Error = "the query returned no data object"
	default:
		r.Bytes, r.RowCount = len(data), countRows(data)
		switch {
		case r.Bytes > d.limits.MaxBytes:
			r.Error = fmt.Sprintf("the query returned %d bytes, more than the %d bytes a script may read in one query; select fewer columns or add a limit", r.Bytes, d.limits.MaxBytes)
		case r.RowCount > d.limits.MaxRows:
			r.Error = fmt.Sprintf("the query returned %d rows, more than the %d rows a script may read in one query; aggregate in the query or add a limit", r.RowCount, d.limits.MaxRows)
		default:
			r.Data = data
		}
	}
	if r.Error != "" {
		r.Data = nil
	}
	if err := d.save(r); err != nil {
		return r, err
	}
	d.records[key] = r
	return r, nil
}

func (d *scriptData) stopped() string {
	if len(d.records) >= d.limits.MaxQueries {
		return fmt.Sprintf("%sthe run reached its limit of %d data queries]", dataStopLabel, d.limits.MaxQueries)
	}
	if d.spent >= d.limits.MaxTime {
		return fmt.Sprintf("%sthe run reached its script data time limit of %s]", dataStopLabel, d.limits.MaxTime)
	}
	return ""
}

func (c *Client) execWithData(ctx context.Context, body execBody) (localtool.ExecResult, error) {
	d := c.data
	for {
		if err := d.prepare(); err != nil {
			return localtool.ExecResult{}, fmt.Errorf("script data could not be prepared: %w", err)
		}
		result, err := c.execOnce(ctx, body)
		if err != nil {
			return result, err
		}
		requests, err := d.requests()
		if err != nil {
			return localtool.ExecResult{}, fmt.Errorf("script data could not be read: %w", err)
		}
		keys := make([]string, 0, len(requests))
		for key := range requests {
			if _, done := d.records[key]; !done {
				keys = append(keys, key)
			}
		}
		if len(keys) == 0 {
			if len(requests) > 0 && result.ExitCode == dataExitCode {
				result.Output += dataStopLabel + "the script asked again for rows it already has]"
			}
			return result, os.RemoveAll(filepath.Join(d.runRoot, dataDir, "requests"))
		}
		slices.Sort(keys)
		for _, key := range keys {
			if note := d.stopped(); note != "" {
				result.Output += note
				return result, nil
			}
			if _, err := d.run(ctx, key, requests[key]); err != nil {
				return localtool.ExecResult{}, err
			}
		}
	}
}

// GraphQLData reads the data object from an execute_graphql payload: the
// GraphQL response, the same as JSON text, or a tool result that wraps it.
func GraphQLData(payload json.RawMessage) (json.RawMessage, error) {
	for range 8 {
		var text string
		if json.Unmarshal(payload, &text) == nil {
			if !json.Valid([]byte(text)) {
				return nil, errors.New("execute_graphql returned text that is not JSON")
			}
			payload = json.RawMessage(text)
			continue
		}
		var obj map[string]json.RawMessage
		if json.Unmarshal(payload, &obj) != nil || obj == nil {
			return nil, errors.New("execute_graphql returned no JSON object")
		}
		data, hasData := obj["data"]
		errs, hasErrors := obj["errors"]
		switch {
		case hasData || hasErrors:
			if hasErrors && !slices.Contains([]string{"null", "[]", "false", `""`}, string(bytes.TrimSpace(errs))) {
				msg := string(errs)
				if len(msg) > 4000 {
					msg = msg[:4000]
				}
				return nil, errors.New(msg)
			}
			if !bytes.HasPrefix(bytes.TrimSpace(data), []byte("{")) {
				return nil, errors.New("execute_graphql returned no data object")
			}
			return data, nil
		case obj["structuredContent"] != nil || obj["content"] != nil:
			if s := obj["structuredContent"]; bytes.HasPrefix(bytes.TrimSpace(s), []byte("{")) {
				payload = s
			} else {
				payload = firstText(obj["content"])
			}
		case obj["result"] != nil:
			payload = obj["result"]
		default:
			return nil, errors.New("execute_graphql returned no data object")
		}
	}
	return nil, errors.New("execute_graphql returned no data object")
}

func firstText(content json.RawMessage) json.RawMessage {
	var items []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	_ = json.Unmarshal(content, &items)
	for _, item := range items {
		if item.Type == "text" {
			raw, _ := json.Marshal(item.Text)
			return raw
		}
	}
	return json.RawMessage(`""`)
}
