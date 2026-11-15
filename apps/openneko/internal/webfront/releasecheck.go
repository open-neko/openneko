package webfront

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// DatasourceQueryCheck runs one execute_graphql call at mcpURL as the org's
// GraphJin service role and requires a row back. The release pipelines run
// it to prove the signed path from web to GraphJin to the data source.
func DatasourceQueryCheck(ctx context.Context, secretsDir, mcpURL, orgID, query string) error {
	a := &API{SecretsDir: secretsDir}
	token, err := a.graphjinToken(orgID, "service", time.Minute)
	if err != nil {
		return fmt.Errorf("mint the GraphJin token: %w", err)
	}
	ep := graphjinEndpoint{url: mcpURL, headers: func(context.Context) (map[string]string, error) {
		return map[string]string{"authorization": "Bearer " + token}, nil
	}}
	result, err := callGraphjinMCPTool(ctx, ep, "execute_graphql", jsObjOf("query", query))
	if err != nil {
		return err
	}
	text := ""
	for _, item := range jsArray(jsGet(result, "content")) {
		if jsGet(item, "type") == "text" {
			s, _ := jsGet(item, "text").(string)
			text += s
		}
	}
	if jsTruthy(jsGet(result, "isError")) {
		return fmt.Errorf("execute_graphql failed: %s", jsSlice(text, 500))
	}
	var payload struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		return errors.New("execute_graphql gave no JSON data")
	}
	for _, field := range payload.Data {
		var rows []json.RawMessage
		if json.Unmarshal(field, &rows) == nil && len(rows) > 0 {
			return nil
		}
	}
	return errors.New("execute_graphql gave no rows")
}
