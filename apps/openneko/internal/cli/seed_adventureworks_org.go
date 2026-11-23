package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/open-neko/neko/apps/openneko/internal/db"
)

const (
	defaultAdventureWorksOrgID                = "adventureworks"
	defaultAdventureWorksOrgName              = "AdventureWorks Cycles"
	defaultAdventureWorksCompanyNote          = "We design and manufacture bicycles and accessories sold to specialty retailers and direct to consumers across North America, Europe, and Australia."
	defaultAdventureWorksFiscalYearStartMonth = 7
)

var (
	defaultAdventureWorksActiveSeats = []string{"CEO", "CFO", "COO"}
	defaultAdventureWorksPriorities  = []string{
		"Defend wholesale margins",
		"Grow DTC in Europe",
	}
)

type adventureWorksOrgSeedConfig struct {
	Target            db.ConnConfig
	GraphJinRoot      string
	UpdateDataSource  bool
	ReconcileAuthMode bool
	AuthMode          string
	Out               io.Writer
}

func adventureWorksOrgSeedConfigFromEnv(out io.Writer) (adventureWorksOrgSeedConfig, error) {
	target, err := defaultConn()
	if err != nil {
		return adventureWorksOrgSeedConfig{}, err
	}
	root := strings.TrimSpace(os.Getenv("ADVENTUREWORKS_GRAPHJIN_ROOT"))
	if root == "" {
		root = "http://graphjin:8080"
	}
	root = strings.TrimRight(root, "/")
	authMode := "none"
	if os.Getenv("ADVENTUREWORKS_AUTH_MODE") == "jwt" {
		authMode = "jwt"
	}
	return adventureWorksOrgSeedConfig{
		Target:            target,
		GraphJinRoot:      root,
		UpdateDataSource:  os.Getenv("ADVENTUREWORKS_UPDATE_DATA_SOURCE") == "1",
		ReconcileAuthMode: os.Getenv("ADVENTUREWORKS_RECONCILE_AUTH_MODE") == "1",
		AuthMode:          authMode,
		Out:               out,
	}, nil
}

func (c adventureWorksOrgSeedConfig) logf(format string, args ...any) {
	if c.Out != nil {
		fmt.Fprintln(c.Out, "[seed-adventureworks] "+fmt.Sprintf(format, args...))
	}
}

type adventureWorksBatchColumn struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Default *bool  `json:"default,omitempty"`
}

type adventureWorksAPIBatch struct {
	Version      int                         `json:"version"`
	Compiled     bool                        `json:"compiled"`
	Compiler     string                      `json:"compiler"`
	RecordsField string                      `json:"recordsField"`
	Columns      []adventureWorksBatchColumn `json:"columns"`
}

type adventureWorksOutputContract struct {
	APIBatch adventureWorksAPIBatch `json:"apiBatch"`
}

type adventureWorksTrialWorkflow struct {
	Name           string
	Description    string
	Goal           string
	Cron           *string
	CronTimezone   string
	OutputContract adventureWorksOutputContract
}

func newWorkflowBatchContract(cols []adventureWorksBatchColumn) adventureWorksOutputContract {
	return adventureWorksOutputContract{
		APIBatch: adventureWorksAPIBatch{
			Version:      1,
			Compiled:     true,
			Compiler:     "workflow",
			RecordsField: "records",
			Columns:      cols,
		},
	}
}

func adventureWorksTrialWorkflows() []adventureWorksTrialWorkflow {
	strPtr := func(s string) *string { return &s }
	boolPtr := func(b bool) *bool { return &b }
	return []adventureWorksTrialWorkflow{
		{
			Name:         "Daily Revenue Health Check",
			Description:  "Snapshot of yesterday's revenue vs trailing 7-day average. Lands on the Briefing each morning.",
			Goal:         "Once per day, query AdventureWorks sales data via the connected GraphJin source. Compute (a) yesterday's total revenue across all territories from sales.salesorderheader.subtotal, and (b) the average daily revenue over the prior 7 days. Emit one workflow_output with kind=finding, mood=good if yesterday is within 10% of the avg, mood=watch if yesterday is 10–25% below, mood=act if >25% below. The output title should name yesterday's revenue and the delta; the body should be 1–2 sentences explaining the comparison.",
			Cron:         strPtr("0 9 * * *"),
			CronTimezone: "UTC",
			OutputContract: newWorkflowBatchContract([]adventureWorksBatchColumn{
				{Name: "Date", Path: "date"},
				{Name: "Revenue", Path: "revenue"},
				{Name: "Trailing 7-day average", Path: "trailing7DayAverage"},
				{Name: "Delta percent", Path: "deltaPercent"},
				{Name: "Mood", Path: "mood"},
			}),
		},
		{
			Name:         "Revenue Drop Alert",
			Description:  "Hourly territory-level revenue drop check. Surfaces sharp declines.",
			Goal:         "Once per hour, query AdventureWorks via GraphJin: for each territoryid in sales.salesorderheader, compute the current trailing-hour revenue and compare it to the same hour-of-week averaged over the prior 4 weeks. Emit a workflow_output with kind=finding, mood=act when any territory's current-hour revenue is below 50% of its baseline, scope=territory:<id>. Skip silently if no territory crosses the threshold. When mood=act, also emit a neko_action_request proposing a send_webhook notification to slack:#revenue-alerts with a one-line summary.",
			Cron:         strPtr("0 * * * *"),
			CronTimezone: "UTC",
			OutputContract: newWorkflowBatchContract([]adventureWorksBatchColumn{
				{Name: "Territory ID", Path: "territoryId"},
				{Name: "Current-hour revenue", Path: "currentHourRevenue"},
				{Name: "Baseline revenue", Path: "baselineRevenue"},
				{Name: "Baseline ratio", Path: "baselineRatio"},
				{Name: "Alert", Path: "alert", Default: boolPtr(false)},
			}),
		},
		{
			Name:         "Slow-Ship Operations",
			Description:  "Daily check for orders stuck in pending status past SLA.",
			Goal:         "Once per day, query AdventureWorks via GraphJin to find rows in sales.salesorderheader where status=1 (pending) and orderdate is more than 5 days ago. Emit one workflow_output with kind=finding, mood=watch when the count is between 1 and 10, mood=act when >10. Title: the count of slow-shipping orders. Body: a short list of the oldest 3 orderids with their orderdate.",
			Cron:         strPtr("30 8 * * *"),
			CronTimezone: "UTC",
			OutputContract: newWorkflowBatchContract([]adventureWorksBatchColumn{
				{Name: "Order ID", Path: "orderId"},
				{Name: "Order date", Path: "orderDate"},
				{Name: "Days pending", Path: "daysPending"},
				{Name: "Status", Path: "status"},
			}),
		},
		{
			Name:         "Stock Reorder Watch",
			Description:  "Reacts to inventory rows that drop below the product's reorder point. Subscription-triggered (no cron); fires per low-stock row.",
			Goal:         "Triggered by a source_change subscription on production.productinventory when a row's quantity falls below the related product's reorderpoint. The trigger_payload includes table, primary_key={productid,locationid}, and snapshot. Query AdventureWorks via GraphJin to fetch the product name, current quantity, reorderpoint, and listprice. Emit one workflow_output with kind=recommendation, mood=act, scope='inventory', topic='reorder_point' summarizing the gap and recommending a reorder quantity. Do NOT mutate productinventory or productorders here — the responder reads only.",
			Cron:         nil,
			CronTimezone: "UTC",
			OutputContract: newWorkflowBatchContract([]adventureWorksBatchColumn{
				{Name: "Product ID", Path: "productId"},
				{Name: "Location ID", Path: "locationId"},
				{Name: "Quantity", Path: "quantity"},
				{Name: "Reorder point", Path: "reorderPoint"},
				{Name: "Recommended reorder", Path: "recommendedReorderQuantity"},
			}),
		},
	}
}

func runAdventureWorksOrgSeed(ctx context.Context, cfg adventureWorksOrgSeedConfig) error {
	conn, err := db.Connect(ctx, cfg.Target, "openneko seed --org")
	if err != nil {
		return err
	}
	defer conn.Close(ctx)

	orgID, err := bootstrapAdventureWorksOrg(ctx, conn, cfg)
	if err != nil {
		return err
	}
	if err := seedAdventureWorksDataSource(ctx, conn, cfg, orgID); err != nil {
		return err
	}
	if err := seedAdventureWorksOnboarding(ctx, conn, cfg, orgID); err != nil {
		return err
	}
	if err := seedAdventureWorksWorkflows(ctx, conn, cfg, orgID); err != nil {
		return err
	}
	if err := seedAdventureWorksSubscription(ctx, conn, cfg, orgID); err != nil {
		return err
	}
	return seedAdventureWorksPolicy(ctx, conn, cfg, orgID)
}

func bootstrapAdventureWorksOrg(ctx context.Context, conn *pgx.Conn, cfg adventureWorksOrgSeedConfig) (string, error) {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, "select pg_advisory_xact_lock(hashtext('openneko.organization.bootstrap'))"); err != nil {
		return "", err
	}
	var orgID, orgName string
	err = tx.QueryRow(ctx, "select id, name from organization order by created_at asc, id asc limit 1").Scan(&orgID, &orgName)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		orgID = defaultAdventureWorksOrgID
		if _, err := tx.Exec(ctx, "insert into organization (id, name) values ($1, $2) on conflict (id) do nothing", orgID, defaultAdventureWorksOrgName); err != nil {
			return "", err
		}
		cfg.logf("created organization %s", orgID)
	case err != nil:
		return "", err
	case orgName == "My Workspace":
		if _, err := tx.Exec(ctx, "update organization set name = $2, updated_at = now() where id = $1", orgID, defaultAdventureWorksOrgName); err != nil {
			return "", err
		}
		cfg.logf("renamed default organization to %s", defaultAdventureWorksOrgName)
	default:
		cfg.logf("keeping existing organization %s", orgID)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return orgID, nil
}

func seedAdventureWorksDataSource(ctx context.Context, conn *pgx.Conn, cfg adventureWorksOrgSeedConfig, orgID string) error {
	var sourceID, label string
	err := conn.QueryRow(ctx, `
		select id, label
		  from data_source
		 where org_id = $1
		 order by (label = 'AdventureWorks') desc, created_at asc
		 limit 1`, orgID).Scan(&sourceID, &label)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}

	graphqlURL := cfg.GraphJinRoot + "/api/v1/graphql"
	subscriptionURL := cfg.GraphJinRoot + "/api/v1/graphql"
	mcpURL := cfg.GraphJinRoot + "/api/v1/mcp"

	switch {
	case errors.Is(err, pgx.ErrNoRows):
		if _, err := conn.Exec(ctx, `
			insert into data_source (
				org_id, kind, graphql_url, subscription_url, mcp_url, label, auth_mode
			) values ($1, 'graphjin', $2, $3, $4, 'AdventureWorks', $5)
			on conflict (org_id, name) do nothing`,
			orgID, graphqlURL, subscriptionURL, mcpURL, cfg.AuthMode); err != nil {
			return err
		}
		cfg.logf("inserted GraphJin data source (auth_mode=%s)", cfg.AuthMode)
	case cfg.UpdateDataSource:
		if _, err := conn.Exec(ctx, `
			update data_source
			   set kind = 'graphjin',
			       graphql_url = $2,
			       subscription_url = $3,
			       mcp_url = $4,
			       label = 'AdventureWorks',
			       auth_mode = $5,
			       updated_at = now()
			 where id = $1`,
			sourceID, graphqlURL, subscriptionURL, mcpURL, cfg.AuthMode); err != nil {
			return err
		}
		cfg.logf("updated GraphJin data source to %s (auth_mode=%s)", cfg.GraphJinRoot, cfg.AuthMode)
	case cfg.ReconcileAuthMode && label == "AdventureWorks":
		if _, err := conn.Exec(ctx, `
			update data_source
			   set auth_mode = $2,
			       updated_at = now()
			 where id = $1`,
			sourceID, cfg.AuthMode); err != nil {
			return err
		}
		cfg.logf("reconciled GraphJin data source auth_mode=%s", cfg.AuthMode)
	default:
		cfg.logf("data source already exists; leaving it unchanged")
	}
	return nil
}

func seedAdventureWorksOnboarding(ctx context.Context, conn *pgx.Conn, cfg adventureWorksOrgSeedConfig, orgID string) error {
	var existing string
	err := conn.QueryRow(ctx, "select org_id from onboarding_wizard where org_id = $1 limit 1", orgID).Scan(&existing)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if errors.Is(err, pgx.ErrNoRows) {
		if _, err := conn.Exec(ctx, `
			insert into onboarding_wizard (
				org_id,
				company_note,
				fiscal_year_start_month,
				active_seats,
				priorities,
				step
			)
			values ($1, $2, $3, $4, $5, 'company')
			on conflict (org_id) do nothing`,
			orgID,
			defaultAdventureWorksCompanyNote,
			defaultAdventureWorksFiscalYearStartMonth,
			defaultAdventureWorksActiveSeats,
			defaultAdventureWorksPriorities); err != nil {
			return err
		}
		cfg.logf("inserted onboarding defaults")
	} else {
		cfg.logf("onboarding defaults already exist; leaving them unchanged")
	}
	return nil
}

func seedAdventureWorksWorkflows(ctx context.Context, conn *pgx.Conn, cfg adventureWorksOrgSeedConfig, orgID string) error {
	trialWorkflows := adventureWorksTrialWorkflows()
	var existingID string
	err := conn.QueryRow(ctx, "select id from workflow_definition where org_id = $1 and name <> 'OpenNeko operations' limit 1", orgID).Scan(&existingID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if errors.Is(err, pgx.ErrNoRows) {
		for _, wf := range trialWorkflows {
			hasCron := wf.Cron != nil
			contractBytes, err := json.Marshal(wf.OutputContract)
			if err != nil {
				return err
			}
			if _, err := conn.Exec(ctx, `
				insert into workflow_definition (
					org_id, name, description, goal, cron, cron_timezone,
					cron_enabled, enabled, status, output_contract, team_approved_by, team_approved_at
				) values ($1, $2, $3, $4, $5, $6, $7, true, 'active', $8::jsonb, 'openneko seed', now())
				on conflict (org_id, owner_user_id, name) do nothing`,
				orgID, wf.Name, wf.Description, wf.Goal, wf.Cron, wf.CronTimezone, hasCron, string(contractBytes)); err != nil {
				return err
			}
		}
		cfg.logf("inserted %d trial workflows", len(trialWorkflows))
	} else {
		cfg.logf("workflows already exist; leaving them unchanged")
	}

	reconciledBatchContracts := int64(0)
	for _, wf := range trialWorkflows {
		batchBytes, err := json.Marshal(wf.OutputContract.APIBatch)
		if err != nil {
			return err
		}
		tag, err := conn.Exec(ctx, `
			update workflow_definition
			   set output_contract = jsonb_set(
			     coalesce(output_contract, '{}'::jsonb),
			     '{apiBatch}',
			     $4::jsonb,
			     true
			   )
			 where org_id = $1
			   and name = $2
			   and goal = $3
			   and not (coalesce(output_contract, '{}'::jsonb) ? 'apiBatch')`,
			orgID, wf.Name, wf.Goal, string(batchBytes))
		if err != nil {
			return err
		}
		reconciledBatchContracts += tag.RowsAffected()
	}
	cfg.logf("reconciled %d workflow-native batch contracts", reconciledBatchContracts)
	return nil
}

func seedAdventureWorksSubscription(ctx context.Context, conn *pgx.Conn, cfg adventureWorksOrgSeedConfig, orgID string) error {
	var stockWatchWorkflowID string
	err := conn.QueryRow(ctx, "select id from workflow_definition where org_id = $1 and name = 'Stock Reorder Watch' limit 1", orgID).Scan(&stockWatchWorkflowID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}

	var existingSubID string
	err = conn.QueryRow(ctx, "select id from subscription where org_id = $1 and workflow_id = $2 and source_kind = 'source_change' limit 1", orgID, stockWatchWorkflowID).Scan(&existingSubID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if errors.Is(err, pgx.ErrNoRows) {
		const filterJSON = `{"table":"productinventory","where":{"quantity":{"lt":{"col":"product.reorderpoint"}}},"primary_key":["productid","locationid"],"version_column":"modifieddate","select":["quantity"]}`
		if _, err := conn.Exec(ctx, `
			insert into subscription (
				org_id, workflow_id, source_kind, filter, enabled,
				max_concurrent_runs, idempotency_key_template
			) values ($1, $2, 'source_change', $3::jsonb, true, 5, $4)`,
			orgID, stockWatchWorkflowID, filterJSON, "reorder-{primary_key}"); err != nil {
			return err
		}
		cfg.logf("wired Stock Reorder Watch subscription")
	}
	return nil
}

func seedAdventureWorksPolicy(ctx context.Context, conn *pgx.Conn, cfg adventureWorksOrgSeedConfig, orgID string) error {
	var policyID string
	err := conn.QueryRow(ctx, "select id from action_policy where org_id = $1 and name = 'revenue_alerts_auto' limit 1", orgID).Scan(&policyID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if errors.Is(err, pgx.ErrNoRows) {
		if _, err := conn.Exec(ctx, `
			insert into action_policy (
				org_id, name, description,
				applies_to_kinds, applies_to_scopes,
				mode, risk_threshold_auto_approve,
				allowed_targets, limits, priority, enabled
			) values ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9::jsonb, $10, true)`,
			orgID,
			"revenue_alerts_auto",
			"Auto-approve revenue alert notifications to slack:#revenue-alerts at risk medium or below.",
			[]string{"send_webhook"},
			[]string{"external"},
			"auto_approve",
			"medium",
			`["slack:#revenue-alerts"]`,
			`{}`,
			800); err != nil {
			return err
		}
		cfg.logf("inserted revenue_alerts_auto policy")
	} else {
		cfg.logf("revenue_alerts_auto policy already exists; leaving it unchanged")
	}
	return nil
}
