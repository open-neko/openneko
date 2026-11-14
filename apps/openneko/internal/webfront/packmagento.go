package webfront

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
)

var magentoRequiredTables = []string{
	"sales_order", "sales_order_item", "sales_order_address", "sales_order_status_history", "sales_order_payment",
	"sales_invoice", "sales_invoice_item", "sales_creditmemo", "sales_creditmemo_item", "sales_shipment",
	"sales_shipment_item", "customer_entity", "customer_address_entity", "catalog_product_entity",
	"inventory_source_item", "store", "store_group", "store_website", "cron_schedule", "indexer_state",
}

var magentoDomainProbes = map[string]string{
	"catalog":    "/V1/products?searchCriteria[pageSize]=1",
	"inventory":  "/V1/inventory/source-items?searchCriteria[pageSize]=1",
	"orders":     "/V1/orders?searchCriteria[pageSize]=1",
	"promotions": "/V1/salesRules/search?searchCriteria[pageSize]=1",
	"content":    "/V1/cmsPage/search?searchCriteria[pageSize]=1",
	"customers":  "/V1/customers/search?searchCriteria[pageSize]=1",
}

type magentoPreflightInput struct {
	host, database, username, password string
	port                               float64
	tablePrefix, baseURL, storeCode    string
	integrationToken                   string
	customersEnabled                   bool
}

// magentoPreflightResult holds the fields of MagentoPreflightResult that the
// doctor reads.
type magentoPreflightResult struct {
	databaseVersion, databaseType string
	tablePrefix                   string
	storeIDs                      []int64
	magentoVersion                string
	operatorReadiness             string
	operatorDomains               map[string]string
	bulkConsumerReadiness         string
}

var (
	magentoIdentifier   = regexp.MustCompile(`^[A-Za-z0-9_]+$`)
	magentoDBVersion    = regexp.MustCompile(`^(\d+)\.(\d+)`)
	magentoGrant        = regexp.MustCompile(`(?i)^GRANT\s+(.+?)\s+ON\s+`)
	magentoGrantOption  = regexp.MustCompile(`(?i)WITH\s+GRANT\s+OPTION`)
	magentoAllPrivilege = regexp.MustCompile(`(?i)ALL PRIVILEGES`)
	magentoVersionText  = regexp.MustCompile(`(?i)2\.4(?:\.\d+(?:-p\d+)?)?`)
)

func quoteMagentoIdentifier(value string) (string, error) {
	if !magentoIdentifier.MatchString(value) {
		return "", fmt.Errorf("invalid Magento SQL identifier: %s", value)
	}
	return "`" + value + "`", nil
}

// supportedMagentoDatabase is supportedMagentoDatabase().
func supportedMagentoDatabase(version string) (string, error) {
	m := magentoDBVersion.FindStringSubmatch(strings.TrimSpace(version))
	if m == nil {
		return "", fmt.Errorf("could not parse MariaDB/MySQL version %s", jsStringify(version, false))
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	if strings.Contains(strings.ToLower(version), "mariadb") {
		if major > 10 || major == 10 && minor >= 6 {
			return "mariadb", nil
		}
		return "", fmt.Errorf("MariaDB 10.6+ is required; found %s", version)
	}
	if major >= 8 {
		return "mysql", nil
	}
	return "", fmt.Errorf("MySQL 8.0+ is required; found %s", version)
}

// checkMagentoGrants is assertReadOnlyGrants() on the lines of SHOW GRANTS.
func checkMagentoGrants(grants []string) error {
	allowed := []string{"SELECT", "SHOW VIEW", "USAGE"}
	for _, grant := range grants {
		if magentoGrantOption.MatchString(grant) {
			return errors.New("analytics account must not have GRANT OPTION")
		}
		var unexpected []string
		if m := magentoGrant.FindStringSubmatch(grant); m != nil {
			for _, p := range strings.Split(m[1], ",") {
				if p = strings.ToUpper(strings.TrimSpace(p)); p != "" && !slices.Contains(allowed, p) {
					unexpected = append(unexpected, p)
				}
			}
		}
		if len(unexpected) > 0 || magentoAllPrivilege.MatchString(grant) {
			list := strings.Join(unexpected, ", ")
			if list == "" {
				list = "ALL PRIVILEGES"
			}
			return fmt.Errorf("analytics account is not read-only (unexpected grants: %s)", list)
		}
	}
	return nil
}

// detectMagentoTablePrefix is detectTablePrefix().
func detectMagentoTablePrefix(tableNames []string, configured string) (string, error) {
	if configured != "" && !magentoIdentifier.MatchString(configured) {
		return "", errors.New("magento.table_prefix may contain letters, numbers, and underscores only")
	}
	candidates := []string{configured}
	if configured == "" {
		candidates = nil
		for _, name := range tableNames {
			if prefix, ok := strings.CutSuffix(name, "sales_order"); ok && !slices.Contains(candidates, prefix) {
				candidates = append(candidates, prefix)
			}
		}
	}
	var valid []string
	for _, prefix := range candidates {
		if !slices.ContainsFunc(magentoRequiredTables, func(t string) bool { return !slices.Contains(tableNames, prefix+t) }) {
			valid = append(valid, prefix)
		}
	}
	switch len(valid) {
	case 0:
		return "", errors.New("Magento analytics tables are missing or the table prefix is incorrect")
	case 1:
		return valid[0], nil
	}
	shown := make([]string, len(valid))
	for i, v := range valid {
		shown[i] = cmpOr(v, "<empty>")
	}
	return "", fmt.Errorf("multiple Magento table prefixes detected: %s", strings.Join(shown, ", "))
}

func cmpOr(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

func isLoopbackHostname(hostname string) bool {
	return hostname == "localhost" || hostname == "127.0.0.1" || hostname == "::1"
}

// readMagentoVersion is readMagentoVersion(). A store that redirects to a
// loopback host is asked again at the routable address with its own Host.
func readMagentoVersion(ctx context.Context, baseURL string, client *http.Client) (string, error) {
	endpoint := strings.TrimRight(baseURL, "/") + "/magento_version"
	get := func(host string, follow bool) (*http.Response, []byte, error) {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, nil, err
		}
		if host != "" {
			req.Host = host
		}
		c := *client
		if !follow {
			c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		}
		res, err := c.Do(req)
		if err != nil {
			return nil, nil, err
		}
		defer res.Body.Close()
		body, err := io.ReadAll(io.LimitReader(res.Body, 64*1024+1))
		if err != nil {
			return nil, nil, err
		}
		if host != "" && len(body) > 64*1024 {
			return nil, nil, errors.New("Magento version response exceeded 64 KiB")
		}
		return res, body, nil
	}
	res, body, err := get("", false)
	if err != nil {
		return "", err
	}
	if location := res.Header.Get("Location"); res.StatusCode >= 300 && res.StatusCode < 400 && location != "" {
		source, _ := url.Parse(endpoint)
		target, err := source.Parse(location)
		if err != nil {
			return "", err
		}
		if !isLoopbackHostname(source.Hostname()) && isLoopbackHostname(target.Hostname()) {
			res, body, err = get(target.Host, false)
		} else {
			res, body, err = get("", true)
		}
		if err != nil {
			return "", err
		}
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return "", fmt.Errorf("Magento version preflight failed: HTTP %d", res.StatusCode)
	}
	match := magentoVersionText.FindString(string(body))
	if match == "" {
		return "", errors.New("Magento version endpoint did not report a supported 2.4.x version")
	}
	if match == "2.4" {
		return "2.4.x", nil
	}
	return match, nil
}

// magentoOperatorReadiness is magentoOperatorReadiness(): one REST probe per
// change domain with the integration token.
func magentoOperatorReadiness(ctx context.Context, in magentoPreflightInput, client *http.Client) (string, map[string]string) {
	domains := map[string]string{}
	if in.integrationToken == "" {
		for _, d := range magentoDomains {
			domains[d] = "integration_token_missing"
			if d == "customers" && !in.customersEnabled {
				domains[d] = "domain_disabled"
			}
		}
		return "integration_token_missing", domains
	}
	base := strings.TrimRight(in.baseURL, "/") + "/rest/" + encodeURIComponent(in.storeCode)
	for _, d := range magentoDomains {
		if d == "customers" && !in.customersEnabled {
			domains[d] = "domain_disabled"
			continue
		}
		domains[d] = probeMagentoDomain(ctx, base+magentoDomainProbes[d], in.integrationToken, client)
	}
	overall := "ready"
	for _, d := range magentoDomains {
		if r := domains[d]; r != "domain_disabled" && r != "ready" {
			if r == "integration_token_invalid" {
				return r, domains
			}
			overall = "acl_missing"
		}
	}
	return overall, domains
}

func probeMagentoDomain(ctx context.Context, endpoint, token string, client *http.Client) string {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "integration_token_invalid"
	}
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := client.Do(req)
	if err != nil {
		return "integration_token_invalid"
	}
	res.Body.Close()
	switch {
	case res.StatusCode == 401:
		return "integration_token_invalid"
	case res.StatusCode == 403:
		return "acl_missing"
	case res.StatusCode >= 200 && res.StatusCode < 300, res.StatusCode == 400, res.StatusCode == 404:
		return "ready"
	}
	return "acl_missing"
}

// runMagentoPreflight is runMagentoPreflight() of
// apps/worker/src/packs/magento-preflight.ts, in the same order of checks.
func runMagentoPreflight(ctx context.Context, in magentoPreflightInput, client *http.Client) (*magentoPreflightResult, error) {
	cfg := mysql.NewConfig()
	cfg.User, cfg.Passwd, cfg.DBName = in.username, in.password, in.database
	cfg.Net, cfg.Addr = "tcp", net.JoinHostPort(in.host, strconv.FormatFloat(in.port, 'f', -1, 64))
	cfg.Timeout = 10 * time.Second
	cfg.ParseTime, cfg.Loc = true, time.UTC
	connector, err := mysql.NewConnector(cfg)
	if err != nil {
		return nil, err
	}
	db := sql.OpenDB(connector)
	defer db.Close()
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	grants, err := magentoColumn(ctx, conn, "SHOW GRANTS FOR CURRENT_USER()")
	if err != nil {
		return nil, err
	}
	if err := checkMagentoGrants(grants); err != nil {
		return nil, err
	}
	var version, sqlMode, timezone sql.NullString
	if err := conn.QueryRowContext(ctx, "SELECT VERSION() AS database_version, @@sql_mode AS sql_mode, @@session.time_zone AS database_timezone").
		Scan(&version, &sqlMode, &timezone); err != nil {
		return nil, err
	}
	tableNames, err := magentoColumn(ctx, conn, "SELECT table_name FROM information_schema.tables WHERE table_schema = ? ORDER BY table_name", in.database)
	if err != nil {
		return nil, err
	}
	prefix, err := detectMagentoTablePrefix(tableNames, in.tablePrefix)
	if err != nil {
		return nil, err
	}
	configTable, err := quoteMagentoIdentifier(prefix + "core_config_data")
	if err != nil {
		return nil, err
	}
	if _, err := magentoColumn(ctx, conn, "SELECT path FROM "+configTable+" WHERE scope = 'default' AND scope_id = 0 AND path IN ('currency/options/base', 'general/locale/timezone')"); err != nil {
		return nil, err
	}
	storeTable, err := quoteMagentoIdentifier(prefix + "store")
	if err != nil {
		return nil, err
	}
	storeQuery, storeArgs := "SELECT store_id FROM "+storeTable+" WHERE is_active = 1 AND code <> 'admin' ORDER BY store_id", []any(nil)
	if code := strings.TrimSpace(in.storeCode); code != "all" {
		storeQuery, storeArgs = "SELECT store_id FROM "+storeTable+" WHERE is_active = 1 AND code = ? ORDER BY store_id", []any{code}
	}
	ids, err := magentoColumn(ctx, conn, storeQuery, storeArgs...)
	if err != nil {
		return nil, err
	}
	var storeIDs []int64
	for _, id := range ids {
		if n, err := strconv.ParseInt(id, 10, 64); err == nil {
			storeIDs = append(storeIDs, n)
		}
	}
	if len(storeIDs) == 0 {
		return nil, fmt.Errorf("Magento store code %s is missing or inactive", jsStringify(in.storeCode, false))
	}
	overall, domains := magentoOperatorReadiness(ctx, in, client)
	if !version.Valid {
		return nil, errors.New("MariaDB/MySQL version preflight returned no row")
	}
	databaseType, err := supportedMagentoDatabase(version.String)
	if err != nil {
		return nil, err
	}
	magentoVersion, err := readMagentoVersion(ctx, in.baseURL, client)
	if err != nil {
		return nil, err
	}
	bulk, err := magentoBulkConsumerReadiness(ctx, conn, prefix, time.Now())
	if err != nil {
		return nil, err
	}
	return &magentoPreflightResult{
		databaseVersion: version.String, databaseType: databaseType, tablePrefix: prefix, storeIDs: storeIDs,
		magentoVersion: magentoVersion, operatorReadiness: overall, operatorDomains: domains, bulkConsumerReadiness: bulk,
	}, nil
}

func magentoBulkConsumerReadiness(ctx context.Context, conn *sql.Conn, prefix string, now time.Time) (string, error) {
	table, err := quoteMagentoIdentifier(prefix + "cron_schedule")
	if err != nil {
		return "", err
	}
	var executed sql.NullTime
	err = conn.QueryRowContext(ctx, "SELECT executed_at FROM "+table+
		" WHERE job_code = 'consumers_runner' AND status = 'success' AND executed_at IS NOT NULL ORDER BY executed_at DESC LIMIT 1").Scan(&executed)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if !executed.Valid || now.Sub(executed.Time) > 30*time.Minute {
		return "bulk_consumers_not_running", nil
	}
	return "ready", nil
}

func magentoColumn(ctx context.Context, conn *sql.Conn, query string, args ...any) ([]string, error) {
	rows, err := conn.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v sql.NullString
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v.String)
	}
	return out, rows.Err()
}
