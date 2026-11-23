package webfront

import (
	"fmt"
	"math"
	"regexp"
	"strings"
)

// This file is packages/llm/src/graphjin/config-problems.ts.

type configProblem struct {
	Field   *string `json:"field"`
	Message string  `json:"message"`
	Detail  *string `json:"detail,omitempty"`
}

func fieldProblem(field, message string) configProblem {
	return configProblem{Field: &field, Message: message}
}

type answerCheckContext struct {
	secretNames    []string
	hasSecretNames bool
	currentSources any
	sourceTables   map[string][]string
}

type currentSourceTables struct {
	name           string
	blocked, admin []string
}

func problemNames(v any) []string {
	out := []string{}
	list, _ := v.([]any)
	for _, item := range list {
		if s := trimmedText(item); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func stringItems(v any) []string {
	out := []string{}
	list, _ := v.([]any)
	for _, item := range list {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func listCurrentSources(v any) []currentSourceTables {
	if text, ok := v.(string); ok {
		parsed, err := jsParse([]byte(text))
		if err != nil {
			parsed = []any{}
		}
		v = parsed
	}
	list, _ := v.([]any)
	out := []currentSourceTables{}
	for _, item := range list {
		record, ok := item.(*jsObj)
		if !ok {
			continue
		}
		name, ok := record.vals["name"].(string)
		if !ok {
			continue
		}
		access, _ := record.vals["access"].(*jsObj)
		if access == nil {
			access = &jsObj{}
		}
		out = append(out, currentSourceTables{name, stringItems(access.vals["blocked_tables"]), stringItems(access.vals["admin_tables"])})
	}
	return out
}

func quotedList(names []string) string {
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = `"` + n + `"`
	}
	return strings.Join(quoted, ", ")
}

// checkSourceConfigAnswers checks the admin's answers before GraphJin sees
// them. Each problem names the answer at fault.
func checkSourceConfigAnswers(payload *jsObj, c answerCheckContext) []configProblem {
	problems := []configProblem{}
	add := func(field, message string) { problems = append(problems, fieldProblem(field, message)) }
	action := selectedText(payload.vals["action"], "")
	sources := listCurrentSources(c.currentSources)
	byName := map[string]*currentSourceTables{}
	for i := range sources {
		byName[strings.ToLower(sources[i].name)] = &sources[i]
	}
	text := func(key string) string { return selectedText(payload.vals[key], "") }

	if action == "add_role" {
		if text("name") == "" {
			add("name", "The role name is empty.")
		}
		if text("match") == "" {
			add("match", "Say which people get this role. The match rule is empty.")
		}
		return problems
	}

	if action == "register_source" {
		name := text("name")
		if name == "" {
			add("name", "Give the source a name.")
		} else if byName[strings.ToLower(name)] != nil {
			add("name", fmt.Sprintf("A source named \"%s\" already exists. Choose another name.", name))
		}
		if kind := text("kind"); kind != "" && kind != "database" {
			return problems
		}
		if text("host") == "" {
			add("host", "The host is empty. Enter the name or address of the database server.")
		}
		if port, ok := payload.vals["port"]; ok && port != nil && port != "" {
			if n := numberOf(port); math.IsNaN(n) || n != math.Trunc(n) || n < 1 || n > 65535 {
				add("port", "The port must be a whole number from 1 to 65535.")
			}
		}
		if text("dbname") == "" {
			add("dbname", "The database name is empty.")
		}
		if text("user") == "" {
			add("user", "The login name is empty.")
		}
		if ref := text("secretRef"); ref == "" {
			add("secretRef", passwordRequiredMessage)
		} else if c.hasSecretNames && !containsString(c.secretNames, ref) {
			add("secretRef", fmt.Sprintf("No saved password is named \"%s\". Save the password again.", ref))
		}
		return problems
	}

	sourceName := text("source")
	if sourceName == "" {
		add("source", "Say which source to change. The source name is empty.")
		return problems
	}
	source := byName[strings.ToLower(sourceName)]
	if len(sources) > 0 && source == nil {
		known := make([]string, len(sources))
		for i, s := range sources {
			known[i] = s.name
		}
		add("source", fmt.Sprintf("No source is named \"%s\". The sources are: %s.", sourceName, strings.Join(known, ", ")))
		return problems
	}

	if action == "set_source_tables" {
		tables := c.sourceTables[strings.ToLower(sourceName)]
		if len(tables) == 0 {
			return problems
		}
		known := map[string]bool{}
		for _, t := range tables {
			known[strings.ToLower(t)] = true
		}
		listed := map[string]bool{}
		if source != nil {
			for _, t := range append(append([]string{}, source.blocked...), source.admin...) {
				listed[strings.ToLower(t)] = true
			}
		}
		for _, field := range []string{"blockedTables", "adminTables", "openTables"} {
			var unknown []string
			for _, t := range problemNames(payload.vals[field]) {
				if !known[strings.ToLower(t)] && !listed[strings.ToLower(t)] {
					unknown = append(unknown, t)
				}
			}
			if len(unknown) == 1 {
				add(field, fmt.Sprintf("The source \"%s\" has no table named \"%s\".", sourceName, unknown[0]))
			}
			if len(unknown) > 1 {
				add(field, fmt.Sprintf("The source \"%s\" has no tables named %s.", sourceName, quotedList(unknown)))
			}
		}
	}
	return problems
}

var (
	connectionFailed = regexp.MustCompile(`^database '([^']*)' \(([^@]*)@([^:]*):(\d+)/([^)]*)\): connection failed: ((?s:.*))$`)
	accessField      = regexp.MustCompile(`^sources\["[^"]*"\]\.access\.(read|write|delete): ((?s:.*))$`)
	hostMissing      = regexp.MustCompile(`no such host|server misbehaving|name resolution|nodename nor servname|could not translate host`)
	portClosed       = regexp.MustCompile(`connection refused|connect: no route|i/o timeout|timed out|network is unreachable`)
	loginRefused     = regexp.MustCompile(`password authentication failed|access denied for user|authentication failed|invalid password`)
	roleMissing      = regexp.MustCompile(`role "[^"]*" does not exist`)
	databaseMissing  = regexp.MustCompile(`database "[^"]*" does not exist|unknown database`)
	connectDenied    = regexp.MustCompile(`permission denied for database|not have connect privilege`)
	tlsFailed        = regexp.MustCompile(`tls|ssl|certificate|x509`)
	nameRequired     = regexp.MustCompile(`^sources\[\d+\]: name is required`)
	nameTaken        = regexp.MustCompile(`duplicate source names|already exists`)
	typeUnsupported  = regexp.MustCompile(`unsupported database type`)
	systemDatabase   = regexp.MustCompile(`(?i)system database`)
)

func connectionProblem(message string) *configProblem {
	m := connectionFailed.FindStringSubmatch(message)
	if m == nil {
		return nil
	}
	user, host, port, dbname := m[2], m[3], m[4], m[5]
	lower := strings.ToLower(m[6])
	problem := func(field, text string) *configProblem {
		p := fieldProblem(field, text)
		p.Detail = &message
		return &p
	}
	switch {
	case hostMissing.MatchString(lower):
		return problem("host", fmt.Sprintf("OpenNeko cannot find the host \"%s\". Check the host name.", host))
	case portClosed.MatchString(lower):
		return problem("port", fmt.Sprintf("Nothing answers on %s port %s. Check the port, and that the database server accepts connections from OpenNeko.", host, port))
	case loginRefused.MatchString(lower):
		return problem("secretRef", fmt.Sprintf("The database refused the login \"%s\" with this password. Check the login name and save the password again.", user))
	case roleMissing.MatchString(lower):
		return problem("user", fmt.Sprintf("The database has no login named \"%s\".", user))
	case databaseMissing.MatchString(lower):
		return problem("dbname", fmt.Sprintf("The server has no database named \"%s\".", dbname))
	case connectDenied.MatchString(lower):
		return problem("user", fmt.Sprintf("The login \"%s\" may not connect to the database \"%s\".", user, dbname))
	case tlsFailed.MatchString(lower):
		return problem("host", fmt.Sprintf("The secure connection to \"%s\" failed. Check the server's TLS settings.", host))
	}
	return problem("host", fmt.Sprintf("OpenNeko could not connect to the database \"%s\" on %s port %s.", dbname, host, port))
}

// mapGraphjinConfigErrors ties each of GraphJin's error texts to the answer
// it came from.
func mapGraphjinConfigErrors(errs any, payload *jsObj) []configProblem {
	if s, ok := errs.(string); ok {
		errs = []any{s}
	}
	list, ok := errs.([]any)
	if !ok {
		return []configProblem{}
	}
	sourceField := "source"
	if selectedText(payload.vals["action"], "") == "register_source" {
		sourceField = "name"
	}
	out := []configProblem{}
	for _, item := range list {
		message := ""
		switch t := item.(type) {
		case string:
			message = t
		case *jsObj:
			message, _ = t.vals["message"].(string)
		}
		if message == "" {
			continue
		}
		detail := message
		with := func(field *string, text string) configProblem {
			return configProblem{Field: field, Message: text, Detail: &detail}
		}
		name := func(s string) *string { return &s }
		if p := connectionProblem(message); p != nil {
			out = append(out, *p)
			continue
		}
		if m := accessField.FindStringSubmatch(message); m != nil {
			out = append(out, with(name(m[1]), fmt.Sprintf("GraphJin does not accept this %s access: %s.", m[1], m[2])))
			continue
		}
		switch {
		case nameRequired.MatchString(message):
			out = append(out, with(name(sourceField), "Give the source a name."))
		case nameTaken.MatchString(message):
			out = append(out, with(name(sourceField), "A source with this name already exists. Choose another name."))
		case typeUnsupported.MatchString(message):
			out = append(out, with(name("type"), "GraphJin does not support this database type."))
		case systemDatabase.MatchString(message):
			out = append(out, with(name("dbname"), "This is a system database. Choose the database that holds the business data."))
		default:
			out = append(out, with(nil, "GraphJin did not accept this change."))
		}
	}
	return out
}

func describeConfigProblems(problems []configProblem) string {
	parts := make([]string, len(problems))
	for i, p := range problems {
		parts[i] = p.Message
	}
	return strings.Join(parts, " ")
}
