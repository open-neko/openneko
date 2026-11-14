package webfront

import (
	"errors"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The saved-query metric of apps/worker/src/jobs/deterministic-metric.ts: a
// reviewed GraphJin query whose result maps to a card with no model.

func recordOf(v any, label string) (*jsObj, error) {
	if o, ok := v.(*jsObj); ok && o != nil {
		return o, nil
	}
	return nil, errors.New(label + " must be an object")
}

func lookup(o *jsObj, key string) any {
	if o == nil {
		return jsUndefined{}
	}
	if v, ok := o.vals[key]; ok {
		return v
	}
	return jsUndefined{}
}

func nullish(v any) bool {
	if v == nil {
		return true
	}
	_, undef := v.(jsUndefined)
	return undef
}

func parseMetricDefinition(value any) (*jsObj, error) {
	definition, err := recordOf(value, "saved-query metric definition")
	if err != nil {
		return nil, err
	}
	execution, err := recordOf(lookup(definition, "execution"), "saved-query metric execution")
	if err != nil {
		return nil, err
	}
	if lookup(execution, "mode") != "saved_query" {
		return nil, errors.New("saved-query metric execution.mode is invalid")
	}
	for _, key := range []string{"source", "query", "document"} {
		if s, ok := execution.vals[key].(string); !ok || jsTrim(s) == "" {
			return nil, errors.New("saved-query metric execution." + key + " is required")
		}
	}
	freshness := jsToNumber(lookup(execution, "freshnessSeconds"))
	if freshness != math.Trunc(freshness) || math.IsInf(freshness, 0) || freshness <= 0 {
		return nil, errors.New("saved-query metric freshnessSeconds must be a positive integer")
	}
	result, err := recordOf(lookup(execution, "result"), "saved-query metric result mapping")
	if err != nil {
		return nil, err
	}
	if _, ok := result.vals["kind"].(string); !ok {
		return nil, errors.New("saved-query metric result.kind is required")
	}
	if title, ok := definition.vals["title"].(string); !ok || jsTrim(title) == "" {
		return nil, errors.New("saved-query metric title is required")
	}
	return definition, nil
}

func positiveOr(v any, fallback float64) float64 {
	n := jsToNumber(v)
	if !math.IsNaN(n) && !math.IsInf(n, 0) && n > 0 {
		return n
	}
	return fallback
}

func msBefore(now time.Time, ms float64) time.Time {
	return now.Add(-time.Duration(math.Trunc(ms)) * time.Millisecond)
}

func metricRuntime(definition *jsObj) *jsObj {
	r, _ := lookup(lookup(definition, "execution").(*jsObj), "runtime").(*jsObj)
	return r
}

// buildSavedQueryVariables is buildSavedQueryVariables().
func buildSavedQueryVariables(value any, now time.Time) (*jsObj, error) {
	definition, err := parseMetricDefinition(value)
	if err != nil {
		return nil, err
	}
	execution := definition.vals["execution"].(*jsObj)
	if v := lookup(execution, "variables"); jsTruthy(v) {
		return resolveWatcherVariables(v, now)
	}
	runtime := metricRuntime(definition)
	windowDays := positiveOr(lookup(runtime, "windowDays"), 30)
	stale := positiveOr(lookup(runtime, "staleAfterSeconds"), 2*60*60)
	aged := positiveOr(lookup(runtime, "agedAfterSeconds"), 2*24*60*60)
	var storeIDs any = []any{1.0}
	switch s := lookup(runtime, "storeIds").(type) {
	case []any:
		if len(s) > 0 {
			storeIDs = s
		}
	case string:
		if s != "" {
			storeIDs = s
		}
	}
	var threshold any = 5.0
	if t, ok := lookup(runtime, "stockThreshold").(float64); ok && !math.IsInf(t, 0) && !math.IsNaN(t) {
		threshold = t
	}
	return jsObjOf("from", jsDate(msBefore(now, windowDays*86_400_000)), "to", jsDate(now), "now", jsDate(now),
		"staleBefore", jsDate(msBefore(now, stale*1_000)), "olderThan", jsDate(msBefore(now, aged*1_000)),
		"storeIds", storeIDs, "threshold", threshold), nil
}

var digitsOnly = regexp.MustCompile(`^\d+$`)

func atPath(root any, path string) any {
	current := root
	for _, segment := range strings.Split(path, ".") {
		switch c := current.(type) {
		case []any:
			if digitsOnly.MatchString(segment) {
				i, err := strconv.Atoi(segment)
				if err != nil || i >= len(c) {
					return jsUndefined{}
				}
				current = c[i]
			} else if segment == "length" {
				current = float64(len(c))
			} else {
				return jsUndefined{}
			}
		case *jsObj:
			if c == nil {
				return jsUndefined{}
			}
			v, ok := c.vals[segment]
			if !ok {
				return jsUndefined{}
			}
			current = v
		default:
			return jsUndefined{}
		}
	}
	return current
}

func numberAt(root any, path string) (float64, error) {
	n := jsToNumber(atPath(root, path))
	if math.IsNaN(n) || math.IsInf(n, 0) {
		return 0, errors.New("saved-query result path " + path + " is not numeric")
	}
	return n, nil
}

func evaluateExpression(root, expression any) (*float64, error) {
	switch e := expression.(type) {
	case float64:
		return &e, nil
	case string:
		n, err := numberAt(root, e)
		return &n, err
	}
	node, err := recordOf(expression, "saved-query result expression")
	if err != nil {
		return nil, err
	}
	if len(node.keys) != 1 {
		return nil, errors.New("saved-query expression must contain one operator")
	}
	operator := node.ordered()[0]
	operands, ok := node.vals[operator].([]any)
	if !ok || len(operands) < 2 {
		return nil, errors.New("saved-query expression " + operator + " requires at least two operands")
	}
	values := make([]float64, len(operands))
	missing := false
	for i, operand := range operands {
		v, err := evaluateExpression(root, operand)
		if err != nil {
			return nil, err
		}
		if v == nil {
			missing = true
		} else {
			values[i] = *v
		}
	}
	if missing {
		return nil, nil
	}
	var out float64
	switch operator {
	case "add":
		for _, v := range values {
			out += v
		}
	case "subtract":
		out = values[0]
		for _, v := range values[1:] {
			out -= v
		}
	case "multiply":
		out = 1
		for _, v := range values {
			out *= v
		}
	case "divide":
		denominator := 1.0
		for _, v := range values[1:] {
			denominator *= v
		}
		if denominator == 0 {
			return nil, nil
		}
		out = values[0] / denominator
	default:
		return nil, errors.New("saved-query result expression operator " + operator + " is not allowed")
	}
	return &out, nil
}

func rowsAt(root any, path string) ([]*jsObj, error) {
	list, ok := atPath(root, path).([]any)
	if !ok {
		return nil, errors.New("saved-query result path " + path + " is not an array")
	}
	rows := make([]*jsObj, len(list))
	for i, row := range list {
		r, err := recordOf(row, path+"."+strconv.Itoa(i))
		if err != nil {
			return nil, err
		}
		rows[i] = r
	}
	return rows, nil
}

func median(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := slices.Clone(values)
	slices.Sort(sorted)
	mid := len(sorted) / 2
	if len(sorted)%2 == 0 {
		return (sorted[mid-1] + sorted[mid]) / 2
	}
	return sorted[mid]
}

var (
	isoDateTime   = regexp.MustCompile(`^([+-]\d{6}|\d{4})(?:-(\d{2})(?:-(\d{2}))?)?(?:[T ](\d{2}):(\d{2})(?::(\d{2})(?:\.(\d+))?)?(Z|[+-]\d{2}:\d{2})?)?$`)
	timestampLike = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}[T ]`)
)

// jsDateParse is new Date(text) for the ISO forms: a date alone is UTC, a
// date and time with no offset is local time. Other forms are invalid.
func jsDateParse(text string) (time.Time, bool) {
	m := isoDateTime.FindStringSubmatch(text)
	if m == nil {
		return time.Time{}, false
	}
	num := func(s string, def int) int {
		if s == "" {
			return def
		}
		n, _ := strconv.Atoi(s)
		return n
	}
	year, month, day := num(m[1], 0), num(m[2], 1), num(m[3], 1)
	hour, minute, second := num(m[4], 0), num(m[5], 0), num(m[6], 0)
	ms := 0
	if m[7] != "" {
		ms = num((m[7] + "00")[:3], 0)
	}
	if month < 1 || month > 12 || day < 1 || day > 31 || hour > 24 || minute > 59 || second > 59 || (hour == 24 && (minute > 0 || second > 0 || ms > 0)) {
		return time.Time{}, false
	}
	loc := time.UTC
	if m[4] != "" && m[8] == "" {
		loc = time.Local
	}
	t := time.Date(year, time.Month(month), day, hour, minute, second, ms*int(time.Millisecond), loc)
	if t.Day() != day {
		return time.Time{}, false
	}
	if m[8] != "" && m[8] != "Z" {
		oh, om := num(m[8][1:3], 0), num(m[8][4:6], 0)
		offset := time.Duration(oh)*time.Hour + time.Duration(om)*time.Minute
		if m[8][0] == '+' {
			t = t.Add(-offset)
		} else {
			t = t.Add(offset)
		}
	}
	return t, true
}

func latestTimestamp(value any) *time.Time {
	var latest *time.Time
	var visit func(any)
	visit = func(candidate any) {
		switch c := candidate.(type) {
		case string:
			if timestampLike.MatchString(c) {
				if t, ok := jsDateParse(c); ok && (latest == nil || t.After(*latest)) {
					latest = &t
				}
			}
		case []any:
			for _, item := range c {
				visit(item)
			}
		case *jsObj:
			if c != nil {
				for _, k := range c.ordered() {
					visit(c.vals[k])
				}
			}
		}
	}
	visit(value)
	return latest
}

func rowLabel(row *jsObj, index int) string {
	for _, key := range []string{"sku", "indexer_id", "source_code", "store_id", "name", "status"} {
		if v := lookup(row, key); !nullish(v) {
			return jsValueString(v)
		}
	}
	return strconv.Itoa(index + 1)
}

func rowValue(row *jsObj) float64 {
	for _, key := range []string{"sum_base_row_invoiced", "net_revenue", "quantity", "sum_qty_invoiced"} {
		if n := jsToNumber(lookup(row, key)); !math.IsNaN(n) && !math.IsInf(n, 0) {
			return n
		}
	}
	return 1
}

type chartPoint struct {
	d string
	v float64
}

type mappedValue struct {
	value     *float64
	valueJSON any
	chart     []chartPoint
}

func lastSegment(path string, back int) string {
	parts := strings.Split(path, ".")
	if len(parts) < back {
		return path
	}
	return parts[len(parts)-back]
}

func pathStrings(v any) []string {
	list, _ := v.([]any)
	out := make([]string, len(list))
	for i, p := range list {
		out[i] = jsValueString(p)
	}
	return out
}

func pathValues(data any, paths []string) *jsObj {
	o := &jsObj{vals: map[string]any{}}
	for _, p := range paths {
		v := atPath(data, p)
		if nullish(v) {
			v = nil
		}
		o.set(p, v)
	}
	return o
}

func ageSeconds(now, t time.Time) float64 {
	return math.Max(0, float64(now.Sub(t).Milliseconds())/1000)
}

// collateCompare stands in for localeCompare() on row keys: letters compare
// without case first, then lower case before upper case.
func collateCompare(a, b string) int {
	if c := strings.Compare(strings.ToLower(a), strings.ToLower(b)); c != 0 {
		return c
	}
	return -strings.Compare(a, b)
}

func mapMetricValue(mapping *jsObj, data *jsObj, now time.Time) (mappedValue, error) {
	switch kind := lookup(mapping, "kind"); kind {
	case "scalar":
		var value *float64
		var err error
		if expr := lookup(mapping, "expression"); !isUndefined(expr) {
			value, err = evaluateExpression(data, expr)
		} else {
			var n float64
			n, err = numberAt(data, jsTemplate(lookup(mapping, "path")))
			value = &n
		}
		if err != nil {
			return mappedValue{}, err
		}
		var json any
		v := 0.0
		if value != nil {
			json, v = *value, *value
		}
		return mappedValue{value, json, []chartPoint{{"Current", v}}}, nil
	case "object":
		paths := pathStrings(lookup(mapping, "paths"))
		var chart []chartPoint
		sum := 0.0
		for _, p := range paths {
			n := jsToNumber(atPath(data, p))
			if !math.IsNaN(n) && !math.IsInf(n, 0) {
				chart = append(chart, chartPoint{lastSegment(p, 2), n})
				sum += n
			}
		}
		if len(chart) == 0 {
			chart = []chartPoint{{"Current", 0}}
		}
		return mappedValue{&sum, pathValues(data, paths), chart}, nil
	case "timestamps":
		paths := pathStrings(lookup(mapping, "paths"))
		var valid []chartPoint
		for _, p := range paths {
			raw := atPath(data, p)
			text := ""
			if !nullish(raw) {
				text = jsValueString(raw)
			}
			if t, ok := jsDateParse(text); ok {
				valid = append(valid, chartPoint{lastSegment(p, 1), ageSeconds(now, t)})
			}
		}
		var value *float64
		if len(valid) > 0 {
			m := valid[0].v
			for _, p := range valid[1:] {
				m = math.Max(m, p.v)
			}
			value = &m
		} else {
			valid = []chartPoint{{"Not yet available", 0}}
		}
		return mappedValue{value, pathValues(data, paths), valid}, nil
	case "timestamp_age_summary":
		rows, err := rowsAt(data, jsTemplate(lookup(mapping, "rowsPath")))
		if err != nil {
			return mappedValue{}, err
		}
		path := jsTemplate(lookup(mapping, "timestampPath"))
		ages := make([]float64, len(rows))
		oldest := 0.0
		for i, row := range rows {
			raw := atPath(row, path)
			text := ""
			if !nullish(raw) {
				text = jsValueString(raw)
			}
			t, ok := jsDateParse(text)
			if !ok {
				return mappedValue{}, errors.New("saved-query timestamp " + path + " is invalid")
			}
			ages[i] = ageSeconds(now, t)
			if i == 0 || ages[i] > oldest {
				oldest = ages[i]
			}
		}
		med := median(ages)
		return mappedValue{&med, jsObjOf("median", med, "max", oldest, "count", float64(len(rows))),
			[]chartPoint{{"Median", med}, {"Oldest", oldest}}}, nil
	case "rows":
		rows, err := rowsAt(data, jsTemplate(lookup(mapping, "path")))
		if err != nil {
			return mappedValue{}, err
		}
		n := float64(len(rows))
		list := make([]any, len(rows))
		var chart []chartPoint
		for i, row := range rows {
			list[i] = row
			if i < 20 {
				chart = append(chart, chartPoint{rowLabel(row, i), rowValue(row)})
			}
		}
		if len(rows) == 0 {
			chart = []chartPoint{{"None", 0}}
		}
		return mappedValue{&n, list, chart}, nil
	case "keyed_subtract":
		left, err := recordOf(lookup(mapping, "left"), "keyed_subtract.left")
		if err != nil {
			return mappedValue{}, err
		}
		right, err := recordOf(lookup(mapping, "right"), "keyed_subtract.right")
		if err != nil {
			return mappedValue{}, err
		}
		totals := &jsObj{vals: map[string]any{}}
		for _, side := range []struct {
			spec *jsObj
			sign float64
		}{{left, 1}, {right, -1}} {
			rows, err := rowsAt(data, jsTemplate(lookup(side.spec, "rowsPath")))
			if err != nil {
				return mappedValue{}, err
			}
			for _, row := range rows {
				key := jsTemplate(atPath(row, jsTemplate(lookup(side.spec, "keyPath"))))
				n, err := numberAt(row, jsTemplate(lookup(side.spec, "valuePath")))
				if err != nil {
					return mappedValue{}, err
				}
				prev, _ := totals.vals[key].(float64)
				totals.set(key, prev+side.sign*n)
			}
		}
		keys := slices.Clone(totals.keys)
		slices.SortStableFunc(keys, collateCompare)
		var chart []chartPoint
		sum := 0.0
		for _, k := range keys {
			v := totals.vals[k].(float64)
			chart = append(chart, chartPoint{k, v})
			sum += v
		}
		if len(chart) == 0 {
			chart = []chartPoint{{"None", 0}}
		}
		return mappedValue{&sum, totals, chart}, nil
	default:
		return mappedValue{}, errors.New("saved-query result kind " + jsTemplate(kind) + " is not supported")
	}
}

func isUndefined(v any) bool {
	_, ok := v.(jsUndefined)
	return ok
}

// currencyFormat is Intl.NumberFormat("en-US", {style: "currency"}): the
// symbol en-US uses, else the code and a no-break space, and the currency's
// minor digits.
var currencyFormat = map[string]struct {
	symbol string
	digits int
}{
	"USD": {"$", 2}, "EUR": {"€", 2}, "GBP": {"£", 2}, "JPY": {"¥", 0}, "INR": {"₹", 2}, "CAD": {"CA$", 2},
	"AUD": {"A$", 2}, "CNY": {"CN¥", 2}, "KRW": {"₩", 0}, "MXN": {"MX$", 2}, "NZD": {"NZ$", 2}, "HKD": {"HK$", 2},
	"BRL": {"R$", 2}, "ILS": {"₪", 2}, "TWD": {"NT$", 2}, "VND": {"₫", 0}, "PHP": {"₱", 2}, "XAF": {"FCFA", 0},
}

func intlNumber(v float64, minFrac, maxFrac int) string {
	s := formatFixed(v, maxFrac)
	if dot := strings.IndexByte(s, '.'); dot >= 0 {
		keep := len(s)
		for keep > dot+1+minFrac && s[keep-1] == '0' {
			keep--
		}
		if keep == dot+1 {
			keep = dot
		}
		s = s[:keep]
	}
	return s
}

func displayMetricValue(value *float64, unit any, currency string) string {
	if value == nil {
		return "Not yet available"
	}
	v := *value
	switch unit {
	case "currency":
		f, ok := currencyFormat[currency]
		prefix := f.symbol
		if !ok {
			f.digits, prefix = 2, currency+" "
		}
		body := intlNumber(math.Abs(v), f.digits, 2)
		if math.Signbit(v) {
			return "-" + prefix + body
		}
		return prefix + body
	case "percent":
		return intlNumber(v*100, 0, 2) + "%"
	case "duration_seconds":
		switch {
		case v >= 86_400:
			return jsToFixed(v/86_400, 1) + "d"
		case v >= 3_600:
			return jsToFixed(v/3_600, 1) + "h"
		}
		return jsNumberText(jsRound(v)) + "s"
	}
	return intlNumber(v, 0, 2)
}

// jsToFixed is Number.prototype.toFixed: the exact value rounds half up.
func jsToFixed(n float64, digits int) string {
	exact := strconv.FormatFloat(math.Abs(n), 'f', 1100, 64)
	dot := strings.IndexByte(exact, '.')
	whole, frac := exact[:dot], exact[dot+1:]
	scaled := []byte(whole + frac[:digits])
	if frac[digits] >= '5' {
		i := len(scaled) - 1
		for ; i >= 0 && scaled[i] == '9'; i-- {
			scaled[i] = '0'
		}
		if i < 0 {
			scaled = append([]byte{'1'}, scaled...)
		} else {
			scaled[i]++
		}
	}
	s := string(scaled[:len(scaled)-digits])
	if digits > 0 {
		s += "." + string(scaled[len(scaled)-digits:])
	}
	if n < 0 && strings.Trim(string(scaled), "0") != "" {
		s = "-" + s
	}
	return s
}

func metricMood(value, baseline *float64, direction any) string {
	if value == nil || baseline == nil || direction == "neutral" || *value == *baseline {
		return "watch"
	}
	switch direction {
	case "up":
		if *value > *baseline {
			return "good"
		}
		return "bad"
	case "down":
		if *value < *baseline {
			return "good"
		}
		return "bad"
	}
	return "watch"
}

type savedMetric struct {
	Result          *jsObj
	Value, Baseline *float64
	DeltaPct        *float64
	ValueJSON       any
	SourceFreshness *time.Time
}

func optNumber(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}

// mapSavedQueryMetric is mapSavedQueryMetric().
func mapSavedQueryMetric(definitionValue, dataValue any, baseline *float64, now time.Time) (savedMetric, error) {
	definition, err := parseMetricDefinition(definitionValue)
	if err != nil {
		return savedMetric{}, err
	}
	data, err := recordOf(dataValue, "saved-query response data")
	if err != nil {
		return savedMetric{}, err
	}
	execution := definition.vals["execution"].(*jsObj)
	mapped, err := mapMetricValue(execution.vals["result"].(*jsObj), data, now)
	if err != nil {
		return savedMetric{}, err
	}
	var delta *float64
	if mapped.value != nil && baseline != nil && *baseline != 0 {
		d := (*mapped.value - *baseline) / math.Abs(*baseline) * 100
		delta = &d
	}
	runtime := metricRuntime(definition)
	currency := "USD"
	if c := lookup(runtime, "currency"); !nullish(c) {
		currency = jsValueString(c)
	}
	chartHint := lookup(definition, "chartHint")
	chartType := "kpi"
	if chartHint == "bar" || chartHint == "table" || chartHint == "distribution" || len(mapped.chart) > 1 {
		chartType = "bar"
	}
	chart := make([]any, len(mapped.chart))
	for i, p := range mapped.chart {
		point := jsObjOf("d", p.d, "v", p.v)
		if chartType == "kpi" {
			t := 0.0
			if baseline != nil {
				t = *baseline
			} else if mapped.value != nil {
				t = *mapped.value
			}
			point.set("t", t)
		}
		chart[i] = point
	}
	title := definition.vals["title"].(string)
	headline := displayMetricValue(mapped.value, lookup(definition, "unit"), currency)
	insight := title + " is " + headline + "; a comparison will appear after the next refresh."
	if baseline != nil {
		change := "with no comparable percentage change"
		if delta != nil {
			direction := "above"
			if *delta < 0 {
				direction = "below"
			}
			change = jsToFixed(math.Abs(*delta), 1) + "% " + direction + " the prior refresh"
		}
		insight = title + " is " + headline + ", " + change + "."
	}
	detail := lookup(definition, "calculationNote")
	if nullish(detail) {
		detail = lookup(definition, "description")
	}
	if nullish(detail) {
		detail = title
	}
	windowDays := positiveOr(lookup(runtime, "windowDays"), 30)
	result := jsObjOf(
		"reasoning", "Computed deterministically from a reviewed saved query; no language model selected or transformed the value.",
		"headlineMetric", headline,
		"headlineLabel", title,
		"insightText", insight,
		"detailText", detail,
		"mood", metricMood(mapped.value, baseline, lookup(definition, "directionGood")),
		"chartType", chartType,
		"chartData", chart,
		"baselineValue", optNumber(baseline),
		"timeWindow", jsObjOf("grain", "month",
			"start", jsDate(msBefore(now, windowDays*86_400_000))[:10],
			"end", jsDate(now)[:10],
			"label", "Last "+jsNumberText(windowDays)+" days"))
	return savedMetric{Result: result, Value: mapped.value, Baseline: baseline, DeltaPct: delta,
		ValueJSON: mapped.valueJSON, SourceFreshness: latestTimestamp(data)}, nil
}
