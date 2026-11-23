package webfront

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/open-neko/openneko/apps/ax-harness/sandbox"
)

//go:embed dataclient/neko_data.py
var nekoDataPy string

//go:embed dataclient/neko-query
var nekoQueryCLI string

// scriptDataCall runs a skill script's query with the run's neko_graphjin
// tools, under the run's identity, policy, audit and trace, and gives the
// execute_graphql result as the tools give it.
func scriptDataCall(b turnBinding, be toolBackend) (func(ctx context.Context, q sandbox.DataQuery) (*jsObj, error), error) {
	if b.Kind != "work" && b.Kind != "" {
		b.GraphjinToolPolicy = ""
	}
	if _, err := parseGraphjinToolPolicy(b.GraphjinToolPolicy); err != nil {
		return nil, err
	}
	tools := graphjinTools(b, be).dynamic
	return func(ctx context.Context, q sandbox.DataQuery) (*jsObj, error) {
		args := jsObjOf("query", q.Query)
		if len(q.Variables) > 0 && string(q.Variables) != "null" {
			vars, err := jsParse(q.Variables)
			if err != nil {
				return nil, errors.New("the query variables are not JSON")
			}
			args.set("variables", vars)
		}
		if q.Namespace != "" {
			args.set("namespace", q.Namespace)
		}
		result, err := tools.call(ctx, "execute_graphql", args)
		if err != nil {
			return nil, err
		}
		return normalizeCallResult(result)
	}, nil
}

// scriptDataBroker is the broker for the script steps, which run from this
// process.
func scriptDataBroker(b turnBinding, be toolBackend) (sandbox.DataBroker, error) {
	call, err := scriptDataCall(b, be)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, q sandbox.DataQuery) (json.RawMessage, error) {
		result, err := call(ctx, q)
		if err != nil {
			return nil, err
		}
		return scriptDataOf(result)
	}, nil
}

func scriptDataOf(result *jsObj) (json.RawMessage, error) {
	text := ""
	for _, item := range jsArray(jsGet(result, "content")) {
		if jsGet(item, "type") == "text" {
			text, _ = jsGet(item, "text").(string)
			break
		}
	}
	if jsTruthy(jsGet(result, "isError")) {
		if text == "" {
			text = "execute_graphql failed"
		}
		return nil, errors.New(text)
	}
	return sandbox.GraphQLData(json.RawMessage(jsStringify(result, false)))
}

// scriptDataTool answers sandbox.DataTool on the bridge with the data
// object of the execute_graphql result, or with the error as a tool error.
func scriptDataTool(call func(ctx context.Context, q sandbox.DataQuery) (*jsObj, error)) func(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	return func(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
		var q sandbox.DataQuery
		if err := json.Unmarshal(raw, &q); err != nil || q.Query == "" {
			return nil, errors.New("the script data request is not valid")
		}
		result, err := call(ctx, q)
		if err != nil {
			return nil, err
		}
		data, err := scriptDataOf(result)
		if err == nil && len(data) > sandbox.MaxDataBytes {
			err = errors.New(sandbox.DataTooLarge(len(data), sandbox.MaxDataBytes))
		}
		if err != nil {
			return json.RawMessage(`{"content":[{"type":"text","text":` + scriptDataText(err.Error()) + `}],"isError":true}`), nil
		}
		return json.RawMessage(`{"content":[{"type":"text","text":` + scriptDataText(`{"data":`+string(data)+`}`) + `}]}`), nil
	}
}

func scriptDataText(s string) string {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimSuffix(b.String(), "\n")
}

// installDataClient writes neko_data.py and neko-query into binRoot.
func installDataClient(binRoot string) error {
	if err := os.MkdirAll(binRoot, 0o777); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(binRoot, "neko_data.py"), []byte(nekoDataPy), 0o666); err != nil {
		return err
	}
	cli := filepath.Join(binRoot, "neko-query")
	if err := os.WriteFile(cli, []byte(nekoQueryCLI), 0o666); err != nil {
		return err
	}
	return os.Chmod(cli, 0o755)
}
