package webfront

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/open-neko/neko/apps/openneko/internal/packs"
	"github.com/open-neko/neko/apps/openneko/internal/secrets"
)

const magentoMaxResponseBytes = 2 * 1024 * 1024

type magentoOperation struct {
	OperationID       string   `json:"operationId"`
	MutationRoot      string   `json:"mutationRoot"`
	ReadPath          string   `json:"readPath"`
	BodyKey           string   `json:"bodyKey"`
	InverseOperation  string   `json:"inverseOperation"`
	EntityIDField     string   `json:"entityIdField"`
	DefaultClass      int      `json:"defaultClass"`
	EntityType        string   `json:"entityType"`
	Reversible        bool     `json:"reversible"`
	NumericPathParams []string `json:"numericPathParams"`
	ResultMode        string   `json:"resultMode"`
}

type magentoActionDefinition struct {
	Kind    string `json:"kind"`
	Domain  string `json:"domain"`
	Adapter struct {
		Kind       string                      `json:"kind"`
		Operations map[string]magentoOperation `json:"operations"`
	} `json:"adapter"`
}

type magentoRuntime struct {
	baseURL, storeCode, graphjinEndpoint, token string
}

type magentoChangeRow struct {
	entityRef string
	path      *jsObj
	query     *jsObj
	body      any
}

type magentoChangePayload struct {
	operation         string
	scope             *jsObj
	rows              []magentoChangeRow
	idempotencyKey    string
	projectedExposure *float64
	autoRuleID        string
}

type magentoStoredRow struct {
	id, entityRef, operationID string
	path, query                *jsObj
	before, after, expected    *jsObj
	status                     string
	reconciled                 *jsObj
}

type magentoStoreControl struct {
	caps        map[string]float64
	autoExecute bool
}

func jsIsRecord(v any) bool {
	o, ok := v.(*jsObj)
	return ok && o != nil
}

func asJSObj(v any) *jsObj {
	if o, ok := v.(*jsObj); ok && o != nil {
		return o
	}
	return nil
}

func emptyJSObj() *jsObj { return &jsObj{vals: map[string]any{}} }

func jsonbArg(v any) any {
	if v == nil {
		return nil
	}
	if o, ok := v.(*jsObj); ok && o == nil {
		return nil
	}
	return jsStringify(v, false)
}

func jsonbObj(raw []byte) *jsObj {
	if parsed, err := jsParse(raw); err == nil {
		if o := asJSObj(parsed); o != nil {
			return o
		}
	}
	return nil
}

// magentoNumber is Number(v) for a JSON value.
func magentoNumber(v any) float64 {
	switch t := v.(type) {
	case nil:
		return 0
	case float64:
		return t
	case bool:
		if t {
			return 1
		}
		return 0
	case string:
		if f, ok := jsNumber(t); ok {
			return f
		}
	}
	return math.NaN()
}

func isSafeInteger(f float64) bool {
	return !math.IsNaN(f) && !math.IsInf(f, 0) && f == math.Trunc(f) && math.Abs(f) <= 9007199254740991
}

// jsStrictEqual is === for JSON values.
func magentoStrictEqual(x, y any) bool {
	switch a := x.(type) {
	case nil:
		return y == nil
	case jsUndefined:
		_, ok := y.(jsUndefined)
		return ok
	case float64:
		b, ok := y.(float64)
		return ok && a == b
	case string:
		b, ok := y.(string)
		return ok && a == b
	case bool:
		b, ok := y.(bool)
		return ok && a == b
	case *jsObj:
		b, ok := y.(*jsObj)
		return ok && a == b
	}
	return false
}

func (a *API) magentoActionDefinitions(ctx context.Context, orgID, kind string) ([]magentoActionDefinition, error) {
	query := `select definition from pack_action_definition where org_id = $1 and enabled`
	args := []any{orgID}
	if kind != "" {
		query += ` and kind = $2 limit 1`
		args = append(args, kind)
	}
	rows, err := a.DB.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	raws, err := pgx.CollectRows(rows, pgx.RowTo[[]byte])
	if err != nil {
		return nil, err
	}
	var out []magentoActionDefinition
	for _, raw := range raws {
		var probe struct {
			Kind    any `json:"kind"`
			Domain  any `json:"domain"`
			Adapter any `json:"adapter"`
		}
		if json.Unmarshal(raw, &probe) != nil {
			continue
		}
		adapter, _ := probe.Adapter.(map[string]any)
		_, kindOK := probe.Kind.(string)
		_, domainOK := probe.Domain.(string)
		if _, adapterOK := adapter["kind"].(string); !kindOK || !domainOK || !adapterOK {
			continue
		}
		var def magentoActionDefinition
		if json.Unmarshal(raw, &def) != nil {
			continue
		}
		out = append(out, def)
	}
	return out, nil
}

func (a *API) magentoOperationAcrossDefinitions(ctx context.Context, orgID, name string) (*magentoActionDefinition, *magentoOperation, error) {
	defs, err := a.magentoActionDefinitions(ctx, orgID, "")
	if err != nil {
		return nil, nil, err
	}
	for i := range defs {
		if op, ok := defs[i].Adapter.Operations[name]; ok {
			return &defs[i], &op, nil
		}
	}
	return nil, nil, fmt.Errorf("Magento operation %s is not installed", name)
}

func magentoGraphjinEndpoint(raw string) string {
	clean := strings.TrimRight(raw, "/")
	if strings.HasSuffix(clean, "/api/v1/graphql") {
		return clean
	}
	return clean + "/api/v1/graphql"
}

func magentoConfigText(v any, fallback string) string {
	if v == nil {
		return fallback
	}
	return jsTemplate(jsValueOf(v))
}

func (a *API) loadMagentoRuntime(ctx context.Context, orgID string) (*magentoRuntime, error) {
	var rawConfig []byte
	err := a.DB.QueryRow(ctx, `
		select config from pack_install
		 where org_id = $1 and pack_id = 'magento' and status = 'installed'
		 order by created_at desc limit 1`, orgID).Scan(&rawConfig)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errors.New("Magento pack is not installed")
	}
	if err != nil {
		return nil, err
	}
	config := map[string]any{}
	_ = json.Unmarshal(rawConfig, &config)
	if config == nil {
		config = map[string]any{}
	}
	var graphqlURL string
	runtime, _ := config["_runtime"].(map[string]any)
	if selection, _ := runtime["source"].(map[string]any); selection != nil {
		source, err := a.resolveSelectedPackSource(ctx, orgID, selection)
		if err != nil {
			return nil, err
		}
		graphqlURL = source.GraphqlURL
	} else {
		var fallback *string
		err := a.DB.QueryRow(ctx, `
			select graphql_url from data_source where org_id = $1 and enabled
			 order by is_default desc, created_at limit 1`, orgID).Scan(&fallback)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		graphqlURL = deref(fallback)
	}
	if graphqlURL == "" {
		return nil, errors.New("Magento GraphJin source is unavailable")
	}
	store, err := secrets.Read(a.SecretsDir)
	if err != nil {
		return nil, err
	}
	token := store["pack.magento"]["MAGENTO_INTEGRATION_TOKEN"]
	if token == "" {
		return nil, errors.New("Magento integration token is unavailable")
	}
	return &magentoRuntime{
		baseURL:          strings.TrimRight(magentoConfigText(config["magento.base_url"], ""), "/"),
		storeCode:        magentoConfigText(config["magento.store_code"], "all"),
		graphjinEndpoint: magentoGraphjinEndpoint(graphqlURL),
		token:            token,
	}, nil
}

var magentoPathParam = regexp.MustCompile(`\{([^}]+)\}`)

func magentoReadURL(rt *magentoRuntime, template string, path *jsObj) (string, error) {
	var missing []string
	resolved := magentoPathParam.ReplaceAllStringFunc(template, func(m string) string {
		key := m[1 : len(m)-1]
		v := jsLookup(path, key)
		_, undef := v.(jsUndefined)
		if v == nil || undef || strings.TrimSpace(jsTemplate(v)) == "" {
			if !slices.Contains(missing, key) {
				missing = append(missing, key)
			}
			return ""
		}
		return encodeURIComponent(jsTemplate(v))
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("Magento read path is missing %s", strings.Join(missing, ", "))
	}
	return rt.baseURL + "/rest/" + encodeURIComponent(rt.storeCode) + resolved, nil
}

func magentoSearchParams(query *jsObj) string {
	var parts []string
	if query == nil {
		return ""
	}
	for _, key := range query.ordered() {
		value := query.vals[key]
		if value == nil {
			continue
		}
		if list, ok := value.([]any); ok {
			for _, item := range list {
				parts = append(parts, url.QueryEscape(key)+"="+url.QueryEscape(jsTemplate(item)))
			}
			continue
		}
		parts = append(parts, url.QueryEscape(key)+"="+url.QueryEscape(jsTemplate(value)))
	}
	return strings.Join(parts, "&")
}

func magentoGet(ctx context.Context, rt *magentoRuntime, target string, timeout time.Duration) (int, any, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("authorization", "Bearer "+rt.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("fetch failed: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, magentoMaxResponseBytes+1))
	if err != nil {
		return 0, nil, err
	}
	if len(raw) > magentoMaxResponseBytes {
		return 0, nil, errors.New("Magento response exceeded 2 MiB")
	}
	if len(raw) == 0 {
		return resp.StatusCode, nil, nil
	}
	if parsed, err := jsParse(raw); err == nil {
		return resp.StatusCode, parsed, nil
	}
	return resp.StatusCode, string(raw), nil
}

func assertMagentoOrderUpdateIdentity(path *jsObj, body any) error {
	entity := jsLookup(body, "entity")
	id := magentoNumber(jsLookup(path, "id"))
	if _, undef := jsLookup(path, "id").(jsUndefined); undef {
		id = math.NaN()
	}
	if !isSafeInteger(id) || id < 1 || !jsIsRecord(entity) || !magentoStrictEqual(jsLookup(entity, "entity_id"), id) {
		return errors.New("Magento order update requires entity.entity_id to match the positive order path id")
	}
	return nil
}

func magentoExpectedAfterImage(op *magentoOperation, body any) any {
	if op.BodyKey != "" && jsIsRecord(body) {
		if v, ok := asJSObj(body).vals[op.BodyKey]; ok {
			return v
		}
	}
	return body
}

// readMagentoEntity reads the current image of the entity a row changes. A
// nil result means Magento holds no such entity.
func readMagentoEntity(ctx context.Context, rt *magentoRuntime, op *magentoOperation, path, query *jsObj, body any) (*jsObj, error) {
	if op.OperationID == "magentoUpdateOrder" {
		if err := assertMagentoOrderUpdateIdentity(path, body); err != nil {
			return nil, err
		}
	}
	if op.ReadPath == "" {
		return nil, nil
	}
	target, err := magentoReadURL(rt, op.ReadPath, path)
	if err != nil {
		return nil, err
	}
	var sourceItem *jsObj
	if op.EntityType == "source_item" {
		items, _ := magentoExpectedAfterImage(op, body).([]any)
		var item *jsObj
		if len(items) == 1 {
			item = asJSObj(items[0])
		}
		sku, _ := jsLookup(item, "sku").(string)
		code, _ := jsLookup(item, "source_code").(string)
		if item == nil || strings.TrimSpace(sku) == "" || strings.TrimSpace(code) == "" {
			return nil, errors.New("Magento inventory rows require exactly one source item with sku and source_code")
		}
		sourceItem = item
		query = emptyJSObj()
		for index, field := range []string{"sku", "source_code"} {
			prefix := fmt.Sprintf("searchCriteria[filter_groups][%d][filters][0]", index)
			query.set(prefix+"[field]", field)
			query.set(prefix+"[value]", item.vals[field])
			query.set(prefix+"[condition_type]", "eq")
		}
	}
	if params := magentoSearchParams(query); params != "" {
		target += "?" + params
	}
	status, reply, err := magentoGet(ctx, rt, target, 15*time.Second)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNotFound {
		if op.OperationID == "magentoUpdateOrder" {
			return nil, errors.New("Magento order update requires an existing order")
		}
		return nil, nil
	}
	if status < 200 || status > 299 {
		return nil, fmt.Errorf("Magento precondition read failed: HTTP %d", status)
	}
	if sourceItem != nil {
		items, ok := jsLookup(reply, "items").([]any)
		if !jsIsRecord(reply) || !ok {
			return nil, errors.New("Magento source-item read returned an invalid collection")
		}
		mismatch := len(items) > 1
		for _, it := range items {
			if !jsIsRecord(it) || !magentoStrictEqual(jsLookup(it, "sku"), sourceItem.vals["sku"]) ||
				!magentoStrictEqual(jsLookup(it, "source_code"), sourceItem.vals["source_code"]) {
				mismatch = true
			}
		}
		if mismatch {
			return nil, errors.New("Magento source-item read did not uniquely match the requested SKU and source")
		}
		if len(items) == 0 {
			return nil, nil
		}
		return asJSObj(items[0]), nil
	}
	if o := asJSObj(reply); o != nil {
		return o, nil
	}
	return jsObjOf("value", reply), nil
}

func magentoCompareCanonical(x, y any) bool { return jsCanonical(x) == jsCanonical(y) }

func magentoDeepContains(actual, expected any) bool {
	if list, ok := expected.([]any); ok {
		got, isList := actual.([]any)
		if !isList {
			return false
		}
		for i, v := range list {
			var item any = jsUndefined{}
			if i < len(got) {
				item = got[i]
			}
			if !magentoDeepContains(item, v) {
				return false
			}
		}
		return true
	}
	if obj := asJSObj(expected); obj != nil {
		if !jsIsRecord(actual) {
			return false
		}
		for _, k := range obj.ordered() {
			if !magentoDeepContains(jsLookup(actual, k), obj.vals[k]) {
				return false
			}
		}
		return true
	}
	return magentoStrictEqual(actual, expected) || jsTemplate(actual) == jsTemplate(expected)
}

func magentoStringField(value any, field string) (string, bool) {
	if list, ok := value.([]any); ok {
		for _, child := range list {
			if found, ok := magentoStringField(child, field); ok {
				return found, true
			}
		}
		return "", false
	}
	obj := asJSObj(value)
	if obj == nil {
		return "", false
	}
	if s, ok := obj.vals[field].(string); ok {
		return s, true
	}
	for _, k := range obj.ordered() {
		if found, ok := magentoStringField(obj.vals[k], field); ok {
			return found, true
		}
	}
	return "", false
}

func isMagentoCreate(op *magentoOperation) bool { return strings.Contains(op.OperationID, "Create") }
func isMagentoDelete(op *magentoOperation) bool { return strings.Contains(op.OperationID, "Delete") }

// magentoReconciliationConfirmed matches reconciliationConfirmed() in
// apps/worker/src/packs/magento-v2-runtime.ts.
func magentoReconciliationConfirmed(op *magentoOperation, reconciled *jsObj, expected any) bool {
	if op.ReadPath == "" {
		return true
	}
	if isMagentoDelete(op) {
		return reconciled == nil
	}
	if reconciled == nil {
		return false
	}
	exp := asJSObj(expected)
	if op.OperationID == "magentoMoveCategory" && exp != nil {
		return magentoStrictEqual(jsLookup(reconciled, "parent_id"), jsLookup(exp, "parentId")) &&
			magentoStrictEqual(jsLookup(reconciled, "position"), jsLookup(exp, "position"))
	}
	if op.EntityType == "sales_rule" && exp != nil {
		next := cloneJSObj(exp)
		if v, ok := exp.vals["coupon_type"]; ok {
			couponTypes := map[string]string{"1": "NO_COUPON", "2": "SPECIFIC_COUPON", "3": "AUTO"}
			if mapped, ok := couponTypes[jsTemplate(v)]; ok {
				next.set("coupon_type", mapped)
			}
		}
		expected, exp = next, next
	}
	if op.EntityType == "customer" && exp != nil {
		next := emptyJSObj()
		for _, k := range exp.ordered() {
			if k != "updated_at" {
				next.set(k, exp.vals[k])
			}
		}
		expected, exp = next, next
	}
	if (op.OperationID == "magentoCreateInvoice" || op.OperationID == "magentoCreateShipment") && exp != nil {
		items, ok := reconciled.vals["items"].([]any)
		if !magentoStrictEqual(jsLookup(reconciled, "order_id"), jsLookup(exp, "order_id")) || !ok || len(items) == 0 {
			return false
		}
		want, present := exp.vals["items"]
		if !present {
			return true
		}
		wantList, ok := want.([]any)
		if !ok || len(wantList) == 0 {
			return false
		}
		for _, w := range wantList {
			wo := asJSObj(w)
			if wo == nil {
				return false
			}
			found := false
			for _, got := range items {
				if jsIsRecord(got) && magentoStrictEqual(jsLookup(got, "order_item_id"), jsLookup(wo, "order_item_id")) &&
					magentoStrictEqual(jsLookup(got, "qty"), jsLookup(wo, "qty")) {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
		return true
	}
	switch op.OperationID {
	case "magentoAddOrderComment":
		comment, _ := magentoStringField(expected, "comment")
		histories, _ := reconciled.vals["status_histories"].([]any)
		if comment == "" {
			return false
		}
		for _, h := range histories {
			if got, ok := magentoStringField(h, "comment"); ok && got == comment {
				return true
			}
		}
		return false
	case "magentoHoldOrder":
		return magentoStrictEqual(jsLookup(reconciled, "state"), "holded")
	case "magentoUnholdOrder":
		return !magentoStrictEqual(jsLookup(reconciled, "state"), "holded")
	case "magentoCancelOrder":
		return magentoStrictEqual(jsLookup(reconciled, "state"), "canceled") || magentoStrictEqual(jsLookup(reconciled, "status"), "canceled")
	}
	if list, ok := expected.([]any); ok && op.EntityType == "source_item" {
		for _, item := range list {
			if !magentoDeepContains(reconciled, item) {
				return false
			}
		}
		return true
	}
	return magentoDeepContains(reconciled, expected)
}

func magentoResponseEntityID(value any, field string) any {
	if list, ok := value.([]any); ok {
		for _, child := range list {
			if found := magentoResponseEntityID(child, field); found != nil {
				return found
			}
		}
		return nil
	}
	obj := asJSObj(value)
	if obj == nil {
		return nil
	}
	switch direct := obj.vals[field].(type) {
	case string, float64:
		return direct
	}
	for _, k := range obj.ordered() {
		if found := magentoResponseEntityID(obj.vals[k], field); found != nil {
			return found
		}
	}
	return nil
}

func magentoReconciledPath(op *magentoOperation, path *jsObj, response any) *jsObj {
	if op.ReadPath == "" {
		return path
	}
	next := cloneJSObj(path)
	for _, m := range magentoPathParam.FindAllStringSubmatch(op.ReadPath, -1) {
		key := m[1]
		if v, ok := next.vals[key]; ok && v != nil && strings.TrimSpace(jsTemplate(v)) != "" {
			continue
		}
		field := op.EntityIDField
		if field == "" {
			field = key
		}
		if value := magentoResponseEntityID(response, field); value != nil {
			next.set(key, value)
		}
	}
	return next
}

func magentoNormalizePath(op *magentoOperation, path *jsObj, storeCode string) (*jsObj, error) {
	out := jsObjOf("storeCode", storeCode)
	if path != nil {
		for _, k := range path.ordered() {
			out.set(k, path.vals[k])
		}
	}
	for _, k := range out.ordered() {
		if !slices.Contains(op.NumericPathParams, k) {
			continue
		}
		n := magentoNumber(out.vals[k])
		if !isSafeInteger(n) || n < 1 {
			return nil, fmt.Errorf("Magento path parameter %s must be a positive integer", k)
		}
		out.vals[k] = n
	}
	return out, nil
}

func magentoProjectBeforeImage(before, requested any) any {
	if _, ok := requested.([]any); ok {
		return before
	}
	req, bef := asJSObj(requested), asJSObj(before)
	if req == nil || bef == nil {
		return before
	}
	projected := emptyJSObj()
	for _, k := range req.ordered() {
		if v, ok := bef.vals[k]; ok {
			projected.set(k, magentoProjectBeforeImage(v, req.vals[k]))
		}
	}
	return projected
}

func magentoInverseBody(inverse, original *magentoOperation, before, originalAfter *jsObj) *jsObj {
	if strings.Contains(inverse.OperationID, "Delete") {
		return emptyJSObj()
	}
	requested := magentoExpectedAfterImage(original, originalAfter)
	var projected any
	if _, ok := requested.([]any); ok {
		projected = []any{before}
	} else if r := asJSObj(requested); r != nil && len(r.vals) > 0 {
		projected = magentoProjectBeforeImage(before, requested)
	} else {
		projected = before
	}
	if inverse.BodyKey != "" {
		return jsObjOf(inverse.BodyKey, projected)
	}
	if o := asJSObj(projected); o != nil {
		return o
	}
	return before
}

var magentoFieldName = regexp.MustCompile(`^[_A-Za-z][_0-9A-Za-z]*$`)

// magentoGraphjinMutationField is the expose_as alias GraphJin gives the
// namespaced mutation root of a pack definition.
func magentoGraphjinMutationField(root string) (string, error) {
	field := strings.TrimPrefix(root, "magento_operator_v2_")
	if field == root || !magentoFieldName.MatchString(field) {
		return "", errors.New("invalid Magento V2 mutation root")
	}
	return field, nil
}

func isTerminalMagentoBulkStatus(status float64) bool {
	return status == 1 || status == 2 || status == 3 || status == 5
}

func magentoBulkUUID(value any) string {
	if obj := asJSObj(value); obj != nil {
		for _, k := range []string{"bulk_uuid", "bulkUuid", "uuid"} {
			if s, ok := obj.vals[k].(string); ok && s != "" {
				return s
			}
		}
	}
	if s, ok := value.(string); ok && utf16Len(s) >= 8 {
		return strings.TrimSuffix(strings.TrimPrefix(s, `"`), `"`)
	}
	return ""
}

func magentoBulkOperations(value any) []*jsObj {
	obj := asJSObj(value)
	if obj == nil {
		return nil
	}
	list, ok := obj.vals["operations_list"].([]any)
	if !ok {
		list, _ = obj.vals["operations"].([]any)
	}
	var out []*jsObj
	for _, item := range list {
		if o := asJSObj(item); o != nil {
			out = append(out, o)
		}
	}
	return out
}

func magentoBulkStatus(op *jsObj) float64 {
	v := jsLookup(op, "status")
	if _, undef := v.(jsUndefined); undef || v == nil {
		v = jsLookup(op, "operation_status")
	}
	if _, undef := v.(jsUndefined); undef || v == nil {
		return -1
	}
	return magentoNumber(v)
}

type magentoMutationResult struct {
	requestID string
	response  any
}

func magentoActionUser(req *actionRequestRecord) string {
	if req.ApprovedBy != nil {
		return *req.ApprovedBy
	}
	if req.ActorUserID != nil {
		return *req.ActorUserID
	}
	return "magento-executor"
}

func (a *API) executeMagentoMutation(ctx context.Context, req *actionRequestRecord, rt *magentoRuntime, op *magentoOperation, path *jsObj, body any, role string) (*magentoMutationResult, error) {
	orderUpdate := op.OperationID == "magentoUpdateOrder"
	if orderUpdate {
		if err := assertMagentoOrderUpdateIdentity(path, body); err != nil {
			return nil, err
		}
	}
	field, err := magentoGraphjinMutationField(op.MutationRoot)
	if err != nil {
		return nil, err
	}
	callPath := path
	if orderUpdate {
		callPath = emptyJSObj()
	}
	normalized, err := magentoNormalizePath(op, callPath, rt.storeCode)
	if err != nil {
		return nil, err
	}
	call := jsObjOf("path", normalized)
	if body != nil {
		if o := asJSObj(body); o == nil || len(o.vals) > 0 {
			call.set("body", body)
		}
	}
	token, err := a.mintGraphjinToken(graphjinClaims{orgID: req.OrgID, userID: magentoActionUser(req), role: role}, 60*time.Second)
	if err != nil {
		return nil, err
	}
	query := fmt.Sprintf("mutation ExecuteMagentoV2($call: JSON!) { %s(call: $call) { ok status_code operation_id request_id response_json } }", field)
	data, messages, err := a.packActionGraphjinQuery(ctx, rt.graphjinEndpoint, role, token, nil, query, jsObjOf("call", call))
	if err != nil {
		return nil, err
	}
	resp := asJSObj(jsLookup(data, field))
	if len(messages) > 0 || resp == nil {
		if len(messages) > 0 {
			return nil, errors.New(strings.Join(messages, "; "))
		}
		return nil, errors.New("Magento mutation returned no result")
	}
	statusLabel := "unknown"
	status := 500.0
	if v := resp.vals["status_code"]; v != nil {
		statusLabel = jsTemplate(v)
		status = magentoNumber(v)
	}
	if resp.vals["ok"] != true || status >= 400 {
		return nil, fmt.Errorf("Magento mutation failed with HTTP %s", statusLabel)
	}
	requestID, _ := resp.vals["request_id"].(string)
	return &magentoMutationResult{requestID: requestID, response: parseResponseJSON(resp.vals["response_json"])}, nil
}

func waitForMagentoBulk(ctx context.Context, rt *magentoRuntime, uuid string, limit, pause time.Duration) (any, error) {
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		target := rt.baseURL + "/rest/" + encodeURIComponent(rt.storeCode) + "/V1/bulk/" + encodeURIComponent(uuid) + "/status"
		status, last, err := magentoGet(ctx, rt, target, 10*time.Second)
		if err != nil {
			return nil, err
		}
		if status < 200 || status > 299 {
			return nil, fmt.Errorf("Magento bulk status failed: HTTP %d", status)
		}
		if ops := magentoBulkOperations(last); len(ops) > 0 {
			done := true
			for _, op := range ops {
				if !isTerminalMagentoBulkStatus(magentoBulkStatus(op)) {
					done = false
				}
			}
			if done {
				return last, nil
			}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(pause):
		}
	}
	return nil, errors.New("Magento bulk operation did not reach a terminal state within 90 seconds")
}

func (a *API) magentoIndexerReceipt(ctx context.Context, rt *magentoRuntime, orgID string) any {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	token, err := a.mintGraphjinToken(graphjinClaims{orgID: orgID, userID: "magento-reconciliation", role: "service"}, 60*time.Second)
	if err != nil {
		return []any{}
	}
	data, _, err := a.packActionGraphjinQuery(ctx, rt.graphjinEndpoint, "", token, nil,
		"query MagentoIndexerReceipt { indexer_state { indexer_id status updated } }", emptyJSObj())
	if err != nil {
		return []any{}
	}
	if list, ok := jsLookup(data, "indexer_state").([]any); ok {
		return list
	}
	return []any{}
}

func (a *API) magentoDomainControl(ctx context.Context, orgID, domain string) (*magentoStoreControl, error) {
	var enabled, autoExecute bool
	var rawCaps []byte
	err := a.DB.QueryRow(ctx, `select enabled, auto_execute, caps from magento_store_control where org_id = $1 and domain = $2 limit 1`,
		orgID, domain).Scan(&enabled, &autoExecute, &rawCaps)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if err != nil || !enabled {
		return nil, fmt.Errorf("Magento %s changes are disabled by the administrator", domain)
	}
	control := &magentoStoreControl{caps: map[string]float64{}, autoExecute: autoExecute}
	var caps map[string]any
	_ = json.Unmarshal(rawCaps, &caps)
	for k, v := range caps {
		if f, ok := v.(float64); ok {
			control.caps[k] = f
		}
	}
	return control, nil
}

func (a *API) hasHumanActionApproval(ctx context.Context, req *actionRequestRecord) (bool, error) {
	if req.ApprovedBy != nil {
		return true, nil
	}
	if req.ApprovedAt == nil {
		return false, nil
	}
	var solo bool
	err := a.DB.QueryRow(ctx, `
		select exists (
		  select 1 from audit_chain
		   where org_id = $1 and entity_kind = 'action_request' and entity_id = $2
		     and event = 'approved' and payload->'soloOperator' = 'true'::jsonb)`,
		req.OrgID, req.ID).Scan(&solo)
	return solo, err
}

func parseMagentoChangePayload(p *jsObj) (*magentoChangePayload, error) {
	operation, _ := p.vals["operation"].(string)
	operation = strings.TrimSpace(operation)
	key, _ := p.vals["idempotency_key"].(string)
	key = strings.TrimSpace(key)
	if operation == "" {
		return nil, errors.New("Magento change needs an operation")
	}
	scope := asJSObj(p.vals["scope"])
	if scope == nil || len(scope.vals) == 0 {
		return nil, errors.New("Magento change needs a named store, website, or source scope")
	}
	rawRows, ok := p.vals["rows"].([]any)
	if !ok || len(rawRows) == 0 {
		return nil, errors.New("Magento change needs at least one row")
	}
	if n := utf16Len(key); n < 8 || n > 200 {
		return nil, errors.New("Magento change needs an idempotency_key between 8 and 200 characters")
	}
	out := &magentoChangePayload{operation: operation, scope: scope, idempotencyKey: key}
	for i, raw := range rawRows {
		item := asJSObj(raw)
		if item == nil {
			return nil, fmt.Errorf("Magento row %d must be an object", i+1)
		}
		ref, _ := item.vals["entity_ref"].(string)
		ref = strings.TrimSpace(ref)
		if ref == "" {
			return nil, fmt.Errorf("Magento row %d needs entity_ref", i+1)
		}
		row := magentoChangeRow{entityRef: ref, path: emptyJSObj(), query: emptyJSObj(), body: emptyJSObj()}
		if v, ok := item.vals["path"]; ok {
			if row.path = asJSObj(v); row.path == nil {
				return nil, fmt.Errorf("Magento row %d path must be an object", i+1)
			}
		}
		if v, ok := item.vals["query"]; ok {
			if row.query = asJSObj(v); row.query == nil {
				return nil, fmt.Errorf("Magento row %d query must be an object", i+1)
			}
		}
		if v, ok := item.vals["body"]; ok {
			_, isList := v.([]any)
			if !jsIsRecord(v) && !isList {
				return nil, fmt.Errorf("Magento row %d body must be an object or array", i+1)
			}
			row.body = v
		}
		out.rows = append(out.rows, row)
	}
	if f, ok := p.vals["projected_exposure"].(float64); ok {
		out.projectedExposure = &f
	}
	out.autoRuleID, _ = p.vals["auto_rule_id"].(string)
	return out, nil
}

type magentoAutoCheck struct {
	autoExecution    bool
	actionsToday     int
	cooldownBreaches []string
}

func (a *API) assertMagentoAutoRule(ctx context.Context, req *actionRequestRecord, payload *magentoChangePayload, domain string) (*magentoAutoCheck, error) {
	if payload.autoRuleID == "" {
		return &magentoAutoCheck{}, nil
	}
	var ruleID, ruleDomain, actionKind string
	var enabled bool
	var dailyCap, cooldown int
	err := a.DB.QueryRow(ctx, `
		select id::text, enabled, domain, action_kind, daily_cap, cooldown_seconds
		  from magento_auto_rule where id::text = $1 and org_id = $2 limit 1`,
		payload.autoRuleID, req.OrgID).Scan(&ruleID, &enabled, &ruleDomain, &actionKind, &dailyCap, &cooldown)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if err != nil || !enabled || ruleDomain != domain || actionKind != req.Kind {
		return nil, errors.New("Magento automatic rule is unavailable for this change")
	}
	var domainCount, ruleCount int
	if err := a.DB.QueryRow(ctx, `
		select count(*)::int,
		       (count(*) filter (where cap_snapshot->>'autoRuleId' = $2))::int
		  from action_changeset
		 where org_id = $1
		   and (cap_snapshot->>'autoExecution')::boolean is true
		   and created_at >= date_trunc('day', now())`, req.OrgID, ruleID).Scan(&domainCount, &ruleCount); err != nil {
		return nil, err
	}
	check := &magentoAutoCheck{autoExecution: true, actionsToday: domainCount, cooldownBreaches: []string{}}
	if cooldown > 0 && len(payload.rows) > 0 {
		refs := make([]string, len(payload.rows))
		for i, r := range payload.rows {
			refs[i] = r.entityRef
		}
		rows, err := a.DB.Query(ctx, `
			select distinct r.entity_ref
			  from action_changeset_row r
			  join action_changeset c on c.id = r.changeset_id
			 where c.org_id = $1 and r.entity_ref = any($2::text[]) and r.status = 'applied'
			   and r.finished_at >= now() - ($3 * interval '1 second')`, req.OrgID, refs, cooldown)
		if err != nil {
			return nil, err
		}
		breaches, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return nil, err
		}
		check.cooldownBreaches = append(check.cooldownBreaches, breaches...)
	}
	if ruleCount >= dailyCap {
		if _, err := a.DB.Exec(ctx, `update magento_auto_rule set enabled = false, suspended_reason = 'daily_cap_exhausted', updated_at = now() where id::text = $1`, ruleID); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("Magento automatic rule daily cap of %d is exhausted", dailyCap)
	}
	return check, nil
}

func (a *API) magentoClassifications(ctx context.Context, orgID, domain, entityType string) ([]packs.MagentoAttributeClassification, error) {
	rows, err := a.DB.Query(ctx, `
		select domain, entity_type, attribute, risk_class, category, rationale
		  from magento_attribute_classification
		 where org_id = $1 and domain = $2 and entity_type = $3`, orgID, domain, entityType)
	if err != nil {
		return nil, err
	}
	stored, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (packs.MagentoAttributeClassification, error) {
		var c packs.MagentoAttributeClassification
		var d, cat string
		var risk int
		err := row.Scan(&d, &c.EntityType, &c.Attribute, &risk, &cat, &c.Rationale)
		c.Domain, c.Category, c.RiskClass = packs.MagentoDomain(d), packs.MagentoAttributeCategory(cat), packs.MagentoRiskClass(risk)
		return c, err
	})
	if err != nil {
		return nil, err
	}
	if len(stored) == 0 {
		return packs.DefaultMagentoAttributeClassifications, nil
	}
	return stored, nil
}

func jsHash(v any) string { return packs.SHA256([]byte(jsCanonical(v))) }

func (a *API) autoApprovePreparedActionRequest(ctx context.Context, req *actionRequestRecord, reason string) error {
	if req.Status != "draft" && req.Status != "pending_approval" {
		return fmt.Errorf("invalid action_request transition: %s → approved", req.Status)
	}
	var approvedAt time.Time
	if err := a.DB.QueryRow(ctx, `
		update action_request
		   set status = 'approved', approved_by_user_id = null, approved_at = now(), updated_at = now()
		 where org_id = $1 and id::text = $2
		 returning approved_at`, req.OrgID, req.ID).Scan(&approvedAt); err != nil {
		return err
	}
	req.Status, req.ApprovedBy, req.ApprovedAt = "approved", nil, &approvedAt
	a.recordAudit(ctx, auditEvent{
		OrgID:      req.OrgID,
		EntityKind: "action_request",
		EntityID:   req.ID,
		Event:      "auto_approved:prepared",
		Payload:    map[string]any{"kind": req.Kind, "reason": reason},
	})
	return nil
}

func (a *API) setActionRequestPayload(ctx context.Context, req *actionRequestRecord, payload *jsObj) error {
	if err := a.updateActionRequestPayload(ctx, req.OrgID, req.ID, req.Kind, payload); err != nil {
		return err
	}
	req.Payload = payload
	return nil
}

// prepareMagentoChangeset matches prepareChangeset() in
// apps/worker/src/packs/magento-v2-runtime.ts: it reads the before-images,
// classifies and caps the change, and stores the change-set preview.
func (a *API) prepareMagentoChangeset(ctx context.Context, req *actionRequestRecord, def *magentoActionDefinition) error {
	payload, err := parseMagentoChangePayload(req.Payload)
	if err != nil {
		return err
	}
	op, ok := def.Adapter.Operations[payload.operation]
	if !ok {
		return fmt.Errorf("Magento operation %s is not allowed for %s", payload.operation, req.Kind)
	}
	control, err := a.magentoDomainControl(ctx, req.OrgID, def.Domain)
	if err != nil {
		return err
	}
	rt, err := a.loadMagentoRuntime(ctx, req.OrgID)
	if err != nil {
		return err
	}
	classifications, err := a.magentoClassifications(ctx, req.OrgID, def.Domain, op.EntityType)
	if err != nil {
		return err
	}
	type preparedRow struct {
		input          magentoChangeRow
		before         *jsObj
		classification packs.MagentoChangeClassification
	}
	prepared := make([]preparedRow, 0, len(payload.rows))
	for _, row := range payload.rows {
		before := emptyJSObj()
		if !isMagentoCreate(&op) {
			read, err := readMagentoEntity(ctx, rt, &op, row.path, row.query, row.body)
			if err != nil {
				return err
			}
			if read != nil {
				before = read
			}
		}
		classification := packs.ClassifyMagentoChange(packs.ClassifyMagentoInput{
			Domain:          packs.MagentoDomain(def.Domain),
			EntityType:      op.EntityType,
			OperationID:     op.OperationID,
			DefaultClass:    packs.MagentoRiskClass(op.DefaultClass),
			Body:            plainJSON(row.body),
			Classifications: classifications,
		})
		if classification.RiskClass == 0 {
			return fmt.Errorf("Magento %s cannot be performed by OpenNeko; complete it in Magento Admin", op.OperationID)
		}
		prepared = append(prepared, preparedRow{input: row, before: before, classification: classification})
	}
	classifiedRisk := packs.MagentoRiskClass(op.DefaultClass)
	for _, row := range prepared {
		classifiedRisk = min(classifiedRisk, row.classification.RiskClass)
	}
	auto, err := a.assertMagentoAutoRule(ctx, req, payload, def.Domain)
	if err != nil {
		return err
	}
	capRows := make([]packs.MagentoChangeRow, len(prepared))
	for i, row := range prepared {
		capRows[i] = packs.MagentoChangeRow{BeforeImage: plainJSON(row.before), AfterImage: plainJSON(row.input.body)}
	}
	caps := packs.EvaluateMagentoCaps(packs.EvaluateMagentoCapsInput{
		Domain:            packs.MagentoDomain(def.Domain),
		OperationID:       op.OperationID,
		RiskClass:         classifiedRisk,
		Rows:              capRows,
		Caps:              control.caps,
		ProjectedExposure: payload.projectedExposure,
		AutoExecution:     auto.autoExecution,
		AutoActionsToday:  auto.actionsToday,
		CooldownBreaches:  auto.cooldownBreaches,
	})
	if !caps.Allowed {
		return fmt.Errorf("Magento caps rejected the change: %s", strings.Join(caps.Violations, "; "))
	}
	if req.Status == "approved" && caps.RiskClass == 1 {
		human, err := a.hasHumanActionApproval(ctx, req)
		if err != nil {
			return err
		}
		if !human {
			return errors.New("This Magento change requires approval from a human administrator")
		}
	}

	var existingID, existingRequest, existingOperation string
	err = a.DB.QueryRow(ctx, `
		select id::text, coalesce(action_request_id::text, ''), operation_id
		  from action_changeset where org_id = $1 and idempotency_key = $2 limit 1`,
		req.OrgID, payload.idempotencyKey).Scan(&existingID, &existingRequest, &existingOperation)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if err == nil {
		if existingRequest != req.ID || existingOperation != payload.operation {
			return errors.New("Magento idempotency key is already bound to a different change-set")
		}
		next := cloneJSObj(req.Payload)
		next.set("changeset_id", existingID)
		return a.setActionRequestPayload(ctx, req, next)
	}

	snapshot := asJSObj(jsValueOf(caps.Snapshot))
	if snapshot == nil {
		snapshot = emptyJSObj()
	}
	snapshot.set("autoRuleId", nil)
	if payload.autoRuleID != "" {
		snapshot.set("autoRuleId", payload.autoRuleID)
	}
	var exposure any
	if caps.ProjectedExposure != nil {
		exposure = strconv.FormatFloat(*caps.ProjectedExposure, 'f', -1, 64)
	}
	status := "pending_approval"
	if req.Status == "approved" {
		status = "approved"
	}
	summary := "Magento " + payload.operation
	if req.Summary != nil {
		summary = *req.Summary
	}
	var changesetID string
	err = pgx.BeginFunc(ctx, a.DB, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			insert into action_changeset (
				org_id, action_request_id, domain, scope, operation_id, risk_class, status,
				summary, idempotency_key, cap_snapshot, projected_exposure, previewed_at
			) values ($1, $2::uuid, $3, $4::jsonb, $5, $6, $7, $8, $9, $10::jsonb, $11::numeric, now())
			returning id::text`,
			req.OrgID, req.ID, def.Domain, jsStringify(payload.scope, false), payload.operation, int(caps.RiskClass), status,
			summary, payload.idempotencyKey, jsStringify(snapshot, false), exposure).Scan(&changesetID); err != nil {
			return err
		}
		for i, row := range prepared {
			after := asJSObj(row.input.body)
			if after == nil {
				after = jsObjOf("items", row.input.body)
			}
			if _, err := tx.Exec(ctx, `
				insert into action_changeset_row (
					changeset_id, row_index, entity_ref, operation_id, path, query,
					before_image, expected_current, after_image, status
				) values ($1::uuid, $2, $3, $4, $5::jsonb, $6::jsonb, $7::jsonb, $7::jsonb, $8::jsonb, 'draft')`,
				changesetID, i, row.input.entityRef, payload.operation, jsStringify(row.input.path, false),
				jsStringify(row.input.query, false), jsStringify(row.before, false), jsStringify(after, false)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	mode := string(packs.MagentoExecutionModeFor(caps.RiskClass))
	a.recordAudit(ctx, auditEvent{
		OrgID:      req.OrgID,
		EntityKind: "action_changeset",
		EntityID:   changesetID,
		Event:      "previewed",
		Payload: map[string]any{
			"actionRequestId": req.ID,
			"domain":          def.Domain,
			"operation":       payload.operation,
			"executionMode":   mode,
			"rows":            len(prepared),
			"violations":      jsValueOf(caps.Violations),
			"escalations":     jsValueOf(caps.Escalations),
		},
	})
	previewRows := make([]any, len(prepared))
	for i, row := range prepared {
		after := magentoExpectedAfterImage(&op, row.input.body)
		before := magentoProjectBeforeImage(row.before, after)
		if def.Domain == string(packs.MagentoCustomers) {
			attrs := make([]any, len(row.classification.Attributes))
			for j, attr := range row.classification.Attributes {
				attrs[j] = attr.Attribute
			}
			previewRows[i] = jsObjOf(
				"entity_ref", row.input.entityRef,
				"attributes", attrs,
				"before_hash", jsHash(before),
				"after_hash", jsHash(after),
				"data_minimized", true,
			)
			continue
		}
		requirements := make([]any, len(row.classification.Attributes))
		for j, attr := range row.classification.Attributes {
			requirements[j] = jsObjOf(
				"attribute", attr.Attribute,
				"category", string(attr.Category),
				"rationale", attr.Rationale,
				"matched", attr.Matched,
				"execution_mode", string(packs.MagentoExecutionModeFor(attr.RiskClass)),
			)
		}
		previewRows[i] = jsObjOf(
			"entity_ref", row.input.entityRef,
			"before", before,
			"after", after,
			"approval_requirements", requirements,
		)
	}
	var previewExposure any
	if caps.ProjectedExposure != nil {
		previewExposure = *caps.ProjectedExposure
	}
	next := cloneJSObj(req.Payload)
	next.set("changeset_id", changesetID)
	next.set("preview", jsObjOf(
		"execution_mode", mode,
		"row_count", float64(len(prepared)),
		"projected_exposure", previewExposure,
		"escalations", jsValueOf(caps.Escalations),
		"caps", jsValueOf(caps.Snapshot),
		"rows", previewRows,
	))
	if err := a.setActionRequestPayload(ctx, req, next); err != nil {
		return err
	}
	if auto.autoExecution && caps.RiskClass == 2 {
		if !control.autoExecute {
			return fmt.Errorf("Magento %s automatic execution is disabled", def.Domain)
		}
		return a.autoApprovePreparedActionRequest(ctx, req,
			fmt.Sprintf("Magento automatic rule %s passed its stored limits", payload.autoRuleID))
	}
	return nil
}

func (a *API) loadMagentoChangesetRows(ctx context.Context, changesetID string) ([]magentoStoredRow, error) {
	rows, err := a.DB.Query(ctx, `
		select id::text, entity_ref, operation_id, path, query, before_image, after_image,
		       expected_current, status, reconciled_image
		  from action_changeset_row where changeset_id = $1::uuid order by row_index`, changesetID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (magentoStoredRow, error) {
		var r magentoStoredRow
		var path, query, before, after, expected, reconciled []byte
		err := row.Scan(&r.id, &r.entityRef, &r.operationID, &path, &query, &before, &after, &expected, &r.status, &reconciled)
		r.path, r.query, r.before, r.after, r.expected = jsonbObj(path), jsonbObj(query), jsonbObj(before), jsonbObj(after), jsonbObj(expected)
		for _, p := range []**jsObj{&r.path, &r.query, &r.before, &r.after, &r.expected} {
			if *p == nil {
				*p = emptyJSObj()
			}
		}
		if reconciled != nil {
			r.reconciled = jsonbObj(reconciled)
		}
		return r, err
	})
}

func jsISOTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

// prepareMagentoUndo matches prepareUndo(): it checks each applied row for
// drift and stores the inverse change-set as a new draft.
func (a *API) prepareMagentoUndo(ctx context.Context, req *actionRequestRecord) error {
	originalID, _ := req.Payload.vals["changeset_id"].(string)
	key, _ := req.Payload.vals["idempotency_key"].(string)
	key = strings.TrimSpace(key)
	if originalID == "" || utf16Len(key) < 8 {
		return errors.New("Magento undo needs changeset_id and idempotency_key")
	}
	var domain, summary, status string
	var risk int
	var scope []byte
	err := a.DB.QueryRow(ctx, `
		select domain, summary, status, risk_class, scope
		  from action_changeset where id::text = $1 and org_id = $2 limit 1`, originalID, req.OrgID).
		Scan(&domain, &summary, &status, &risk, &scope)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if err != nil || (status != "applied" && status != "partially_applied") {
		return errors.New("Only an applied or partially applied Magento change-set can be undone")
	}
	rows, err := a.loadMagentoChangesetRows(ctx, originalID)
	if err != nil {
		return err
	}
	rt, err := a.loadMagentoRuntime(ctx, req.OrgID)
	if err != nil {
		return err
	}
	type inverseRow struct {
		original      magentoStoredRow
		operationName string
		current       *jsObj
		body          *jsObj
	}
	var inverseRows []inverseRow
	for _, row := range rows {
		if row.status != "applied" {
			continue
		}
		_, current, err := a.magentoOperationAcrossDefinitions(ctx, req.OrgID, row.operationID)
		if err != nil {
			return err
		}
		if !current.Reversible || current.InverseOperation == "" {
			return fmt.Errorf("Magento operation %s does not have a safe inverse", row.operationID)
		}
		_, inverse, err := a.magentoOperationAcrossDefinitions(ctx, req.OrgID, current.InverseOperation)
		if err != nil {
			return err
		}
		image, err := readMagentoEntity(ctx, rt, current, row.path, row.query, row.after)
		if err != nil {
			return err
		}
		if image == nil {
			image = emptyJSObj()
		}
		var expected any = row.reconciled
		if row.reconciled == nil {
			expected = magentoExpectedAfterImage(current, row.after)
		}
		if len(image.vals) > 0 && !magentoDeepContains(image, expected) {
			if _, err := a.DB.Exec(ctx, `
				update action_changeset_row
				   set status = 'drifted', reconciled_image = $2::jsonb,
				       error = 'current Magento value drifted after the original change', updated_at = now()
				 where id::text = $1`, row.id, jsStringify(image, false)); err != nil {
				return err
			}
			return fmt.Errorf("Magento entity %s drifted; inverse draft was not created", row.entityRef)
		}
		inverseRows = append(inverseRows, inverseRow{
			original:      row,
			operationName: current.InverseOperation,
			current:       image,
			body:          magentoInverseBody(inverse, current, row.before, row.after),
		})
	}
	if len(inverseRows) == 0 {
		return errors.New("Magento change-set has no applied rows to undo")
	}
	operationName := inverseRows[0].operationName
	for _, row := range inverseRows {
		if row.operationName != operationName {
			return errors.New("Magento inverse rows span multiple operations; split them into separate undo drafts")
		}
	}
	nextStatus := "pending_approval"
	if req.Status == "approved" {
		nextStatus = "approved"
	}
	snapshot := jsObjOf("inverseOf", originalID, "driftCheckedAt", jsISOTime(time.Now()))
	var createdID string
	err = pgx.BeginFunc(ctx, a.DB, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			insert into action_changeset (
				org_id, action_request_id, inverse_of_id, domain, scope, operation_id, risk_class,
				status, summary, idempotency_key, cap_snapshot, previewed_at
			) values ($1, $2::uuid, $3::uuid, $4, $5::jsonb, $6, $7, $8, $9, $10, $11::jsonb, now())
			returning id::text`,
			req.OrgID, req.ID, originalID, domain, string(scope), operationName, risk,
			nextStatus, "Undo: "+summary, key, jsStringify(snapshot, false)).Scan(&createdID); err != nil {
			return err
		}
		for i, row := range inverseRows {
			if _, err := tx.Exec(ctx, `
				insert into action_changeset_row (
					changeset_id, row_index, entity_ref, operation_id, path, query,
					before_image, expected_current, after_image, status
				) values ($1::uuid, $2, $3, $4, $5::jsonb, $6::jsonb, $7::jsonb, $7::jsonb, $8::jsonb, 'draft')`,
				createdID, i, row.original.entityRef, operationName, jsStringify(row.original.path, false),
				jsStringify(row.original.query, false), jsStringify(row.current, false), jsStringify(row.body, false)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	a.recordAudit(ctx, auditEvent{
		OrgID:      req.OrgID,
		EntityKind: "action_changeset",
		EntityID:   createdID,
		Event:      "inverse_previewed",
		Payload:    map[string]any{"inverseOf": originalID, "rows": len(inverseRows)},
	})
	next := cloneJSObj(req.Payload)
	next.set("changeset_id", createdID)
	next.set("inverse_of", originalID)
	next.set("operation", operationName)
	next.set("preview", jsObjOf(
		"execution_mode", string(packs.MagentoExecutionModeFor(packs.MagentoRiskClass(risk))),
		"row_count", float64(len(inverseRows)),
	))
	return a.setActionRequestPayload(ctx, req, next)
}

// magentoActionCreated is the Magento part of the action request created
// hooks (registerMagentoV2Runtime()).
func (a *API) magentoActionCreated(ctx context.Context, req *actionRequestRecord) error {
	switch {
	case req.Kind == "magento.financial_handoff":
		return nil
	case req.Kind == "magento.undo_changeset":
		return a.prepareMagentoUndo(ctx, req)
	case !strings.HasPrefix(req.Kind, "magento.manage_"):
		return nil
	}
	defs, err := a.magentoActionDefinitions(ctx, req.OrgID, req.Kind)
	if err != nil {
		return err
	}
	if len(defs) == 0 || defs[0].Adapter.Kind != "magento_changeset" {
		return fmt.Errorf("Magento action definition %s is unavailable", req.Kind)
	}
	return a.prepareMagentoChangeset(ctx, req, &defs[0])
}

func (a *API) setMagentoRow(ctx context.Context, rowID, assignments string, args ...any) error {
	_, err := a.DB.Exec(ctx, `update action_changeset_row set `+assignments+`, updated_at = now() where id::text = $1`,
		append([]any{rowID}, args...)...)
	return err
}

// executeMagentoChangeset matches executeChangeset(): it writes each approved
// row through GraphJin and reads Magento back to confirm the result. A row
// whose write may have reached Magento is never retried.
func (a *API) executeMagentoChangeset(ctx context.Context, req *actionRequestRecord) (*actionExecutionOutcome, error) {
	changesetID, _ := req.Payload.vals["changeset_id"].(string)
	if changesetID == "" {
		return nil, errors.New("Magento action has no prepared change-set")
	}
	var requestID, status, operationID string
	var risk int
	err := a.DB.QueryRow(ctx, `
		select coalesce(action_request_id::text, ''), status, operation_id, risk_class
		  from action_changeset where id::text = $1 and org_id = $2 limit 1`, changesetID, req.OrgID).
		Scan(&requestID, &status, &operationID, &risk)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if err != nil || requestID != req.ID {
		return nil, errors.New("Magento change-set does not belong to this approved request")
	}
	if risk == 1 {
		human, err := a.hasHumanActionApproval(ctx, req)
		if err != nil {
			return nil, err
		}
		if !human {
			return nil, errors.New("This Magento change requires approval from a human administrator")
		}
	}
	if status != "approved" && status != "pending_approval" {
		return nil, fmt.Errorf("Magento change-set status=%s; expected approved", status)
	}
	def, op, err := a.magentoOperationAcrossDefinitions(ctx, req.OrgID, operationID)
	if err != nil {
		return nil, err
	}
	if _, err := a.magentoDomainControl(ctx, req.OrgID, def.Domain); err != nil {
		return nil, err
	}
	rt, err := a.loadMagentoRuntime(ctx, req.OrgID)
	if err != nil {
		return nil, err
	}
	rows, err := a.loadMagentoChangesetRows(ctx, changesetID)
	if err != nil {
		return nil, err
	}
	bulk := op.ResultMode == "async_bulk"
	if bulk && op.ReadPath != "" {
		drifted := false
		for _, row := range rows {
			current, err := readMagentoEntity(ctx, rt, op, row.path, row.query, row.after)
			if err != nil {
				return nil, err
			}
			var seen any = current
			if current == nil {
				seen = emptyJSObj()
			}
			if len(row.expected.vals) > 0 && !magentoCompareCanonical(seen, row.expected) {
				drifted = true
				if err := a.setMagentoRow(ctx, row.id, `status = 'drifted', reconciled_image = $2::jsonb,
					error = 'Magento value changed after preview', finished_at = now()`, jsonbArg(current)); err != nil {
					return nil, err
				}
			}
		}
		if drifted {
			if _, err := a.DB.Exec(ctx, `update action_changeset set status = 'reconcile_required', reconciled_at = now(), updated_at = now() where id::text = $1`, changesetID); err != nil {
				return nil, err
			}
			return nil, errors.New("Magento bulk change-set drifted after preview; nothing was submitted")
		}
	}
	role := "magento_ops_executor"
	if risk == 1 {
		role = "magento_sensitive_executor"
	}
	if _, err := a.DB.Exec(ctx, `
		update action_changeset
		   set status = 'executing', approved_by_user_id = $2, approved_at = coalesce($3, now()), updated_at = now()
		 where id::text = $1`, changesetID, nullableStringPtr(req.ApprovedBy), req.ApprovedAt); err != nil {
		return nil, err
	}

	var bulkUUID string
	var bulkStatus any
	if bulk {
		var items []any
		for _, row := range rows {
			body := any(row.after)
			if list, ok := row.after.vals["items"].([]any); ok && len(row.after.vals) == 1 {
				body = list
			}
			if list, ok := body.([]any); ok {
				items = append(items, list...)
			} else {
				items = append(items, body)
			}
		}
		if items == nil {
			items = []any{}
		}
		result, err := a.executeMagentoMutation(ctx, req, rt, op, emptyJSObj(), items, role)
		if err != nil {
			return nil, err
		}
		if bulkUUID = magentoBulkUUID(result.response); bulkUUID == "" {
			return nil, errors.New("Magento async bulk response did not include bulk_uuid")
		}
		if _, err := a.DB.Exec(ctx, `update action_changeset set bulk_uuid = $2, updated_at = now() where id::text = $1`, changesetID, bulkUUID); err != nil {
			return nil, err
		}
		if _, err := a.DB.Exec(ctx, `
			update action_changeset_row
			   set status = 'submitted', external_ref = $2, started_at = now(), updated_at = now()
			 where changeset_id = $1::uuid`, changesetID, bulkUUID); err != nil {
			return nil, err
		}
		if bulkStatus, err = waitForMagentoBulk(ctx, rt, bulkUUID, 90*time.Second, magentoBulkPoll); err != nil {
			return nil, err
		}
	}

	applied, failed, reconcileRequired := 0, 0, 0
	firstRowError := ""
	terminal := magentoBulkOperations(bulkStatus)
	for index, row := range rows {
		writeAttempted := bulk
		rowErr := func() error {
			if bulk {
				code := -1.0
				if index < len(terminal) {
					code = magentoBulkStatus(terminal[index])
				}
				if code != 1 {
					reconcileRequired++
					return a.setMagentoRow(ctx, row.id, `status = 'reconcile_required', error = $2, finished_at = now()`,
						"Magento bulk row failed with terminal status "+jsNumberText(code))
				}
			}
			var current *jsObj
			if !isMagentoCreate(op) {
				read, err := readMagentoEntity(ctx, rt, op, row.path, row.query, row.after)
				if err != nil {
					return err
				}
				current = read
			}
			var seen any = current
			if current == nil {
				seen = emptyJSObj()
			}
			if !bulk && op.ReadPath != "" && len(row.expected.vals) > 0 && !magentoCompareCanonical(seen, row.expected) {
				reconcileRequired++
				return a.setMagentoRow(ctx, row.id, `status = 'drifted', reconciled_image = $2::jsonb,
					error = 'Magento value changed after preview', finished_at = now()`, jsonbArg(current))
			}
			var response *magentoMutationResult
			if !bulk {
				if err := a.setMagentoRow(ctx, row.id, `status = 'submitted', started_at = now()`); err != nil {
					return err
				}
				writeAttempted = true
				result, err := a.executeMagentoMutation(ctx, req, rt, op, row.path, row.after, role)
				if err != nil {
					return err
				}
				response = result
			}
			path := row.path
			if response != nil {
				path = magentoReconciledPath(op, row.path, response.response)
			}
			if !magentoCompareCanonical(path, row.path) {
				if err := a.setMagentoRow(ctx, row.id, `path = $2::jsonb`, jsStringify(path, false)); err != nil {
					return err
				}
			}
			fulfillment := op.OperationID == "magentoCreateInvoice" || op.OperationID == "magentoCreateShipment"
			readOp := op
			if fulfillment {
				var resp any
				if response != nil {
					resp = response.response
				}
				documentID := magentoNumber(resp)
				if !isSafeInteger(documentID) || documentID < 1 {
					return errors.New("Magento fulfillment response did not identify the created document")
				}
				docs := "shipment"
				if op.OperationID == "magentoCreateInvoice" {
					docs = "invoices"
				}
				copied := *op
				copied.ReadPath = "/V1/" + docs + "/" + jsNumberText(documentID)
				readOp = &copied
			}
			reconciled, err := readMagentoEntity(ctx, rt, readOp, path, row.query, row.after)
			if err != nil {
				return err
			}
			expected := magentoExpectedAfterImage(op, row.after)
			if exp := asJSObj(expected); fulfillment && exp != nil {
				next := cloneJSObj(exp)
				next.set("order_id", magentoNumber(jsLookupOrUndefinedNaN(path, "orderId")))
				expected = next
			}
			if exp := asJSObj(expected); op.OperationID == "magentoMoveCategory" && exp != nil {
				position := 1.0
				if afterID := exp.vals["afterId"]; jsTruthy(afterID) {
					copied := *op
					copied.ReadPath = "/V1/categories/{categoryId}"
					sibling, err := readMagentoEntity(ctx, rt, &copied, jsObjOf("categoryId", afterID), emptyJSObj(), nil)
					if err != nil {
						return err
					}
					position = magentoNumber(jsLookupOrUndefinedNaN(sibling, "position")) + 1
				}
				next := cloneJSObj(exp)
				next.set("position", position)
				expected = next
			}
			confirmed := magentoReconciliationConfirmed(op, reconciled, expected)
			externalRef := bulkUUID
			if externalRef == "" && response != nil {
				externalRef = response.requestID
			}
			rowStatus, rowError := "applied", any(nil)
			if confirmed {
				applied++
			} else {
				reconcileRequired++
				rowStatus, rowError = "reconcile_required", "Magento write succeeded but reconciliation did not confirm the requested values"
			}
			return a.setMagentoRow(ctx, row.id, `status = $2, external_ref = $3, reconciled_image = $4::jsonb, error = $5, finished_at = now()`,
				rowStatus, nullableStringPtr(&externalRef), jsonbArg(reconciled), rowError)
		}()
		if rowErr == nil {
			continue
		}
		message := rowErr.Error()
		if firstRowError == "" {
			firstRowError = message
		}
		rowStatus, rowMessage := "failed", message
		if writeAttempted {
			reconcileRequired++
			rowStatus, rowMessage = "reconcile_required", "Magento write outcome needs reconciliation: "+message
		} else {
			failed++
		}
		if err := a.setMagentoRow(ctx, row.id, `status = $2, error = $3, finished_at = now()`, rowStatus, rowMessage); err != nil {
			return nil, err
		}
	}
	final := "applied"
	switch {
	case applied > 0 && (failed > 0 || reconcileRequired > 0):
		final = "partially_applied"
	case reconcileRequired > 0:
		final = "reconcile_required"
	case failed > 0:
		final = "failed"
	}
	if _, err := a.DB.Exec(ctx, `
		update action_changeset set status = $2, executed_at = now(), reconciled_at = now(), updated_at = now()
		 where id::text = $1`, changesetID, final); err != nil {
		return nil, err
	}
	var auditBulk any
	if bulkUUID != "" {
		auditBulk = bulkUUID
	}
	a.recordAudit(ctx, auditEvent{
		OrgID:      req.OrgID,
		EntityKind: "action_changeset",
		EntityID:   changesetID,
		Event:      "execution:" + final,
		Payload: map[string]any{
			"operation":         operationID,
			"applied":           applied,
			"failed":            failed,
			"reconcileRequired": reconcileRequired,
			"bulkUuid":          auditBulk,
		},
	})
	if ruleID, _ := req.Payload.vals["auto_rule_id"].(string); ruleID != "" && final != "failed" {
		if _, err := a.DB.Exec(ctx, `
			update magento_auto_rule set last_fired_at = now(), updated_at = now()
			 where id::text = $1 and org_id = $2`, ruleID, req.OrgID); err != nil {
			return nil, err
		}
	}
	if final == "failed" {
		if firstRowError != "" {
			return nil, fmt.Errorf("Every Magento change-set row failed: %s", firstRowError)
		}
		return nil, errors.New("Every Magento change-set row failed")
	}
	outcomeStatus := final
	if final == "applied" {
		outcomeStatus = "succeeded"
	}
	externalRef := changesetID
	if bulkUUID != "" {
		externalRef = bulkUUID
	}
	return &actionExecutionOutcome{
		Status:             outcomeStatus,
		CommandOrOperation: &operationID,
		ExternalRef:        &externalRef,
		ChangesetID:        &changesetID,
		Result: jsObjOf(
			"changeset_id", changesetID,
			"status", final,
			"row_count", float64(len(rows)),
			"applied", float64(applied),
			"failed", float64(failed),
			"reconcile_required", float64(reconcileRequired),
			"bulk_uuid", auditBulk,
			"bulk_status_recorded", bulkStatus != nil,
			"indexers", a.magentoIndexerReceipt(ctx, rt, req.OrgID),
			"retry", "never",
		),
	}, nil
}

func jsLookupOrUndefinedNaN(v any, key string) any {
	got := jsLookup(v, key)
	if _, undef := got.(jsUndefined); undef {
		return math.NaN()
	}
	return got
}

var magentoBulkPoll = time.Second

var magentoHandoffKinds = []string{"online_refund", "return_approval", "financial_configuration", "store_credit_over_cap"}

// executeMagentoFinancialHandoff stores the handoff for a person to finish
// in Magento admin. It never calls Magento.
func (a *API) executeMagentoFinancialHandoff(ctx context.Context, req *actionRequestRecord) (*actionExecutionOutcome, error) {
	kind, _ := req.Payload.vals["handoff_kind"].(string)
	entityRef, _ := req.Payload.vals["entity_ref"].(string)
	draft, evidence := asJSObj(req.Payload.vals["draft"]), asJSObj(req.Payload.vals["evidence"])
	if !slices.Contains(magentoHandoffKinds, kind) || entityRef == "" || draft == nil || evidence == nil {
		return nil, errors.New("Magento financial handoff payload is invalid")
	}
	var id string
	if err := a.DB.QueryRow(ctx, `
		insert into magento_financial_handoff (org_id, action_request_id, kind, entity_ref, status, draft, evidence)
		values ($1, $2::uuid, $3, $4, 'ready_for_human', $5::jsonb, $6::jsonb)
		returning id::text`, req.OrgID, req.ID, kind, entityRef, jsStringify(draft, false), jsStringify(evidence, false)).Scan(&id); err != nil {
		return nil, err
	}
	a.recordAudit(ctx, auditEvent{
		OrgID:      req.OrgID,
		EntityKind: "magento_financial_handoff",
		EntityID:   id,
		Event:      "ready_for_human",
		Payload:    map[string]any{"kind": kind, "entityRef": entityRef, "executePath": false},
	})
	command := "magento_admin_handoff"
	return &actionExecutionOutcome{
		Status:             "succeeded",
		CommandOrOperation: &command,
		ExternalRef:        &id,
		Result: jsObjOf(
			"handoff_id", id,
			"status", "ready_for_human",
			"execute_path", false,
			"next_step", "A human completes this operation in Magento admin.",
		),
	}, nil
}
