package webfront

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/open-neko/neko/apps/openneko/internal/config"
)

var errNoContextRemote = remoteError{msg: "Connect a remote repository first."}

type contextRemoteRow struct {
	orgID, url, mode, branch                      string
	kinds, skillBases                             []byte
	username, token, sshPrivate, sshPublic, hosts *string
	hostConfirmed                                 bool
	publishedAt                                   *time.Time
	publishStatus, publishDetail, publishLink     *string
}

func (a *API) readContextRemote(ctx context.Context, orgID string) (*contextRemoteRow, error) {
	row := contextRemoteRow{orgID: orgID}
	err := a.DB.QueryRow(ctx, `select url, mode, branch, kinds, skill_bases, username, token, ssh_private_key,
		ssh_public_key, ssh_known_hosts, ssh_host_confirmed, last_published_at, last_publish_status,
		last_publish_detail, last_publish_link from context_remote where org_id = $1`, orgID).Scan(
		&row.url, &row.mode, &row.branch, &row.kinds, &row.skillBases, &row.username, &row.token, &row.sshPrivate,
		&row.sshPublic, &row.hosts, &row.hostConfirmed, &row.publishedAt, &row.publishStatus,
		&row.publishDetail, &row.publishLink)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// storedKinds is storedKinds() in config-vcs/remote-settings.ts: retired
// kinds such as library drop out.
func storedKinds(raw []byte) []string {
	var saved []string
	if json.Unmarshal(raw, &saved) != nil || saved == nil {
		saved = defaultContextRemoteKinds
	}
	kinds := []string{}
	for _, kind := range saved {
		if slices.Contains(contextRemoteKinds, kind) {
			kinds = append(kinds, kind)
		}
	}
	return kinds
}

type remoteSSHSettings struct {
	PublicKey        string   `json:"publicKey"`
	HostFingerprints []string `json:"hostFingerprints"`
	HostConfirmed    bool     `json:"hostConfirmed"`
}

type remoteLastPublish struct {
	At     string  `json:"at"`
	Status string  `json:"status"`
	Detail string  `json:"detail"`
	Link   *string `json:"link"`
}

type contextRemoteSettings struct {
	URL         string             `json:"url"`
	Transport   string             `json:"transport"`
	SSH         *remoteSSHSettings `json:"ssh"`
	Provider    string             `json:"provider"`
	Mode        string             `json:"mode"`
	Branch      string             `json:"branch"`
	Kinds       []string           `json:"kinds"`
	Username    *string            `json:"username"`
	HasToken    bool               `json:"hasToken"`
	LastPublish *remoteLastPublish `json:"lastPublish"`
}

func (row *contextRemoteRow) settings() *contextRemoteSettings {
	u, err := url.Parse(row.url)
	if err != nil {
		u = &url.URL{}
	}
	s := &contextRemoteSettings{URL: row.url, Transport: "https", Provider: remoteProvider(u), Mode: row.mode,
		Branch: row.branch, Kinds: storedKinds(row.kinds), Username: row.username, HasToken: deref(row.token) != ""}
	if remoteTransport(u) == "ssh" {
		s.Transport = "ssh"
		if deref(row.sshPublic) != "" {
			s.SSH = &remoteSSHSettings{PublicKey: *row.sshPublic, HostFingerprints: hostKeyFingerprints(deref(row.hosts)), HostConfirmed: row.hostConfirmed}
		}
	}
	if row.publishedAt != nil && deref(row.publishStatus) != "" {
		s.LastPublish = &remoteLastPublish{At: jsTime(*row.publishedAt), Status: *row.publishStatus,
			Detail: deref(row.publishDetail), Link: row.publishLink}
	}
	return s
}

func (a *API) contextRemoteSettings(ctx context.Context, orgID string) (*contextRemoteSettings, error) {
	row, err := a.readContextRemote(ctx, orgID)
	if err != nil || row == nil {
		return nil, err
	}
	return row.settings(), nil
}

// access is remoteAccess() in config-vcs/remote-settings.ts.
func (a *API) remoteAccess(row *contextRemoteRow) (remoteAccess, error) {
	u, err := parseRemoteURL(row.url)
	if err != nil {
		return remoteAccess{}, err
	}
	ssh := remoteTransport(u) == "ssh"
	if ssh && !row.hostConfirmed {
		return remoteAccess{}, remoteError{msg: "Confirm the server's SSH host key first."}
	}
	access := remoteAccess{url: u, username: deref(row.username)}
	if row.token != nil && *row.token != "" {
		if access.token, err = config.MaybeDecryptValue(a.SecretsDir, *row.token); err != nil {
			return remoteAccess{}, err
		}
	}
	if ssh && deref(row.sshPrivate) != "" {
		if access.sshKey, err = config.MaybeDecryptValue(a.SecretsDir, *row.sshPrivate); err != nil {
			return remoteAccess{}, err
		}
		access.knownHosts = deref(row.hosts)
	}
	return access, nil
}

func (a *API) sendRemoteFailure(w http.ResponseWriter, r *http.Request, err error) {
	var re remoteError
	if errors.As(err, &re) {
		sendError(w, http.StatusBadRequest, re.msg)
		return
	}
	a.fail(w, r, err)
}

func (a *API) contextRemoteGet(w http.ResponseWriter, r *http.Request) {
	act, ok := a.settingsAdmin(w, r)
	if !ok {
		return
	}
	remote, err := a.contextRemoteSettings(r.Context(), act.OrgID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	sendJSON(w, http.StatusOK, map[string]any{"remote": remote})
}

func (a *API) contextRemotePut(w http.ResponseWriter, r *http.Request) {
	act, ok := a.settingsAdmin(w, r)
	if !ok {
		return
	}
	body := readJSONObject(r)
	if body == nil {
		sendError(w, http.StatusBadRequest, "A JSON body is required.")
		return
	}
	remote, err := a.saveContextRemote(r.Context(), act, body)
	if err != nil {
		a.sendRemoteFailure(w, r, err)
		return
	}
	sendJSON(w, http.StatusOK, map[string]any{"remote": remote})
}

func sshEndpoint(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "ssh" {
		return ""
	}
	port := u.Port()
	if port == "" {
		port = "22"
	}
	return u.Hostname() + ":" + port
}

// saveContextRemote is saveContextRemote() in config-vcs/remote-settings.ts.
func (a *API) saveContextRemote(ctx context.Context, act actor, draft map[string]any) (*contextRemoteSettings, error) {
	rawURL, _ := draft["url"].(string)
	parsed, err := parseRemoteURL(rawURL)
	if err != nil {
		return nil, err
	}
	remoteURL := parsed.String()
	mode, _ := draft["mode"].(string)
	if mode != "push" && mode != "pull_request" {
		return nil, remoteError{msg: "Choose push or pull request."}
	}
	branch, _ := draft["branch"].(string)
	branch = strings.TrimSpace(branch)
	if !validBranchName(branch) {
		return nil, remoteError{msg: "Enter a valid branch name, such as main."}
	}
	rawKinds, _ := draft["kinds"].([]any)
	kinds := []any{}
	for _, k := range rawKinds {
		kind, isString := k.(string)
		if !isString || !slices.Contains(contextRemoteKinds, kind) {
			return nil, remoteError{msg: "Choose at least one kind of context to publish."}
		}
		if !slices.Contains(kinds, any(kind)) {
			kinds = append(kinds, kind)
		}
	}
	if len(kinds) == 0 {
		return nil, remoteError{msg: "Choose at least one kind of context to publish."}
	}
	var username *string
	switch v := draft["username"].(type) {
	case string:
		username = optional(strings.TrimSpace(v))
	case nil:
	default:
		return nil, remoteError{msg: "Enter the user name as text."}
	}
	kindsJSON, _ := json.Marshal(kinds)
	cols := []string{"url", "mode", "branch", "kinds", "username", "updated_by_user_id", "updated_at"}
	vals := []any{remoteURL, mode, branch, string(kindsJSON), username, optional(act.UserID), time.Now()}

	rawToken, tokenChanged := draft["token"]
	if tokenChanged {
		var token *string
		switch v := rawToken.(type) {
		case string:
			if v = strings.TrimSpace(v); v != "" {
				enc, err := config.EncryptValue(a.SecretsDir, v)
				if err != nil {
					return nil, err
				}
				token = &enc
			}
		case nil:
		default:
			return nil, remoteError{msg: "Enter the token as text."}
		}
		cols, vals = append(cols, "token"), append(vals, token)
	}
	sshKeyChanged, hostKeysChanged := false, false
	if remoteTransport(parsed) == "ssh" {
		previous, err := a.readContextRemote(ctx, act.OrgID)
		if err != nil {
			return nil, err
		}
		if previous == nil || deref(previous.sshPrivate) == "" || truthy(draft["newSshKey"]) {
			private, public, err := generateDeployKey("openneko-" + act.OrgID)
			if err != nil {
				return nil, err
			}
			enc, err := config.EncryptValue(a.SecretsDir, private)
			if err != nil {
				return nil, err
			}
			cols, vals = append(cols, "ssh_private_key", "ssh_public_key"), append(vals, enc, public)
			sshKeyChanged = true
		}
		if previous == nil || deref(previous.hosts) == "" || sshEndpoint(previous.url) != sshEndpoint(remoteURL) {
			port := 22
			if p, err := strconv.Atoi(parsed.Port()); err == nil {
				port = p
			}
			hosts, err := scanHostKeys(parsed.Hostname(), port)
			if err != nil {
				return nil, err
			}
			cols, vals = append(cols, "ssh_known_hosts", "ssh_host_confirmed"), append(vals, hosts, false)
			hostKeysChanged = true
		}
	}
	params := []string{}
	sets := []string{}
	for i, col := range cols {
		cast := ""
		if col == "kinds" {
			cast = "::jsonb"
		}
		params = append(params, fmt.Sprintf("$%d%s", i+2, cast))
		sets = append(sets, col+" = excluded."+col)
	}
	_, err = a.DB.Exec(ctx, `insert into context_remote (org_id, `+strings.Join(cols, ", ")+`) values ($1, `+
		strings.Join(params, ", ")+`) on conflict (org_id) do update set `+strings.Join(sets, ", "), append([]any{act.OrgID}, vals...)...)
	if err != nil {
		return nil, err
	}
	a.recordAudit(ctx, auditEvent{OrgID: act.OrgID, EntityKind: "context_remote", EntityID: act.OrgID, Event: "context:remote_changed",
		Payload: map[string]any{"actorUserId": nullable(act.UserID), "url": remoteURL, "mode": mode, "branch": branch, "kinds": kinds,
			"tokenChanged": tokenChanged, "sshKeyChanged": sshKeyChanged, "hostKeysChanged": hostKeysChanged}})
	return a.contextRemoteSettings(ctx, act.OrgID)
}

func (a *API) contextRemoteDelete(w http.ResponseWriter, r *http.Request) {
	act, ok := a.settingsAdmin(w, r)
	if !ok {
		return
	}
	if _, err := a.DB.Exec(r.Context(), `delete from context_remote where org_id = $1`, act.OrgID); err != nil {
		a.fail(w, r, err)
		return
	}
	a.recordAudit(r.Context(), auditEvent{OrgID: act.OrgID, EntityKind: "context_remote", EntityID: act.OrgID,
		Event: "context:remote_removed", Payload: map[string]any{"actorUserId": nullable(act.UserID)}})
	sendJSON(w, http.StatusOK, map[string]any{"remote": nil})
}

// contextRemoteHostKey records that the administrator confirmed the SSH
// server host keys that OpenNeko read on save.
func (a *API) contextRemoteHostKey(w http.ResponseWriter, r *http.Request) {
	act, ok := a.settingsAdmin(w, r)
	if !ok {
		return
	}
	row, err := a.readContextRemote(r.Context(), act.OrgID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if row == nil || deref(row.hosts) == "" {
		sendError(w, http.StatusBadRequest, "This remote has no SSH host key to confirm.")
		return
	}
	if _, err := a.DB.Exec(r.Context(), `update context_remote set ssh_host_confirmed = true where org_id = $1`, act.OrgID); err != nil {
		a.fail(w, r, err)
		return
	}
	fingerprints := []any{}
	for _, f := range hostKeyFingerprints(*row.hosts) {
		fingerprints = append(fingerprints, f)
	}
	a.recordAudit(r.Context(), auditEvent{OrgID: act.OrgID, EntityKind: "context_remote", EntityID: act.OrgID,
		Event: "context:host_key_confirmed", Payload: map[string]any{"actorUserId": nullable(act.UserID), "fingerprints": fingerprints}})
	row.hostConfirmed = true
	sendJSON(w, http.StatusOK, map[string]any{"remote": row.settings()})
}

func (a *API) publishOptions(row *contextRemoteRow, root string) (publishOptions, error) {
	access, err := a.remoteAccess(row)
	if err != nil {
		return publishOptions{}, err
	}
	return publishOptions{root: root, branch: row.branch, access: access, kinds: storedKinds(row.kinds),
		exclude: unmodifiedBuiltinSkills(filepath.Join(root, "skills")), bases: readSkillBases(row.skillBases)}, nil
}

// contextRemotePreview lists what a publish would send now. With
// ?kind=…&name=… it gives the diff for that one item.
func (a *API) contextRemotePreview(w http.ResponseWriter, r *http.Request) {
	act, ok := a.settingsAdmin(w, r)
	if !ok {
		return
	}
	ctx := context.WithoutCancel(r.Context())
	row, err := a.readContextRemote(ctx, act.OrgID)
	if err == nil && row == nil {
		err = errNoContextRemote
	}
	if err != nil {
		a.sendRemoteFailure(w, r, err)
		return
	}
	opts, err := a.publishOptions(row, orgAgentRoot(act.OrgID))
	if err != nil {
		a.sendRemoteFailure(w, r, err)
		return
	}
	q := r.URL.Query()
	if kind, name := q.Get("kind"), q.Get("name"); kind != "" && name != "" {
		if !slices.Contains(opts.kinds, kind) {
			sendError(w, http.StatusBadRequest, "This remote does not publish that kind.")
			return
		}
		diff, err := publishItemDiff(opts, kind, name)
		if err != nil {
			a.sendRemoteFailure(w, r, err)
			return
		}
		sendJSON(w, http.StatusOK, diff)
		return
	}
	items, err := previewPublish(opts)
	if err != nil {
		a.sendRemoteFailure(w, r, err)
		return
	}
	sendJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) contextRemotePublish(w http.ResponseWriter, r *http.Request) {
	act, ok := a.settingsAdmin(w, r)
	if !ok {
		return
	}
	ctx := context.WithoutCancel(r.Context())
	row, err := a.readContextRemote(ctx, act.OrgID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var result publishResult
	if row == nil {
		err = errNoContextRemote
	} else {
		result, err = a.publishOrgContext(ctx, act, row)
	}
	remote, readErr := a.contextRemoteSettings(ctx, act.OrgID)
	if readErr != nil {
		a.fail(w, r, readErr)
		return
	}
	if err != nil {
		var re remoteError
		if !errors.As(err, &re) {
			a.fail(w, r, err)
			return
		}
		sendJSON(w, http.StatusBadRequest, map[string]any{"error": re.msg, "remote": remote})
		return
	}
	sendJSON(w, http.StatusOK, map[string]any{"result": result, "remote": remote})
}

// publishOrgContext is publishOrgContext() in config-vcs/remote-settings.ts:
// publish, then record the outcome on the row and in the audit chain.
func (a *API) publishOrgContext(ctx context.Context, act actor, row *contextRemoteRow) (publishResult, error) {
	record := func(status, detail, link string) error {
		if len(detail) > 1000 {
			detail = detail[:1000]
		}
		_, err := a.DB.Exec(ctx, `update context_remote set last_published_at = now(), last_publish_status = $2,
			last_publish_detail = $3, last_publish_link = $4 where org_id = $1`, act.OrgID, status, detail, optional(link))
		return err
	}
	root := orgAgentRoot(act.OrgID)
	opts, err := a.publishOptions(row, root)
	var result publishResult
	if err == nil {
		result, err = publishContext(opts, row.mode, time.Now())
	}
	if err != nil {
		var re remoteError
		if !errors.As(err, &re) {
			log.Printf("[config-vcs] publish failed: %v", err)
			re = remoteError{msg: "Publishing failed."}
		}
		if recErr := record("failed", re.msg, re.link); recErr != nil {
			return result, recErr
		}
		return result, re
	}
	if err := record("ok", result.Detail, deref(result.Link)); err != nil {
		return result, err
	}
	if result.Status == "pushed" && result.SHA != nil && result.Head != "" {
		remote := folderTrees(root, *result.SHA, "skills")
		local := folderTrees(root, result.Head, "skills")
		bases := readSkillBases(row.skillBases)
		for name, tree := range remote {
			if local[name] != "" {
				bases[name] = skillBase{Remote: tree, Local: local[name]}
			}
		}
		if err := a.saveSkillBases(ctx, act.OrgID, bases); err != nil {
			return result, err
		}
	}
	a.recordAudit(ctx, auditEvent{OrgID: act.OrgID, EntityKind: "context_remote", EntityID: act.OrgID, Event: "context:published",
		Payload: map[string]any{"actorUserId": nullable(act.UserID), "status": result.Status, "branch": result.Branch,
			"sha": nullable(deref(result.SHA)), "link": nullable(deref(result.Link))}})
	return result, nil
}

func (a *API) saveSkillBases(ctx context.Context, orgID string, bases map[string]any) error {
	raw, err := json.Marshal(bases)
	if err != nil {
		return err
	}
	_, err = a.DB.Exec(ctx, `update context_remote set skill_bases = $2::jsonb where org_id = $1`, orgID, string(raw))
	return err
}

// checkContextUpdates asks the worker, which validates packs, to compare
// the remote repository with OpenNeko's skills and packs.
func (a *API) checkContextUpdates(ctx context.Context, orgID string) (any, error) {
	row, err := a.readContextRemote(ctx, orgID)
	if err == nil && row == nil {
		err = errNoContextRemote
	}
	if err != nil {
		return nil, err
	}
	status, reply, err := a.workerSend(ctx, http.MethodGet, "/admin/context-remote/updates", workerAdminTimeout, nil, nil)
	if err != nil {
		return nil, errWorkerDown
	}
	parsed, _ := jsParse(reply)
	obj, _ := parsed.(*jsObj)
	if status >= 400 {
		if obj != nil {
			if message, isString := obj.vals["error"].(string); isString {
				return nil, remoteError{msg: message}
			}
		}
		return nil, remoteError{msg: fmt.Sprintf("Update check failed (%d)", status)}
	}
	if obj == nil {
		return nil, fmt.Errorf("worker update check: reply is not a JSON object")
	}
	return obj.vals["updates"], nil
}

var errWorkerDown = errors.New("worker is unavailable")

func (a *API) sendUpdatesFailure(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, errWorkerDown) {
		sendError(w, http.StatusBadGateway, err.Error())
		return
	}
	a.sendRemoteFailure(w, r, err)
}

// sendJSValue writes a body that holds values from jsParse.
func sendJSValue(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(jsStringify(body, false)))
}

// contextRemoteUpdates lists the skills and packs that changed in the
// remote repository. With ?skill=… it compares the repository's version of
// that skill with OpenNeko's.
func (a *API) contextRemoteUpdates(w http.ResponseWriter, r *http.Request) {
	act, ok := a.settingsAdmin(w, r)
	if !ok {
		return
	}
	ctx := context.WithoutCancel(r.Context())
	if skill := r.URL.Query().Get("skill"); skill != "" {
		row, err := a.readContextRemote(ctx, act.OrgID)
		if err == nil && row == nil {
			err = errNoContextRemote
		}
		if err != nil {
			a.sendRemoteFailure(w, r, err)
			return
		}
		diff, err := incomingSkillDiff(orgAgentRoot(act.OrgID), row.branch, skill)
		if err != nil {
			a.sendRemoteFailure(w, r, err)
			return
		}
		sendJSON(w, http.StatusOK, diff)
		return
	}
	updates, err := a.checkContextUpdates(ctx, act.OrgID)
	if err != nil {
		a.sendUpdatesFailure(w, r, err)
		return
	}
	body := &jsObj{}
	if updates != nil {
		body.set("updates", updates)
	}
	sendJSValue(w, http.StatusOK, body)
}

type skillChoice struct{ name, choice string }

type packUploadResult struct {
	ID    string `json:"id"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// contextRemoteApply brings in the chosen skills and adds the chosen packs
// as new versions.
func (a *API) contextRemoteApply(w http.ResponseWriter, r *http.Request) {
	act, ok := a.settingsAdmin(w, r)
	if !ok {
		return
	}
	body := readJSONObject(r)
	var skills []skillChoice
	if list, isList := body["skills"].([]any); isList {
		for _, item := range list {
			m, _ := item.(map[string]any)
			name, isString := m["name"].(string)
			choice, _ := m["choice"].(string)
			if isString && (choice == "remote" || choice == "local") {
				skills = append(skills, skillChoice{name, choice})
			}
		}
	}
	packs := []string{}
	if list, isList := body["packs"].([]any); isList {
		for _, item := range list {
			if id, isString := item.(string); isString {
				packs = append(packs, id)
			}
		}
	}
	ctx := context.WithoutCancel(r.Context())
	sha, archives, err := a.applyContextUpdates(ctx, act, skills, packs)
	if err != nil {
		a.sendRemoteFailure(w, r, err)
		return
	}
	results := []packUploadResult{}
	for _, archive := range archives {
		header := http.Header{"Content-Type": {"application/zip"}, "X-Openneko-Actor": {encodeURIComponent(act.UserID)}}
		status, reply, err := a.workerSend(ctx, http.MethodPost, "/admin/packs/upload", workerAdminTimeout, header, archive.bytes)
		message := ""
		switch {
		case err != nil:
			message = err.Error()
		case status >= 400:
			message = fmt.Sprintf("Upload failed (%d)", status)
			parsed, _ := jsParse(reply)
			if obj, isObj := parsed.(*jsObj); isObj {
				if text, isString := obj.vals["error"].(string); isString {
					message = text
				}
			}
		}
		results = append(results, packUploadResult{ID: archive.id, OK: message == "", Error: message})
	}
	updates, err := a.checkContextUpdates(ctx, act.OrgID)
	if err != nil {
		a.sendUpdatesFailure(w, r, err)
		return
	}
	reply := &jsObj{}
	reply.set("sha", nullable(sha))
	var packList []any
	for _, p := range results {
		item := &jsObj{}
		item.set("id", p.ID)
		item.set("ok", p.OK)
		if p.Error != "" {
			item.set("error", p.Error)
		}
		packList = append(packList, item)
	}
	if packList == nil {
		packList = []any{}
	}
	reply.set("packs", packList)
	if updates != nil {
		reply.set("updates", updates)
	}
	sendJSValue(w, http.StatusOK, reply)
}

type packArchive struct {
	id    string
	bytes []byte
}

// applyContextUpdates is applyOrgUpdates() in config-vcs/remote-settings.ts:
// it applies the choices against the remote tip from the last check. It
// commits the skills and returns the pack archives for the pack uploader.
func (a *API) applyContextUpdates(ctx context.Context, act actor, choices []skillChoice, packs []string) (string, []packArchive, error) {
	row, err := a.readContextRemote(ctx, act.OrgID)
	if err == nil && row == nil {
		err = errNoContextRemote
	}
	if err != nil {
		return "", nil, err
	}
	root := orgAgentRoot(act.OrgID)
	tip := gitRef(root, "refs/openneko/remote/"+row.branch)
	if tip == "" {
		return "", nil, remoteError{msg: "Check for updates first."}
	}
	remoteSkills := folderTrees(root, tip, "skills")
	var unknown, bringIn []string
	for _, c := range choices {
		if remoteSkills[c.name] == "" {
			unknown = append(unknown, c.name)
		} else if c.choice == "remote" {
			bringIn = append(bringIn, c.name)
		}
	}
	if len(unknown) > 0 {
		return "", nil, remoteError{msg: "The repository has no skill named " + strings.Join(unknown, ", ") + "."}
	}
	sort.Strings(bringIn)
	source := ""
	if u, err := url.Parse(row.url); err == nil {
		source = u.Hostname() + strings.TrimSuffix(u.Path, ".git")
	}
	noun := "skills"
	if len(bringIn) == 1 {
		noun = "skill"
	}
	sha, err := bringInRemoteSkills(root, tip, bringIn, "Brought in "+noun+" from "+source+": "+strings.Join(bringIn, ", "))
	if err != nil {
		return "", nil, err
	}
	if sha != "" {
		a.insertConfigChange(ctx, configChange{OrgID: act.OrgID, Kind: "skill", Ref: strings.Join(bringIn, ", "),
			Actor: act.UserID, SHA: sha, Summary: "Brought in from " + source})
	}
	head, err := gitOut(root, "rev-parse", "HEAD")
	if err != nil {
		return "", nil, err
	}
	localSkills := folderTrees(root, strings.TrimSpace(head), "skills")
	bases := readSkillBases(row.skillBases)
	chosen := []any{}
	for _, c := range choices {
		bases[c.name] = skillBase{Remote: remoteSkills[c.name], Local: localSkills[c.name]}
		chosen = append(chosen, map[string]any{"name": c.name, "choice": c.choice})
	}
	if err := a.saveSkillBases(ctx, act.OrgID, bases); err != nil {
		return "", nil, err
	}
	var archives []packArchive
	packIDs := []any{}
	for _, id := range packs {
		data, err := remotePackArchive(root, tip, id)
		if err != nil {
			return "", nil, err
		}
		archives = append(archives, packArchive{id, data})
		packIDs = append(packIDs, id)
	}
	a.recordAudit(ctx, auditEvent{OrgID: act.OrgID, EntityKind: "context_remote", EntityID: act.OrgID, Event: "context:pulled",
		Payload: map[string]any{"actorUserId": nullable(act.UserID), "tip": tip, "skills": chosen, "packs": packIDs, "sha": nullable(sha)}})
	return sha, archives, nil
}
