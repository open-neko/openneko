package webfront

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/open-neko/neko/apps/openneko/internal/llm"
)

// ProviderCredentialWarning is verifyAiCredentials() of the former worker as
// one line: empty when each enabled primary provider has a credential that
// reads and validates.
func (a *API) ProviderCredentialWarning(ctx context.Context) string {
	rows, err := a.DB.Query(ctx, `select org_id, provider from llm_provider_config
		where scope = 'primary' and enabled order by org_id`)
	if err != nil {
		return fmt.Sprintf("openneko web: warning: the model provider settings cannot be read: %v", err)
	}
	type stored struct{ org, provider string }
	var set []stored
	for rows.Next() {
		var s stored
		if err := rows.Scan(&s.org, &s.provider); err != nil {
			rows.Close()
			return fmt.Sprintf("openneko web: warning: the model provider settings cannot be read: %v", err)
		}
		if llm.IsPrimaryProvider(s.provider) {
			set = append(set, s)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Sprintf("openneko web: warning: the model provider settings cannot be read: %v", err)
	}
	routed, err := a.turnRoutedOrgs(ctx)
	if err != nil {
		return fmt.Sprintf("openneko web: warning: the model routes cannot be read: %v", err)
	}
	if len(set) == 0 && len(routed) == 0 {
		return "openneko web: warning: no model provider is set; an admin sets one in Admin > Settings"
	}
	var failed []string
	for _, s := range set {
		if _, err := llm.PrimarySettings(ctx, a.DB, a.SecretsDir, s.org); err != nil {
			failed = append(failed, fmt.Sprintf("org %s (%s): %v", s.org, s.provider, err))
		}
	}
	for _, org := range routed {
		if _, err := llm.ResolveTaskRoute(ctx, a.DB, a.SecretsDir, org, "turn"); err != nil {
			failed = append(failed, fmt.Sprintf("org %s (turn model): %v", org, err))
		}
	}
	if len(failed) == 0 {
		return ""
	}
	return "openneko web: warning: the model provider credential cannot be read for " + strings.Join(failed, "; ")
}

func (a *API) turnRoutedOrgs(ctx context.Context) ([]string, error) {
	rows, err := a.DB.Query(ctx, `select r.org_id from llm_task_route r
		join llm_model m on m.org_id = r.org_id and m.id = r.model_id
		where r.task = 'turn' and m.enabled order by r.org_id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}
