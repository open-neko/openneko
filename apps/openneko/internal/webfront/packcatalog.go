package webfront

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/open-neko/neko/apps/openneko/internal/config"
	"github.com/open-neko/neko/apps/openneko/internal/packs"
)

var packFailureNextStep = map[string]string{
	"upload":           "Correct the archive layout or manifest, then upload the pack again.",
	"inspection":       "Correct the reported manifest or artifact, then inspect the pack again.",
	"review":           "Correct the reported configuration or artifact, then review the pack again.",
	"oauth":            "Check the OAuth client, redirect URI, and requested scopes, then connect the account again.",
	"store_management": "Review the Magento store-management setting or rule limits and try again.",
}

type packDiagnostic struct {
	Code     string   `json:"code"`
	Phase    string   `json:"phase"`
	Causes   []string `json:"causes"`
	NextStep string   `json:"nextStep"`
}

// sendPackFailure is packFailure(): the message and a diagnostic for the
// pack author.
func sendPackFailure(w http.ResponseWriter, status int, err error, phase string) {
	message := err.Error()
	sendJSON(w, status, struct {
		Error      string         `json:"error"`
		Diagnostic packDiagnostic `json:"diagnostic"`
	}{message, packDiagnostic{Code: "pack_operation_failed", Phase: phase, Causes: []string{message}, NextStep: packFailureNextStep[phase]}})
}

func packsEmbeddedRoot() string {
	if dir := strings.TrimSpace(os.Getenv("OPENNEKO_PACKS_DIR")); dir != "" {
		abs, _ := filepath.Abs(dir)
		return abs
	}
	wd, _ := os.Getwd()
	return filepath.Join(wd, "packs")
}

func packUploadsRoot(orgID string) string {
	return filepath.Join(config.RootDir(), "agents", "orgs", strings.ReplaceAll(encodeURIComponent(orgID), ".", "%2E"), "packs")
}

func embeddedPackDirs(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() && packs.PackIDPattern.MatchString(e.Name()) {
			ids = append(ids, e.Name())
		}
	}
	return ids, nil
}

// packBundle is PackService.bundle(): an embedded pack first, else the
// org's uploaded pack.
func packBundle(orgID, packID, version string) (*packs.Bundle, error) {
	if !packs.PackIDPattern.MatchString(packID) {
		return nil, errors.New("invalid pack id")
	}
	var bundle *packs.Bundle
	var err error
	embedded := filepath.Join(packsEmbeddedRoot(), packID)
	if _, statErr := os.Stat(filepath.Join(embedded, "pack.yaml")); statErr == nil {
		bundle, err = packs.LoadBundle(embedded)
	} else {
		bundle, err = packs.LoadUploaded(packUploadsRoot(orgID), orgID, packID, version)
	}
	if err != nil {
		return nil, err
	}
	if packID != "magento" {
		for i, a := range bundle.Artifacts {
			v := a.Record()
			if a.Kind == packs.KindSource && v["kind"] == "database" && !packTruthy(v["host"]) {
				bundle.Artifacts[i].TargetRef = packID + ".binding." + packString(v["name"])
			}
		}
	}
	return bundle, nil
}

func packTruthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case string:
		return t != ""
	case float64:
		return t != 0
	}
	return true
}

func packString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case nil:
		return "undefined"
	}
	return jsStringify(v, false)
}

type packListEntry struct {
	ID        string  `json:"id"`
	Source    string  `json:"source"`
	Name      string  `json:"name"`
	Version   string  `json:"version"`
	Installed bool    `json:"installed"`
	Status    string  `json:"status"`
	LastError *string `json:"lastError"`
}

func (a *API) packList(ctx context.Context, orgID string) ([]packListEntry, error) {
	root := packsEmbeddedRoot()
	dirs, err := embeddedPackDirs(root)
	if err != nil {
		return nil, err
	}
	type install struct {
		status    string
		lastError *string
	}
	installs := map[string]install{}
	rows, err := a.DB.Query(ctx, `select pack_id, status, last_error from pack_install where org_id = $1`, orgID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		var row install
		if err := rows.Scan(&id, &row.status, &row.lastError); err != nil {
			rows.Close()
			return nil, err
		}
		installs[id] = row
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var bundles []*packs.Bundle
	for _, id := range dirs {
		info, err := os.Lstat(filepath.Join(root, id, "pack.yaml"))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() && info.Mode()&fs.ModeSymlink == 0 {
			continue
		}
		b, err := packBundle(orgID, id, "")
		if err != nil {
			return nil, err
		}
		bundles = append(bundles, b)
	}
	uploaded, err := packs.ListUploaded(packUploadsRoot(orgID), orgID)
	if err != nil {
		return nil, err
	}
	bundles = append(bundles, uploaded...)
	out := make([]packListEntry, 0, len(bundles))
	for _, b := range bundles {
		meta := b.Manifest.Metadata
		e := packListEntry{ID: meta.ID, Source: "embedded", Name: meta.Name, Version: meta.Version, Status: "available"}
		if b.Upload != nil {
			e.Source = "uploaded"
		}
		if row, ok := installs[meta.ID]; ok {
			e.Installed, e.Status, e.LastError = row.status == "installed", row.status, row.lastError
		}
		out = append(out, e)
	}
	return out, nil
}

var magentoPermissions = map[string]string{
	"database":    "view-only reporting",
	"apiWrite":    "specific Magento changes require approval and must be enabled individually",
	"customerPii": "available to authenticated read-only queries",
	"paymentData": "operational fields available; credentials and raw gateway payloads blocked",
}

type packBinding struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}

type packInspection struct {
	Source              string            `json:"source"`
	Upload              *packs.Upload     `json:"upload,omitempty"`
	Manifest            map[string]any    `json:"manifest"`
	ManifestHash        string            `json:"manifestHash"`
	BundleHash          string            `json:"bundleHash"`
	BindingRequirements []packBinding     `json:"bindingRequirements"`
	Permissions         map[string]string `json:"permissions"`
}

func inspectPack(orgID, packID, version string) (*packInspection, error) {
	b, err := packBundle(orgID, packID, version)
	if err != nil {
		return nil, err
	}
	out := &packInspection{Source: "embedded", Upload: b.Upload, Manifest: b.Manifest.Normalized(), ManifestHash: b.ManifestHash,
		BundleHash: b.BundleHash, BindingRequirements: []packBinding{}, Permissions: magentoPermissions}
	if b.Upload != nil {
		out.Source = "uploaded"
	}
	for _, art := range b.Artifacts {
		v := art.Record()
		if art.Kind == packs.KindSource && v["kind"] == "database" && !packTruthy(v["host"]) {
			out.BindingRequirements = append(out.BindingRequirements, packBinding{Key: art.Key, Name: packString(v["name"])})
		}
	}
	if packID != "magento" {
		out.Permissions = packs.Permissions(b)
	}
	return out, nil
}

var operatorReason = regexp.MustCompile(`^operator:([^:]+):(.*)$`)

func isoMillis(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

// packStatus is PackService.status(); nil when the org never installed it.
func (a *API) packStatus(ctx context.Context, orgID, packID string) (*jsObj, error) {
	var id, version, status string
	var installedAt *time.Time
	var lastError *string
	var rawConfig []byte
	err := a.DB.QueryRow(ctx, `select id::text, version, status, installed_at, last_error, config::text from pack_install
		where org_id = $1 and pack_id = $2 order by created_at desc limit 1`, orgID, packID).
		Scan(&id, &version, &status, &installedAt, &lastError, &rawConfig)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	parsed, err := jsParse(rawConfig)
	if err != nil {
		return nil, err
	}
	cfg, _ := parsed.(*jsObj)
	if cfg == nil {
		cfg = &jsObj{vals: map[string]any{}}
	}
	readiness := &jsObj{vals: map[string]any{}}
	if packTruthy(cfg.vals["_definitionOnly"]) {
		readiness.set("setup", readinessEntry("blocked", "configuration_required"))
	}
	rows, err := a.DB.Query(ctx, `select readiness, readiness_reason from pack_artifact where pack_install_id = $1`, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var state string
		var reason *string
		if err := rows.Scan(&state, &reason); err != nil {
			rows.Close()
			return nil, err
		}
		if reason == nil || !strings.HasPrefix(*reason, "operator:") {
			continue
		}
		capability, why := "operator", strings.TrimPrefix(*reason, "operator:")
		if m := operatorReason.FindStringSubmatch(*reason); m != nil {
			capability, why = m[1], m[2]
		}
		if _, ok := readiness.vals[capability]; !ok || state == "blocked" {
			readiness.set(capability, readinessEntry(state, why))
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	runtime, _ := cfg.vals["_runtime"].(*jsObj)
	if runtime != nil {
		list, _ := runtime.vals["readiness"].([]any)
		for _, c := range list {
			capability := packString(c)
			if _, ok := readiness.vals[capability]; ok {
				continue
			}
			if status == "installed" {
				readiness.set(capability, readinessEntry("ready", nil))
			} else {
				readiness.set(capability, readinessEntry("blocked", "pack_not_active"))
			}
		}
	}
	inputs := &jsObj{vals: map[string]any{}}
	for _, k := range cfg.ordered() {
		if !strings.HasPrefix(k, "_") {
			inputs.set(k, cfg.vals[k])
		}
	}
	configuration := &jsObj{}
	configuration.set("required", cfg.vals["_definitionOnly"] == true)
	configuration.set("inputs", inputs)
	bindings := any(&jsObj{vals: map[string]any{}})
	if runtime != nil {
		if source, ok := runtime.vals["source"].(*jsObj); ok {
			if sourceID, present := source.vals["id"]; present {
				configuration.set("dataSourceId", sourceID)
			}
		}
		if b, present := runtime.vals["bindings"]; present && b != nil {
			bindings = b
		}
	}
	configuration.set("sourceBindings", bindings)
	out := &jsObj{}
	out.set("packId", packID)
	out.set("version", version)
	out.set("status", status)
	out.set("readiness", readiness)
	if installedAt != nil {
		out.set("installedAt", isoMillis(*installedAt))
	} else {
		out.set("installedAt", nil)
	}
	if lastError != nil {
		out.set("lastError", *lastError)
	} else {
		out.set("lastError", nil)
	}
	out.set("configuration", configuration)
	return out, nil
}

func readinessEntry(status string, reason any) *jsObj {
	o := &jsObj{}
	o.set("status", status)
	if s, ok := reason.(string); ok {
		o.set("reason", s)
	} else {
		o.set("reason", nil)
	}
	return o
}

func (a *API) servePackList(w http.ResponseWriter, r *http.Request, act actor) {
	list, err := a.packList(r.Context(), act.OrgID)
	if err != nil {
		sendError(w, http.StatusInternalServerError, err.Error())
		return
	}
	sendJSON(w, http.StatusOK, map[string]any{"packs": list})
}

func (a *API) servePackUpload(w http.ResponseWriter, r *http.Request, act actor, data []byte) {
	dirs, err := embeddedPackDirs(packsEmbeddedRoot())
	if err != nil {
		sendPackFailure(w, http.StatusBadRequest, err, "upload")
		return
	}
	var actorID *string
	if act.UserID != "" {
		actorID = &act.UserID
	}
	upload, err := packs.StoreUpload(r.Context(), packUploadsRoot(act.OrgID), act.OrgID, data, append([]string{"magento"}, dirs...), actorID)
	if err != nil {
		sendPackFailure(w, http.StatusBadRequest, err, "upload")
		return
	}
	sendJSON(w, http.StatusOK, upload)
}

func (a *API) servePackRead(w http.ResponseWriter, r *http.Request, act actor, packID, action string) {
	switch action {
	case "inspect":
		out, err := inspectPack(act.OrgID, packID, r.URL.Query().Get("version"))
		if err != nil {
			sendPackFailure(w, http.StatusBadRequest, err, "inspection")
			return
		}
		sendJSON(w, http.StatusOK, out)
	case "plan":
		bundle, err := packBundle(act.OrgID, packID, r.URL.Query().Get("version"))
		if err == nil {
			var plan *packs.Plan
			if plan, err = a.packPlan(r.Context(), act.OrgID, bundle); err == nil {
				sendJSON(w, http.StatusOK, plan)
				return
			}
		}
		sendPackFailure(w, http.StatusBadRequest, err, "inspection")
	case "status":
		out, err := a.packStatus(r.Context(), act.OrgID, packID)
		if err != nil {
			sendPackFailure(w, http.StatusBadRequest, err, "inspection")
			return
		}
		if out == nil {
			sendError(w, http.StatusNotFound, fmt.Sprintf("pack %s is not installed", packID))
			return
		}
		sendJSON(w, http.StatusOK, out)
	case "doctor":
		out, err := a.packDoctor(context.WithoutCancel(r.Context()), act.OrgID, packID)
		if err != nil {
			sendPackFailure(w, http.StatusBadRequest, err, "inspection")
			return
		}
		sendJSON(w, http.StatusOK, out)
	}
}
