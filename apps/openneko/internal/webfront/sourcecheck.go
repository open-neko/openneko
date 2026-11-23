package webfront

import (
	"context"
	"errors"
	"fmt"
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
	a.serveSourceCheck(w, r, act)
}

func (a *API) serveSourceCheck(w http.ResponseWriter, r *http.Request, act actor) {
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
// request body or from the stored secret that secretRef names.
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
	if pw, ok := field(body, "password"); ok {
		if s, ok := pw.(string); ok && s != "" {
			in.Password = s
		}
	}
	ref := answer(body, "secretRef")
	if in.Password == "" && ref != "" {
		var enc string
		var host, dbname, login *string
		var port *int
		err := a.DB.QueryRow(ctx, `select value_enc, host, port, dbname, login, allow_unverified_tls from data_source_secret
			where org_id = $1 and name = $2 limit 1`, orgID, ref).Scan(&enc, &host, &port, &dbname, &login, &in.AllowUnverifiedTLS)
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
		in.PasswordTarget = &sourcecheck.Target{Host: deref(host), Database: deref(dbname), User: deref(login)}
		if port != nil {
			in.PasswordTarget.Port = float64(*port)
		}
	}
	return sourcecheck.Check(ctx, in), nil
}

type sourcePasswordRefusal struct{ field, message string }

func (e sourcePasswordRefusal) Error() string { return e.message }

// storedSourcePassword is the stored password named ref, for a source
// at host and port, with the database and login it was saved with, only.
func (a *API) storedSourcePassword(ctx context.Context, orgID, ref, kind, host string, port *float64, database, user string) (string, error) {
	var enc string
	var storedHost, storedDB, storedLogin *string
	var storedPort *int
	err := a.DB.QueryRow(ctx, `select value_enc, host, port, dbname, login from data_source_secret where org_id = $1 and name = $2 limit 1`,
		orgID, ref).Scan(&enc, &storedHost, &storedPort, &storedDB, &storedLogin)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("no stored source secret named \"%s\"; add it in GraphJin Config first", ref)
	}
	if err != nil {
		return "", err
	}
	target := sourcecheck.Target{Host: deref(storedHost), Database: deref(storedDB), User: deref(storedLogin)}
	if storedPort != nil {
		target.Port = float64(*storedPort)
	}
	if field, message := target.Refusal(kind, host, port, database, user); message != "" {
		return "", sourcePasswordRefusal{field, message}
	}
	return config.MaybeDecryptValue(a.SecretsDir, enc)
}

// sourcePasswordResolver gives buildGraphjinConfigUpdate the stored password
// for the database source that payload describes.
func (a *API) sourcePasswordResolver(ctx context.Context, orgID string, payload *jsObj, got func(ref, password string)) func(string) (string, error) {
	return func(ref string) (string, error) {
		var port *float64
		if v := payload.vals["port"]; jsTruthy(v) {
			n := numberOf(v)
			port = &n
		}
		password, err := a.storedSourcePassword(ctx, orgID, ref, selectedText(payload.vals["type"], "postgres"), trimmedText(payload.vals["host"]), port,
			trimmedText(payload.vals["dbname"]), trimmedText(payload.vals["user"]))
		if err == nil && got != nil {
			got(ref, password)
		}
		return password, err
	}
}
