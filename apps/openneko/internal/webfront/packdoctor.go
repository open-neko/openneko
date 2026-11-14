package webfront

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/open-neko/neko/apps/openneko/internal/packs"
)

var magentoDomains = []string{"catalog", "inventory", "orders", "promotions", "content", "customers"}

// operatorReadinessDetail is operatorReadinessDetail() of
// apps/worker/src/packs/pack-artifacts.ts.
func operatorReadinessDetail(reason string) string {
	switch reason {
	case "integration_token_missing":
		return "View only. Add a Magento API token if you later want to allow specific, approval-required changes; store insights and automations are fully available without it."
	case "integration_token_invalid":
		return "View only because Magento rejected the saved API token. Store insights and automations are unaffected."
	case "acl_missing":
		return "View only because the saved API token does not have the required Magento permissions. Store insights and automations are unaffected."
	case "graphjin_version_unsupported":
		return "View only in this version. Store insights and automations are fully available."
	case "ready":
		return "Approved Magento changes are available. Each area follows its configured approval and automation limits."
	case "domain_disabled":
		return "This change domain is disabled by the administrator."
	}
	return "View-only access could not be checked because the reporting connection is unavailable."
}

type packCheck struct{ id, status, detail string }

func packDoctorResult(packID, status string, checks []packCheck) *jsObj {
	list := make([]any, len(checks))
	for i, c := range checks {
		list[i] = jsObjOf("id", c.id, "status", c.status, "detail", c.detail)
	}
	return jsObjOf("packId", packID, "status", status, "checks", list)
}

// packServiceQuery is graphjinQuery() with a service token, as the pack
// preflight and the Magento smoke queries send it.
func (a *API) packServiceQuery(ctx context.Context, endpoint, orgID, userID, query string, variables *jsObj, xRole bool) (any, error) {
	token, err := a.mintGraphjinToken(graphjinClaims{orgID: orgID, userID: userID, role: "service"}, 60*time.Second)
	if err != nil {
		return nil, err
	}
	if err := a.checkLegacyPackOAuth(ctx, orgID, endpoint); err != nil {
		return nil, err
	}
	header := http.Header{"Content-Type": {"application/json"}, "Authorization": {"Bearer " + token}}
	if xRole {
		header.Set("X-Role", "service")
	}
	if variables == nil {
		variables = &jsObj{vals: map[string]any{}}
	}
	return watcherGraphQL(ctx, endpoint, header, query, variables)
}

var defaultPackVariables = map[string]any{
	"from":        map[string]any{"kind": "seconds_ago", "seconds": float64(30 * 86400)},
	"to":          map[string]any{"kind": "now"},
	"now":         map[string]any{"kind": "now"},
	"staleBefore": map[string]any{"kind": "seconds_ago", "seconds": float64(86400)},
	"olderThan":   map[string]any{"kind": "seconds_ago", "seconds": float64(2 * 86400)},
	"threshold":   map[string]any{"kind": "literal", "value": float64(0)},
}

// packReadPreflight is runPackReadPreflight() of
// apps/worker/src/packs/preflight.ts: every metric, watcher and saved query
// of the pack runs once against the source.
func (a *API) packReadPreflight(ctx context.Context, bundle *packs.Bundle, bound map[string]string, endpoint, orgID string, inputs map[string]any) error {
	endpoint = graphjinGraphQLEndpoint(endpoint)
	now := time.Now()
	results := map[string]any{}
	exercised := map[string]bool{}
	savedQuery := func(name string) (*packs.Artifact, string, error) {
		for i, art := range bundle.Artifacts {
			if art.Kind == packs.KindSavedQuery && strings.TrimSuffix(path.Base(art.Path), path.Ext(art.Path)) == name {
				if text, ok := bound[art.Path]; ok {
					return &bundle.Artifacts[i], text, nil
				}
				if text, ok := art.Content.(string); ok {
					return &bundle.Artifacts[i], text, nil
				}
			}
		}
		return nil, "", fmt.Errorf("pack saved query %s is missing", name)
	}
	run := func(name string, variables any) (any, error) {
		if variables == nil {
			variables = defaultPackVariables
		}
		definition, err := packValue(variables, inputs, nil)
		if err != nil {
			return nil, err
		}
		resolved, err := resolveWatcherVariables(jsValueOf(definition), now)
		if err != nil {
			return nil, err
		}
		key := name + "\x00" + jsStringify(resolved, false)
		if data, ok := results[key]; ok {
			return data, nil
		}
		art, text, err := savedQuery(name)
		if err != nil {
			return nil, err
		}
		data, err := a.packServiceQuery(ctx, endpoint, orgID, "pack-preflight", text, resolved, true)
		if err == nil && data == nil {
			err = errors.New("GraphJin returned no data")
		}
		if err != nil {
			return nil, fmt.Errorf("Pack query preflight failed for saved query %q at %s: %v. Check the saved query, source operation exposure, and credentials, then upload and review the corrected pack.", name, art.Path, err)
		}
		exercised[name] = true
		results[key] = data
		return data, nil
	}
	for _, art := range bundle.Artifacts {
		value := art.Record()
		switch art.Kind {
		case packs.KindMetric:
			execution, _ := value["execution"].(map[string]any)
			name := packString(execution["query"])
			data, err := run(name, execution["variables"])
			if err != nil {
				return err
			}
			mapping, _ := execution["result"].(map[string]any)
			if p, ok := mapping["path"].(string); ok && mapping["kind"] == "scalar" {
				switch extractValueAtPath(data, p).(type) {
				case nil, jsUndefined:
					return fmt.Errorf("pack metric %s result path is missing or null", art.Key)
				}
			}
			_, document, _ := savedQuery(name)
			withDocument := map[string]any{}
			for k, v := range execution {
				withDocument[k] = v
			}
			withDocument["document"] = document
			definition := map[string]any{}
			for k, v := range value {
				definition[k] = v
			}
			definition["execution"] = withDocument
			if _, err := mapSavedQueryMetric(jsValueOf(definition), data, nil, now); err != nil {
				return err
			}
		case packs.KindWatcher:
			data, err := run(packString(value["query"]), value["variables"])
			if err != nil {
				return err
			}
			if _, missing := extractValueAtPath(data, packString(value["valuePath"])).(jsUndefined); missing {
				return fmt.Errorf("pack watcher %s result path is missing", art.Key)
			}
		}
	}
	for _, art := range bundle.Artifacts {
		name := strings.TrimSuffix(path.Base(art.Path), path.Ext(art.Path))
		if art.Kind == packs.KindSavedQuery && !exercised[name] {
			if _, err := run(name, nil); err != nil {
				return err
			}
		}
	}
	return nil
}

// magentoAnalyticsSmoke is runMagentoAnalyticsSmoke(): sales_order reads,
// customer columns read, and the protect_code blocklist holds.
func (a *API) magentoAnalyticsSmoke(ctx context.Context, endpoint, orgID string) error {
	where := `(where: { created_at: { gte: "1970-01-01 00:00:00" } }, limit: 1)`
	check := func(query, prefix string) error {
		data, err := a.packServiceQuery(ctx, endpoint, orgID, "pack-health", query, nil, false)
		var gqlErr graphqlErrors
		if errors.As(err, &gqlErr) {
			return fmt.Errorf("%s: %s", prefix, gqlErr)
		}
		if err != nil {
			return err
		}
		if _, ok := jsGet(data, "sales_order").([]any); !ok {
			return fmt.Errorf("%s: sales_order result unavailable", prefix)
		}
		return nil
	}
	if err := check(`query MagentoPackAnalyticsSmoke { sales_order`+where+` { entity_id } }`, "Magento analytics smoke query failed"); err != nil {
		return err
	}
	if err := check(`query MagentoPackOperationalDataCanary { sales_order`+where+` { customer_id customer_email customer_firstname } }`, "Magento operational data check failed"); err != nil {
		return err
	}
	_, err := a.packServiceQuery(ctx, endpoint, orgID, "pack-health", `query MagentoPackSecretColumnCanary { sales_order`+where+` { protect_code } }`, nil, false)
	var gqlErr graphqlErrors
	if errors.As(err, &gqlErr) {
		return nil
	}
	if err != nil {
		return err
	}
	return errors.New("Magento secret-data check failed: GraphJin did not enforce the sales_order protect_code blocklist")
}

// packDoctor is PackService.doctor().
func (a *API) packDoctor(ctx context.Context, orgID, packID string) (*jsObj, error) {
	existing, err := a.latestPackInstall(ctx, orgID, packID)
	if err != nil {
		return nil, err
	}
	if existing == nil || existing.Status == "removed" {
		return packDoctorResult(packID, "blocked", []packCheck{{"installation", "blocked", "pack is not installed"}}), nil
	}
	bundle, err := installedPackBundle(orgID, packID, existing)
	if err != nil {
		return nil, err
	}
	supplied := &jsObj{vals: map[string]any{}}
	for _, k := range existing.Config.ordered() {
		if bundle.Manifest.InputDeclared(k) {
			supplied.set(k, existing.Config.vals[k])
		}
	}
	prior, _ := existing.Config.vals["_runtime"].(*jsObj)
	if packID != "magento" {
		check, err := a.packQueryDoctor(ctx, orgID, bundle, supplied, prior)
		if err != nil {
			log.Printf("[packs] doctor %s: %v", packID, err)
			return packDoctorResult(packID, "blocked", []packCheck{{"queries", "blocked", "Pack configuration or query preflight failed."}}), nil
		}
		return packDoctorResult(packID, "ready", []packCheck{check}), nil
	}

	var checks []packCheck
	var preflight *magentoPreflightResult
	inputs, err := resolvePackInputs(bundle, supplied)
	if err == nil {
		var secretValues map[string]string
		if secretValues, err = a.resolvePackSecrets(bundle, &jsObj{vals: map[string]any{}}); err == nil {
			preflight, err = runMagentoPreflight(ctx, magentoInputFrom(inputs, secretValues), magentoHTTPClient)
		}
	}
	if err != nil {
		checks = append(checks, packCheck{"analytics", "blocked", err.Error()})
	} else {
		checks = append(checks,
			packCheck{"analytics", "ready", fmt.Sprintf("%s %s; SELECT-only grants verified", preflight.databaseType, preflight.databaseVersion)},
			packCheck{"magento", "ready", fmt.Sprintf("%s; store IDs %s", preflight.magentoVersion, joinInts(preflight.storeIDs))})
	}

	graphqlURL := ""
	if selection, ok := prior.lookup("source"); ok && isJSObj(selection) {
		source, err := a.resolveSelectedPackSource(ctx, orgID, plainJSON(selection).(map[string]any))
		if err != nil {
			return nil, err
		}
		graphqlURL = source.GraphqlURL
	} else if bundle.Manifest.Artifacts.GraphJin != nil {
		var url *string
		err := a.DB.QueryRow(ctx, `select graphql_url from data_source where org_id = $1 and enabled order by is_default desc, created_at limit 1`, orgID).Scan(&url)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		if url != nil {
			graphqlURL = *url
		}
	}
	configFile := strings.TrimSpace(os.Getenv("OPENNEKO_GRAPHJIN_CONFIG"))
	_, statErr := os.Stat(configFile)
	if graphqlURL == "" || configFile == "" || statErr != nil {
		checks = append(checks, packCheck{"graphjin", "blocked", "customer GraphJin endpoint/config volume is unavailable"})
	} else if revision, err := a.magentoGraphjinCheck(ctx, graphjinGraphQLEndpoint(graphqlURL), orgID); err != nil {
		checks = append(checks, packCheck{"graphjin", "blocked", err.Error()})
	} else {
		checks = append(checks,
			packCheck{"graphjin", "ready", "catalog " + revision},
			packCheck{"analytics-query", "ready", "Magento sales_order smoke query succeeded"})
	}

	if preflight != nil {
		for _, domain := range magentoDomains {
			reason := preflight.operatorDomains[domain]
			status := "blocked"
			switch reason {
			case "ready":
				status = "ready"
			case "domain_disabled":
				status = "optional"
			}
			checks = append(checks, packCheck{"changes-" + domain, status, operatorReadinessDetail(reason)})
		}
		if preflight.bulkConsumerReadiness == "ready" {
			checks = append(checks, packCheck{"bulk-consumers", "ready", "Magento async bulk consumers completed recently."})
		} else {
			checks = append(checks, packCheck{"bulk-consumers", "blocked", "Magento async bulk consumers are not reporting recent successful work."})
		}
	} else {
		checks = append(checks, packCheck{"changes", "optional", operatorReadinessDetail("")})
	}
	status := "ready"
	for _, c := range checks {
		operator := strings.HasPrefix(c.id, "changes-") || c.id == "bulk-consumers"
		if c.status != "blocked" {
			continue
		}
		if !operator && !strings.HasPrefix(c.id, "changes") {
			status = "blocked"
			break
		}
		if operator {
			status = "degraded"
		}
	}
	return packDoctorResult(packID, status, checks), nil
}

func (a *API) packQueryDoctor(ctx context.Context, orgID string, bundle *packs.Bundle, supplied, prior *jsObj) (packCheck, error) {
	inputs, err := resolvePackInputs(bundle, supplied)
	if err != nil {
		return packCheck{}, err
	}
	secretValues, err := a.resolvePackSecrets(bundle, &jsObj{vals: map[string]any{}})
	if err != nil {
		return packCheck{}, err
	}
	runtime, err := a.packRuntime(ctx, orgID, bundle, &jsObj{vals: map[string]any{}}, prior)
	if err != nil {
		return packCheck{}, err
	}
	bindings := map[string]string{}
	for k, v := range runtime.vals["bindings"].(*jsObj).vals {
		bindings[k], _ = v.(string)
	}
	plainInputs, _ := plainJSON(inputs).(map[string]any)
	if err := declarativePackChecks(bundle, plainInputs, secretValues, bindings); err != nil {
		return packCheck{}, err
	}
	source, ok := runtime.vals["source"].(*jsObj)
	if !ok {
		return packCheck{"configuration", "ready", "Pack configuration passed. No data connection is required."}, nil
	}
	_, bound, err := bindPackQueries(bundle, bindings)
	if err != nil {
		return packCheck{}, err
	}
	if err := a.packReadPreflight(ctx, bundle, bound, jsValueString(source.vals["graphqlUrl"]), orgID, plainInputs); err != nil {
		return packCheck{}, err
	}
	return packCheck{"queries", "ready", "Pack queries and response mappings passed."}, nil
}

func (a *API) magentoGraphjinCheck(ctx context.Context, endpoint, orgID string) (string, error) {
	token, err := a.mintGraphjinToken(graphjinClaims{orgID: orgID, userID: "pack-doctor", role: "admin"}, 60*time.Second)
	if err != nil {
		return "", err
	}
	if err := a.checkLegacyPackOAuth(ctx, orgID, endpoint); err != nil {
		return "", err
	}
	data, err := watcherGraphQL(ctx, endpoint, http.Header{"Content-Type": {"application/json"}, "Authorization": {"Bearer " + token}},
		`query { gj_config(id: "current") { catalog_revision } }`, &jsObj{vals: map[string]any{}})
	if err != nil {
		return "", err
	}
	revision := jsGet(jsGet(data, "gj_config"), "catalog_revision")
	if !packTruthy(revision) {
		return "", errors.New("catalog revision unavailable")
	}
	if err := a.magentoAnalyticsSmoke(ctx, endpoint, orgID); err != nil {
		return "", err
	}
	return jsValueString(revision), nil
}

func nullishOr(v, fallback any) any {
	if _, undef := v.(jsUndefined); undef || v == nil {
		return fallback
	}
	return v
}

func joinInts(values []int64) string {
	parts := make([]string, len(values))
	for i, v := range values {
		parts[i] = fmt.Sprint(v)
	}
	return strings.Join(parts, ", ")
}
