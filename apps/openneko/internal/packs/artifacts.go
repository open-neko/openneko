package packs

import (
	"fmt"
	"strings"
)

var (
	artifactKey = matches(artifactKeyRe, "invalid artifact key")
	targetRef   = matches(targetRefRe, "invalid target ref")
	gqlName     = matches(gqlNameRe, "invalid GraphQL name")
	anyRecord   = record(anyValue, anyValue)
	strRecord   = func(value rule) rule { return record(str(0), value) }

	baseFields = []field{req("key", artifactKey), req("targetRef", targetRef)}

	sourceFileRule = strictObj(req("sources", array(looseObj(opt("key", artifactKey), req("name", targetRef), req("kind", enum("database", "api"))), 1)))

	metricRule = strictObj(append(baseFields,
		req("title", str(1)), req("description", str(1)), req("role", str(1)), req("chartHint", str(1)), req("unit", str(1)),
		req("directionGood", enum("up", "down", "neutral")), req("cadence", str(1)), req("calculationNote", str(1)),
		req("execution", strictObj(
			req("mode", literal("saved_query")), req("source", str(1)), req("query", str(1)), req("result", anyRecord),
			req("freshnessSeconds", integer(1)), opt("variables", anyRecord),
		)),
	)...)

	workflowRule = strictObj(append(baseFields,
		req("name", str(1)), req("description", str(0)), req("enabled", boolean), req("status", literal("active")), req("goal", str(1)),
		req("schedule", nullable(strictObj(req("cron", str(1)), req("timezoneInput", str(1)), req("enabled", boolean)))),
		opt("networkHosts", array(str(1), 0)), opt("activation", str(0)),
		req("outputContract", strictObj(req("kind", str(1)), req("required", array(str(0), 1)))),
	)...)

	watcherRule = strictObj(append(baseFields,
		req("name", str(1)), req("description", str(0)), req("workflow", artifactKey), req("enabled", boolean), req("activation", str(1)),
		req("query", str(1)), req("valuePath", str(1)), req("operator", enum("gt", "gte", "lt", "lte", "eq", "ne")),
		req("threshold", nullable(scalar)), req("cadenceSeconds", integer(1)), req("debounceSeconds", integer(0)), req("cooldownSeconds", integer(0)),
		req("severity", enum("low", "medium", "high", "critical")), req("dedupeKey", str(1)),
		opt("readinessSignals", array(str(0), 0)), opt("variables", anyRecord),
	)...)

	mappingLeaf = union(
		strictObj(req("input", str(1))),
		strictObj(req("packInput", str(1))),
		strictObj(req("const", nullable(scalar))),
	)

	legacyActionRule = strictObj(append(baseFields,
		req("kind", targetRef), req("description", str(1)),
		req("readiness", strictObj(req("capability", str(1)), req("unavailableReasons", array(str(0), 1)))),
		req("inputSchema", anyRecord),
		req("adapter", strictObj(
			req("kind", literal("graphjin_api_operation")), req("source", str(1)), req("operationId", str(1)), req("mutationRoot", str(1)),
			req("preconditionQuery", str(1)), req("mutationQuery", str(1)), req("reconciliationQuery", str(1)),
			req("request", strictObj(req("path", strRecord(mapping)), req("body", strRecord(mapping)))),
			req("receipt", strictObj(req("fields", array(str(0), 0)), req("redact", array(str(0), 0)))),
			req("ambiguousOutcome", literal("reconcile_required")), req("retry", literal("never")),
		)),
	)...)

	apiActionRule = strictObj(append(baseFields,
		req("kind", targetRef), req("description", str(1)),
		req("readiness", strictObj(req("capability", literal("operator")), req("unavailableReasons", array(str(0), 1)))),
		req("inputSchema", anyRecord), opt("example", anyRecord),
		opt("targets", strictObj(req("fields", array(matches(fieldPathRe, "invalid field path"), 1)), opt("as", enum("value", "email_domain")))),
		req("adapter", strictObj(
			req("kind", literal("graphjin_api_operation")), req("source", str(1)), req("spec", str(1)),
			req("operations", record(str(1), strictObj(
				req("operationId", str(1)), req("mutationRoot", gqlName), opt("reversible", boolean),
				opt("idempotencyHeader", matches(headerNameRe, "invalid header name")),
			))),
			opt("lockedBody", record(matches(bodyFieldRe, "invalid body field"), strictObj(req("input", str(1))))),
			req("ambiguousOutcome", literal("reconcile_required")), req("retry", literal("never")),
		)),
	)...)

	magentoDomain = enum("catalog", "inventory", "orders", "promotions", "content", "customers")
	magentoOpRule = strictObj(
		req("operationId", str(1)), req("mutationRoot", gqlName), opt("readRoot", gqlName), opt("readOperationId", str(1)),
		opt("readPath", func(v any, path string) error {
			if s, ok := v.(string); !ok || !strings.HasPrefix(s, "/V1/") {
				return issue(path, `must start with "/V1/"`)
			}
			return nil
		}),
		opt("bodyKey", str(1)), opt("inverseOperation", str(1)), opt("entityIdField", str(1)),
		req("defaultClass", union(literal(float64(1)), literal(float64(2)))), req("entityType", str(1)), req("reversible", boolean),
		opt("numericPathParams", array(str(1), 0)), opt("resultMode", enum("sync", "async_bulk")),
	)
	magentoActionRule = strictObj(append(baseFields,
		req("kind", targetRef), req("description", str(1)), req("domain", magentoDomain),
		req("readiness", strictObj(req("capability", literal("operator")), req("domain", magentoDomain), req("unavailableReasons", array(str(0), 1)))),
		req("inputSchema", anyRecord),
		req("adapter", union(
			strictObj(
				req("kind", enum("magento_governed_operation", "magento_changeset")), req("source", literal("magento_operator")),
				req("spec", literal("magento-operator-v2")), req("operations", record(str(1), magentoOpRule)),
				req("ambiguousOutcome", literal("reconcile_required")), req("retry", literal("never")),
			),
			strictObj(
				req("kind", literal("magento_financial_handoff")),
				req("handoffKinds", array(enum("online_refund", "return_approval", "financial_configuration", "store_credit_over_cap"), 1)),
			),
		)),
	)...)

	policyRule = strictObj(append(baseFields,
		req("name", str(1)), req("description", str(0)), req("appliesToKinds", array(targetRef, 1)), req("appliesToScopes", array(targetRef, 1)),
		req("mode", enum("ask", "auto", "draft", "never")), opt("approverRole", literal("admin")), req("priority", integer(-1e300)),
		req("enabled", boolean), req("allowedTargets", anyRecord), req("limits", anyRecord),
	)...)

	relationshipsRule = strictObj(append(baseFields,
		req("source", str(1)),
		req("relationships", array(strictObj(
			req("left", str(1)), req("right", str(1)),
			req("cardinality", enum("one_to_one", "one_to_many", "many_to_one", "many_to_many")),
		), 0)),
	)...)
)

func mapping(v any, path string) error {
	if err := mappingLeaf(v, path); err == nil {
		return nil
	}
	if err := record(str(0), mapping)(v, path); err != nil {
		return issue(path, "invalid mapping")
	}
	return nil
}

func artifactRules() map[ArtifactKind]rule {
	return map[ArtifactKind]rule{
		KindSource:        sourceFileRule,
		KindMetric:        metricRule,
		KindWorkflow:      workflowRule,
		KindWatcher:       watcherRule,
		KindAction:        union(legacyActionRule, apiActionRule, magentoActionRule),
		KindPolicy:        policyRule,
		KindRelationships: relationshipsRule,
	}
}

// ValidateArtifactContent is validateArtifactContent().
func ValidateArtifactContent(kind ArtifactKind, content any, path string) error {
	if r, ok := artifactRules()[kind]; ok {
		if err := r(content, ""); err != nil {
			return fmt.Errorf("invalid %s artifact %s: %w", kind, path, err)
		}
	}
	if kind == KindSpec {
		spec, ok := content.(map[string]any)
		_, openapi := spec["openapi"].(string)
		if !ok || !openapi || !truthy(spec["paths"]) {
			return fmt.Errorf("invalid OpenAPI artifact %s", path)
		}
	}
	return nil
}

func truthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case string:
		return t != ""
	case float64:
		return t != 0
	}
	return true
}
