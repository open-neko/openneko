// Package batch runs an admitted file-backed script and resolves its read-only
// GraphJin cache misses without involving the model in each query.
package batch

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"time"
)

type Config struct {
	Script, ScriptSHA256, WorkDir, ArtifactDir, TargetDay string
	Columns                                               []string
	MaxQueries                                            int
}

type Result struct {
	Artifact string `json:"artifact"`
	SHA256   string `json:"sha256"`
	Rows     int    `json:"rows"`
	Queries  int    `json:"queries"`
}

// Query runs at the trusted boundary. It must enforce actor/source policy and
// return a plain GraphQL response; the script receives only a scoped cache file.
type Query func(context.Context, string) ([]byte, error)

// Run accepts only a pinned script, a fixed UTC date and host-owned paths.
// Queries are reads: an interrupted query may be repeated, while a persisted
// response is reused by the script on the next attempt.
func Run(ctx context.Context, cfg Config, query Query) (Result, error) {
	var result Result
	day, err := time.Parse("2006-01-02", cfg.TargetDay)
	if err != nil || day.Format("2006-01-02") != cfg.TargetDay || !filepath.IsAbs(cfg.Script) || !filepath.IsAbs(cfg.WorkDir) || !filepath.IsAbs(cfg.ArtifactDir) || cfg.MaxQueries < 1 || cfg.MaxQueries > 256 || len(cfg.Columns) == 0 || query == nil {
		return result, fmt.Errorf("invalid batch admission")
	}
	stat, err := os.Lstat(cfg.Script)
	if err != nil || !stat.Mode().IsRegular() || stat.Size() > 2<<20 {
		return result, fmt.Errorf("batch script unavailable")
	}
	script, err := os.ReadFile(cfg.Script)
	if err != nil || len(script) > 2<<20 {
		return result, fmt.Errorf("batch script unreadable")
	}
	sum := sha256.Sum256(script)
	if !strings.EqualFold(hex.EncodeToString(sum[:]), cfg.ScriptSHA256) {
		return result, fmt.Errorf("batch script changed")
	}
	for _, dir := range []string{cfg.WorkDir, cfg.ArtifactDir} {
		if info, err := os.Lstat(dir); err != nil || !info.IsDir() {
			return result, fmt.Errorf("batch directory unavailable")
		}
	}
	lock, err := os.OpenFile(filepath.Join(cfg.WorkDir, ".batch.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return result, err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return result, fmt.Errorf("batch already running")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	cache := filepath.Join(cfg.WorkDir, "graphjin-cache")
	for _, dir := range []string{cache, filepath.Join(cache, "requests"), filepath.Join(cache, "responses"), filepath.Join(cache, "receipts")} {
		if err := os.Mkdir(dir, 0700); err != nil && !os.IsExist(err) {
			return result, fmt.Errorf("batch cache unavailable: %w", err)
		}
		if info, err := os.Lstat(dir); err != nil || !info.IsDir() {
			return result, fmt.Errorf("batch cache directory invalid")
		}
	}
	output := filepath.Join(cfg.WorkDir, "union_final.csv")
	summary := filepath.Join(cfg.WorkDir, "summary.json")
	if _, count, err := pending(cache); err != nil {
		return result, err
	} else {
		result.Queries = count
	}
	for attempt := 0; attempt <= cfg.MaxQueries; attempt++ {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		// The admitted script is the only child. No broker/model credential is
		// passed to it; future general shell tools need a separate boundary.
		cmd := exec.CommandContext(ctx, "python3", cfg.Script,
			"--target-day", cfg.TargetDay, "--work-dir", cfg.WorkDir,
			"--output", output, "--summary", summary, "--max-runtime", "1200")
		cmd.Dir = filepath.Dir(cfg.Script)
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "LANG=C.UTF-8", "PYTHONDONTWRITEBYTECODE=1", "OPENNEKO_QUERY_CACHE_DIR=" + cache}
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		// The pipeline starts source children in new sessions. SIGINT lets its
		// finally block terminate those children; OpenShell teardown remains
		// responsible for noncooperative descendants.
		cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
		cmd.WaitDelay = 5 * time.Second
		log, err := os.OpenFile(filepath.Join(cfg.WorkDir, "pipeline_stdout.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
		if err != nil {
			return result, err
		}
		writer := &boundedWriter{out: log, remaining: 1 << 20}
		cmd.Stdout, cmd.Stderr = writer, writer
		runErr := cmd.Run()
		closeErr := log.Close()
		if closeErr != nil {
			return result, closeErr
		}
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if runErr == nil {
			if requests, _, err := pending(cache); err != nil || len(requests) != 0 {
				return result, fmt.Errorf("batch completed with invalid or unresolved query cache")
			}
			return publish(cfg, output, summary, result)
		}
		requests, _, err := pending(cache)
		if err != nil || len(requests) == 0 || result.Queries+len(requests) > cfg.MaxQueries {
			return result, fmt.Errorf("batch script failed without admissible query requests")
		}
		for _, request := range requests {
			if err := ctx.Err(); err != nil {
				return result, err
			}
			data, err := query(ctx, request.Query)
			if err != nil || len(data) == 0 || len(data) > 16<<20 || !json.Valid(data) {
				return result, fmt.Errorf("batch query failed or exceeded file limit: %w", err)
			}
			var response struct {
				Data   json.RawMessage `json:"data"`
				Errors []any           `json:"errors"`
			}
			if json.Unmarshal(data, &response) != nil || len(response.Data) == 0 || string(response.Data) == "null" || len(response.Errors) != 0 {
				return result, fmt.Errorf("batch query returned an error or no data")
			}
			receiptHash := sha256.Sum256(data)
			receipt, _ := json.Marshal(queryReceipt{request.ID, hex.EncodeToString(receiptHash[:]), len(data)})
			if err := atomicFileOrSame(filepath.Join(cache, "receipts", request.ID+".json"), receipt); err != nil {
				return result, err
			}
			responsePath := filepath.Join(cache, "responses", request.ID+".json")
			if err := atomicFile(responsePath, data); err != nil {
				return result, err
			}
			result.Queries++
		}
	}
	return result, fmt.Errorf("batch query limit reached")
}

type cacheRequest struct {
	SchemaVersion int    `json:"schema_version"`
	ID            string `json:"id"`
	Tool          string `json:"tool"`
	Arguments     struct {
		Query string `json:"query"`
	} `json:"arguments"`
	ResponsePath string `json:"response_path"`
	Query        string `json:"-"`
}

type queryReceipt struct {
	QuerySHA256    string `json:"query_sha256"`
	ResponseSHA256 string `json:"response_sha256"`
	Bytes          int    `json:"bytes"`
}

func pending(cache string) ([]cacheRequest, int, error) {
	entries, err := os.ReadDir(filepath.Join(cache, "requests"))
	if err != nil {
		return nil, 0, err
	}
	var requests []cacheRequest
	seen := make(map[string]bool, len(entries))
	completed := 0
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(cache, "requests", entry.Name())
		if info, err := os.Lstat(path); err != nil || !info.Mode().IsRegular() || info.Size() > 65536 {
			return nil, 0, fmt.Errorf("invalid batch query request")
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, 0, err
		}
		var request cacheRequest
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF || request.SchemaVersion != 1 || request.Tool != "mcp_neko_graphjin_execute_graphql" || len(request.Arguments.Query) == 0 || len(request.Arguments.Query) > 60000 {
			return nil, 0, fmt.Errorf("invalid batch query request")
		}
		hash := sha256.Sum256([]byte(request.Arguments.Query))
		id := hex.EncodeToString(hash[:])
		response := filepath.Join(cache, "responses", id+".json")
		if request.ID != id || entry.Name() != id+".json" || request.ResponsePath != response {
			return nil, 0, fmt.Errorf("batch query request identity mismatch")
		}
		seen[id] = true
		if info, err := os.Lstat(response); err == nil {
			if !info.Mode().IsRegular() || info.Size() > 16<<20 {
				return nil, 0, fmt.Errorf("batch response file invalid")
			}
			data, err := os.ReadFile(response)
			if err != nil {
				return nil, 0, err
			}
			receiptPath := filepath.Join(cache, "receipts", id+".json")
			receiptInfo, err := os.Lstat(receiptPath)
			if err != nil || !receiptInfo.Mode().IsRegular() || receiptInfo.Size() > 1024 {
				return nil, 0, fmt.Errorf("batch query receipt missing or invalid")
			}
			receiptData, err := os.ReadFile(receiptPath)
			var receipt queryReceipt
			responseHash := sha256.Sum256(data)
			if err != nil || json.Unmarshal(receiptData, &receipt) != nil || receipt.QuerySHA256 != id || receipt.ResponseSHA256 != hex.EncodeToString(responseHash[:]) || receipt.Bytes != len(data) {
				return nil, 0, fmt.Errorf("batch query receipt mismatch")
			}
			completed++
			continue
		} else if !os.IsNotExist(err) {
			return nil, 0, err
		}
		request.Query = request.Arguments.Query
		requests = append(requests, request)
	}
	for _, dir := range []string{"responses", "receipts"} {
		files, err := os.ReadDir(filepath.Join(cache, dir))
		if err != nil {
			return nil, 0, err
		}
		for _, file := range files {
			if strings.HasPrefix(file.Name(), ".batch-") {
				continue // A killed atomic write cannot authorize a response.
			}
			id := strings.TrimSuffix(file.Name(), ".json")
			if !strings.HasSuffix(file.Name(), ".json") || !seen[id] {
				return nil, 0, fmt.Errorf("orphan batch query file")
			}
		}
	}
	return requests, completed, nil
}

func publish(cfg Config, output, summary string, result Result) (Result, error) {
	var declared struct {
		Status    string `json:"status"`
		TargetDay string `json:"target_day"`
		Merge     struct {
			FinalRows int `json:"final_rows"`
		} `json:"merge"`
	}
	raw, err := os.ReadFile(summary)
	if err != nil || len(raw) > 1<<20 || json.Unmarshal(raw, &declared) != nil || declared.Status != "completed" || declared.TargetDay != cfg.TargetDay {
		return result, fmt.Errorf("batch summary invalid")
	}
	info, err := os.Lstat(output)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<20 {
		return result, fmt.Errorf("batch CSV missing or too large")
	}
	file, err := os.Open(output)
	if err != nil {
		return result, err
	}
	reader := csv.NewReader(file)
	reader.FieldsPerRecord = len(cfg.Columns)
	header, err := reader.Read()
	if err != nil || !reflect.DeepEqual(header, cfg.Columns) {
		file.Close()
		return result, fmt.Errorf("batch CSV columns invalid")
	}
	rows := 0
	for {
		_, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			file.Close()
			return result, fmt.Errorf("batch CSV malformed: %w", err)
		}
		rows++
	}
	if err := file.Close(); err != nil || rows != declared.Merge.FinalRows {
		return result, fmt.Errorf("batch CSV row count mismatch")
	}
	data, err := os.ReadFile(output)
	if err != nil {
		return result, err
	}
	artifact := filepath.Join(cfg.ArtifactDir, "union_final.csv")
	if err := atomicFileOrSame(artifact, data); err != nil {
		return result, err
	}
	hash := sha256.Sum256(data)
	result.Artifact = artifact
	result.SHA256 = hex.EncodeToString(hash[:])
	result.Rows = rows
	return result, nil
}

func atomicFile(path string, data []byte) error {
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("refusing to replace existing batch file")
	} else if !os.IsNotExist(err) {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".batch-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Link(tmp.Name(), path); err != nil {
		return err
	}
	return nil
}

func atomicFileOrSame(path string, data []byte) error {
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Size() != int64(len(data)) {
			return fmt.Errorf("existing batch file differs")
		}
		prior, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(prior, data) {
			return fmt.Errorf("existing batch file differs")
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	return atomicFile(path, data)
}

type boundedWriter struct {
	out       io.Writer
	remaining int
}

func (w *boundedWriter) Write(p []byte) (int, error) {
	if w.remaining > 0 {
		n := len(p)
		if n > w.remaining {
			n = w.remaining
		}
		if _, err := w.out.Write(p[:n]); err != nil {
			return 0, err
		}
		w.remaining -= n
	}
	return len(p), nil
}
