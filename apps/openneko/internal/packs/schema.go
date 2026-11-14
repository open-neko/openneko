package packs

import (
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"
)

type rule func(v any, path string) error

type field struct {
	name     string
	check    rule
	optional bool
}

func req(name string, check rule) field { return field{name: name, check: check} }
func opt(name string, check rule) field { return field{name: name, check: check, optional: true} }

func issue(path, format string, args ...any) error {
	if path == "" {
		path = "(root)"
	}
	return fmt.Errorf("%s: %s", path, fmt.Sprintf(format, args...))
}

func join(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

func object(strict bool, fields ...field) rule {
	return func(v any, path string) error {
		m, ok := v.(map[string]any)
		if !ok {
			return issue(path, "expected object")
		}
		known := map[string]bool{}
		for _, f := range fields {
			known[f.name] = true
			value, present := m[f.name]
			if !present {
				if !f.optional {
					return issue(join(path, f.name), "required")
				}
				continue
			}
			if f.optional && value == nil {
				return issue(join(path, f.name), "must not be null")
			}
			if err := f.check(value, join(path, f.name)); err != nil {
				return err
			}
		}
		if strict {
			keys := make([]string, 0, len(m))
			for k := range m {
				if !known[k] {
					keys = append(keys, k)
				}
			}
			if len(keys) > 0 {
				slices.Sort(keys)
				return issue(path, "unrecognized key(s): %s", strings.Join(keys, ", "))
			}
		}
		return nil
	}
}

func strictObj(fields ...field) rule { return object(true, fields...) }
func looseObj(fields ...field) rule  { return object(false, fields...) }

func str(min int) rule {
	return func(v any, path string) error {
		s, ok := v.(string)
		if !ok {
			return issue(path, "expected string")
		}
		if len([]rune(s)) < min {
			return issue(path, "too short")
		}
		return nil
	}
}

func strMax(min, max int) rule {
	return func(v any, path string) error {
		if err := str(min)(v, path); err != nil {
			return err
		}
		if len(utf16Units(v.(string))) > max {
			return issue(path, "too long")
		}
		return nil
	}
}

func utf16Units(s string) []uint16 {
	out := []uint16{}
	for _, r := range s {
		if r >= 0x10000 {
			out = append(out, 0, 0)
		} else {
			out = append(out, 0)
		}
	}
	return out
}

func matches(re *regexp.Regexp, message string) rule {
	return func(v any, path string) error {
		s, ok := v.(string)
		if !ok {
			return issue(path, "expected string")
		}
		if !re.MatchString(s) {
			return issue(path, "%s", message)
		}
		return nil
	}
}

func enum(values ...string) rule {
	return func(v any, path string) error {
		s, ok := v.(string)
		if !ok || !slices.Contains(values, s) {
			return issue(path, "expected one of %s", strings.Join(values, ", "))
		}
		return nil
	}
}

func literal(want any) rule {
	return func(v any, path string) error {
		if v != want {
			return issue(path, "expected %v", want)
		}
		return nil
	}
}

func boolean(v any, path string) error {
	if _, ok := v.(bool); !ok {
		return issue(path, "expected boolean")
	}
	return nil
}

func number(v any, path string) error {
	if _, ok := v.(float64); !ok {
		return issue(path, "expected number")
	}
	return nil
}

func integer(min float64) rule {
	return func(v any, path string) error {
		f, ok := v.(float64)
		if !ok || math.Trunc(f) != f || math.IsInf(f, 0) {
			return issue(path, "expected integer")
		}
		if f < min {
			return issue(path, "too small")
		}
		return nil
	}
}

func anyValue(any, string) error { return nil }

func array(item rule, min int) rule {
	return func(v any, path string) error {
		list, ok := v.([]any)
		if !ok {
			return issue(path, "expected array")
		}
		if len(list) < min {
			return issue(path, "too small")
		}
		for i, value := range list {
			if err := item(value, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
		return nil
	}
}

func record(key, value rule) rule {
	return func(v any, path string) error {
		m, ok := v.(map[string]any)
		if !ok {
			return issue(path, "expected record")
		}
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			if err := key(k, join(path, k)); err != nil {
				return err
			}
			if err := value(m[k], join(path, k)); err != nil {
				return err
			}
		}
		return nil
	}
}

func union(rules ...rule) rule {
	return func(v any, path string) error {
		var first error
		for _, r := range rules {
			err := r(v, path)
			if err == nil {
				return nil
			}
			if first == nil {
				first = err
			}
		}
		return issue(path, "no union member matched (%v)", first)
	}
}

func nullable(r rule) rule {
	return func(v any, path string) error {
		if v == nil {
			return nil
		}
		return r(v, path)
	}
}

func scalar(v any, path string) error {
	switch v.(type) {
	case string, float64, bool:
		return nil
	}
	return issue(path, "expected string, number or boolean")
}
