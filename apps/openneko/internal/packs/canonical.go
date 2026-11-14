package packs

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// CanonicalJSON is canonicalJson() from packages/packs: JSON.stringify with
// object keys sorted by UTF-16 code units.
func CanonicalJSON(v any) (string, error) {
	var b strings.Builder
	if err := writeCanonical(&b, v); err != nil {
		return "", err
	}
	return b.String(), nil
}

func SHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func CanonicalHash(v any) (string, error) {
	text, err := CanonicalJSON(v)
	if err != nil {
		return "", err
	}
	return SHA256([]byte(text)), nil
}

func mustHash(v any) string {
	h, err := CanonicalHash(v)
	if err != nil {
		panic(err)
	}
	return h
}

func utf16Less(x, y string) int {
	return slices.Compare(utf16.Encode([]rune(x)), utf16.Encode([]rune(y)))
}

func writeCanonical(b *strings.Builder, v any) error {
	switch t := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		b.WriteString(strconv.FormatBool(t))
	case string:
		writeString(b, t)
	case float64:
		if math.IsInf(t, 0) || math.IsNaN(t) {
			return fmt.Errorf("cannot canonicalize a non-finite number")
		}
		b.WriteString(numberText(t))
	case int:
		b.WriteString(strconv.Itoa(t))
	case []any:
		b.WriteByte('[')
		for i, item := range t {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := writeCanonical(b, item); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		slices.SortFunc(keys, utf16Less)
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			writeString(b, k)
			b.WriteByte(':')
			if err := writeCanonical(b, t[k]); err != nil {
				return err
			}
		}
		b.WriteByte('}')
	default:
		raw, err := json.Marshal(v)
		if err != nil {
			return err
		}
		generic, err := decodeGeneric(raw)
		if err != nil {
			return err
		}
		return writeCanonical(b, generic)
	}
	return nil
}

func decodeGeneric(raw []byte) (any, error) {
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func numberText(f float64) string {
	if f == 0 {
		return "0"
	}
	out, _ := json.Marshal(f)
	return string(out)
}

func writeString(b *strings.Builder, s string) {
	b.WriteByte('"')
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
}

func itoa(n int) string { return strconv.Itoa(n) }

func joinComma(list []string) string { return strings.Join(list, ", ") }
