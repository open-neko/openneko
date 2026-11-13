package webfront

import (
	"errors"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// jsRe compiles a JavaScript pattern whose \s and . must keep their
// JavaScript meaning: \s takes the Unicode spaces and . stops at every line
// terminator, where RE2 takes ASCII spaces and stops only at \n.
func jsRe(pattern string) *regexp.Regexp {
	pattern = strings.ReplaceAll(pattern, `[^\s`, `[^`+jsSpaceClass)
	pattern = strings.ReplaceAll(pattern, `\s`, `[`+jsSpaceClass+`]`)
	pattern = strings.ReplaceAll(pattern, `(.`, `([^\n\r\x{2028}\x{2029}]`)
	pattern = strings.ReplaceAll(pattern, `|.`, `|[^\n\r\x{2028}\x{2029}]`)
	return regexp.MustCompile(pattern)
}

// jsNumberString is String(n).
func jsNumberString(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	}
	return jsNumberText(f)
}

type expr struct {
	kind        string
	value       float64
	name, op    string
	left, right *expr
	args        []*expr
}

var exprArity = map[string][2]int{"min": {1, 8}, "max": {1, 8}, "abs": {1, 1}, "floor": {1, 1}, "ceil": {1, 1}, "round": {1, 2}}

type exprToken struct {
	kind  string
	num   float64
	value string
}

var (
	exprNumber = regexp.MustCompile(`^(\d+(\.\d*)?|\.\d+)([eE][+-]?\d+)?`)
	exprName   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*`)
)

func tokenizeExpr(source string) ([]exprToken, error) {
	var tokens []exprToken
	for i := 0; i < len(source); {
		rest := source[i:]
		r := []rune(rest)[0]
		if jsSpace(r) {
			i += len(string(r))
			continue
		}
		if m := exprNumber.FindString(rest); m != "" {
			n, _ := strconv.ParseFloat(m, 64)
			tokens = append(tokens, exprToken{kind: "number", num: n})
			i += len(m)
			continue
		}
		if m := exprName.FindString(rest); m != "" {
			tokens = append(tokens, exprToken{kind: "name", value: strings.ToLower(m)})
			i += len(m)
			continue
		}
		if strings.ContainsRune("+-*/^(),", r) {
			tokens = append(tokens, exprToken{kind: "op", value: string(r)})
			i++
			continue
		}
		return nil, errors.New(`unexpected "` + string(r) + `"`)
	}
	return tokens, nil
}

func parseExpr(source string) (*expr, error) {
	tokens, err := tokenizeExpr(source)
	if err != nil {
		return nil, err
	}
	pos := 0
	takeOp := func(v string) bool {
		if pos < len(tokens) && tokens[pos].kind == "op" && tokens[pos].value == v {
			pos++
			return true
		}
		return false
	}
	var additive, power func() (*expr, error)
	primary := func() (*expr, error) {
		if pos >= len(tokens) {
			return nil, errors.New("the formula ends early")
		}
		t := tokens[pos]
		pos++
		switch {
		case t.kind == "number":
			return &expr{kind: "number", value: t.num}, nil
		case t.kind == "name":
			if !takeOp("(") {
				return &expr{kind: "name", name: t.value}, nil
			}
			arity, ok := exprArity[t.value]
			if !ok {
				return nil, errors.New("unknown function " + t.value)
			}
			var args []*expr
			if !takeOp(")") {
				for {
					arg, err := additive()
					if err != nil {
						return nil, err
					}
					args = append(args, arg)
					if !takeOp(",") {
						break
					}
				}
				if !takeOp(")") {
					return nil, errors.New("close " + t.value + "( with )")
				}
			}
			if len(args) < arity[0] || len(args) > arity[1] {
				count := strconv.Itoa(arity[0])
				if arity[0] != arity[1] {
					count += " to " + strconv.Itoa(arity[1])
				}
				return nil, errors.New(t.value + " takes " + count + " values")
			}
			return &expr{kind: "call", name: t.value, args: args}, nil
		case t.value == "(":
			inner, err := additive()
			if err != nil {
				return nil, err
			}
			if !takeOp(")") {
				return nil, errors.New("close ( with )")
			}
			return inner, nil
		case t.value == "-":
			operand, err := power()
			if err != nil {
				return nil, err
			}
			return &expr{kind: "negate", left: operand}, nil
		case t.value == "+":
			return power()
		}
		return nil, errors.New(`unexpected "` + t.value + `"`)
	}
	power = func() (*expr, error) {
		base, err := primary()
		if err != nil || !takeOp("^") {
			return base, err
		}
		exp, err := power()
		if err != nil {
			return nil, err
		}
		return &expr{kind: "binary", op: "^", left: base, right: exp}, nil
	}
	binary := func(next func() (*expr, error), ops ...string) func() (*expr, error) {
		return func() (*expr, error) {
			left, err := next()
			if err != nil {
				return nil, err
			}
			for {
				op := ""
				for _, o := range ops {
					if takeOp(o) {
						op = o
						break
					}
				}
				if op == "" {
					return left, nil
				}
				right, err := next()
				if err != nil {
					return nil, err
				}
				left = &expr{kind: "binary", op: op, left: left, right: right}
			}
		}
	}
	additive = binary(binary(power, "*", "/"), "+", "-")
	if len(tokens) == 0 {
		return nil, errors.New("the formula is empty")
	}
	e, err := additive()
	if err != nil {
		return nil, err
	}
	if pos < len(tokens) {
		t := tokens[pos]
		if t.kind == "number" {
			return nil, errors.New(`unexpected "` + jsNumberString(t.num) + `"`)
		}
		return nil, errors.New(`unexpected "` + t.value + `"`)
	}
	return e, nil
}

func (e *expr) names(into map[string]bool, order *[]string) {
	switch e.kind {
	case "name":
		if !into[e.name] {
			into[e.name] = true
			*order = append(*order, e.name)
		}
	case "negate":
		e.left.names(into, order)
	case "binary":
		e.left.names(into, order)
		e.right.names(into, order)
	case "call":
		for _, a := range e.args {
			a.names(into, order)
		}
	}
}

func jsRound(x float64) float64 {
	r := math.Floor(x)
	if x-r >= 0.5 {
		r++
	}
	return r
}

func jsPow(x, y float64) float64 {
	if math.IsInf(y, 0) && math.Abs(x) == 1 || math.IsNaN(y) {
		return math.NaN()
	}
	return math.Pow(x, y)
}

func (e *expr) eval(scope map[string]float64) float64 {
	switch e.kind {
	case "number":
		return e.value
	case "name":
		if v, ok := scope[e.name]; ok {
			return v
		}
		return math.NaN()
	case "negate":
		return -e.left.eval(scope)
	case "call":
		args := make([]float64, len(e.args))
		for i, a := range e.args {
			args[i] = a.eval(scope)
		}
		switch e.name {
		case "min", "max":
			out := args[0]
			for _, a := range args[1:] {
				if e.name == "min" {
					out = math.Min(out, a)
				} else {
					out = math.Max(out, a)
				}
			}
			return out
		case "abs":
			return math.Abs(args[0])
		case "floor":
			return math.Floor(args[0])
		case "ceil":
			return math.Ceil(args[0])
		}
		digits := 0.0
		if len(args) > 1 {
			digits = args[1]
		}
		factor := jsPow(10, digits)
		return jsRound(args[0]*factor) / factor
	}
	l, r := e.left.eval(scope), e.right.eval(scope)
	switch e.op {
	case "+":
		return l + r
	case "-":
		return l - r
	case "*":
		return l * r
	case "/":
		return l / r
	}
	return jsPow(l, r)
}

// formatFixed is Intl.NumberFormat("en-US") with a fixed number of
// decimals: ICU rounds the shortest decimal form of the double half away
// from zero, so 1.005 gives 1.01 where toFixed gives 1.00.
func formatFixed(v float64, decimals int) string {
	digits := strconv.FormatFloat(math.Abs(v), 'f', -1, 64)
	whole, frac, _ := strings.Cut(digits, ".")
	if len(frac) > decimals {
		up := frac[decimals] >= '5'
		frac = frac[:decimals]
		if up {
			n := []byte(whole + frac)
			i := len(n) - 1
			for ; i >= 0 && n[i] == '9'; i-- {
				n[i] = '0'
			}
			if i < 0 {
				n = append([]byte{'1'}, n...)
			} else {
				n[i]++
			}
			whole, frac = string(n[:len(n)-decimals]), string(n[len(n)-decimals:])
		}
	}
	frac += strings.Repeat("0", decimals-len(frac))
	var b strings.Builder
	if math.Signbit(v) {
		b.WriteByte('-')
	}
	for i, c := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	if decimals > 0 {
		b.WriteString("." + frac)
	}
	return b.String()
}

// jsToPrecision2 is Number(x.toPrecision(2)) for a positive finite x: the
// exact value of the double, rounded half up to two significant digits.
func jsToPrecision2(x float64) float64 {
	mantissa, exp, _ := strings.Cut(strconv.FormatFloat(x, 'e', 800, 64), "e")
	d := strings.ReplaceAll(mantissa, ".", "")
	n, _ := strconv.Atoi(d[:2])
	if d[2] >= '5' {
		n++
	}
	e, _ := strconv.Atoi(exp)
	out, _ := strconv.ParseFloat(strconv.Itoa(n)+"e"+strconv.Itoa(e-1), 64)
	return out
}

var (
	toolMarkupAttr   = regexp.MustCompile(`(?i)\b(src|href|action|formaction|srcset|poster|data)[` + jsSpaceClass + `]*=`)
	toolMarkupURL    = regexp.MustCompile(`(?i)url\(`)
	toolScheme       = regexp.MustCompile(`(?i)^[a-z][a-z0-9+.-]*:`)
	toolMarkupPlain  = []*regexp.Regexp{jsRe(`(?i)\b(src|href|action|formaction|srcset|poster)\s*=\s*["']?\s*//`), jsRe(`(?i)<\s*(iframe|frame|object|embed|link|base|meta|portal)\b`)}
	toolScriptBodies = regexp.MustCompile(`(?i)<script\b[^>]*>([\s\S]*?)</script` + `[` + jsSpaceClass + `]*>`)
	toolHandlers     = jsRe(`(?i)\son[a-z]+\s*=\s*("[^"]*"|'[^']*'|[^\s>]+)`)
	toolScript       = []*regexp.Regexp{
		jsRe(`\blocation\s*(=|\.|\[)`),
		jsRe(`\b(window|self|top|parent|globalThis|document)\s*\.\s*(location|open)\b`),
		jsRe(`\bopen\s*\(`),
		jsRe(`\bimport\s*\(`),
		regexp.MustCompile(`\b(fetch|XMLHttpRequest|WebSocket|EventSource|sendBeacon|Worker|SharedWorker)\b`),
	}
)

// schemeAfter is the JavaScript `\s*["']?\s*(?!data:|#)[a-z][a-z0-9+.-]*:`
// from at: RE2 has no lookahead, so each place a scheme can start is tried.
func schemeAfter(s string, at int, hash bool) bool {
	skip := func(i int) int {
		for i < len(s) {
			r := []rune(s[i:])[0]
			if !jsSpace(r) {
				break
			}
			i += len(string(r))
		}
		return i
	}
	starts := []int{skip(at)}
	if p := starts[0]; p < len(s) && (s[p] == '"' || s[p] == '\'') {
		starts = append(starts, skip(p+1))
	}
	for _, p := range starts {
		rest := s[p:]
		if strings.HasPrefix(strings.ToLower(rest), "data:") || hash && strings.HasPrefix(rest, "#") {
			continue
		}
		if toolScheme.MatchString(rest) {
			return true
		}
	}
	return false
}

func answerToolReachesOut(html string) bool {
	for _, m := range toolMarkupAttr.FindAllStringIndex(html, -1) {
		if schemeAfter(html, m[1], true) {
			return true
		}
	}
	for _, m := range toolMarkupURL.FindAllStringIndex(html, -1) {
		if schemeAfter(html, m[1], false) {
			return true
		}
	}
	for _, re := range toolMarkupPlain {
		if re.MatchString(html) {
			return true
		}
	}
	var parts []string
	for _, m := range toolScriptBodies.FindAllStringSubmatch(html, -1) {
		parts = append(parts, m[1])
	}
	for _, m := range toolHandlers.FindAllStringSubmatch(html, -1) {
		parts = append(parts, m[1])
	}
	script := strings.Join(parts, "\n")
	for _, re := range toolScript {
		if re.MatchString(script) {
			return true
		}
	}
	return false
}
