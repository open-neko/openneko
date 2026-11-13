package webfront

import (
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
}

type zArr struct {
	elem     zType
	min, max *float64
	desc     string
}

type zField struct {
	name     string
	typ      zType
	optional bool
}

type zObj struct {
	fields []zField
	strict bool
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
	zLengthIssues(text, s.min, s.max, path, issues)
	return text
}

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
	return out
}

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
		if !present {
			if !f.optional {
				*issues = append(*issues, zTypeIssue(f.typ.expected(), jsUndefined{}, zPath(path, f.name)))
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
		props.set(f.name, f.typ.jsonSchema())
		if !f.optional {
			required = append(required, f.name)
		}
	}
	out := jsObjOf("type", "object", "properties", props, "required", required)
	if o.strict {
		out.set("additionalProperties", false)
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
