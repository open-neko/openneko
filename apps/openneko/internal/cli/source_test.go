package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const failedCheck = `{"check":{"ok":false,"supported":true,"type":"mysql","host":"db.internal","port":3306,"database":"shop","user":"writer",
"findings":[
 {"step":"resolve","status":"ok","message":"db.internal resolves to 10.0.0.5."},
 {"step":"tls","status":"warn","message":"TLS is off."},
 {"step":"read_only","status":"fail","field":"user","message":"writer holds rights beyond reading (INSERT).","sql":["REVOKE ALL PRIVILEGES, GRANT OPTION FROM 'writer'@'%';","GRANT SELECT, SHOW VIEW ON ` + "`shop`" + `.* TO 'writer'@'%';"]}
],"tables":[{"name":"sales_order","rows":3}],"moreTables":0}}`

func runSourceCheck(t *testing.T, stdin string, reply string, args ...string) (map[string]any, string, error) {
	t.Helper()
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/admin/sources/check" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(server.Close)
	t.Setenv("WORKER_ADMIN_URL", server.URL)
	command := newSourceCmd()
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetErr(io.Discard)
	command.SilenceUsage = true
	command.SilenceErrors = true
	command.SetIn(strings.NewReader(stdin))
	command.SetArgs(append([]string{"check"}, args...))
	err := command.Execute()
	return body, output.String(), err
}

func TestSourceCheckPrintsEachStepAndTheSQL(t *testing.T) {
	body, output, err := runSourceCheck(t, "", failedCheck,
		"--type", "mysql", "--host", "db.internal", "--port", "3306", "--database", "shop", "--user", "writer", "--secret", "shop-password")
	if !errors.Is(err, errSourceNotReady) {
		t.Fatalf("err = %v, want errSourceNotReady", err)
	}
	if body["secretRef"] != "shop-password" || body["port"] != float64(3306) || body["dbname"] != "shop" {
		t.Fatalf("request body = %v", body)
	}
	if _, ok := body["password"]; ok {
		t.Fatalf("request body holds a password: %v", body)
	}
	for _, want := range []string{
		"source: mysql writer@db.internal:3306/shop",
		"  ok    resolve   db.internal resolves to 10.0.0.5.",
		"  warn  tls       TLS is off.",
		"  FAIL  read_only writer holds rights beyond reading (INSERT).",
		"Ask the database administrator to run this SQL:",
		"          REVOKE ALL PRIVILEGES, GRANT OPTION FROM 'writer'@'%';",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("output lacks %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, "tables the login can read") {
		t.Errorf("a failed check listed tables:\n%s", output)
	}
}

func TestSourceCheckReadsThePasswordFromStandardInput(t *testing.T) {
	passed := strings.Replace(strings.Replace(failedCheck, `"ok":false`, `"ok":true`, 1), `"status":"fail"`, `"status":"ok"`, 1)
	body, output, err := runSourceCheck(t, "pw from stdin\n", passed,
		"--type", "mysql", "--host", "db.internal", "--database", "shop", "--user", "writer", "--password-stdin")
	if err != nil {
		t.Fatalf("err = %v\n%s", err, output)
	}
	if body["password"] != "pw from stdin" {
		t.Fatalf("password = %v", body["password"])
	}
	if _, ok := body["port"]; ok {
		t.Fatalf("port sent without --port: %v", body)
	}
	if !strings.Contains(output, "tables the login can read: 1\n  sales_order (about 3 rows)") {
		t.Fatalf("output lacks the table list:\n%s", output)
	}
}

func TestSourceCheckNeedsAPassword(t *testing.T) {
	_, _, err := runSourceCheck(t, "", failedCheck, "--host", "h", "--database", "d", "--user", "u")
	if err == nil || !strings.Contains(err.Error(), "--secret") {
		t.Fatalf("err = %v", err)
	}
	_, _, err = runSourceCheck(t, "", failedCheck, "--host", "h", "--database", "d", "--user", "u", "--password-stdin")
	if err == nil || !strings.Contains(err.Error(), "no password") {
		t.Fatalf("err = %v", err)
	}
}

func TestSourceCheckJSON(t *testing.T) {
	_, output, err := runSourceCheck(t, "", failedCheck, "--host", "h", "--database", "d", "--user", "u", "--secret", "s", "-o", "json")
	if !errors.Is(err, errSourceNotReady) {
		t.Fatalf("err = %v", err)
	}
	var decoded struct {
		Check sourceCheckResult `json:"check"`
	}
	if jsonErr := json.Unmarshal([]byte(output), &decoded); jsonErr != nil || len(decoded.Check.Findings) != 3 {
		t.Fatalf("output is not the check JSON (%v):\n%s", jsonErr, output)
	}
}
