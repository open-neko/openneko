package packs

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

type Metadata struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Version   string `json:"version"`
	Publisher string `json:"publisher"`
	Category  string `json:"category"`
}

type Management struct {
	Label string `json:"label"`
	Path  string `json:"path"`
}

type Compatibility struct {
	OpenNeko string `json:"openneko"`
	GraphJin *struct {
		Analytics string `json:"analytics"`
		Operator  string `json:"operator"`
	} `json:"graphjin,omitempty"`
	Applications []struct {
		ID       string   `json:"id"`
		Editions []string `json:"editions"`
		Versions string   `json:"versions"`
	} `json:"applications"`
	Databases []struct {
		Engine   string `json:"engine"`
		Versions string `json:"versions"`
	} `json:"databases"`
}

type Input struct {
	Key         string  `json:"key"`
	Type        string  `json:"type"`
	Required    *bool   `json:"required,omitempty"`
	Default     any     `json:"default"`
	Discover    *bool   `json:"discover,omitempty"`
	Values      *[]any  `json:"values,omitempty"`
	Description *string `json:"description,omitempty"`
}

type Secret struct {
	Key      string `json:"key"`
	Purpose  string `json:"purpose"`
	Required bool   `json:"required"`
}

type OAuthConnection struct {
	Key           string `json:"key"`
	ProviderLabel string `json:"providerLabel"`
	Scope         string `json:"scope"`
	Experience    *struct {
		Description       string  `json:"description"`
		SetupInstructions *string `json:"setupInstructions,omitempty"`
		HelpURL           *string `json:"helpUrl,omitempty"`
	} `json:"experience,omitempty"`
	AuthorizationURL    string             `json:"authorizationUrl"`
	TokenURL            string             `json:"tokenUrl"`
	UserInfoURL         string             `json:"userInfoUrl"`
	ClientIDInput       string             `json:"clientIdInput"`
	ClientSecret        string             `json:"clientSecret"`
	AccessToken         string             `json:"accessToken"`
	RefreshToken        string             `json:"refreshToken"`
	Scopes              []string           `json:"scopes"`
	AuthorizationParams *map[string]string `json:"authorizationParams,omitempty"`
	AccountIDField      string             `json:"accountIdField"`
	AccountLabelField   string             `json:"accountLabelField"`
}

type GraphJinArtifacts struct {
	Sources       string   `json:"sources"`
	Relationships string   `json:"relationships"`
	Specs         []string `json:"specs"`
	SavedQueries  *string  `json:"savedQueries,omitempty"`
}

type Artifacts struct {
	GraphJin  *GraphJinArtifacts `json:"graphjin,omitempty"`
	Metrics   *string            `json:"metrics,omitempty"`
	Workflows *string            `json:"workflows,omitempty"`
	Watchers  *string            `json:"watchers,omitempty"`
	Actions   *string            `json:"actions,omitempty"`
	Policies  *string            `json:"policies,omitempty"`
	Skills    []string           `json:"skills"`
}

type Health struct {
	RequiredPreflight []string            `json:"requiredPreflight"`
	Readiness         map[string][]string `json:"readiness"`
	PostInstall       []string            `json:"postInstall"`
	PostWriteCanary   []string            `json:"postWriteCanary"`
}

// Manifest is the parsed pack.yaml with the schema defaults applied.
type Manifest struct {
	APIVersion    string            `json:"apiVersion"`
	Kind          string            `json:"kind"`
	Metadata      Metadata          `json:"metadata"`
	Management    *Management       `json:"management,omitempty"`
	Compatibility Compatibility     `json:"compatibility"`
	Inputs        []Input           `json:"inputs"`
	Secrets       []Secret          `json:"secrets"`
	OAuth         []OAuthConnection `json:"oauth"`
	Channels      *[]string         `json:"channels,omitempty"`
	Permissions   struct {
		Network []string `json:"network"`
	} `json:"permissions"`
	Artifacts Artifacts `json:"artifacts"`
	Health    Health    `json:"health"`

	normalized map[string]any
}

// Normalized is the manifest as JSON values, the input of the manifest hash.
func (m *Manifest) Normalized() map[string]any { return m.normalized }

func (m *Manifest) InputDeclared(key string) bool {
	for _, in := range m.Inputs {
		if in.Key == key {
			return true
		}
	}
	return false
}

var (
	slug         = matches(PackIDPattern, "must be a lowercase slug")
	semver       = matches(versionPattern, "must be a semantic version")
	relativePath = func(v any, path string) error {
		if err := str(1)(v, path); err != nil {
			return err
		}
		s := v.(string)
		if strings.Contains(s, `\`) {
			return issue(path, "must use forward slashes")
		}
		if strings.HasPrefix(s, "/") || containsPart(s, "..") {
			return issue(path, "must stay within the pack root")
		}
		return nil
	}
	urlString = func(v any, path string) error {
		s, ok := v.(string)
		if !ok {
			return issue(path, "expected string")
		}
		u, err := url.Parse(s)
		if err != nil || u.Scheme == "" || (u.Host == "" && u.Opaque == "") {
			return issue(path, "invalid URL")
		}
		return nil
	}
	inputKey = matches(keyPattern, "invalid key")

	manifestRule = strictObj(
		req("apiVersion", literal("openneko.app/v1")),
		req("kind", literal("SolutionPack")),
		req("metadata", strictObj(req("id", slug), req("name", str(1)), req("version", semver), req("publisher", slug), req("category", slug))),
		opt("management", strictObj(req("label", str(1)), req("path", func(v any, path string) error {
			s, ok := v.(string)
			if !ok || !appPathPattern.MatchString(s) || strings.HasPrefix(s, "//") {
				return issue(path, "management path must be a local application path")
			}
			return nil
		}))),
		req("compatibility", strictObj(
			req("openneko", str(1)),
			opt("graphjin", strictObj(req("analytics", str(1)), req("operator", str(1)))),
			req("applications", array(strictObj(req("id", slug), req("editions", array(slug, 1)), req("versions", str(1))), 0)),
			req("databases", array(strictObj(req("engine", enum("mariadb", "mysql", "postgres")), req("versions", str(1))), 0)),
		)),
		req("inputs", array(func(v any, path string) error {
			if err := strictObj(
				req("key", inputKey),
				req("type", enum("string", "url", "integer", "enum", "timezone", "boolean")),
				opt("required", boolean),
				opt("default", scalar),
				opt("discover", boolean),
				opt("values", array(scalar, 0)),
				opt("description", str(0)),
			)(v, path); err != nil {
				return err
			}
			m := v.(map[string]any)
			values, has := m["values"].([]any)
			if m["type"] == "enum" && len(values) == 0 {
				return issue(join(path, "values"), "enum inputs require values")
			}
			if m["type"] != "enum" && has {
				return issue(join(path, "values"), "values are valid only for enum inputs")
			}
			return nil
		}, 0)),
		req("secrets", array(strictObj(
			req("key", inputKey),
			req("purpose", enum("graphjin_source", "graphjin_api_auth", "pack_runtime", "pack_oauth_client", "pack_oauth_token")),
			opt("required", boolean),
		), 0)),
		opt("oauth", array(strictObj(
			req("key", slug),
			req("providerLabel", str(1)),
			opt("scope", enum("deployment", "user")),
			opt("experience", strictObj(
				req("description", strMax(1, 500)),
				opt("setupInstructions", strMax(1, 4000)),
				opt("helpUrl", func(v any, path string) error {
					if err := urlString(v, path); err != nil {
						return err
					}
					if u, _ := url.Parse(v.(string)); u.Scheme != "https" {
						return issue(path, "help URL must use HTTPS")
					}
					return nil
				}),
			)),
			req("authorizationUrl", urlString),
			req("tokenUrl", urlString),
			req("userInfoUrl", urlString),
			req("clientIdInput", str(1)),
			req("clientSecret", str(1)),
			req("accessToken", str(1)),
			req("refreshToken", str(1)),
			req("scopes", array(str(1), 1)),
			opt("authorizationParams", record(str(0), str(0))),
			opt("accountIdField", str(1)),
			opt("accountLabelField", str(1)),
		), 0)),
		opt("channels", func(v any, path string) error {
			if err := array(enum("slack", "telegram", "hud"), 0)(v, path); err != nil {
				return err
			}
			seen := map[any]bool{}
			for _, c := range v.([]any) {
				if seen[c] {
					return issue(path, "duplicate channel")
				}
				seen[c] = true
			}
			return nil
		}),
		opt("permissions", strictObj(opt("network", array(matches(hostPattern, "invalid host"), 0)))),
		req("artifacts", strictObj(
			opt("graphjin", strictObj(req("sources", relativePath), req("relationships", relativePath), req("specs", array(relativePath, 0)), opt("savedQueries", relativePath))),
			opt("metrics", relativePath),
			opt("workflows", relativePath),
			opt("watchers", relativePath),
			opt("actions", relativePath),
			opt("policies", relativePath),
			req("skills", array(relativePath, 0)),
		)),
		req("health", strictObj(
			req("requiredPreflight", array(slug, 0)),
			req("readiness", record(slug, array(slug, 0))),
			req("postInstall", array(slug, 0)),
			req("postWriteCanary", array(slug, 0)),
		)),
	)
)

func containsPart(path, part string) bool {
	for _, p := range strings.Split(path, "/") {
		if p == part {
			return true
		}
	}
	return false
}

// ParseManifest is parseManifest(): the schema check, the cross checks and
// the defaults.
func ParseManifest(input any) (*Manifest, error) {
	if err := manifestRule(input, ""); err != nil {
		return nil, fmt.Errorf("invalid pack manifest: %w", err)
	}
	normalized := cloneJSON(input).(map[string]any)
	for _, s := range normalized["secrets"].([]any) {
		secret := s.(map[string]any)
		if _, ok := secret["required"]; !ok {
			secret["required"] = true
		}
	}
	if _, ok := normalized["oauth"]; !ok {
		normalized["oauth"] = []any{}
	}
	for _, c := range normalized["oauth"].([]any) {
		conn := c.(map[string]any)
		for key, def := range map[string]string{"scope": "deployment", "accountIdField": "sub", "accountLabelField": "email"} {
			if _, ok := conn[key]; !ok {
				conn[key] = def
			}
		}
	}
	perms, ok := normalized["permissions"].(map[string]any)
	if !ok {
		perms = map[string]any{}
		normalized["permissions"] = perms
	}
	if _, ok := perms["network"]; !ok {
		perms["network"] = []any{}
	}
	raw, err := json.Marshal(normalized)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("invalid pack manifest: %w", err)
	}
	m.normalized = normalized
	if err := m.crossCheck(); err != nil {
		return nil, fmt.Errorf("invalid pack manifest: %w", err)
	}
	return &m, nil
}

func (m *Manifest) crossCheck() error {
	var problems []string
	if m.Artifacts.GraphJin != nil && m.Compatibility.GraphJin == nil {
		problems = append(problems, "compatibility.graphjin: GraphJin artifacts require GraphJin compatibility")
	}
	inputs, secrets := map[string]bool{}, map[string]bool{}
	for i, in := range m.Inputs {
		if inputs[in.Key] {
			problems = append(problems, fmt.Sprintf("inputs[%d].key: duplicate inputs key", i))
		}
		inputs[in.Key] = true
	}
	for i, s := range m.Secrets {
		if secrets[s.Key] {
			problems = append(problems, fmt.Sprintf("secrets[%d].key: duplicate secrets key", i))
		}
		secrets[s.Key] = true
	}
	hosts := map[string]bool{}
	for _, h := range m.Permissions.Network {
		hosts[strings.ToLower(h)] = true
	}
	connections := map[string]bool{}
	for i, c := range m.OAuth {
		at := fmt.Sprintf("oauth[%d]", i)
		if connections[c.Key] {
			problems = append(problems, at+".key: duplicate OAuth connection key")
		}
		connections[c.Key] = true
		if !inputs[c.ClientIDInput] {
			problems = append(problems, at+".clientIdInput: OAuth clientIdInput must reference a declared input")
		}
		for name, ref := range map[string]string{"clientSecret": c.ClientSecret, "accessToken": c.AccessToken, "refreshToken": c.RefreshToken} {
			if !secrets[ref] {
				problems = append(problems, fmt.Sprintf("%s.%s: OAuth %s must reference a declared secret", at, name, name))
			}
		}
		for name, raw := range map[string]string{"authorizationUrl": c.AuthorizationURL, "tokenUrl": c.TokenURL, "userInfoUrl": c.UserInfoURL} {
			u, _ := url.Parse(raw)
			if u.Scheme != "https" {
				problems = append(problems, fmt.Sprintf("%s.%s: OAuth endpoints must use HTTPS", at, name))
			}
			if !hosts[strings.ToLower(u.Hostname())] {
				problems = append(problems, fmt.Sprintf("%s.%s: OAuth endpoint host must be declared in permissions.network", at, name))
			}
		}
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

func cloneJSON(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			out[k] = cloneJSON(x)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = cloneJSON(x)
		}
		return out
	}
	return v
}
