package sandbox

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/open-neko/openneko/apps/ax-harness/internal/localtool"
)

// DeclaredSuffix names a script's sidecar file: report.py declares its
// queries in report.py.queries.json.
const DeclaredSuffix = ".queries.json"

const (
	declaredDir        = "queries"
	maxDeclaredFile    = 64 << 10
	maxDeclaredQueries = 20
)

var (
	declaredName  = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	declaredParam = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
	shellVariable = regexp.MustCompile(`^\$(?:\{([A-Za-z_][A-Za-z0-9_]*)\}|([A-Za-z_][A-Za-z0-9_]*))`)
)

type declaredFile struct {
	Queries map[string]declaredQuery `json:"queries"`
}

type declaredQuery struct {
	Query     string                       `json:"query"`
	Namespace string                       `json:"namespace,omitempty"`
	Params    map[string]declaredParamSpec `json:"params,omitempty"`
}

type declaredParamSpec struct {
	Type    string          `json:"type,omitempty"`
	Default json.RawMessage `json:"default,omitempty"`
}

type declaredScript struct {
	path string
	args []string
}

// commandWords splits a shell command into simple commands and their words.
// It expands $NAME and ${NAME} from vars outside single quotes and leaves
// any other text as written.
func commandWords(command string, vars map[string]string) [][]string {
	var commands [][]string
	var words []string
	var word strings.Builder
	inWord, single, double := false, false, false
	endWord := func() {
		if inWord {
			words = append(words, word.String())
		}
		word.Reset()
		inWord = false
	}
	endCommand := func() {
		endWord()
		if len(words) > 0 {
			commands = append(commands, words)
		}
		words = nil
	}
	for i := 0; i < len(command); i++ {
		ch := command[i]
		switch {
		case single:
			if ch == '\'' {
				single = false
			} else {
				word.WriteByte(ch)
			}
		case ch == '\\' && i+1 < len(command) && (!double || strings.IndexByte("\"\\$`", command[i+1]) >= 0):
			i++
			word.WriteByte(command[i])
			inWord = true
		case ch == '$':
			inWord = true
			if m := shellVariable.FindStringSubmatch(command[i:]); m != nil {
				name := m[1] + m[2]
				if value, ok := vars[name]; ok {
					word.WriteString(value)
					i += len(m[0]) - 1
					continue
				}
			}
			word.WriteByte(ch)
		case double:
			if ch == '"' {
				double = false
			} else {
				word.WriteByte(ch)
			}
		case ch == '\'':
			single, inWord = true, true
		case ch == '"':
			double, inWord = true, true
		case ch == ' ' || ch == '\t':
			endWord()
		case strings.IndexByte("|;&()<>\n", ch) >= 0:
			endCommand()
		default:
			word.WriteByte(ch)
			inWord = true
		}
	}
	endCommand()
	return commands
}

// declaredScripts finds the words of a command that name a file in the
// workspace with a sidecar file beside it.
func (c *Client) declaredScripts(command, dir, skillDir string, env map[string]string) []declaredScript {
	vars := map[string]string{}
	for name, value := range env {
		vars[name] = value
	}
	if skillDir != "" {
		vars["SKILL_DIR"] = skillDir
	}
	base := c.workspace
	if dir != "" {
		base = dir
	}
	var found []declaredScript
	for _, words := range commandWords(command, vars) {
		for i, w := range words {
			if w == "" || strings.HasPrefix(w, "-") {
				continue
			}
			p := w
			if !filepath.IsAbs(p) {
				p = filepath.Join(base, p)
			}
			p = filepath.Clean(p)
			if _, err := c.relative(p); err != nil {
				continue
			}
			script, err := os.Stat(p)
			if err != nil || !script.Mode().IsRegular() {
				continue
			}
			if side, err := os.Lstat(p + DeclaredSuffix); err == nil && side.Mode().IsRegular() {
				found = append(found, declaredScript{path: p, args: words[i+1:]})
			}
		}
	}
	return found
}

func readDeclared(path string) (declaredFile, error) {
	var f declaredFile
	raw, err := os.ReadFile(path)
	if err != nil {
		return f, err
	}
	if len(raw) > maxDeclaredFile {
		return f, fmt.Errorf("it is larger than %d bytes", maxDeclaredFile)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return f, fmt.Errorf("it is not valid: %v", err)
	}
	if len(f.Queries) == 0 || len(f.Queries) > maxDeclaredQueries {
		return f, fmt.Errorf("it must list 1 to %d queries", maxDeclaredQueries)
	}
	for name, q := range f.Queries {
		if !declaredName.MatchString(name) {
			return f, fmt.Errorf("the query name %q must be lower case letters, digits and _", name)
		}
		if strings.TrimSpace(q.Query) == "" {
			return f, fmt.Errorf("the query %s has no query text", name)
		}
		for param, spec := range q.Params {
			if !declaredParam.MatchString(param) {
				return f, fmt.Errorf("the query %s has a parameter name %q that GraphQL cannot use", name, param)
			}
			if !slices.Contains([]string{"", "string", "integer", "number", "boolean"}, spec.Type) {
				return f, fmt.Errorf("the parameter %s of query %s has type %q; use string, integer, number or boolean", param, name, spec.Type)
			}
			if len(spec.Default) > 0 && !paramTypeOK(spec.Type, spec.Default) {
				return f, fmt.Errorf("the default of parameter %s of query %s is not a %s", param, name, paramType(spec.Type))
			}
		}
	}
	return f, nil
}

func paramType(t string) string {
	if t == "" {
		return "string"
	}
	return t
}

func paramTypeOK(t string, raw json.RawMessage) bool {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return false
	}
	switch paramType(t) {
	case "string":
		_, ok := v.(string)
		return ok
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "integer":
		n, ok := v.(float64)
		return ok && n == float64(int64(n))
	default:
		_, ok := v.(float64)
		return ok
	}
}

// flagValue reads --name=value or --name value from a script's arguments.
func flagValue(args []string, name string) (string, bool) {
	flag := "--" + name
	for i, a := range args {
		if v, ok := strings.CutPrefix(a, flag+"="); ok {
			return v, true
		}
		if a == flag && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}

func paramValue(t, text string) (json.RawMessage, error) {
	switch paramType(t) {
	case "integer":
		n, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			return nil, errors.New("is not an integer")
		}
		return json.RawMessage(strconv.FormatInt(n, 10)), nil
	case "number":
		n, err := strconv.ParseFloat(text, 64)
		if err != nil {
			return nil, errors.New("is not a number")
		}
		return json.Marshal(n)
	case "boolean":
		b, err := strconv.ParseBool(text)
		if err != nil {
			return nil, errors.New("is not true or false")
		}
		return json.Marshal(b)
	default:
		return json.Marshal(text)
	}
}

func (q declaredQuery) request(name string, args []string) (DataQuery, error) {
	r := DataQuery{Query: q.Query, Namespace: q.Namespace}
	if len(q.Params) == 0 {
		return r, nil
	}
	vars := map[string]json.RawMessage{}
	for param, spec := range q.Params {
		text, given := flagValue(args, param)
		switch {
		case given:
			v, err := paramValue(spec.Type, text)
			if err != nil {
				return r, fmt.Errorf("--%s %s", param, err)
			}
			vars[param] = v
		case len(spec.Default) > 0:
			vars[param] = spec.Default
		default:
			return r, fmt.Errorf("the query %s needs --%s", name, param)
		}
	}
	raw, err := json.Marshal(vars)
	r.Variables = raw
	return r, err
}

func declaredKey(q DataQuery) string {
	raw, _ := json.Marshal(q)
	sum := sha256.Sum256(append([]byte("declared\n"), raw...))
	return hex.EncodeToString(sum[:])
}

// runDeclared runs the queries that the command's script declares and
// writes each data object to RUN_DIR/.neko-data/queries/<name>.json. A
// non-empty text is the reason the script may not start.
func (c *Client) runDeclared(ctx context.Context, command, dir, skillDir string, env map[string]string) (string, error) {
	if c.data != nil {
		if err := os.RemoveAll(filepath.Join(c.data.runRoot, dataDir, declaredDir)); err != nil {
			return "", err
		}
	}
	scripts := c.declaredScripts(command, dir, skillDir, env)
	if len(scripts) == 0 {
		return "", nil
	}
	rel, _ := c.relative(scripts[0].path)
	if len(scripts) > 1 {
		return "this command starts more than one script with declared queries; run each one in its own command", nil
	}
	if c.data == nil {
		return rel + " declares its queries in " + filepath.Base(rel) + DeclaredSuffix + ", and this run has no script data", nil
	}
	file, err := readDeclared(scripts[0].path + DeclaredSuffix)
	if err != nil {
		return fmt.Sprintf("%s%s: %v", rel, DeclaredSuffix, err), nil
	}
	names := make([]string, 0, len(file.Queries))
	for name := range file.Queries {
		names = append(names, name)
	}
	slices.Sort(names)
	requests := map[string]DataQuery{}
	for _, name := range names {
		r, err := file.Queries[name].request(name, scripts[0].args)
		if err != nil {
			return fmt.Sprintf("%s did not start: %v", rel, err), nil
		}
		requests[name] = r
	}
	out := filepath.Join(c.data.runRoot, dataDir, declaredDir)
	if err := os.MkdirAll(out, 0o755); err != nil {
		return "", err
	}
	for _, name := range names {
		key := declaredKey(requests[name])
		r, done := c.data.records[key]
		if !done {
			if note := c.data.stopped(); note != "" {
				return fmt.Sprintf("%s did not start: %s", rel, strings.Trim(strings.TrimPrefix(note, dataStopLabel), "]")), nil
			}
			if r, err = c.data.run(ctx, key, requests[name]); err != nil {
				return "", err
			}
		}
		if r.Error != "" {
			return fmt.Sprintf("%s did not start: its declared query %s failed: %s", rel, name, r.Error), nil
		}
		if err := os.WriteFile(filepath.Join(out, name+".json"), r.Data, 0o644); err != nil {
			return "", err
		}
	}
	return "", nil
}

func refused(reason string) localtool.ExecResult {
	return localtool.ExecResult{ExitCode: 1, Output: reason + "\n"}
}
