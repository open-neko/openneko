package webfront

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	graphjinStateFile    = ".openneko-graphjin-state"
	graphjinConfigMaxLen = 512 * 1024
	describedSourceLimit = 5
)

type graphjinRunState struct {
	State    string  `json:"state"`
	Reason   *string `json:"reason"`
	Since    *string `json:"since"`
	Restarts float64 `json:"restarts"`
}

type graphjinFileView struct {
	Path  *string           `json:"path"`
	YAML  *string           `json:"yaml"`
	Error *string           `json:"error"`
	State *graphjinRunState `json:"state"`
}

type graphjinDescription struct {
	Source  map[string]string `json:"source"`
	Error   *string           `json:"error"`
	Summary any               `json:"summary"`
}

var yamlErrorLine = regexp.MustCompile(`line (\d+)`)

func strPtr(s string) *string { return &s }

func readGraphjinRunState(configFile string) *graphjinRunState {
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(configFile), graphjinStateFile))
	if err != nil {
		return nil
	}
	var parsed map[string]any
	if json.Unmarshal(raw, &parsed) != nil {
		return nil
	}
	state, ok := parsed["state"].(string)
	if !ok {
		return nil
	}
	out := &graphjinRunState{State: state}
	if reason, _ := parsed["reason"].(string); reason != "" {
		out.Reason = strPtr(cutUTF16(redactSourceConfig(reason).(string), 500))
	}
	if since, ok := parsed["since"].(string); ok {
		out.Since = &since
	}
	out.Restarts, _ = parsed["restarts"].(float64)
	return out
}

// Comments are dropped: a comment can hold a secret that no key names.
func readGraphjinFileView(configFile string) graphjinFileView {
	path := strings.TrimSpace(configFile)
	if path == "" {
		return graphjinFileView{Error: strPtr("OPENNEKO_GRAPHJIN_CONFIG is not set")}
	}
	view := graphjinFileView{Path: &path, State: readGraphjinRunState(path)}
	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		view.Error = strPtr("the file does not exist")
	case errors.Is(err, fs.ErrPermission):
		view.Error = strPtr("OpenNeko may not read the file")
	case err != nil:
		view.Error = strPtr("OpenNeko could not read the file")
	case len(raw) > graphjinConfigMaxLen:
		view.Error = strPtr("the file is larger than 512 KB")
	}
	if view.Error != nil {
		return view
	}
	doc, err := yamlToJS(raw)
	if err != nil {
		line := "?"
		if m := yamlErrorLine.FindStringSubmatch(err.Error()); m != nil {
			line = m[1]
		}
		view.Error = strPtr("the file is not valid YAML (line " + line + ")")
		return view
	}
	if doc == nil {
		doc = &jsObj{vals: map[string]any{}}
	}
	text, err := encodeYAML(jsToYAMLNode(redactSourceConfig(doc)))
	if err != nil {
		view.Error = strPtr("OpenNeko could not read the file")
		return view
	}
	view.YAML = &text
	return view
}

func (a *API) adminGraphjinConfig(w http.ResponseWriter, r *http.Request) {
	act, ok := a.settingsAdmin(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	rows, err := a.DB.Query(ctx, `select id::text, name from data_source
		where org_id = $1 and enabled and kind = 'graphjin' order by name limit $2`, act.OrgID, describedSourceLimit)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	sources, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) ([2]string, error) {
		var s [2]string
		return s, row.Scan(&s[0], &s[1])
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	results := make([]*graphjinDescription, len(sources))
	var wg sync.WaitGroup
	for i, s := range sources {
		wg.Go(func() {
			one, cancel := context.WithTimeout(ctx, 8*time.Second)
			defer cancel()
			d := &graphjinDescription{Source: map[string]string{"id": s[0], "name": s[1]}}
			view, err := a.readSourceConfigView(one, act.OrgID, act.UserID, &s[0])
			if err != nil {
				d.Error = strPtr(cutUTF16(err.Error(), 1000))
				results[i] = d
				return
			}
			if denied, _ := jsGet(view, "denied").(bool); denied {
				return
			}
			if msg, ok := jsGet(view, "error").(string); ok {
				d.Error = &msg
			}
			d.Summary = jsGet(view, "summary")
			results[i] = d
		})
	}
	wg.Wait()
	descriptions := []*graphjinDescription{}
	for _, d := range results {
		if d != nil {
			descriptions = append(descriptions, d)
		}
	}
	sendJSON(w, http.StatusOK, map[string]any{
		"descriptions": descriptions,
		"file":         readGraphjinFileView(os.Getenv("OPENNEKO_GRAPHJIN_CONFIG")),
	})
}
