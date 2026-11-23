package webfront

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
	"gopkg.in/yaml.v3"
)

const graphjinAPIMutationKind = "graphjin.api_mutation"

type graphjinMutationPlan struct {
	source, operation, method, path string
	body                            any
	query                           string
	variables                       map[string]any
	savedQuery, namespace           string
}

func (p *graphjinMutationPlan) payload() *jsObj {
	out := jsObjOf("source", p.source, "operation", p.operation, "method", p.method, "path", p.path,
		"body", jsValueOf(p.body), "query", p.query)
	if p.variables != nil {
		out.set("variables", jsValueOf(p.variables))
	}
	if p.savedQuery != "" {
		out.set("savedQuery", p.savedQuery)
		if p.namespace != "" {
			out.set("namespace", p.namespace)
		}
	}
	return out
}

type graphjinAPIOperation struct {
	source, spec, operationID string
	specsDir                  string
}

func assertDatabaseSourcesReadOnly(root *yaml.Node) error {
	sources := yamlMapValue(root, "sources")
	if sources == nil || sources.Tag == "!!null" {
		return nil
	}
	if sources.Kind != yaml.SequenceNode {
		return errors.New("GraphJin config sources must be a YAML list")
	}
	for _, item := range sources.Content {
		if item.Kind != yaml.MappingNode {
			return errors.New("GraphJin config source entries must be YAML objects")
		}
		if isNonDatabaseGrantSourceKind(strings.ToLower(yamlScalarField(item, "kind"))) {
			continue
		}
		readOnly := yamlMapValue(item, "read_only")
		var on bool
		if readOnly == nil || readOnly.Tag != "!!bool" || readOnly.Decode(&on) != nil || !on {
			return fmt.Errorf(`GraphJin database source "%s" is not read_only`, orDefault(yamlScalarField(item, "name"), "<unnamed>"))
		}
	}
	return nil
}

// configAPIOperations maps each GraphQL root field of an API source to its
// operation.
func configAPIOperations(root *yaml.Node) map[string]graphjinAPIOperation {
	out := map[string]graphjinAPIOperation{}
	sources := yamlMapValue(root, "sources")
	if sources == nil || sources.Kind != yaml.SequenceNode {
		return out
	}
	for _, source := range sources.Content {
		if source.Kind != yaml.MappingNode || strings.ToLower(yamlScalarField(source, "kind")) != "api" {
			continue
		}
		specs := yamlMapValue(source, "specs")
		if specs == nil || specs.Kind != yaml.MappingNode {
			continue
		}
		for i := 0; i+1 < len(specs.Content); i += 2 {
			operations := yamlMapValue(specs.Content[i+1], "operations")
			if operations == nil || operations.Kind != yaml.MappingNode {
				continue
			}
			for j := 0; j+1 < len(operations.Content); j += 2 {
				id := operations.Content[j].Value
				field := id
				if operations.Content[j+1].Kind == yaml.MappingNode {
					field = orDefault(yamlScalarField(operations.Content[j+1], "expose_as"), id)
				}
				out[field] = graphjinAPIOperation{source: yamlScalarField(source, "name"), spec: specs.Content[i].Value,
					operationID: id, specsDir: yamlScalarField(source, "specs_dir")}
			}
		}
	}
	return out
}

// specOperationRoute reads the method and path of an operation from its
// OpenAPI file; specs_dir may name the GraphJin container's own folder.
func specOperationRoute(configFile string, op graphjinAPIOperation) (string, string) {
	configDir := filepath.Dir(configFile)
	dirs := []string{}
	if op.specsDir != "" {
		if filepath.IsAbs(op.specsDir) {
			dirs = append(dirs, op.specsDir)
		} else {
			dirs = append(dirs, filepath.Join(configDir, op.specsDir))
		}
		dirs = append(dirs, filepath.Join(configDir, filepath.Base(op.specsDir)))
	}
	dirs = append(dirs, filepath.Join(configDir, "specs"))
	for _, dir := range dirs {
		for _, ext := range []string{".yaml", ".yml", ".json"} {
			raw, err := os.ReadFile(filepath.Join(dir, op.spec+ext))
			if err != nil {
				continue
			}
			var doc struct {
				Paths map[string]map[string]struct {
					OperationID string `yaml:"operationId"`
				} `yaml:"paths"`
			}
			if yaml.Unmarshal(raw, &doc) != nil {
				continue
			}
			for path, methods := range doc.Paths {
				for method, spec := range methods {
					if spec.OperationID == op.operationID {
						return strings.ToUpper(method), path
					}
				}
			}
		}
	}
	return "", ""
}

// planGraphjinAPIMutation gives nil for a document with no mutation. A
// mutation passes only when the config proves every database source
// read-only and its one root field is an API operation.
func planGraphjinAPIMutation(configFile, query string, variables map[string]any) (*graphjinMutationPlan, error) {
	doc, perr := parser.ParseQuery(&ast.Source{Input: query})
	if perr != nil {
		if graphqlMutationWord.MatchString(query) {
			return nil, errors.New("OpenNeko cannot parse the mutation")
		}
		return nil, nil
	}
	var mutation *ast.OperationDefinition
	for _, op := range doc.Operations {
		if op.Operation == ast.Mutation {
			mutation = op
		}
	}
	if mutation == nil {
		return nil, nil
	}
	if len(doc.Operations) != 1 || len(doc.Fragments) != 0 {
		return nil, errors.New("send the mutation alone, with no other operation and no fragment")
	}
	configFile = strings.TrimSpace(configFile)
	if configFile == "" {
		return nil, errors.New("OPENNEKO_GRAPHJIN_CONFIG is not set, so OpenNeko cannot prove that database sources are read-only")
	}
	raw, err := os.ReadFile(configFile)
	if err != nil {
		return nil, fmt.Errorf("cannot read %s: %v", configFile, err)
	}
	root, err := parseGroupGrantsYAML(string(raw))
	if err != nil {
		return nil, err
	}
	if err := assertDatabaseSourcesReadOnly(root.Content[0]); err != nil {
		return nil, err
	}
	operations := configAPIOperations(root.Content[0])
	var field *ast.Field
	for _, sel := range mutation.SelectionSet {
		f, ok := sel.(*ast.Field)
		if !ok {
			return nil, errors.New("the mutation must name its fields, with no fragment")
		}
		if f.Name == "__typename" {
			continue
		}
		if _, ok := operations[f.Name]; !ok {
			return nil, fmt.Errorf("%s is not an API operation; a database source takes no mutation from a run", f.Name)
		}
		if field != nil {
			return nil, errors.New("send one API operation per mutation")
		}
		field = f
	}
	if field == nil {
		return nil, errors.New("the mutation names no API operation")
	}
	body := map[string]any{}
	for _, arg := range field.Arguments {
		v, err := arg.Value.Value(variables)
		if err != nil {
			return nil, fmt.Errorf("argument %s: %v", arg.Name, err)
		}
		body[arg.Name] = v
	}
	op := operations[field.Name]
	method, path := specOperationRoute(configFile, op)
	return &graphjinMutationPlan{source: op.source, operation: field.Name, method: orDefault(method, "unknown"),
		path: orDefault(path, "not found in the OpenAPI file"), body: body, query: query, variables: variables}, nil
}

func goVariables(v any) (map[string]any, error) {
	if v == nil {
		return nil, nil
	}
	if _, ok := v.(*jsObj); !ok {
		return nil, errors.New("variables must be an object")
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(jsStringify(v, false)), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// savedQueryText asks GraphJin for a saved query; ok is false when the
// reply names no operation and no query.
func savedQueryText(ctx context.Context, ep graphjinEndpoint, name, namespace string) (operation, query string, ok bool) {
	in := jsObjOf("name", name)
	if namespace != "" {
		in.set("namespace", namespace)
	}
	result, err := graphjinMCPRequest(ctx, ep, "tools/call", jsObjOf("name", "get_saved_query", "arguments", in))
	if err != nil {
		return "", "", false
	}
	details, _ := jsGet(result, "structuredContent").(*jsObj)
	if details == nil {
		if content, ok := jsGet(result, "content").([]any); ok && len(content) > 0 {
			if text, ok := jsGet(content[0], "text").(string); ok {
				parsed, _ := jsParse([]byte(text))
				details, _ = parsed.(*jsObj)
			}
		}
	}
	operation, _ = jsGet(details, "operation").(string)
	query, _ = jsGet(details, "query").(string)
	return operation, query, operation != "" || query != ""
}

func savedQueryIsMutation(operation, query string) bool {
	return strings.EqualFold(operation, "mutation") || leadingMutation.MatchString(query)
}

// planGraphjinMutation gives nil when the call changes nothing. A saved
// query whose kind GraphJin does not give counts as a mutation.
func (r *runTools) planGraphjinMutation(ctx context.Context, in graphjinToolCall) (*graphjinMutationPlan, error) {
	if in.Name != "execute_graphql" && in.Name != "execute_saved_query" {
		return nil, nil
	}
	query := jsStringOrEmpty(jsGet(in.Arguments, "query"))
	if in.Name == "execute_graphql" && !graphqlMutationWord.MatchString(query) {
		return nil, nil
	}
	if refusal, err := r.api.savedQueryRefusal(ctx, in); err != nil || refusal != nil {
		return nil, err
	}
	if in.RunID == nil {
		return nil, errors.New("GraphJin mutation blocked: only a run can ask for a change")
	}
	if err := r.api.runHoldsDataSource(ctx, in.OrgID, in.RunID); err != nil {
		return nil, err
	}
	name, namespace := "", ""
	if in.Name == "execute_saved_query" {
		ep, err := r.api.graphjinMCPAccess(ctx, in.OrgID, in.RunID)
		if err != nil {
			return nil, err
		}
		name = jsStringOrEmpty(jsGet(in.Arguments, "name"))
		namespace, _ = jsGet(in.Arguments, "namespace").(string)
		operation, text, ok := savedQueryText(ctx, ep, name, namespace)
		if ok && !savedQueryIsMutation(operation, text) {
			return nil, nil
		}
		if !ok || text == "" {
			return nil, errors.New("GraphJin saved mutation blocked: OpenNeko cannot read the saved query")
		}
		query = text
	}
	variables, err := goVariables(jsGet(in.Arguments, "variables"))
	if err != nil {
		return nil, errors.New("GraphJin mutation blocked: " + err.Error())
	}
	plan, err := planGraphjinAPIMutation(os.Getenv("OPENNEKO_GRAPHJIN_CONFIG"), query, variables)
	if err != nil {
		return nil, errors.New("GraphJin mutation blocked: " + err.Error())
	}
	if plan == nil {
		return nil, errors.New("GraphJin saved mutation blocked: OpenNeko cannot read the saved query")
	}
	plan.savedQuery, plan.namespace = name, namespace
	return plan, nil
}

// requestGraphjinMutation turns a run's API mutation into an action request.
func (b turnBinding) requestGraphjinMutation(ctx context.Context, be toolBackend, plan *graphjinMutationPlan) (any, error) {
	intent := "Call " + plan.operation + " on the " + plan.source + " API"
	result, err := b.runPackAction(ctx, be, graphjinAPIMutationKind, "external", jsObjOf("payload", plan.payload(), "intent", intent))
	if err != nil {
		return nil, err
	}
	return toolResultObj(result), nil
}

// executeGraphjinAPIMutation checks the approved mutation again and sends
// it under the identity of the run that asked for it.
func (a *API) executeGraphjinAPIMutation(ctx context.Context, req *actionRequestRecord) (*actionExecutionOutcome, error) {
	if req.WorkRunID == nil || *req.WorkRunID == "" {
		return nil, errors.New("a GraphJin API mutation needs the run that asked for it")
	}
	query, _ := req.Payload.vals["query"].(string)
	saved, _ := req.Payload.vals["savedQuery"].(string)
	namespace, _ := req.Payload.vals["namespace"].(string)
	variables, err := goVariables(req.Payload.vals["variables"])
	if err != nil {
		return nil, err
	}
	plan, err := planGraphjinAPIMutation(os.Getenv("OPENNEKO_GRAPHJIN_CONFIG"), query, variables)
	if err == nil && plan == nil {
		err = errors.New("the request holds no mutation")
	}
	if err != nil {
		return nil, errors.New("GraphJin mutation blocked: " + err.Error())
	}
	if plan.source != req.Payload.vals["source"] || plan.operation != req.Payload.vals["operation"] {
		return nil, errors.New("GraphJin mutation blocked: the operation differs from the approved one")
	}
	ep, err := a.graphjinMCPAccess(ctx, req.OrgID, req.WorkRunID)
	if err != nil {
		return nil, err
	}
	tool, args := "execute_graphql", jsObjOf("query", query)
	if saved != "" {
		if _, text, _ := savedQueryText(ctx, ep, saved, namespace); text != query {
			return nil, errors.New("GraphJin mutation blocked: the saved query changed after the approval")
		}
		tool, args = "execute_saved_query", jsObjOf("name", saved)
		if namespace != "" {
			args.set("namespace", namespace)
		}
	}
	if v, ok := req.Payload.vals["variables"]; ok {
		args.set("variables", v)
	}
	result, err := callGraphjinMCPTool(ctx, ep, tool, args)
	if err != nil {
		return nil, err
	}
	text := jsStringify(result, false)
	if jsGet(result, "isError") == true || graphjinReplyHasErrors(result) {
		return nil, errors.New("GraphJin refused the mutation: " + factValue(text))
	}
	operation := plan.source + "." + plan.operation
	return &actionExecutionOutcome{Status: "succeeded", CommandOrOperation: &operation, Result: jsObjOf("graphjin", result)}, nil
}

func graphjinReplyHasErrors(result any) bool {
	content, _ := jsGet(result, "content").([]any)
	for _, item := range content {
		text, _ := jsGet(item, "text").(string)
		parsed, _ := jsParse([]byte(text))
		if list, ok := jsGet(parsed, "errors").([]any); ok && len(list) > 0 {
			return true
		}
	}
	if list, ok := jsGet(jsGet(result, "structuredContent"), "errors").([]any); ok && len(list) > 0 {
		return true
	}
	return false
}
