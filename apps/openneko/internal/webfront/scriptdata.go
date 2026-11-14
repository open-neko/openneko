package webfront

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

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
	}, nil
}

// scriptDataTool answers sandbox.DataTool on the bridge with the
// execute_graphql result; the harness reads the data object from it.
func scriptDataTool(call func(ctx context.Context, q sandbox.DataQuery) (*jsObj, error)) func(ctx context.Context, raw json.RawMessage) (*jsObj, error) {
	return func(ctx context.Context, raw json.RawMessage) (*jsObj, error) {
		var q sandbox.DataQuery
		if err := json.Unmarshal(raw, &q); err != nil || q.Query == "" {
			return nil, errors.New("the script data request is not valid")
		}
		return call(ctx, q)
	}
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
