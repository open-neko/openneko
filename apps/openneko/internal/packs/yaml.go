package packs

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// parseYAML reads one YAML document into JSON-like values with the YAML 1.2
// core schema, as the `yaml` package does in Node.
func parseYAML(raw []byte) (any, error) {
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, nil
		}
		return nil, err
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); err == nil {
		return nil, errors.New("source contains multiple documents")
	}
	return yamlValue(&doc)
}

func yamlValue(n *yaml.Node) (any, error) {
	switch n.Kind {
	case yaml.DocumentNode:
		if len(n.Content) == 0 {
			return nil, nil
		}
		return yamlValue(n.Content[0])
	case yaml.AliasNode:
		return yamlValue(n.Alias)
	case yaml.SequenceNode:
		out := make([]any, 0, len(n.Content))
		for _, c := range n.Content {
			v, err := yamlValue(c)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	case yaml.MappingNode:
		out := make(map[string]any, len(n.Content)/2)
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, err := yamlValue(n.Content[i])
			if err != nil {
				return nil, err
			}
			key, err := yamlKey(k)
			if err != nil {
				return nil, err
			}
			if _, dup := out[key]; dup {
				return nil, fmt.Errorf("line %d: map keys must be unique", n.Content[i].Line)
			}
			v, err := yamlValue(n.Content[i+1])
			if err != nil {
				return nil, err
			}
			out[key] = v
		}
		return out, nil
	case yaml.ScalarNode:
		return yamlScalar(n)
	}
	return nil, fmt.Errorf("unsupported YAML node at line %d", n.Line)
}

func yamlKey(k any) (string, error) {
	switch t := k.(type) {
	case string:
		return t, nil
	case nil:
		return "", nil
	case bool:
		return strconv.FormatBool(t), nil
	case float64:
		return numberText(t), nil
	}
	return "", errors.New("unsupported YAML map key")
}

func yamlScalar(n *yaml.Node) (any, error) {
	v := n.Value
	if n.Style&(yaml.DoubleQuotedStyle|yaml.SingleQuotedStyle|yaml.LiteralStyle|yaml.FoldedStyle) != 0 {
		return v, nil
	}
	switch n.Tag {
	case "!!str", "!!timestamp", "!!binary":
		return v, nil
	case "!!null":
		return nil, nil
	}
	if n.Tag != "" && n.Tag != "!" && !strings.HasPrefix(n.Tag, "!!") {
		return v, nil
	}
	switch v {
	case "", "~", "null", "Null", "NULL":
		return nil, nil
	case "true", "True", "TRUE":
		return true, nil
	case "false", "False", "FALSE":
		return false, nil
	case ".inf", ".Inf", ".INF", "+.inf", "+.Inf", "+.INF":
		return math.Inf(1), nil
	case "-.inf", "-.Inf", "-.INF":
		return math.Inf(-1), nil
	case ".nan", ".NaN", ".NAN":
		return math.NaN(), nil
	}
	if coreInt.MatchString(v) {
		f, err := strconv.ParseFloat(v, 64)
		if err == nil {
			return f, nil
		}
	}
	if strings.HasPrefix(v, "0o") && octalInt.MatchString(v[2:]) {
		i, err := strconv.ParseUint(v[2:], 8, 64)
		if err == nil {
			return float64(i), nil
		}
	}
	if strings.HasPrefix(v, "0x") && hexInt.MatchString(v[2:]) {
		i, err := strconv.ParseUint(v[2:], 16, 64)
		if err == nil {
			return float64(i), nil
		}
	}
	if coreFloat.MatchString(v) {
		f, err := strconv.ParseFloat(v, 64)
		if err == nil {
			return f, nil
		}
	}
	return v, nil
}
