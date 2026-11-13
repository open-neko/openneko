package webfront

import (
	"math"
	"regexp"
	"slices"
	"strings"
)

// zType is the part of a zod v4 schema that the tool servers use: it parses
// a JSON.parse value as zod does, with zod's issues in zod's order, and
// gives the JSON Schema that z.toJSONSchema gives.
type zType interface {
	parse(v any, path []any, issues *[]any) any
	jsonSchema() *jsObj
	expected() string
}

type zStr struct {
	trim     bool
	min, max *float64
	enum     []string
	desc     string
	descLast string
	datetime bool
	pattern  *regexp.Regexp
	refine   func(string) bool
}

// zInt is z.number().int() with min, max and positive().
type zInt struct {
	min, max *float64
	positive bool
}

// zAny is z.unknown().
type zAny struct{}

type zArr struct {
	elem     zType
	min, max *float64
	desc     string
}

// zField is one key of an object. desc and def belong to the optional or
// default wrapper, so the JSON Schema puts them before the type's keys.
type zField struct {
	name     string
	typ      zType
	optional bool
	desc     string
	def      func() any
}

type zObj struct {
	fields []zField
	strict bool
	desc   string
}

func (f zField) jsonSchema() *jsObj {
	if f.def == nil && f.desc == "" {
		return f.typ.jsonSchema()
	}
	out := &jsObj{}
	if f.def != nil {
		out.set("default", f.def())
	}
	if f.desc != "" {
		out.set("description", f.desc)
	}
	inner := f.typ.jsonSchema()
	for _, k := range inner.ordered() {
		out.set(k, inner.vals[k])
	}
	return out
}

type zRecord struct {
	key, val zType
}

func zText(min, max float64) *zStr { return &zStr{trim: true, min: num(min), max: num(max)} }

func zEnum(desc string, values ...string) *zStr { return &zStr{enum: values, desc: desc} }

func zPath(path []any, more ...any) []any { return append(slices.Clone(path), more...) }

func zTypeIssue(expected string, v any, path []any) *jsObj {
	received := "undefined"
	if _, undefined := v.(jsUndefined); !undefined {
		received = jsTypeName(v)
	}
	return zodIssue("expected", expected, "code", "invalid_type", "path", path,
		"message", "Invalid input: expected "+expected+", received "+received)
}

func (s *zStr) expected() string { return "string" }

func (s *zStr) parse(v any, path []any, issues *[]any) any {
	text, ok := v.(string)
	if s.enum != nil {
		if !ok || !containsString(s.enum, text) {
			values := make([]any, len(s.enum))
			quoted := make([]string, len(s.enum))
			for i, e := range s.enum {
				values[i] = e
				quoted[i] = jsStringify(e, false)
			}
			*issues = append(*issues, zodIssue("code", "invalid_value", "values", values, "path", path,
				"message", "Invalid option: expected one of "+strings.Join(quoted, "|")))
		}
		return v
	}
	if !ok {
		*issues = append(*issues, zTypeIssue("string", v, path))
		zLengthIssues(v, s.min, s.max, path, issues)
		return v
	}
	if s.trim {
		text = strings.TrimFunc(text, isJSSpace)
	}
	if s.pattern != nil && !s.pattern.MatchString(text) {
		shown := "/" + s.pattern.String() + "/"
		*issues = append(*issues, zodIssue("origin", "string", "code", "invalid_format", "format", "regex", "pattern", shown,
			"path", path, "message", "Invalid string: must match pattern "+shown))
	}
	zLengthIssues(text, s.min, s.max, path, issues)
	if s.refine != nil && !s.refine(text) {
		*issues = append(*issues, zodIssue("code", "custom", "path", path, "message", "Invalid input"))
	}
	if s.datetime && !zodDatetime.MatchString(text) {
		*issues = append(*issues, zodIssue("origin", "string", "code", "invalid_format", "format", "datetime",
			"pattern", "/"+zodDatetimeText+"/", "path", path, "message", "Invalid ISO datetime"))
	}
	return text
}

const zodDatetimeText = `^(?:(?:\d\d[2468][048]|\d\d[13579][26]|\d\d0[48]|[02468][048]00|[13579][26]00)-02-29|\d{4}-(?:(?:0[13578]|1[02])-(?:0[1-9]|[12]\d|3[01])|(?:0[469]|11)-(?:0[1-9]|[12]\d|30)|(?:02)-(?:0[1-9]|1\d|2[0-8])))T(?:(?:[01]\d|2[0-3]):[0-5]\d(?::[0-5]\d(?:\.\d+)?)?(?:Z))$`

var zodDatetime = regexp.MustCompile(zodDatetimeText)

// zLengthIssues is zod's minLength and maxLength checks. They run on any
// value with a length, also after a type issue, and name the value's own kind.
func zLengthIssues(v any, min, max *float64, path []any, issues *[]any) {
	var origin, unit string
	var n float64
	switch t := v.(type) {
	case string:
		origin, unit, n = "string", " characters", float64(utf16Len(t))
	case []any:
		origin, unit, n = "array", " items", float64(len(t))
	case *jsObj:
		length, ok := t.vals["length"].(float64)
		if !ok {
			return
		}
		origin, n = "unknown", length
	default:
		return
	}
	expect := func(op string, limit float64) string {
		if unit == "" {
			return "expected " + origin + " to be " + op + jsNumberText(limit)
		}
		return "expected " + origin + " to have " + op + jsNumberText(limit) + unit
	}
	if min != nil && n < *min {
		*issues = append(*issues, zodIssue("origin", origin, "code", "too_small", "minimum", *min, "inclusive", true, "path", path,
			"message", "Too small: "+expect(">=", *min)))
	}
	if max != nil && n > *max {
		*issues = append(*issues, zodIssue("origin", origin, "code", "too_big", "maximum", *max, "inclusive", true, "path", path,
			"message", "Too big: "+expect("<=", *max)))
	}
}

func (s *zStr) jsonSchema() *jsObj {
	out := &jsObj{}
	if s.desc != "" {
		out.set("description", s.desc)
	}
	out.set("type", "string")
	if s.datetime {
		out.set("format", "date-time")
		out.set("pattern", zodDatetimeText)
	}
	if s.enum != nil {
		values := make([]any, len(s.enum))
		for i, e := range s.enum {
			values[i] = e
		}
		out.set("enum", values)
		return out
	}
	if s.min != nil {
		out.set("minLength", *s.min)
	}
	if s.max != nil {
		out.set("maxLength", *s.max)
	}
	if s.pattern != nil {
		out.set("pattern", s.pattern.String())
	}
	if s.descLast != "" {
		out.set("description", s.descLast)
	}
	return out
}

func (n *zInt) expected() string { return "number" }

func (n *zInt) parse(v any, path []any, issues *[]any) any {
	f, ok := v.(float64)
	if !ok {
		*issues = append(*issues, zTypeIssue("number", v, path))
		return v
	}
	if f != math.Trunc(f) || math.Abs(f) > maxSafeInt {
		*issues = append(*issues, zodIssue("expected", "int", "format", "safeint", "code", "invalid_type", "path", path,
			"message", "Invalid input: expected int, received number"))
		return v
	}
	if n.positive && f <= 0 {
		*issues = append(*issues, zodIssue("origin", "number", "code", "too_small", "minimum", 0.0, "inclusive", false, "path", path,
			"message", "Too small: expected number to be >0"))
	}
	if n.min != nil && f < *n.min {
		*issues = append(*issues, zodIssue("origin", "number", "code", "too_small", "minimum", *n.min, "inclusive", true, "path", path,
			"message", "Too small: expected number to be >="+jsNumberText(*n.min)))
	}
	if n.max != nil && f > *n.max {
		*issues = append(*issues, zodIssue("origin", "number", "code", "too_big", "maximum", *n.max, "inclusive", true, "path", path,
			"message", "Too big: expected number to be <="+jsNumberText(*n.max)))
	}
	return f
}

func (n *zInt) jsonSchema() *jsObj {
	out := jsObjOf("type", "integer")
	if n.positive {
		out.set("exclusiveMinimum", 0.0)
	}
	if n.min != nil {
		out.set("minimum", *n.min)
	}
	if n.max != nil {
		out.set("maximum", *n.max)
	}
	return out
}

func (zAny) expected() string                   { return "unknown" }
func (zAny) parse(v any, _ []any, _ *[]any) any { return v }
func (zAny) jsonSchema() *jsObj                 { return &jsObj{} }

func (a *zArr) expected() string { return "array" }

func (a *zArr) parse(v any, path []any, issues *[]any) any {
	items, ok := v.([]any)
	if !ok {
		*issues = append(*issues, zTypeIssue("array", v, path))
		zLengthIssues(v, a.min, a.max, path, issues)
		return v
	}
	out := make([]any, len(items))
	for i, item := range items {
		out[i] = a.elem.parse(item, zPath(path, i), issues)
	}
	zLengthIssues(items, a.min, a.max, path, issues)
	return out
}

func (a *zArr) jsonSchema() *jsObj {
	out := &jsObj{}
	if a.desc != "" {
		out.set("description", a.desc)
	}
	if a.min != nil {
		out.set("minItems", *a.min)
	}
	if a.max != nil {
		out.set("maxItems", *a.max)
	}
	out.set("type", "array")
	out.set("items", a.elem.jsonSchema())
	return out
}

func (o *zObj) expected() string { return "object" }

func (o *zObj) parse(v any, path []any, issues *[]any) any {
	in, ok := v.(*jsObj)
	if !ok {
		*issues = append(*issues, zTypeIssue("object", v, path))
		return v
	}
	out := &jsObj{}
	for _, f := range o.fields {
		value, present := in.vals[f.name]
		if !present && f.def != nil {
			out.set(f.name, f.def())
			continue
		}
		if !present {
			if !f.optional {
				f.typ.parse(jsUndefined{}, zPath(path, f.name), issues)
			}
			continue
		}
		out.set(f.name, f.typ.parse(value, zPath(path, f.name), issues))
	}
	if o.strict {
		var unknown []any
		var quoted []string
		for _, k := range in.ordered() {
			if !slices.ContainsFunc(o.fields, func(f zField) bool { return f.name == k }) {
				unknown = append(unknown, k)
				quoted = append(quoted, `"`+k+`"`)
			}
		}
		if len(unknown) > 0 {
			word := "key"
			if len(unknown) > 1 {
				word = "keys"
			}
			*issues = append(*issues, zodIssue("code", "unrecognized_keys", "keys", unknown, "path", zPath(path),
				"message", "Unrecognized "+word+": "+strings.Join(quoted, ", ")))
		}
	}
	return out
}

func (o *zObj) jsonSchema() *jsObj {
	props := &jsObj{}
	required := []any{}
	for _, f := range o.fields {
		props.set(f.name, f.jsonSchema())
		if !f.optional && f.def == nil {
			required = append(required, f.name)
		}
	}
	out := jsObjOf("type", "object", "properties", props)
	if len(required) > 0 {
		out.set("required", required)
	}
	if o.strict {
		out.set("additionalProperties", false)
	}
	if o.desc != "" {
		out.set("description", o.desc)
	}
	return out
}

func (r *zRecord) expected() string { return "record" }

func (r *zRecord) parse(v any, path []any, issues *[]any) any {
	in, ok := v.(*jsObj)
	if !ok {
		*issues = append(*issues, zTypeIssue("record", v, path))
		return v
	}
	out := &jsObj{}
	for _, k := range in.ordered() {
		var keyIssues []any
		key := r.key.parse(k, []any{}, &keyIssues)
		if len(keyIssues) > 0 {
			*issues = append(*issues, zodIssue("code", "invalid_key", "origin", "record", "issues", keyIssues,
				"path", zPath(path, k), "message", "Invalid key in record"))
			continue
		}
		out.set(key.(string), r.val.parse(in.vals[k], zPath(path, k), issues))
	}
	return out
}

func (r *zRecord) jsonSchema() *jsObj {
	return jsObjOf("type", "object", "propertyNames", r.key.jsonSchema(), "additionalProperties", r.val.jsonSchema())
}

// zPrim is z.boolean(), z.number() or z.null().
type zPrim string

func (p zPrim) expected() string { return string(p) }

func (p zPrim) parse(v any, path []any, issues *[]any) any {
	ok := false
	switch p {
	case "boolean":
		_, ok = v.(bool)
	case "number":
		_, ok = v.(float64)
	case "null":
		ok = v == nil
	}
	if !ok {
		*issues = append(*issues, zTypeIssue(string(p), v, path))
	}
	return v
}

func (p zPrim) jsonSchema() *jsObj { return jsObjOf("type", string(p)) }

// zUnion is z.union() of options that fail with a type issue only.
type zUnion []zType

func (u zUnion) expected() string { return "union" }

func (u zUnion) parse(v any, path []any, issues *[]any) any {
	errs := make([]any, 0, len(u))
	for _, option := range u {
		var own []any
		out := option.parse(v, []any{}, &own)
		if len(own) == 0 {
			return out
		}
		errs = append(errs, own)
	}
	*issues = append(*issues, zodIssue("code", "invalid_union", "errors", errs, "path", path, "message", "Invalid input"))
	return v
}

func (u zUnion) jsonSchema() *jsObj {
	options := make([]any, len(u))
	for i, option := range u {
		options[i] = option.jsonSchema()
	}
	return jsObjOf("anyOf", options)
}

// zNullable is .nullable(): null passes, anything else goes to the inner type.
type zNullable struct{ inner zType }

func (n zNullable) expected() string { return n.inner.expected() }

func (n zNullable) parse(v any, path []any, issues *[]any) any {
	if v == nil {
		return nil
	}
	return n.inner.parse(v, path, issues)
}

func (n zNullable) jsonSchema() *jsObj {
	return jsObjOf("anyOf", []any{n.inner.jsonSchema(), zPrim("null").jsonSchema()})
}
