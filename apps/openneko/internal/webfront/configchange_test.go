package webfront

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func fixtureList(t *testing.T, fixture any, key string, floor int) []any {
	t.Helper()
	list, _ := jsGet(fixture, key).([]any)
	if len(list) < floor {
		t.Fatalf("fixture %s holds %d cases, want at least %d", key, len(list), floor)
	}
	return list
}

func sameJS(a, b any) bool { return jsStringify(a, false) == jsStringify(b, false) }

func sameYAML(t *testing.T, got, want string) bool {
	t.Helper()
	if got == "" || want == "" {
		return got == want
	}
	g, err := yamlToJS([]byte(got))
	if err != nil {
		t.Fatalf("Go wrote YAML that does not parse: %v\n%s", err, got)
	}
	w, err := yamlToJS([]byte(want))
	if err != nil {
		t.Fatal(err)
	}
	return sameJS(g, w)
}

func errorText(err error) any {
	if err == nil {
		return nil
	}
	return err.Error()
}

func TestConfigChangeMatchesNode(t *testing.T) {
	raw, err := os.ReadFile("testdata/configchange.json")
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := jsParse(raw)
	if err != nil {
		t.Fatal(err)
	}
	resolve := func(name string) (string, error) {
		switch name {
		case "erp-password":
			return "pw-planted", nil
		case "empty-secret":
			return "", nil
		}
		return "", errors.New(`no stored source secret named "` + name + `"; add it in GraphJin Config first`)
	}

	for _, item := range fixtureList(t, fixture, "updates", 54) {
		name := jsGet(item, "name")
		var opts configUpdateOptions
		if o, ok := jsGet(item, "options").(*jsObj); ok {
			opts = configUpdateOptions{managedOpenAPISpecsDir: o.vals["managedOpenApiSpecsDir"].(string), managedLocalFilesRoot: o.vals["managedLocalFilesRoot"].(string)}
		}
		update, err := buildGraphjinConfigUpdate(jsGet(item, "payload").(*jsObj), resolve, opts)
		if want, ok := jsGet(item, "error").(string); ok {
			if err == nil || err.Error() != want {
				t.Errorf("%s: error %v, want %q", name, err, want)
			}
			if (jsGet(item, "errorName") == "PasswordRequiredError") != errors.Is(err, errPasswordRequired) {
				t.Errorf("%s: password-required error %v", name, err)
			}
			continue
		}
		if err != nil || !sameJS(update, jsGet(item, "update")) {
			t.Errorf("%s: update %s (%v)\nwant %s", name, jsStringify(update, false), err, jsStringify(jsGet(item, "update"), false))
		}
	}

	for _, item := range fixtureList(t, fixture, "readOnly", 6) {
		err := assertDatabaseSourcesStayReadOnly(jsGet(item, "update").(*jsObj), jsGet(item, "current"))
		if got, want := errorText(err), jsGet(item, "error"); got != want {
			t.Errorf("%s: read-only check %v, want %v", jsGet(item, "name"), got, want)
		}
	}

	for _, item := range fixtureList(t, fixture, "inputs", 3) {
		literal, definitions, variables := graphjinInputWithJSONVariables(jsGet(item, "value"))
		if literal != jsGet(item, "literal") || definitions != jsGet(item, "variableDefinitions") || !sameJS(variables, jsGet(item, "variables")) {
			t.Errorf("input %s: %s %s %s", jsStringify(jsGet(item, "value"), false), literal, definitions, jsStringify(variables, false))
		}
	}

	for _, item := range fixtureList(t, fixture, "hashes", 54) {
		if got := graphjinConfigPatchHash(jsGet(item, "payload")); got != jsGet(item, "hash") {
			t.Errorf("hash of %s: %s", jsStringify(jsGet(item, "payload"), false), got)
		}
	}

	for _, item := range fixtureList(t, fixture, "answers", 13) {
		var c answerCheckContext
		if ctx, ok := jsGet(item, "context").(*jsObj); ok {
			if names, ok := ctx.vals["secretNames"].([]any); ok {
				c.hasSecretNames = true
				c.secretNames = stringItems(names)
			}
			c.currentSources = ctx.vals["currentSources"]
			if pairs, ok := ctx.vals["sourceTables"].([]any); ok {
				c.sourceTables = map[string][]string{}
				for _, pair := range pairs {
					p := pair.([]any)
					c.sourceTables[p[0].(string)] = stringItems(p[1])
				}
			}
		}
		problems := checkSourceConfigAnswers(jsGet(item, "payload").(*jsObj), c)
		if !sameJS(decodeJS(t, string(mustJSON(problems))), jsGet(item, "problems")) || describeConfigProblems(problems) != jsGet(item, "text") {
			t.Errorf("answers %s: %s", jsGet(item, "name"), mustJSON(problems))
		}
	}

	for _, item := range fixtureList(t, fixture, "gjErrors", 9) {
		problems := mapGraphjinConfigErrors(jsGet(item, "errors"), jsGet(item, "payload").(*jsObj))
		if !sameJS(decodeJS(t, string(mustJSON(problems))), jsGet(item, "problems")) || describeConfigProblems(problems) != jsGet(item, "text") {
			t.Errorf("GraphJin errors %s: %s", jsGet(item, "name"), mustJSON(problems))
		}
	}

	for _, item := range fixtureList(t, fixture, "merges", 16) {
		name := jsGet(item, "name")
		doc, err := mergeGraphjinConfigUpdate([]byte(jsGet(item, "raw").(string)), jsGet(item, "update").(*jsObj))
		if want, ok := jsGet(item, "error").(string); ok {
			if err == nil || (err.Error() != want && !(name == "bad yaml" && strings.HasPrefix(err.Error(), "GraphJin config is not valid YAML: "))) {
				t.Errorf("merge %s: error %v, want %q", name, err, want)
			}
			continue
		}
		if err != nil {
			t.Errorf("merge %s: %v", name, err)
			continue
		}
		text, err := encodeYAML(doc)
		if err != nil || !sameYAML(t, text, jsGet(item, "value").(string)) {
			t.Errorf("merge %s:\n%s\nwant\n%s", name, text, jsGet(item, "value"))
		}
	}

	for _, item := range fixtureList(t, fixture, "describes", 6) {
		current, _ := jsGet(item, "current").(*jsObj)
		got, err := describeGraphjinConfigChange(current.vals["sources"], current.vals["roles"], jsGet(item, "update").(*jsObj))
		want := jsGet(item, "value")
		if err != nil {
			t.Errorf("describe %s: %v", jsGet(item, "name"), err)
			continue
		}
		for _, side := range []string{"before", "after"} {
			if !sameJS(got.vals[side], jsGet(want, side)) {
				t.Errorf("describe %s %s: %s\nwant %s", jsGet(item, "name"), side, jsStringify(got.vals[side], false), jsStringify(jsGet(want, side), false))
			}
			if g, w := jsGet(got.vals["yaml"], side).(string), jsGet(jsGet(want, "yaml"), side).(string); !sameYAML(t, g, w) || strings.Contains(g, "pw-planted") {
				t.Errorf("describe %s %s YAML:\n%s\nwant\n%s", jsGet(item, "name"), side, g, w)
			}
		}
	}

	for _, item := range fixtureList(t, fixture, "manifests", 15) {
		got, err := parseManagedFileSourceManifest(jsGet(item, "value"), jsGet(item, "sourceName").(string))
		if want, ok := jsGet(item, "error").(string); ok {
			if err == nil || err.Error() != want {
				t.Errorf("manifest %s: error %v, want %q", jsGet(item, "name"), err, want)
			}
		} else if err != nil || !sameJS(got, jsGet(item, "value")) {
			t.Errorf("manifest %s: %s (%v)", jsGet(item, "name"), jsStringify(got, false), err)
		}
	}

	for _, item := range fixtureList(t, fixture, "roots", 3) {
		got, err := managedFileSourceRuntimeRoot(jsGet(item, "orgId").(string), jsGet(item, "sourceName").(string), jsGet(item, "runtimeBase").(string))
		if want, ok := jsGet(item, "error").(string); ok {
			if err == nil || err.Error() != want {
				t.Errorf("root %v: error %v, want %q", jsGet(item, "sourceName"), err, want)
			}
		} else if err != nil || got != jsGet(item, "value") {
			t.Errorf("root %v: %s (%v)", jsGet(item, "sourceName"), got, err)
		}
	}
}
