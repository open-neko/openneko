package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"strings"
	"testing"

	"github.com/open-neko/neko/apps/openneko/internal/config"
	"github.com/open-neko/neko/apps/openneko/internal/testpg"
)

const failedCheck = `{"check":{"ok":false,"supported":true,"type":"mysql","host":"db.internal","port":3306,"database":"shop","user":"writer",
"findings":[
 {"step":"resolve","status":"ok","message":"db.internal resolves to 10.0.0.5."},
 {"step":"tls","status":"warn","message":"TLS is off."},
 {"step":"read_only","status":"fail","field":"user","message":"writer holds rights beyond reading (INSERT).","sql":["REVOKE ALL PRIVILEGES, GRANT OPTION FROM 'writer'@'%';","GRANT SELECT, SHOW VIEW ON ` + "`shop`" + `.* TO 'writer'@'%';"]}
],"tables":[{"name":"sales_order","rows":3}],"moreTables":0}}`

func runSourceCheck(t *testing.T, stdin string, reply string, args ...string) (sourceCheckOptions, string, string, error) {
	t.Helper()
	var seenOpts sourceCheckOptions
	var seenPassword string
	var decoded struct {
		Check sourceCheckResult `json:"check"`
	}
	if err := json.Unmarshal([]byte(reply), &decoded); err != nil {
		t.Fatal(err)
	}
	prev := checkSource
	checkSource = func(_ context.Context, opts sourceCheckOptions, password string) (sourceCheckResult, error) {
		seenOpts = opts
		seenPassword = password
		return decoded.Check, nil
	}
	t.Cleanup(func() { checkSource = prev })
	command := newSourceCmd()
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetErr(io.Discard)
	command.SilenceUsage = true
	command.SilenceErrors = true
	command.SetIn(strings.NewReader(stdin))
	command.SetArgs(append([]string{"check"}, args...))
	err := command.Execute()
	return seenOpts, seenPassword, output.String(), err
}

func TestSourceCheckPrintsEachStepAndTheSQL(t *testing.T) {
	opts, password, output, err := runSourceCheck(t, "", failedCheck,
		"--type", "mysql", "--host", "db.internal", "--port", "3306", "--database", "shop", "--user", "writer", "--secret", "shop-password")
	if !errors.Is(err, errSourceNotReady) {
		t.Fatalf("err = %v, want errSourceNotReady", err)
	}
	if opts.secret != "shop-password" || opts.port != 3306 || opts.database != "shop" || password != "" {
		t.Fatalf("opts = %+v password = %q", opts, password)
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
	opts, password, output, err := runSourceCheck(t, "pw from stdin\n", passed,
		"--type", "mysql", "--host", "db.internal", "--database", "shop", "--user", "writer", "--password-stdin")
	if err != nil {
		t.Fatalf("err = %v\n%s", err, output)
	}
	if password != "pw from stdin" {
		t.Fatalf("password = %q", password)
	}
	if opts.port != 0 {
		t.Fatalf("port set without --port: %+v", opts)
	}
	if !strings.Contains(output, "tables the login can read: 1\n  sales_order (about 3 rows)") {
		t.Fatalf("output lacks the table list:\n%s", output)
	}
}

func TestSourceCheckNeedsAPassword(t *testing.T) {
	_, _, _, err := runSourceCheck(t, "", failedCheck, "--host", "h", "--database", "d", "--user", "u")
	if err == nil || !strings.Contains(err.Error(), "--secret") {
		t.Fatalf("err = %v", err)
	}
	_, _, _, err = runSourceCheck(t, "", failedCheck, "--host", "h", "--database", "d", "--user", "u", "--password-stdin")
	if err == nil || !strings.Contains(err.Error(), "no password") {
		t.Fatalf("err = %v", err)
	}
}

func TestSourceCheckJSON(t *testing.T) {
	_, _, output, err := runSourceCheck(t, "", failedCheck, "--host", "h", "--database", "d", "--user", "u", "--secret", "s", "-o", "json")
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

func TestSourceCheckReadsStoredSecretFromDatabase(t *testing.T) {
	pool := testpg.Pool(t, "0001_initial_schema.sql", "0029_data_source_secret.sql")
	ctx := context.Background()
	var schema string
	if err := pool.QueryRow(ctx, `select current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	u, err := url.Parse(testpg.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	if err := config.WriteLocalDatabaseURL("", u.String()); err != nil {
		t.Fatal(err)
	}
	pw, _ := u.User.Password()
	enc, err := config.EncryptValue("", pw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `insert into organization (id, slug, name) values ('org', 'org', 'Org')`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `insert into data_source_secret (org_id, name, value_enc) values ('org', 'db-pw', $1)`, enc); err != nil {
		t.Fatal(err)
	}
	host, portStr, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatal(err)
	}
	command := newSourceCmd()
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetErr(io.Discard)
	command.SilenceUsage = true
	command.SilenceErrors = true
	command.SetArgs([]string{
		"check", "--type", "postgres", "--host", host, "--port", portStr,
		"--database", strings.TrimPrefix(u.Path, "/"), "--user", u.User.Username(), "--secret", "db-pw", "-o", "json",
	})
	_ = command.Execute()
	var decoded struct {
		Check sourceCheckResult `json:"check"`
	}
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatalf("unmarshal %s: %v", output.String(), err)
	}
	loginPassed := false
	for _, f := range decoded.Check.Findings {
		if f.Step == "login" && f.Status == "ok" {
			loginPassed = true
		}
	}
	if !loginPassed {
		t.Fatalf("expected login pass with stored secret, got %+v", decoded.Check.Findings)
	}
}
