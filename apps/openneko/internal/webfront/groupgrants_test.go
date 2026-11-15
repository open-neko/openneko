package webfront

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestGroupGrantsNodeParity(t *testing.T) {
	raw, err := os.ReadFile("testdata/groupgrants.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		APIOperations []struct {
			Name       string   `json:"name"`
			Raw        string   `json:"raw"`
			Operations []string `json:"operations"`
		} `json:"apiOperations"`
		ReadModes []struct {
			Name  string            `json:"name"`
			Raw   string            `json:"raw"`
			Modes map[string]string `json:"modes"`
		} `json:"readModes"`
		Applies []struct {
			Name                string                `json:"name"`
			Raw                 string                `json:"raw"`
			Groups              []groupGrantGroup     `json:"groups"`
			Rules               []dataAccessRuleInput `json:"rules"`
			APIOperationHolders map[string][]string   `json:"apiOperationHolders"`
			PreviousReadModes   map[string]string     `json:"previousReadModes"`
			Changed             bool                  `json:"changed"`
			Content             string                `json:"content"`
		} `json:"applies"`
		Removes []struct {
			Name              string            `json:"name"`
			Raw               string            `json:"raw"`
			PreviousReadModes map[string]string `json:"previousReadModes"`
			Changed           bool              `json:"changed"`
			Content           string            `json:"content"`
		} `json:"removes"`
		Errors []struct {
			Name  string `json:"name"`
			Raw   string `json:"raw"`
			Error string `json:"error"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}

	for _, c := range fixture.APIOperations {
		got := listConfigAPIOperations(c.Raw)
		if !slices.Equal(got, c.Operations) {
			t.Errorf("apiOperations %s: got %v, want %v", c.Name, got, c.Operations)
		}
	}

	for _, c := range fixture.ReadModes {
		got := readDatabaseSourceReadModes(c.Raw)
		if len(got) != len(c.Modes) {
			t.Errorf("readModes %s: got %v, want %v", c.Name, got, c.Modes)
			continue
		}
		for k, v := range c.Modes {
			if got[k] != v {
				t.Errorf("readModes %s[%s]: got %q, want %q", c.Name, k, got[k], v)
			}
		}
	}

	for _, c := range fixture.Applies {
		model, err := buildGroupGrantsModel(c.Groups, c.Rules, c.APIOperationHolders)
		if err != nil {
			t.Fatalf("build model %s: %v", c.Name, err)
		}
		got, err := applyGroupGrantsToConfig(c.Raw, model, c.PreviousReadModes)
		if err != nil {
			t.Fatalf("apply %s: %v", c.Name, err)
		}
		if got.Changed != c.Changed {
			t.Errorf("apply %s changed: got %v, want %v", c.Name, got.Changed, c.Changed)
		}
		sameYAML(t, got.Content, c.Content)
		if got.Changed {
			again, err := applyGroupGrantsToConfig(got.Content, model, c.PreviousReadModes)
			if err != nil {
				t.Fatalf("reapply %s: %v", c.Name, err)
			}
			if again.Changed || again.Content != got.Content {
				t.Errorf("reapply %s not idempotent: changed=%v", c.Name, again.Changed)
			}
		}
	}

	for _, c := range fixture.Removes {
		got, err := removeGroupGrantsFromConfig(c.Raw, c.PreviousReadModes)
		if err != nil {
			t.Fatalf("remove %s: %v", c.Name, err)
		}
		if got.Changed != c.Changed {
			t.Errorf("remove %s changed: got %v, want %v", c.Name, got.Changed, c.Changed)
		}
		sameYAML(t, got.Content, c.Content)
	}

	emptyModel := groupGrantsModel{
		Roles:         []groupGrantsRole{},
		Grants:        map[string]map[string][]groupTableGrant{},
		APIOperations: map[string][]string{},
	}
	for _, c := range fixture.Errors {
		_, err := applyGroupGrantsToConfig(c.Raw, emptyModel, map[string]string{})
		if err == nil {
			t.Errorf("error case %s: expected error %q, got nil", c.Name, c.Error)
			continue
		}
		wantPrefix := strings.SplitN(c.Error, ":", 2)[0]
		if !strings.Contains(err.Error(), wantPrefix) {
			t.Errorf("error case %s: got %q, want prefix %q", c.Name, err.Error(), wantPrefix)
		}
	}
}

func TestGroupGrantsService(t *testing.T) {
	f, db, _ := groupsFront(t)
	ctx := t.Context()
	admin := sessionCookie("admin")

	const configYAML = `identity:
  role_mode: first
  role_claims: [role]
sources:
  - name: shop
    kind: database
    access:
      read: authenticated
  - name: erp
    kind: database
    access:
      read: account
      namespace_column: tenant_id
  - name: payments
    kind: api
    specs:
      stripe:
        operations:
          refund: {}
`
	configFile := filepath.Join(t.TempDir(), "dev.yml")
	if err := os.WriteFile(configFile, []byte(configYAML), 0o640); err != nil {
		t.Fatal(err)
	}
	var restarts atomic.Int32
	deps := &groupGrantsDeps{
		ConfigFile: configFile,
		Restart: func(ctx context.Context, path string) error {
			restarts.Add(1)
			return nil
		},
		Catalog: func(ctx context.Context, sources []string) (map[string][]catalogTableColumns, error) {
			return map[string][]catalogTableColumns{
				"shop": {{Schema: "public", Table: "orders", Columns: []string{"id", "amount"}}},
				"erp":  {{Schema: "", Table: "invoices", Columns: []string{"id", "total"}}},
			}, nil
		},
	}

	noop, err := f.API.applyGroupGrants(ctx, "org", deps)
	if err != nil || noop.Enabled || noop.Changed || len(noop.Roles) != 0 || restarts.Load() != 0 {
		t.Fatalf("disabled apply: %+v err=%v restarts=%d", noop, err, restarts.Load())
	}

	groupID := decode(t, call(f, "POST", "/api/v1/admin/groups", `{"name":"Finance"}`, admin))["group"].(map[string]any)["id"].(string)
	var everyoneID string
	if err := db.QueryRow(ctx, `select id::text from user_group where org_id = 'org' and slug = 'everyone'`).Scan(&everyoneID); err != nil {
		t.Fatal(err)
	}

	if _, err := f.API.revokeItem(ctx, actor{OrgID: "org", UserID: "admin"}, everyoneID, "api_operation", "*"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.API.grantItem(ctx, actor{OrgID: "org", UserID: "admin"}, groupID, "data_source", "shop"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.API.grantItem(ctx, actor{OrgID: "org", UserID: "admin"}, groupID, "data_source", "erp"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.API.grantItem(ctx, actor{OrgID: "org", UserID: "admin"}, groupID, "api_operation", "payments:stripe:refund"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.API.upsertDataAccessRule(ctx, actor{OrgID: "org", UserID: "admin"}, groupID, "shop", "public", "orders", []string{"id", "amount"}, []byte(`{"column":"amount","op":"gt","value":10}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.API.upsertDataAccessRule(ctx, actor{OrgID: "org", UserID: "admin"}, groupID, "erp", "", "invoices", []string{"id", "total"}, []byte(`{"column":"total","op":"gt","value":100}`)); err != nil {
		t.Fatal(err)
	}

	enabled, err := f.API.enableGroupGrants(ctx, "org", nil, deps)
	if err != nil {
		t.Fatalf("enable: %v", err)
	}
	if !enabled.Enabled || !enabled.Changed || enabled.SeededRules != 2 || !slices.Equal(enabled.Roles, []string{"og_everyone", "og_finance"}) || restarts.Load() != 1 {
		t.Fatalf("enable result: %+v restarts=%d", enabled, restarts.Load())
	}

	rawAfterEnable, err := os.ReadFile(configFile)
	if err != nil {
		t.Fatal(err)
	}
	parsedAfterEnable, err := yamlToJS(rawAfterEnable)
	if err != nil {
		t.Fatal(err)
	}
	textAfterEnable := jsStringify(parsedAfterEnable, false)
	for _, want := range []string{
		`"role_mode":"union"`,
		`"name":"erp","kind":"database","access":{"read":"admin","namespace_column":"tenant_id"`,
		`"filter":"{ tenant_id: { eq: $account_id } }"`,
		`"filter":"{ and: [{ tenant_id: { eq: $account_id } }, { total: { gt: 100 } }] }"`,
		`"allowed_roles":["og_finance"]`,
	} {
		if !strings.Contains(textAfterEnable, want) {
			t.Errorf("enabled config lacks %s: %s", want, textAfterEnable)
		}
	}

	second, err := f.API.applyGroupGrants(ctx, "org", deps)
	if err != nil || !second.Enabled || second.Changed || restarts.Load() != 1 {
		t.Errorf("second apply: %+v err=%v restarts=%d", second, err, restarts.Load())
	}

	disabled, err := f.API.disableGroupGrants(ctx, "org", nil, deps)
	if err != nil || !disabled.Changed || restarts.Load() != 2 {
		t.Fatalf("disable: %+v err=%v restarts=%d", disabled, err, restarts.Load())
	}
	rawAfterDisable, err := os.ReadFile(configFile)
	if err != nil {
		t.Fatal(err)
	}
	parsedAfterDisable, err := yamlToJS(rawAfterDisable)
	if err != nil {
		t.Fatal(err)
	}
	textAfterDisable := jsStringify(parsedAfterDisable, false)
	for _, want := range []string{
		`"role_mode":"first"`,
		`"name":"shop","kind":"database","access":{"read":"authenticated"}`,
		`"name":"erp","kind":"database","access":{"read":"account","namespace_column":"tenant_id"}`,
	} {
		if !strings.Contains(textAfterDisable, want) {
			t.Errorf("disabled config lacks %s: %s", want, textAfterDisable)
		}
	}

	var batchRuns atomic.Int32
	done := make(chan string, 2)
	f.API.GroupGrantsDelay = 15 * time.Millisecond
	f.API.GroupGrantsRun = func(org string) error {
		batchRuns.Add(1)
		done <- org
		return nil
	}
	f.API.scheduleGroupGrantsApply(ctx, "org")
	f.API.scheduleGroupGrantsApply(ctx, "org")
	f.API.scheduleGroupGrantsApply(ctx, "org")
	select {
	case gotOrg := <-done:
		if gotOrg != "org" || batchRuns.Load() != 1 {
			t.Errorf("debounced apply: org=%q runs=%d", gotOrg, batchRuns.Load())
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for scheduled group grants apply")
	}
}

func TestGroupGrantsFailureKeepsOldConfig(t *testing.T) {
	f, _, _ := groupsFront(t)
	ctx := t.Context()
	const configYAML = "sources:\n  - name: shop\n    kind: database\n    access:\n      read: authenticated\n"
	configFile := filepath.Join(t.TempDir(), "dev.yml")
	if err := os.WriteFile(configFile, []byte(configYAML), 0o640); err != nil {
		t.Fatal(err)
	}
	var restarts []string
	failFirst := true
	deps := &groupGrantsDeps{
		ConfigFile: configFile,
		Restart: func(ctx context.Context, path string) error {
			raw, _ := os.ReadFile(path)
			restarts = append(restarts, string(raw))
			if failFirst && len(restarts) == 1 {
				return errors.New("graphjin_restart_required: GraphJin did not answer")
			}
			return nil
		},
		Catalog: func(ctx context.Context, sources []string) (map[string][]catalogTableColumns, error) {
			return map[string][]catalogTableColumns{"shop": {{Schema: "public", Table: "orders", Columns: []string{"id"}}}}, nil
		},
	}
	readConfig := func() string {
		raw, err := os.ReadFile(configFile)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}

	if _, err := f.API.enableGroupGrants(ctx, "org", nil, deps); err == nil || !strings.Contains(err.Error(), "graphjin_restart_required") {
		t.Fatalf("enable with a failed restart: err=%v", err)
	}
	if got := readConfig(); got != configYAML {
		t.Errorf("config after the failed enable:\n%s", got)
	}
	if len(restarts) != 2 || restarts[1] != configYAML {
		t.Errorf("restarts after the failed enable: %q", restarts)
	}
	if on, err := f.API.groupGrantsEnabled(ctx, "org"); err != nil || on {
		t.Errorf("group grants on after a failed enable: %v %v", on, err)
	}

	failFirst = false
	restarts = nil
	if _, err := f.API.enableGroupGrants(ctx, "org", nil, deps); err != nil {
		t.Fatal(err)
	}
	enabledConfig := readConfig()
	if !strings.Contains(enabledConfig, "og_everyone") {
		t.Fatalf("enabled config: %s", enabledConfig)
	}

	failFirst = true
	restarts = nil
	if _, err := f.API.disableGroupGrants(ctx, "org", nil, deps); err == nil {
		t.Fatal("disable with a failed restart succeeded")
	}
	if got := readConfig(); got != enabledConfig {
		t.Errorf("config after the failed disable:\n%s", got)
	}
	if on, err := f.API.groupGrantsEnabled(ctx, "org"); err != nil || !on {
		t.Errorf("group grants off after a failed disable: %v %v", on, err)
	}
}

func TestScheduledGroupGrantsApplyRetriesWhenTheConfigIsBusy(t *testing.T) {
	f, _, _ := groupsFront(t)
	f.API.GroupGrantsDelay = 10 * time.Millisecond
	var runs atomic.Int32
	done := make(chan struct{})
	f.API.GroupGrantsRun = func(string) error {
		if runs.Add(1) == 1 {
			return errGraphjinConfigBusy
		}
		close(done)
		return nil
	}
	f.API.scheduleGroupGrantsApply(t.Context(), "org")
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("no retry after a busy config lock; runs=%d", runs.Load())
	}
}
