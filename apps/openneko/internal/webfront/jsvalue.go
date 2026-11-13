package webfront

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"
)

// jsObj is a JavaScript object from JSON.parse: integer-like keys first in
// numeric order, then the other keys in insertion order. A repeated key keeps
// its first place and takes the last value.
type jsObj struct {
	keys []string
	vals map[string]any
}

func (o *jsObj) set(k string, v any) {
	if o.vals == nil {
		o.vals = map[string]any{}
	}
	if _, ok := o.vals[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.vals[k] = v
}

func (o *jsObj) MarshalJSON() ([]byte, error) { return []byte(jsStringify(o, false)), nil }

func (o *jsObj) ordered() []string {
	keys := slices.Clone(o.keys)
	slices.SortStableFunc(keys, func(x, y string) int {
		xi, xok := arrayIndex(x)
		yi, yok := arrayIndex(y)
		switch {
		case xok && yok:
			return cmp.Compare(xi, yi)
		case xok:
			return -1
		case yok:
			return 1
		}
		return 0
	})
	return keys
}

// jsParse is JSON.parse: objects become *jsObj, numbers float64.
func jsParse(raw []byte) (any, error) {
	if !json.Valid(raw) {
		return nil, errors.New("invalid JSON")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	return jsParseValue(dec)
}

func jsParseValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		if t == '[' {
			list := []any{}
			for dec.More() {
				v, err := jsParseValue(dec)
				if err != nil {
					return nil, err
				}
				list = append(list, v)
			}
			_, err := dec.Token()
			return list, err
		}
		obj := &jsObj{vals: map[string]any{}}
		for dec.More() {
			k, err := dec.Token()
			if err != nil {
				return nil, err
			}
			v, err := jsParseValue(dec)
			if err != nil {
				return nil, err
			}
			obj.set(k.(string), v)
		}
		_, err := dec.Token()
		return obj, err
	case json.Number:
		f, err := strconv.ParseFloat(string(t), 64)
		if err != nil && !errors.Is(err, strconv.ErrRange) {
			return nil, err
		}
		return f, nil
	}
	return tok, nil
}

// jsStringify is JSON.stringify(v), or JSON.stringify(v, null, 2) when
// indent is true.
func jsStringify(v any, indent bool) string {
	if indent {
		return jsStringifyIndent(v, "  ")
	}
	var b strings.Builder
	writeJS(&b, v, false)
	return b.String()
}

// jsStringifyIndent is JSON.stringify(v, null, indent) for an indent of spaces.
func jsStringifyIndent(v any, indent string) string {
	var b strings.Builder
	writeJS(&b, v, false)
	var out bytes.Buffer
	if err := json.Indent(&out, []byte(b.String()), "", indent); err != nil {
		return b.String()
	}
	return out.String()
}

// jsCanonical is canonicalJson(): keys sorted by UTF-16 code units.
func jsCanonical(v any) string {
	var b strings.Builder
	writeJS(&b, v, true)
	return b.String()
}

func writeJS(b *strings.Builder, v any, sorted bool) {
	switch t := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		b.WriteString(strconv.FormatBool(t))
	case string:
		writeJSString(b, t)
	case float64:
		b.WriteString(jsNumberText(t))
	case int:
		b.WriteString(strconv.Itoa(t))
	case []any:
		b.WriteByte('[')
		for i, item := range t {
			if i > 0 {
				b.WriteByte(',')
			}
			writeJS(b, item, sorted)
		}
		b.WriteByte(']')
	case *jsObj:
		keys := t.ordered()
		if sorted {
			slices.SortFunc(keys, func(x, y string) int {
				return slices.Compare(utf16.Encode([]rune(x)), utf16.Encode([]rune(y)))
			})
		}
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			writeJSString(b, k)
			b.WriteByte(':')
			writeJS(b, t.vals[k], sorted)
		}
		b.WriteByte('}')
	}
}

func jsNumberText(f float64) string {
	if math.IsInf(f, 0) || math.IsNaN(f) {
		return "null"
	}
	if f == 0 {
		return "0"
	}
	out, _ := json.Marshal(f)
	return string(out)
}

// largestArray is largestArrayLength(): the longest array at any depth.
func largestArray(v any) int {
	largest := 0
	switch t := v.(type) {
	case []any:
		largest = len(t)
		for _, item := range t {
			largest = max(largest, largestArray(item))
		}
	case *jsObj:
		for _, item := range t.vals {
			largest = max(largest, largestArray(item))
		}
	}
	return largest
}

// jsSpread is {...v} for a value read from a jsonb column.
func jsSpread(v any) *jsObj {
	out := &jsObj{vals: map[string]any{}}
	switch t := v.(type) {
	case *jsObj:
		for _, k := range t.ordered() {
			out.set(k, t.vals[k])
		}
	case []any:
		for i, item := range t {
			out.set(strconv.Itoa(i), item)
		}
	case string:
		for i, unit := range utf16.Encode([]rune(t)) {
			out.set(strconv.Itoa(i), string(utf16.Decode([]uint16{unit})))
		}
	}
	return out
}

// jsToFixed2 is n.toFixed(2): the exact binary value, a tie rounds up.
func jsToFixed2(n float64) string {
	neg := n < 0
	exact := strconv.FormatFloat(math.Abs(n), 'f', 1100, 64)
	dot := strings.IndexByte(exact, '.')
	whole, frac := exact[:dot], exact[dot+1:]
	cents, _ := strconv.ParseInt(whole+frac[:2], 10, 64)
	if frac[2] >= '5' {
		cents++
	}
	s := strconv.FormatInt(cents/100, 10) + "." + strconv.FormatInt(cents%100+100, 10)[1:]
	if neg && cents != 0 {
		s = "-" + s
	}
	return s
}

// sendJSValue writes a body that holds values from jsParse.
func sendJSValue(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(jsStringify(body, false)))
}
