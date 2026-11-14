package webfront

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/open-neko/neko/apps/openneko/internal/config"
	"github.com/open-neko/neko/apps/openneko/internal/packs"
	"github.com/open-neko/neko/apps/openneko/internal/secrets"
)

const graphjinNoKeystoreMessage = "The connected GraphJin has no secrets keystore key, so it cannot store this pack's credentials. Set secrets.keystore.key in that GraphJin's config, for example with GJ_SECRETS_KEYSTORE_KEY, restart GraphJin, then install the pack again."

var packEnvKeyChars = regexp.MustCompile(`[^A-Za-z0-9]+`)

func secretEnvKey(key string) string {
	return strings.ToUpper(packEnvKeyChars.ReplaceAllString(key, "_"))
}

// plainJSON is a JS value as the plain Go value of its JSON text.
func plainJSON(v any) any {
	var out any
	_ = json.Unmarshal([]byte(jsStringify(v, false)), &out)
	return out
}

func jsObjOrEmpty(v any) *jsObj {
	if o, ok := v.(*jsObj); ok {
		return o
	}
	return &jsObj{vals: map[string]any{}}
}

func isJSObj(v any) bool {
	_, ok := v.(*jsObj)
	return ok
}

// resolvePackInputs is resolveInputs() of apps/worker/src/packs/configuration.ts.
func resolvePackInputs(bundle *packs.Bundle, supplied *jsObj) (*jsObj, error) {
	for _, k := range supplied.ordered() {
		if !bundle.Manifest.InputDeclared(k) {
			return nil, fmt.Errorf("unknown pack input %s", k)
		}
	}
	out := &jsObj{vals: map[string]any{}}
	for _, in := range bundle.Manifest.Inputs {
		required := in.Required != nil && *in.Required
		value, ok := supplied.vals[in.Key]
		if !ok {
			value, ok = in.Default, in.Default != nil
		}
		if !ok {
			if required {
				return nil, fmt.Errorf("required pack input %s is missing", in.Key)
			}
			continue
		}
		switch in.Type {
		case "string", "timezone", "url":
			text, isString := value.(string)
			if !isString || (in.Type != "string" && strings.TrimSpace(text) == "") {
				if in.Type == "string" {
					return nil, fmt.Errorf("pack input %s must be a string", in.Key)
				}
				return nil, fmt.Errorf("pack input %s must be a non-empty %s", in.Key, in.Type)
			}
			normalized := strings.TrimSpace(text)
			if required && normalized == "" {
				return nil, fmt.Errorf("required pack input %s must not be empty", in.Key)
			}
			switch in.Type {
			case "url":
				u, err := url.Parse(normalized)
				if err != nil || u.Scheme == "" || (u.Host == "" && u.Opaque == "" && (u.Scheme == "http" || u.Scheme == "https")) {
					return nil, fmt.Errorf("pack input %s must be an absolute URL", in.Key)
				}
				if u.Scheme != "http" && u.Scheme != "https" {
					return nil, fmt.Errorf("pack input %s must use HTTP or HTTPS", in.Key)
				}
				out.set(in.Key, strings.TrimRight(normalized, "/"))
			case "timezone":
				if !ianaTimezone(normalized) {
					return nil, fmt.Errorf("pack input %s must be a valid IANA timezone", in.Key)
				}
				out.set(in.Key, normalized)
			default:
				out.set(in.Key, normalized)
			}
		case "integer":
			n, isNumber := value.(float64)
			if !isNumber {
				n = jsToNumber(value)
			}
			if math.IsNaN(n) || math.IsInf(n, 0) || n != math.Trunc(n) {
				return nil, fmt.Errorf("pack input %s must be an integer", in.Key)
			}
			out.set(in.Key, n)
		case "boolean":
			if _, ok := value.(bool); !ok {
				return nil, fmt.Errorf("pack input %s must be a boolean", in.Key)
			}
			out.set(in.Key, value)
		case "enum":
			var values []any
			if in.Values != nil {
				values = *in.Values
			}
			if !slices.ContainsFunc(values, func(c any) bool { return strictEqual(c, value) || c == nil && value == nil }) {
				parts := make([]string, len(values))
				for i, v := range values {
					parts[i] = jsValueString(v)
				}
				return nil, fmt.Errorf("pack input %s must be one of: %s", in.Key, strings.Join(parts, ", "))
			}
			out.set(in.Key, value)
		}
	}
	if port, ok := out.vals["database.port"]; ok {
		if n, isNumber := port.(float64); !isNumber || n < 1 || n > 65535 {
			return nil, errors.New("database.port must be an integer from 1 to 65535")
		}
	}
	return out, nil
}

// resolvePackSecrets is resolveSecrets(): the values only.
func (a *API) resolvePackSecrets(bundle *packs.Bundle, request *jsObj) (map[string]string, error) {
	values, _, _, err := a.resolvePackSecretState(bundle, request)
	return values, err
}

// resolvePackSecretState is resolveSecrets(): the values, the keys to clear
// and the store they came from.
func (a *API) resolvePackSecretState(bundle *packs.Bundle, request *jsObj) (map[string]string, map[string]bool, secrets.Store, error) {
	store, err := secrets.Read(a.SecretsDir)
	if err != nil {
		return nil, nil, nil, err
	}
	current := store["pack."+bundle.Manifest.Metadata.ID]
	direct := jsObjOrEmpty(request.vals["secrets"])
	refs := jsObjOrEmpty(request.vals["secretRefs"])
	for _, k := range append(direct.ordered(), refs.ordered()...) {
		if !slices.ContainsFunc(bundle.Manifest.Secrets, func(s packs.Secret) bool { return s.Key == k }) {
			return nil, nil, nil, fmt.Errorf("unknown pack secret %s", k)
		}
	}
	personal := map[string]bool{}
	for _, c := range bundle.Manifest.OAuth {
		if c.Scope == "user" {
			personal[c.AccessToken], personal[c.RefreshToken] = true, true
		}
	}
	values, cleared := map[string]string{}, map[string]bool{}
	for _, s := range bundle.Manifest.Secrets {
		given, hasGiven := direct.vals[s.Key]
		ref, hasRef := refs.vals[s.Key]
		if personal[s.Key] {
			if packTruthy(given) || packTruthy(ref) {
				return nil, nil, nil, errors.New("Personal OAuth tokens must be connected on Integrations")
			}
			cleared[s.Key] = true
			continue
		}
		text, isString := given.(string)
		if hasGiven && !isString {
			return nil, nil, nil, fmt.Errorf("pack secret %s must be a string", s.Key)
		}
		if hasGiven && strings.TrimSpace(text) == "" && !s.Required {
			cleared[s.Key] = true
			continue
		}
		value := current[secretEnvKey(s.Key)]
		if hasRef && packTruthy(ref) {
			value = current[jsValueString(ref)]
		}
		if hasGiven {
			value = text
		}
		if s.Required && strings.TrimSpace(value) == "" {
			return nil, nil, nil, fmt.Errorf("required pack secret %s is missing", s.Key)
		}
		if value != "" {
			values[s.Key] = value
		}
	}
	return values, cleared, store, nil
}

func graphjinConfigTables(file string) ([]any, error) {
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	parsed, err := packs.ParseYAML(raw)
	if err != nil {
		return nil, err
	}
	m, _ := parsed.(map[string]any)
	tables, _ := m["tables"].([]any)
	return tables, nil
}

// verifyPackQueryTables is verifyPackQueryTables() of packages/llm/src/graphjin/pack-source.ts.
func verifyPackQueryTables(tables []any) error {
	if len(tables) == 0 {
		return nil
	}
	file := os.Getenv("OPENNEKO_GRAPHJIN_CONFIG")
	if file == "" {
		return errors.New("Pack query routing cannot be verified")
	}
	current, err := graphjinConfigTables(file)
	if err != nil {
		return err
	}
	for _, expected := range tables {
		name := plainJSON(expected).(map[string]any)["name"]
		idx := slices.IndexFunc(current, func(t any) bool {
			m, _ := t.(map[string]any)
			return m != nil && strictEqual(m["name"], name)
		})
		if idx < 0 || graphjinConfigPatchHash(jsValueOf(current[idx])) != graphjinConfigPatchHash(jsValueOf(plainJSON(expected))) {
			return errors.New("Pack query routing changed; restore the reviewed table mapping")
		}
	}
	return nil
}

func sourceObj(s *packSource) *jsObj {
	return jsObjOf("id", s.ID, "graphqlUrl", s.GraphqlURL, "authMode", s.AuthMode)
}

func configText(v any) string {
	if v == nil {
		return ""
	}
	return packString(v)
}

// packRuntime is PackService.runtime(): the data source, the source
// bindings and their hashes.
func (a *API) packRuntime(ctx context.Context, orgID string, bundle *packs.Bundle, request, prior *jsObj) (*jsObj, error) {
	m := bundle.Manifest
	readiness := make([]any, len(m.Health.ReadinessKeys))
	for i, k := range m.Health.ReadinessKeys {
		readiness[i] = k
	}
	dataSourceID := ""
	if packTruthy(request.vals["dataSourceId"]) {
		dataSourceID = packString(request.vals["dataSourceId"])
	}
	requested, hasBindings := request.vals["sourceBindings"].(*jsObj)
	if m.Artifacts.GraphJin == nil {
		if dataSourceID != "" || hasBindings && len(requested.keys) > 0 {
			return nil, errors.New("this pack does not use a data source")
		}
		if prior != nil && prior.vals["source"] != nil {
			return nil, errors.New("removing GraphJin artifacts requires uninstall first")
		}
		return jsObjOf("bindings", &jsObj{vals: map[string]any{}}, "bindingHashes", &jsObj{vals: map[string]any{}}, "readiness", readiness), nil
	}
	if prior != nil {
		tables, _ := prior.vals["tables"].([]any)
		if err := verifyPackQueryTables(tables); err != nil {
			return nil, err
		}
	}
	var source *packSource
	if dataSourceID == "" && prior != nil && isJSObj(prior.vals["source"]) {
		var err error
		if source, err = a.resolveSelectedPackSource(ctx, orgID, plainJSON(prior.vals["source"]).(map[string]any)); err != nil {
			return nil, err
		}
	} else {
		if dataSourceID == "" && bundle.Upload != nil {
			return nil, errors.New("select an enabled organization dataSourceId before installing a custom pack")
		}
		query := `select id::text, graphql_url, auth_mode, is_default from data_source where org_id = $1 and enabled`
		args := []any{orgID}
		if dataSourceID != "" {
			query += ` and id::text = $2`
			args = append(args, dataSourceID)
		}
		rows, err := a.DB.Query(ctx, query+` order by is_default desc, created_at limit 2`, args...)
		if err != nil {
			return nil, err
		}
		type candidate struct {
			packSource
			isDefault bool
		}
		var available []candidate
		for rows.Next() {
			var c candidate
			var url, mode *string
			if err := rows.Scan(&c.ID, &url, &mode, &c.isDefault); err != nil {
				rows.Close()
				return nil, err
			}
			if url != nil {
				c.GraphqlURL = *url
			}
			if mode != nil {
				c.AuthMode = *mode
			}
			available = append(available, c)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		if dataSourceID == "" && len(available) > 1 && !available[0].isDefault {
			return nil, errors.New("Choose a default data connection before installing this pack")
		}
		if len(available) == 0 {
			return nil, errors.New("selected pack data source is unavailable in this organization")
		}
		source = &available[0].packSource
	}
	if slices.ContainsFunc(m.OAuth, func(c packs.OAuthConnection) bool { return c.Scope == "user" }) && source.AuthMode != "jwt" {
		return nil, errors.New("Personal connections require a GraphJin data connection with JWT authentication")
	}
	var references []packs.Artifact
	for _, art := range bundle.Artifacts {
		if art.Kind == packs.KindSource && strings.HasPrefix(art.TargetRef, m.Metadata.ID+".binding.") {
			references = append(references, art)
		}
	}
	isReference := func(key string) bool {
		return slices.ContainsFunc(references, func(r packs.Artifact) bool { return r.Key == key })
	}
	bindings := &jsObj{vals: map[string]any{}}
	if hasBindings {
		bindings = requested
	} else if prior != nil {
		old := jsObjOrEmpty(prior.vals["bindings"])
		for _, k := range old.ordered() {
			if isReference(k) {
				bindings.set(k, old.vals[k])
			}
		}
	}
	for _, k := range bindings.ordered() {
		if !isReference(k) {
			return nil, errors.New("unknown pack source binding")
		}
	}
	hashes := &jsObj{vals: map[string]any{}}
	if len(references) > 0 {
		target, err := a.resolvePackGraphjinTarget(ctx, orgID, source.GraphqlURL)
		if err != nil {
			return nil, err
		}
		var configured []any
		if target.Mode == "api" {
			live, err := target.Live(ctx)
			if err != nil {
				return nil, err
			}
			configured, _ = live["sources"].([]any)
		} else {
			raw, err := os.ReadFile(target.ConfigFile)
			if err != nil {
				return nil, err
			}
			parsed, err := packs.ParseYAML(raw)
			if err != nil {
				return nil, err
			}
			file, _ := parsed.(map[string]any)
			configured, _ = file["sources"].([]any)
		}
		data, messages, err := a.packGraphjinQuery(ctx, target.Endpoint, orgID, `query { gj_config(id: "current") { sources } }`, nil, 30*time.Second)
		if err != nil {
			return nil, err
		}
		root, _ := data.(map[string]any)
		gj, _ := root["gj_config"].(map[string]any)
		running, isList := gj["sources"].([]any)
		if len(messages) > 0 || !isList {
			return nil, errors.New("cannot verify selected GraphJin sources")
		}
		find := func(list []any, name any) map[string]any {
			for _, item := range list {
				if s, ok := item.(map[string]any); ok && strictEqual(s["name"], name) {
					return s
				}
			}
			return nil
		}
		var priorHashes *jsObj
		if prior != nil {
			priorHashes = jsObjOrEmpty(prior.vals["bindingHashes"])
		}
		for _, art := range references {
			name := bindings.vals[art.Key]
			current, live := find(configured, name), find(running, name)
			if !packTruthy(name) || current == nil || live == nil || current["kind"] != "database" || current["read_only"] != true || live["kind"] != "database" || live["read_only"] != true {
				return nil, fmt.Errorf("binding %s requires an existing read-only database source", art.Key)
			}
			for _, k := range []string{"name", "kind", "type", "host", "port", "dbname"} {
				if configText(current[k]) != configText(live[k]) {
					return nil, fmt.Errorf("binding %s differs from the selected live source", art.Key)
				}
			}
			var engines []string
			for _, d := range m.Compatibility.Databases {
				engines = append(engines, d.Engine)
			}
			if t, _ := current["type"].(string); len(engines) > 0 && !slices.Contains(engines, t) {
				return nil, fmt.Errorf("binding %s database engine is incompatible", art.Key)
			}
			hash := graphjinConfigPatchHash(jsValueOf(current))
			hashes.set(art.Key, hash)
			if old, _ := priorHashes.lookup(art.Key); !hasBindings && packTruthy(old) && old != hash {
				return nil, fmt.Errorf("binding %s changed; explicitly reconfigure it", art.Key)
			}
		}
	}
	return jsObjOf("source", sourceObj(source), "bindings", bindings, "bindingHashes", hashes, "readiness", readiness), nil
}

func (o *jsObj) lookup(k string) (any, bool) {
	if o == nil {
		return nil, false
	}
	v, ok := o.vals[k]
	return v, ok
}

var nativeTargetQuery = map[packs.ArtifactKind]string{
	packs.KindMetric:   `select 1 from metric where org_id = $1 and role = $2 and slug = $3 limit 1`,
	packs.KindWorkflow: `select 1 from workflow_definition where org_id = $1 and owner_user_id = '' and name = $2 limit 1`,
	packs.KindWatcher:  `select 1 from watcher where org_id = $1 and name = $2 limit 1`,
	packs.KindPolicy:   `select 1 from action_policy where org_id = $1 and name = $2 limit 1`,
	packs.KindAction:   `select 1 from pack_action_definition where org_id = $1 and kind = $2 limit 1`,
}

// assertNativeTargetsAvailable is assertNativeTargetsAvailable() of
// apps/worker/src/packs/native-targets.ts.
func (a *API) assertNativeTargetsAvailable(ctx context.Context, orgID string, bundle *packs.Bundle, plan *packs.Plan) error {
	creates := map[string]bool{}
	for _, e := range plan.Entries {
		if e.Action == "create" {
			creates[string(e.Kind)+":"+e.Key] = true
		}
	}
	for _, art := range bundle.Artifacts {
		value, isObject := art.Content.(map[string]any)
		query, native := nativeTargetQuery[art.Kind]
		if !creates[string(art.Kind)+":"+art.Key] || !isObject || !native {
			continue
		}
		args := []any{orgID}
		switch art.Kind {
		case packs.KindMetric:
			args = append(args, packString(value["role"]), art.TargetRef)
		case packs.KindAction:
			args = append(args, packString(value["kind"]))
		default:
			args = append(args, packString(value["name"]))
		}
		var one int
		err := a.DB.QueryRow(ctx, query, args...).Scan(&one)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		return fmt.Errorf("%s target %s already exists and is not owned by pack %s", art.Kind, art.TargetRef, bundle.Manifest.Metadata.ID)
	}
	return nil
}

// packReviewHash is PackService.reviewHash(): an HMAC that install checks
// against the same review.
func (a *API) packReviewHash(orgID string, bundle *packs.Bundle, plan *packs.Plan, operation string, actorUserID any, request, inputs *jsObj, secretValues map[string]string, runtime *jsObj) (string, error) {
	reviewed := map[string]any{}
	for k, v := range secretValues {
		if !slices.ContainsFunc(bundle.Manifest.Secrets, func(s packs.Secret) bool { return s.Key == k && s.Purpose == "pack_oauth_token" }) {
			reviewed[k] = v
		}
	}
	contentHash := bundle.BundleHash
	if bundle.Upload != nil {
		contentHash = bundle.Upload.ContentHash
	}
	refs := request.vals["secretRefs"]
	if refs == nil {
		refs = &jsObj{vals: map[string]any{}}
	}
	text, err := packs.CanonicalHash(map[string]any{
		"orgId": orgID, "actor": actorUserID, "operation": operation, "plan": plainJSON(jsValueOf(plan)),
		"contentHash": contentHash, "inputs": plainJSON(inputs), "secrets": reviewed, "runtime": plainJSON(runtime),
		"secretRefs": plainJSON(refs),
	})
	if err != nil {
		return "", err
	}
	secret, err := config.SigningSecret(a.SecretsDir, "graphjin:"+orgID)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, []byte(base64.StdEncoding.EncodeToString(secret)))
	mac.Write([]byte("pack-review/v1:" + text))
	return hex.EncodeToString(mac.Sum(nil)), nil
}

type packInstallRow struct {
	Version, Status, Source string
	Config                  *jsObj
}

func (a *API) latestPackInstall(ctx context.Context, orgID, packID string) (*packInstallRow, error) {
	var row packInstallRow
	var raw []byte
	err := a.DB.QueryRow(ctx, `select version, status, source, config::text from pack_install where org_id = $1 and pack_id = $2 order by created_at desc limit 1`,
		orgID, packID).Scan(&row.Version, &row.Status, &row.Source, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	parsed, err := jsParse(raw)
	if err != nil {
		return nil, err
	}
	row.Config = jsObjOrEmpty(parsed)
	return &row, nil
}

// installedPackBundle is PackService.installedBundle(): an uploaded pack must
// still hold the approved contents.
func installedPackBundle(orgID, packID string, row *packInstallRow) (*packs.Bundle, error) {
	version := ""
	if row.Source == "uploaded" {
		version = row.Version
	}
	bundle, err := packBundle(orgID, packID, version)
	if err != nil {
		return nil, err
	}
	if row.Source == "uploaded" {
		pinned := jsObjOrEmpty(row.Config.vals["_bundle"])
		if bundle.Upload == nil || !strictEqual(bundle.Upload.ContentHash, pinned.vals["contentHash"]) {
			return nil, errors.New("installed pack contents do not match the approved version")
		}
	}
	return bundle, nil
}

// packReview is PackService.review().
func (a *API) packReview(ctx context.Context, orgID, packID string, actorUserID any, request *jsObj, operation string) (*jsObj, error) {
	existing, err := a.latestPackInstall(ctx, orgID, packID)
	if err != nil {
		return nil, err
	}
	if operation != "install" && (existing == nil || existing.Status != "installed") {
		return nil, errors.New("pack must be installed before reviewing this operation")
	}
	version := ""
	if packTruthy(request.vals["version"]) {
		version = packString(request.vals["version"])
	}
	if operation == "configure" && version != "" && version != existing.Version {
		return nil, errors.New("configure cannot change the installed pack version")
	}
	var bundle *packs.Bundle
	if operation == "configure" {
		bundle, err = installedPackBundle(orgID, packID, existing)
	} else {
		bundle, err = packBundle(orgID, packID, version)
	}
	if err != nil {
		return nil, err
	}
	supplied := &jsObj{vals: map[string]any{}}
	var prior *jsObj
	if existing != nil {
		for _, k := range existing.Config.ordered() {
			if bundle.Manifest.InputDeclared(k) {
				supplied.set(k, existing.Config.vals[k])
			}
		}
		if p, ok := existing.Config.vals["_runtime"].(*jsObj); ok {
			prior = p
		}
	}
	given := jsObjOrEmpty(request.vals["inputs"])
	for _, k := range given.ordered() {
		supplied.set(k, given.vals[k])
	}
	inputs, err := resolvePackInputs(bundle, supplied)
	if err != nil {
		return nil, err
	}
	secretValues, err := a.resolvePackSecrets(bundle, request)
	if err != nil {
		return nil, err
	}
	runtime, err := a.packRuntime(ctx, orgID, bundle, request, prior)
	if err != nil {
		return nil, err
	}
	if packID != "magento" {
		bindings := map[string]string{}
		for k, v := range runtime.vals["bindings"].(*jsObj).vals {
			bindings[k], _ = v.(string)
		}
		bound, err := packQueryTables(bundle, bindings)
		if err != nil {
			return nil, err
		}
		var prior *jsObj
		if existing != nil {
			prior, _ = existing.Config.vals["_runtime"].(*jsObj)
		}
		runtime.set("tables", mergePackTables(prior, bound))
		plainInputs, _ := plainJSON(inputs).(map[string]any)
		if err := declarativePackChecks(bundle, plainInputs, secretValues, bindings); err != nil {
			return nil, err
		}
		if selection, ok := runtime.vals["source"].(*jsObj); ok && bundle.Manifest.Artifacts.GraphJin != nil && len(secretValues) > 0 {
			if source, err := a.resolveSelectedPackSource(ctx, orgID, plainJSON(selection).(map[string]any)); err == nil {
				if target, err := a.resolvePackGraphjinTarget(ctx, orgID, source.GraphqlURL); err == nil &&
					target.Mode == "api" && target.KeystoreConfigured != nil && !*target.KeystoreConfigured {
					return nil, errors.New(graphjinNoKeystoreMessage)
				}
			}
		}
	}
	plan, err := a.packPlan(ctx, orgID, bundle)
	if err != nil {
		return nil, err
	}
	if err := a.assertNativeTargetsAvailable(ctx, orgID, bundle, plan); err != nil {
		return nil, err
	}
	inspection, err := inspectPack(orgID, packID, bundle.Manifest.Metadata.Version)
	if err != nil {
		return nil, err
	}
	hash, err := a.packReviewHash(orgID, bundle, plan, operation, actorUserID, request, inputs, secretValues, runtime)
	if err != nil {
		return nil, err
	}
	out := jsObjOrEmpty(jsValueOf(inspection))
	keys := make([]any, 0, len(secretValues))
	for _, s := range bundle.Manifest.Secrets {
		if _, ok := secretValues[s.Key]; ok {
			keys = append(keys, s.Key)
		}
	}
	for _, kv := range [][2]any{{"operation", operation}, {"inputs", inputs}, {"runtime", runtime}, {"plan", jsValueOf(plan)}, {"secrets", keys}, {"reviewHash", hash}} {
		out.set(kv[0].(string), kv[1])
	}
	return out, nil
}

func (a *API) servePackReview(w http.ResponseWriter, r *http.Request, act actor, packID string, body *jsObj) {
	operation := "install"
	if v, ok := body.vals["operation"]; ok && v != nil {
		operation, _ = v.(string)
		if operation != "install" && operation != "configure" && operation != "upgrade" {
			sendPackFailure(w, http.StatusBadRequest, errors.New("invalid pack review operation"), "review")
			return
		}
	}
	out, err := a.packReview(context.WithoutCancel(r.Context()), act.OrgID, packID, nullable(act.UserID), body, operation)
	if err != nil {
		sendPackFailure(w, http.StatusBadRequest, err, "review")
		return
	}
	sendJSON(w, http.StatusOK, out)
}
