package webfront

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/open-neko/neko/apps/openneko/internal/config"
	"github.com/open-neko/neko/apps/openneko/internal/sourcecheck"
)

// answer reads a card answer as the setup cards send it: a string, or a
// list that holds one string.
func answer(body any, name string) string {
	v, _ := field(body, name)
	if list, ok := v.([]any); ok && len(list) == 1 {
		v = list[0]
	}
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

func answerPort(body any) *float64 {
	v, _ := field(body, "port")
	if n, ok := v.(float64); ok {
		return &n
	}
	n, err := strconv.ParseFloat(answer(body, "port"), 64)
	if err != nil || n == 0 || n != n {
		return nil
	}
	return &n
}

func (a *API) sourceCheck(w http.ResponseWriter, r *http.Request) {
	act, ok := a.settingsAdmin(w, r)
	if !ok {
		return
	}
	body, ok := readObjectBody(w, r)
	if !ok {
		return
	}
	check, err := a.checkSourceForOrg(r.Context(), act.OrgID, body)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	sendJSON(w, http.StatusOK, map[string]any{"check": check})
}

// checkSourceForOrg is checkSourceForOrg() in
// packages/llm/src/graphjin/source-check.ts: the password comes from the
// stored secret that secretRef names.
func (a *API) checkSourceForOrg(ctx context.Context, orgID string, body any) (sourcecheck.Result, error) {
	in := sourcecheck.Input{
		Type:     answer(body, "type"),
		Host:     answer(body, "host"),
		Port:     answerPort(body),
		Database: answer(body, "dbname"),
		User:     answer(body, "user"),
	}
	if in.Type == "" {
		in.Type = "postgres"
	}
	ref := answer(body, "secretRef")
	if ref != "" {
		var enc string
		err := a.DB.QueryRow(ctx, `select value_enc from data_source_secret where org_id = $1 and name = $2 limit 1`,
			orgID, ref).Scan(&enc)
		if errors.Is(err, pgx.ErrNoRows) {
			check := sourcecheck.Check(ctx, in)
			for i, f := range check.Findings {
				if f.Step == "login" && f.Status == "fail" {
					check.Findings[i].Message = `No stored password is named "` + ref + `". Save the password in the card first.`
				}
			}
			return check, nil
		}
		if err != nil {
			return sourcecheck.Result{}, err
		}
		if in.Password, err = config.MaybeDecryptValue(a.SecretsDir, enc); err != nil {
			return sourcecheck.Result{}, err
		}
	}
	return sourcecheck.Check(ctx, in), nil
}
