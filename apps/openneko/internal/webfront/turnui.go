package webfront

import (
	"context"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

// render_cards takes raw arguments, as createDynamicMcpServer() does in
// Node, so the bridge-shaped calls can be recovered before validation.

func describedText(max float64, desc string) *zStr {
	s := zText(1, max)
	s.desc = desc
	return s
}

func describedList(min, max float64, item float64, desc string) *zArr {
	a := &zArr{elem: zText(1, item), max: num(max), desc: desc}
	if min > 0 {
		a.min = num(min)
	}
	return a
}

var richCardFields = []string{"layout", "rowDetails", "drillDown", "controls", "values", "computed", "map", "mapValue", "diagram", "tool"}

var cardFields = []zField{
	{name: "title", typ: zText(1, 120)},
	{name: "stage", optional: true, typ: zEnum(`"draft" for an early card while you are still checking figures; "final" for the finished answer.`, "draft", "final")},
	{name: "layout", optional: true, typ: zEnum(`"compare" shows each table row as a side-by-side panel; use it for 2 to 6 items compared on the same measures.`, "report", "compare")},
	{name: "table", optional: true, typ: describedText(20_000, "A pipe table: a header row, then one row per line, cells separated by |.")},
	{name: "chart", optional: true, typ: zEnum("Chart the table: the first column gives the labels, chartColumn the values.", "line", "bar", "area", "donut")},
	{name: "chartColumn", optional: true, typ: describedText(60, "The table column to chart. Write its cells as plain numbers, such as 12699845.92.")},
	{name: "keyFigures", optional: true, typ: describedList(1, 6, 160, `Headline numbers, each written as "Label: value" with an optional "(note)".`)},
	{name: "rowDetails", optional: true, typ: describedList(1, 20, 8000, `Sub-tables that table rows open in place, each "Row name >> pipe table": the row's first-column name, then the sub-table's header row and rows, one per line.`)},
	{name: "drillDown", optional: true, typ: describedText(200, "A follow-up question for one table row, chart point, map place, or diagram step. Write {label} where its name goes.")},
	{name: "controls", optional: true, typ: describedList(1, 4, 200, `Inputs the reader can move, each "name: Label (unit) = default (min to max, step n)", such as "price_change: Price change (%) = 5 (-20 to 20, step 1)", or a choice "name: Label = Option (number) | Option (number)", where the first option is the default and formulas use the chosen number.`)},
	{name: "values", optional: true, typ: describedList(0, 24, 120, `Fixed figures the formulas use, each "name = number", such as "road_revenue = 1496791.67".`)},
	{name: "computed", optional: true, typ: describedList(1, 6, 300, `Results that recompute as the controls move, each "Label (unit): formula" using control and value names, + - * / ^, min, max, round, abs.`)},
	{name: "map", optional: true, typ: describedList(1, 80, 160, `Places to plot, each "Place @ latitude, longitude: value".`)},
	{name: "mapValue", optional: true, typ: describedText(60, "What the map values measure, with the unit, such as Sales (USD).")},
	{name: "diagram", optional: true, typ: describedList(1, 40, 200, `Steps of a process or the links between parts, each "From -> To" with an optional ": label".`)},
	{name: "tool", optional: true, typ: describedText(40_000, "A small interactive tool as an HTML fragment with inline <script>. Put every figure it needs in the fragment. It runs in a frame as narrow as 320px that already styles labels, inputs, selects, buttons, output and tables, so add little CSS.")},
	{name: "callout", optional: true, typ: describedText(600, "A status, risk, or recommended action.")},
	{name: "mood", optional: true, typ: zEnum("The callout's tone.", "good", "watch", "act")},
	{name: "followUps", optional: true, typ: describedList(1, 4, 200, "Follow-up questions the operator can ask next.")},
}

func cardShape(rich bool) *zObj {
	if rich {
		return &zObj{fields: cardFields}
	}
	var fields []zField
	for _, f := range cardFields {
		if !containsString(richCardFields, f.name) {
			fields = append(fields, f)
		}
	}
	return &zObj{fields: fields}
}

var (
	richCardShape = cardShape(true)
	coreCardShape = cardShape(false)
)

func cardTools(b turnBinding, be toolBackend) *dynamicTools {
	rich := b.CardSchema == "rich"
	shape := coreCardShape
	if rich {
		shape = richCardShape
	}
	return &dynamicTools{server: "neko_ui",
		list: func(context.Context) ([]any, error) {
			schema := shape.jsonSchema()
			schema.set("$schema", "https://json-schema.org/draft/2020-12/schema")
			schema.set("additionalProperties", false)
			return []any{jsObjOf("name", "render_cards", "description", joinWords(
				"Show the evidence for an answer as cards. Every field is text: a title, a pipe table,",
				"an optional chart of one table column, keyFigures, a callout and followUps."), "inputSchema", schema)}, nil
		},
		call: func(ctx context.Context, name string, args *jsObj) (any, error) {
			if name != "render_cards" {
				return cardResult("Unknown tool: "+name, true), nil
			}
			var input any = jsUndefined{}
			if args != nil {
				input = args
			}
			messages, problems := renderCards(input, rich, "answer-"+uuid.NewString())
			if len(problems) > 0 {
				return cardResult("Cards not shown. Fix and call again:\n- "+strings.Join(problems, "\n- "), true), nil
			}
			if err := be.emitEvent(ctx, jsObjOf("type", "surface", "messages", messages)); err != nil {
				return nil, err
			}
			return cardResult("Cards shown.", false), nil
		}}
}

func cardResult(text string, isError bool) *jsObj {
	out := jsObjOf("content", []any{jsObjOf("type", "text", "text", text)})
	if isError {
		out.set("isError", true)
	}
	return out
}

var cardListFields = []string{"keyFigures", "followUps", "controls", "values", "computed", "map", "diagram"}

func jsonObjectText(v any) any {
	s, ok := v.(string)
	if !ok {
		return v
	}
	if parsed, err := jsParse([]byte(s)); err == nil {
		if obj, ok := parsed.(*jsObj); ok {
			return obj
		}
	}
	return v
}

// normalizeCards is normalizeRenderCardsInput(): models nest the call, send
// it as JSON text, write a list as lines, or fill unused fields with null,
// false or "". Validation still decides what is accepted.
func normalizeCards(value any, rich bool) any {
	answer := jsonObjectText(value)
	for depth := 0; depth < 3; depth++ {
		obj, ok := answer.(*jsObj)
		if !ok {
			break
		}
		if _, hasTitle := obj.vals["title"]; hasTitle {
			break
		}
		var inner any = jsUndefined{}
		if v, ok := obj.vals["arguments"]; ok {
			inner = v
		} else if keys := obj.ordered(); len(keys) == 1 {
			inner = obj.vals[keys[0]]
		}
		nested, ok := jsonObjectText(inner).(*jsObj)
		if !ok {
			break
		}
		answer = nested
	}
	obj, ok := answer.(*jsObj)
	if !ok {
		return answer
	}
	out := &jsObj{}
	for _, field := range obj.ordered() {
		raw := obj.vals[field]
		known := slices.ContainsFunc(cardFields, func(f zField) bool { return f.name == field })
		list, isList := raw.([]any)
		if !known || !rich && containsString(richCardFields, field) || raw == nil || raw == false || raw == "" || isList && len(list) == 0 {
			continue
		}
		if s, ok := raw.(string); ok && containsString(cardListFields, field) {
			items := []any{}
			for _, line := range cardLines(s) {
				if line = strings.TrimFunc(line, isJSSpace); line != "" {
					items = append(items, line)
				}
			}
			raw = items
		}
		out.set(field, raw)
	}
	return out
}

// cardLines splits text into lines; some models write \ and n for a break.
func cardLines(s string) []string { return strings.Split(strings.ReplaceAll(s, `\n`, "\n"), "\n") }

func renderCards(value any, rich bool, surfaceID string) ([]any, []string) {
	shape := coreCardShape
	if rich {
		shape = richCardShape
	}
	var issues []any
	parsed := shape.parse(normalizeCards(value, rich), []any{}, &issues)
	if len(issues) > 0 {
		problems := make([]string, len(issues))
		for i, issue := range issues {
			o := issue.(*jsObj)
			var parts []string
			for _, p := range o.vals["path"].([]any) {
				if n, ok := p.(int); ok {
					parts = append(parts, strconv.Itoa(n))
				} else {
					parts = append(parts, p.(string))
				}
			}
			path := strings.Join(parts, ".")
			if path == "" {
				path = "input"
			}
			problems[i] = path + ": " + o.vals["message"].(string)
		}
		return nil, problems
	}
	a := cardArgs{parsed.(*jsObj)}
	components, problems := a.components()
	if len(problems) > 0 {
		return nil, problems
	}
	root := jsObjOf("id", "root", "component", "Answer", "title", a.str("title"))
	if a.str("stage") == "draft" {
		root.set("stage", "draft")
	}
	ids := make([]any, len(components))
	for i, c := range components {
		ids[i] = c.vals["id"]
	}
	root.set("children", ids)
	all := []any{root}
	for _, c := range components {
		all = append(all, c)
	}
	return []any{jsObjOf("version", a2uiVersion, "createSurface", jsObjOf("surfaceId", surfaceID,
		"catalogId", a2uiCatalogID, "dataModel", &jsObj{}, "components", all))}, nil
}

type cardArgs struct{ o *jsObj }

func (a cardArgs) has(k string) bool { _, ok := a.o.vals[k]; return ok }

func (a cardArgs) str(k string) string { s, _ := a.o.vals[k].(string); return s }

func (a cardArgs) list(k string) []string {
	items, _ := a.o.vals[k].([]any)
	out := make([]string, len(items))
	for i, v := range items {
		out[i] = v.(string)
	}
	return out
}

type cardTable struct {
	columns []string
	rows    [][]string
}

func plainNumber(cell string) (float64, bool) {
	compact := strings.ReplaceAll(strings.TrimFunc(cell, isJSSpace), ",", "")
	if compact == "" {
		return 0, false
	}
	v, ok := jsNumber(compact)
	return v, ok && !math.IsInf(v, 0)
}

func tableCells(line string) []string {
	row := strings.TrimFunc(line, isJSSpace)
	row = strings.TrimPrefix(row, "|")
	row = strings.TrimSuffix(row, "|")
	cells := strings.Split(row, "|")
	for i, c := range cells {
		cells[i] = strings.TrimFunc(c, isJSSpace)
	}
	return cells
}

func readCardTable(source string) (cardTable, string) {
	var rows []string
	for _, line := range cardLines(source) {
		divider := strings.Trim(line, "|-: ") == ""
		if strings.TrimFunc(line, isJSSpace) != "" && !divider {
			rows = append(rows, line)
		}
	}
	if len(rows) < 2 {
		return cardTable{}, "table needs a header row and at least one data row."
	}
	t := cardTable{columns: tableCells(rows[0])}
	for _, line := range rows[1:] {
		t.rows = append(t.rows, tableCells(line))
	}
	for i, row := range t.rows {
		if len(row) != len(t.columns) {
			return cardTable{}, "table row " + strconv.Itoa(i+1) + " needs " + strconv.Itoa(len(t.columns)) + " cells, one per column."
		}
	}
	return t, ""
}

func (t cardTable) numeric(i int) bool {
	if i < 1 {
		return false
	}
	for _, row := range t.rows {
		if _, ok := plainNumber(row[i]); !ok {
			return false
		}
	}
	return true
}

func (t cardTable) component() *jsObj {
	columns := make([]any, len(t.columns))
	decimals := make([]int, len(t.columns))
	for i, label := range t.columns {
		c := jsObjOf("key", "c"+strconv.Itoa(i), "label", label)
		decimals[i] = -1
		if t.numeric(i) {
			c.set("align", "right")
			fraction, largest := false, 0.0
			for _, row := range t.rows {
				v, _ := plainNumber(row[i])
				fraction = fraction || v != math.Trunc(v)
				largest = math.Max(largest, math.Abs(v))
			}
			decimals[i] = 0
			if fraction && largest < 10_000 {
				decimals[i] = 2
			}
		}
		columns[i] = c
	}
	rows := make([]any, len(t.rows))
	for r, row := range t.rows {
		out := &jsObj{}
		for i, cell := range row {
			if decimals[i] >= 0 {
				v, _ := plainNumber(cell)
				cell = formatFixed(v, decimals[i])
			}
			out.set("c"+strconv.Itoa(i), cell)
		}
		rows[r] = out
	}
	return jsObjOf("columns", columns, "rows", rows)
}

func withDrill(c *jsObj, drill string) *jsObj {
	if drill != "" {
		c.set("drill", drill)
	}
	return c
}

func (a cardArgs) chart(t cardTable) (*jsObj, string) {
	requested := -1
	if a.has("chartColumn") {
		want := strings.ToLower(strings.TrimFunc(a.str("chartColumn"), isJSSpace))
		for i, name := range t.columns {
			if strings.ToLower(name) == want {
				requested = i
				break
			}
		}
		if requested < 1 {
			return nil, "chartColumn must be one of the table's value columns: " + strings.Join(t.columns[1:], ", ") + "."
		}
	}
	column := requested
	for i := range t.columns {
		if column >= 1 {
			break
		}
		if t.numeric(i) {
			column = i
		}
	}
	if column < 1 {
		return nil, "chart needs a table column of plain numbers, such as 12699845.92; name it in chartColumn."
	}
	data := make([]any, len(t.rows))
	negative := false
	for i, row := range t.rows {
		v, ok := plainNumber(row[column])
		if !ok {
			return nil, "table row " + strconv.Itoa(i+1) + " needs a plain number in the " + t.columns[column] + " column, such as 12699845.92."
		}
		negative = negative || v < 0
		data[i] = jsObjOf("d", row[0], "v", v)
	}
	if len(data) < 2 || len(data) > 60 {
		return nil, "chart needs 2 to 60 table rows."
	}
	if a.str("chart") == "donut" && (len(data) > 8 || negative) {
		return nil, "a donut needs 2 to 8 rows with nonnegative values."
	}
	return jsObjOf("id", "chart", "component", "Chart", "type", a.str("chart"), "title", a.str("title"),
		"valueLabel", t.columns[column], "data", data), ""
}

func (a cardArgs) rowDetails(t cardTable, problems *[]string) *jsObj {
	if !a.has("rowDetails") {
		return nil
	}
	names := map[string]bool{}
	for _, row := range t.rows {
		names[strings.TrimFunc(row[0], isJSSpace)] = true
	}
	details := &jsObj{}
	for i, entry := range a.list("rowDetails") {
		path := "rowDetails." + strconv.Itoa(i)
		split := strings.Index(entry, ">>")
		name := ""
		if split > 0 {
			name = strings.TrimFunc(entry[:split], isJSSpace)
		}
		if name == "" || !names[name] {
			*problems = append(*problems, path+` needs "Row name >> pipe table", where the row name is a first-column cell of table.`)
			continue
		}
		sub, problem := readCardTable(entry[split+2:])
		if problem != "" {
			*problems = append(*problems, path+": "+strings.Replace(problem, "table ", "the sub-table ", 1))
			continue
		}
		details.set(name, sub.component())
	}
	if len(details.keys) == 0 {
		return nil
	}
	return details
}

func keyFigure(line string, index int) (*jsObj, string) {
	colon := strings.Index(line, ":")
	value := ""
	if colon > 0 {
		value = strings.TrimFunc(line[colon+1:], isJSSpace)
	}
	sub := ""
	if open := strings.LastIndex(value, "("); open > 0 && strings.HasSuffix(value, ")") {
		sub = strings.TrimFunc(value[open+1:len(value)-1], isJSSpace)
		value = strings.TrimFunc(value[:open], isJSSpace)
	}
	if value == "" {
		return nil, "keyFigures." + strconv.Itoa(index) + ` needs the form "Label: value".`
	}
	out := jsObjOf("label", strings.TrimFunc(line[:colon], isJSSpace), "value", value)
	if sub != "" {
		out.set("sub", sub)
	}
	return out, ""
}

var (
	labelUnitRe   = jsRe(`^(.*?)\s*\(([^()]+)\)\s*$`)
	choiceRe      = jsRe(`^\s*([A-Za-z_][A-Za-z0-9_]*)\s*:\s*(.+?)\s*=\s*(.+\|.+)$`)
	choiceOptRe   = jsRe(`^\s*(.+?)\s*\(\s*(-?[\d.,]+)\s*\)\s*$`)
	sliderRe      = jsRe(`(?i)^\s*([A-Za-z_][A-Za-z0-9_]*)\s*:\s*(.+?)\s*=\s*(-?[\d.,]+)\s*\(\s*(-?[\d.,]+)\s*to\s*(-?[\d.,]+)\s*(?:,\s*step\s*([\d.,]+))?\s*\)\s*$`)
	namedValueRe  = jsRe(`^\s*([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(-?[\d.,]+)\s*$`)
	controlNameRe = jsRe(`^\s*([A-Za-z_][A-Za-z0-9_]*)\s*:`)
	valueNameRe   = jsRe(`^\s*([A-Za-z_][A-Za-z0-9_]*)\s*=`)
	mapPointRe    = jsRe(`^\s*(.+?)\s*@\s*(-?\d+(?:\.\d+)?)\s*,\s*(-?\d+(?:\.\d+)?)\s*:\s*(-?[\d.,]+)\s*$`)
)

// labelUnit splits "Label (unit)", with a unit of 1 to 16 UTF-16 units.
func labelUnit(o *jsObj, source string) {
	source = strings.TrimFunc(source, isJSSpace)
	if m := labelUnitRe.FindStringSubmatch(source); m != nil && utf16Len(m[2]) <= 16 {
		o.set("label", strings.TrimFunc(m[1], isJSSpace))
		o.set("unit", strings.TrimFunc(m[2], isJSSpace))
		return
	}
	o.set("label", source)
}

type cardControl struct {
	o        *jsObj
	name     string
	value    float64
	hasValue bool
}

func control(line string, index int) (cardControl, string) {
	fault := func(message string) (cardControl, string) {
		return cardControl{}, "controls." + strconv.Itoa(index) + " " + message
	}
	if m := choiceRe.FindStringSubmatch(line); m != nil {
		var options []any
		var values []float64
		for _, part := range strings.Split(m[3], "|") {
			om := choiceOptRe.FindStringSubmatch(part)
			if om == nil {
				return fault(`needs each choice in the form "Option (number)", separated by |.`)
			}
			v, ok := plainNumber(om[2])
			if !ok {
				return fault(`needs each choice in the form "Option (number)", separated by |.`)
			}
			options = append(options, jsObjOf("label", om[1], "value", v))
			values = append(values, v)
		}
		if len(options) > 12 {
			return fault("has more than 12 choices.")
		}
		name := strings.ToLower(m[1])
		o := jsObjOf("name", name)
		labelUnit(o, m[2])
		o.set("value", values[0])
		o.set("min", slices.Min(values))
		o.set("max", slices.Max(values))
		o.set("step", 1.0)
		o.set("options", options)
		return cardControl{o: o, name: name, value: values[0], hasValue: true}, ""
	}
	m := sliderRe.FindStringSubmatch(line)
	if m == nil {
		return fault(`needs the form "name: Label (unit) = default (min to max, step n)".`)
	}
	value, hasValue := plainNumber(m[3])
	lo, loOK := plainNumber(m[4])
	hi, hiOK := plainNumber(m[5])
	if !loOK || !hiOK || !(lo < hi) {
		return fault("needs min below max.")
	}
	if hasValue && (value < lo || value > hi) {
		return fault("needs its default between min and max.")
	}
	step, hasStep := 0.0, false
	if m[6] != "" {
		step, hasStep = plainNumber(m[6])
	}
	if hasStep && !(step > 0) {
		return fault("needs a step above 0.")
	}
	if !hasStep {
		step = jsToPrecision2((hi - lo) / 100)
	}
	name := strings.ToLower(m[1])
	o := jsObjOf("name", name)
	labelUnit(o, m[2])
	if hasValue {
		o.set("value", value)
	}
	o.set("min", lo)
	o.set("max", hi)
	o.set("step", step)
	return cardControl{o: o, name: name, value: value, hasValue: hasValue}, ""
}

func (a cardArgs) whatIf(problems *[]string) *jsObj {
	if !a.has("controls") && !a.has("computed") {
		return nil
	}
	if !a.has("controls") || !a.has("computed") {
		*problems = append(*problems, "controls and computed go together: the controls move, the computed results follow.")
		return nil
	}
	lines := a.list("controls")
	var controls []any
	scope := map[string]float64{}
	declared := map[string]bool{}
	for i, line := range lines {
		if m := controlNameRe.FindStringSubmatch(line); m != nil {
			declared[strings.ToLower(m[1])] = true
		}
		c, problem := control(line, i)
		if problem != "" {
			*problems = append(*problems, problem)
			continue
		}
		controls = append(controls, c.o)
		scope[c.name] = math.NaN()
		if c.hasValue {
			scope[c.name] = c.value
		}
	}
	values := &jsObj{}
	valueLines := a.list("values")
	for i, line := range valueLines {
		if m := valueNameRe.FindStringSubmatch(line); m != nil {
			declared[strings.ToLower(m[1])] = true
		}
		m := namedValueRe.FindStringSubmatch(line)
		var v float64
		ok := m != nil
		if ok {
			v, ok = plainNumber(m[2])
		}
		if !ok {
			*problems = append(*problems, "values."+strconv.Itoa(i)+` needs the form "name = number".`)
			continue
		}
		name := strings.ToLower(m[1])
		values.set(name, v)
		scope[name] = v
	}
	if len(declared) != len(lines)+len(valueLines) {
		*problems = append(*problems, "each control and value needs its own name.")
	}
	valid := len(controls) == len(lines)
	outputs := []any{}
	for i, line := range a.list("computed") {
		path := "computed." + strconv.Itoa(i)
		colon := strings.Index(line, ":")
		if colon < 1 {
			*problems = append(*problems, path+` needs the form "Label (unit): formula".`)
			continue
		}
		expression := strings.TrimFunc(line[colon+1:], isJSSpace)
		parsed, err := parseExpr(expression)
		if err != nil {
			*problems = append(*problems, path+": "+err.Error()+".")
			continue
		}
		var order []string
		parsed.names(map[string]bool{}, &order)
		var unknown []string
		for _, n := range order {
			if !declared[n] {
				unknown = append(unknown, n)
			}
		}
		if len(unknown) > 0 {
			*problems = append(*problems, path+" uses "+strings.Join(unknown, ", ")+"; name each one in controls or values.")
			continue
		}
		if v := parsed.eval(scope); valid && (math.IsNaN(v) || math.IsInf(v, 0)) {
			*problems = append(*problems, path+" gives no finite number at the default control values.")
			continue
		}
		out := &jsObj{}
		labelUnit(out, line[:colon])
		out.set("expression", expression)
		outputs = append(outputs, out)
	}
	if controls == nil {
		controls = []any{}
	}
	return jsObjOf("id", "whatIf", "component", "WhatIf", "controls", controls, "values", values, "outputs", outputs)
}

func (a cardArgs) mapComponent(problems *[]string) *jsObj {
	if !a.has("map") {
		return nil
	}
	points := []any{}
	negative := false
	for i, line := range a.list("map") {
		m := mapPointRe.FindStringSubmatch(line)
		var v, lat, lon float64
		ok := m != nil
		if ok {
			v, ok = plainNumber(m[4])
			lat, _ = strconv.ParseFloat(m[2], 64)
			lon, _ = strconv.ParseFloat(m[3], 64)
		}
		if !ok || math.Abs(lat) > 90 || math.Abs(lon) > 180 {
			*problems = append(*problems, "map."+strconv.Itoa(i)+` needs the form "Place @ latitude, longitude: value" with latitude -90 to 90 and longitude -180 to 180.`)
			continue
		}
		negative = negative || v < 0
		points = append(points, jsObjOf("label", m[1], "lat", lat, "lon", lon, "v", v))
	}
	if negative {
		*problems = append(*problems, "map values need to be 0 or more.")
	}
	valueLabel := "Value"
	if a.has("mapValue") {
		valueLabel = a.str("mapValue")
	}
	return withDrill(jsObjOf("id", "map", "component", "AnswerMap", "title", a.str("title"), "valueLabel", valueLabel, "points", points), a.str("drillDown"))
}

func (a cardArgs) diagram(problems *[]string) *jsObj {
	if !a.has("diagram") {
		return nil
	}
	ids := map[string]string{}
	nodes := []any{}
	node := func(label string) string {
		key := strings.ToLower(label)
		if _, ok := ids[key]; !ok {
			ids[key] = label
			nodes = append(nodes, jsObjOf("id", label, "label", label))
		}
		return ids[key]
	}
	edges := []any{}
	for i, line := range a.list("diagram") {
		arrow := strings.Index(line, "->")
		if arrow < 0 {
			if label := strings.TrimFunc(line, isJSSpace); label != "" {
				node(label)
			}
			continue
		}
		from := strings.TrimFunc(line[:arrow], isJSSpace)
		rest := line[arrow+2:]
		to, label, _ := strings.Cut(rest, ":")
		to = strings.TrimFunc(to, isJSSpace)
		label = strings.TrimFunc(label, isJSSpace)
		if from == "" || to == "" {
			*problems = append(*problems, "diagram."+strconv.Itoa(i)+` needs the form "From -> To" with an optional ": label".`)
			continue
		}
		edge := jsObjOf("from", node(from), "to", node(to))
		if label != "" {
			edge.set("label", label)
		}
		edges = append(edges, edge)
	}
	if len(nodes) > 30 {
		*problems = append(*problems, "a diagram holds up to 30 steps.")
	}
	return withDrill(jsObjOf("id", "diagram", "component", "Diagram", "title", a.str("title"), "nodes", nodes, "edges", edges), a.str("drillDown"))
}

var cardSections = []string{"table", "keyFigures", "callout", "followUps", "computed", "map", "diagram", "tool"}

func (a cardArgs) components() ([]*jsObj, []string) {
	var out []*jsObj
	var problems []string
	if !slices.ContainsFunc(cardSections, a.has) {
		problems = append(problems, "Fill at least one of "+strings.Join(cardSections, ", ")+".")
	}
	drill := a.str("drillDown")
	if a.has("drillDown") && !strings.Contains(drill, "{label}") {
		problems = append(problems, "drillDown needs {label} where the item's name goes.")
	}
	if a.has("keyFigures") {
		items := []any{}
		for i, line := range a.list("keyFigures") {
			figure, problem := keyFigure(line, i)
			if problem != "" {
				problems = append(problems, problem)
				continue
			}
			items = append(items, figure)
		}
		out = append(out, jsObjOf("id", "keyFigures", "component", "KeyFigures", "items", items))
	}
	for _, c := range []*jsObj{a.mapComponent(&problems), a.diagram(&problems)} {
		if c != nil {
			out = append(out, c)
		}
	}
	var table cardTable
	tableOK := false
	if a.has("table") {
		var problem string
		if table, problem = readCardTable(a.str("table")); problem != "" {
			problems = append(problems, problem)
		} else {
			tableOK = true
		}
	}
	if a.has("chart") && !a.has("table") {
		problems = append(problems, "chart draws from table; add a table.")
	}
	compare := a.str("layout") == "compare"
	if compare && !a.has("table") {
		problems = append(problems, "compare shows table rows side by side; add a table.")
	}
	if a.has("chart") && tableOK {
		if chart, problem := a.chart(table); problem != "" {
			problems = append(problems, problem)
		} else {
			out = append(out, withDrill(chart, drill))
		}
	}
	if tableOK {
		if compare && (len(table.rows) < 2 || len(table.rows) > 6) {
			problems = append(problems, "compare needs 2 to 6 table rows; use the report layout for more.")
		}
		details := a.rowDetails(table, &problems)
		kind := "Table"
		if compare {
			kind = "Compare"
		}
		c := jsObjOf("id", "table", "component", kind)
		rows := table.component()
		c.set("columns", rows.vals["columns"])
		c.set("rows", rows.vals["rows"])
		if details != nil {
			c.set("details", details)
		}
		out = append(out, withDrill(c, drill))
	} else if a.has("rowDetails") {
		problems = append(problems, "rowDetails open under table rows; add a table.")
	}
	if w := a.whatIf(&problems); w != nil {
		out = append(out, w)
	}
	if a.has("tool") {
		if answerToolReachesOut(a.str("tool")) {
			problems = append(problems, "tool runs offline inside the answer; put every figure it needs in the fragment, and load, open, or navigate to nothing outside it.")
		} else {
			out = append(out, jsObjOf("id", "tool", "component", "AnswerTool", "title", a.str("title"), "html", a.str("tool")))
		}
	}
	if a.has("callout") {
		mood := "watch"
		if a.has("mood") {
			mood = a.str("mood")
		}
		out = append(out, jsObjOf("id", "callout", "component", "Callout", "mood", mood, "text", a.str("callout")))
	}
	if a.has("followUps") {
		options := []any{}
		for _, prompt := range a.list("followUps") {
			options = append(options, jsObjOf("label", prompt, "prompt", prompt))
		}
		out = append(out, jsObjOf("id", "followUps", "component", "Choice", "options", options))
	}
	return out, problems
}
