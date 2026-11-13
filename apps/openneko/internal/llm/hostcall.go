package llm

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/open-neko/openneko/apps/ax-harness/embedding"
	"github.com/open-neko/openneko/apps/ax-harness/inference"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

type Price struct {
	InputUSDPer1M  float64 `json:"inputUsdPer1M"`
	OutputUSDPer1M float64 `json:"outputUsdPer1M"`
}

// RoutedModel is one side of a task route. Ref is "default" for the org's
// primary provider, else the llm_model id.
type RoutedModel struct {
	Ref      string
	Name     string
	Settings Settings
	Price    *Price
}

type TaskRoute struct {
	Task     string
	Primary  RoutedModel
	Fallback *RoutedModel
}

type ModelRecord struct {
	ID, Name, Provider, Model string
	Config                    map[string]any
	Sealed                    map[string]any
	Enabled                   bool
	Price                     *Price
}

const recordColumns = "id::text, name, provider, model, config, secrets, enabled, input_usd_per_1m::text, output_usd_per_1m::text"

func scanRecord(row pgx.CollectableRow) (ModelRecord, error) {
	var (
		m        ModelRecord
		cfg, sec []byte
		in, out  *string
	)
	if err := row.Scan(&m.ID, &m.Name, &m.Provider, &m.Model, &cfg, &sec, &m.Enabled, &in, &out); err != nil {
		return m, err
	}
	_ = json.Unmarshal(cfg, &m.Config)
	_ = json.Unmarshal(sec, &m.Sealed)
	if m.Config == nil {
		m.Config = map[string]any{}
	}
	if in != nil && out != nil {
		i, _ := strconv.ParseFloat(*in, 64)
		o, _ := strconv.ParseFloat(*out, 64)
		m.Price = &Price{i, o}
	}
	return m, nil
}

// TaskRouteRecords is taskRouteRecords() in packages/llm/src/model-routes.ts:
// the task's own route wins, then "default". A disabled or missing model
// counts as unset.
func TaskRouteRecords(ctx context.Context, db DB, orgID, task string) (string, *ModelRecord, *ModelRecord, error) {
	rows, err := db.Query(ctx, `select task, model_id::text, fallback_model_id::text from llm_task_route
	  where org_id = $1 and task in ($2, 'default')`, orgID, task)
	if err != nil {
		return "", nil, nil, err
	}
	type route struct {
		task            string
		model, fallback *string
	}
	var own, def *route
	var r route
	if _, err := pgx.ForEachRow(rows, []any{&r.task, &r.model, &r.fallback}, func() error {
		copied := r
		if copied.task == task {
			own = &copied
		} else if copied.task == "default" {
			def = &copied
		}
		return nil
	}); err != nil {
		return "", nil, nil, err
	}
	chosen := own
	if chosen == nil {
		chosen = def
	}
	if chosen == nil {
		return "default", nil, nil, nil
	}
	var ids []string
	for _, id := range []*string{chosen.model, chosen.fallback} {
		if id != nil && *id != "" {
			ids = append(ids, *id)
		}
	}
	byID := map[string]*ModelRecord{}
	if len(ids) > 0 {
		rows, err := db.Query(ctx, `select `+recordColumns+` from llm_model where org_id = $1 and id = any($2::uuid[]) and enabled`, orgID, ids)
		if err != nil {
			return "", nil, nil, err
		}
		records, err := pgx.CollectRows(rows, scanRecord)
		if err != nil {
			return "", nil, nil, err
		}
		for i := range records {
			byID[records[i].ID] = &records[i]
		}
	}
	pick := func(id *string) *ModelRecord {
		if id == nil {
			return nil
		}
		return byID[*id]
	}
	primary, fallback := pick(chosen.model), pick(chosen.fallback)
	if fallback != nil && primary != nil && fallback.ID == primary.ID {
		fallback = nil
	}
	return chosen.task, primary, fallback, nil
}

func routed(secretsDir string, m *ModelRecord) (*RoutedModel, error) {
	if m == nil || !m.Enabled || !IsPrimaryProvider(m.Provider) {
		return nil, nil
	}
	secrets, err := DecryptSecrets(secretsDir, m.Sealed)
	if err != nil {
		return nil, err
	}
	name := m.Name
	s := Settings{Source: "org", Provider: m.Provider, Model: m.Model, Enabled: true, Label: &name, Config: m.Config, Secrets: secrets}
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return &RoutedModel{Ref: m.ID, Name: m.Name, Settings: s, Price: m.Price}, nil
}

// ResolveTaskRoute is resolveTaskRoute(): an unset model means the org's
// primary provider; a fallback that fails its checks is left out.
func ResolveTaskRoute(ctx context.Context, db DB, secretsDir, orgID, task string) (TaskRoute, error) {
	routeTask, primaryRec, fallbackRec, err := TaskRouteRecords(ctx, db, orgID, task)
	if err != nil {
		return TaskRoute{}, err
	}
	out := TaskRoute{Task: routeTask}
	primary, err := routed(secretsDir, primaryRec)
	if err != nil {
		return TaskRoute{}, err
	}
	if primary == nil {
		s, err := PrimarySettings(ctx, db, secretsDir, orgID)
		if err != nil {
			return TaskRoute{}, err
		}
		primary = &RoutedModel{Ref: "default", Name: "default", Settings: s}
	}
	out.Primary = *primary
	if fallback, err := routed(secretsDir, fallbackRec); err != nil {
		log.Printf("[models] fallback for %s skipped: %v", task, err)
	} else {
		out.Fallback = fallback
	}
	return out, nil
}

var axProfiles = map[string]string{
	"mistral":     "mistral",
	"groq":        "groq",
	"cohere":      "cohere",
	"together":    "together",
	"deepseek":    "deepseek",
	"huggingface": "huggingface-router",
	"openrouter":  "openrouter",
	"reka":        "reka",
	"x-grok":      "grok",
	"vertex":      "vertex-ai",
}

const AxAzureAPIVersion = "2024-10-21"

// AxRoute is resolveAxModelRoute() in packages/llm/src/provider-runtime.ts.
func AxRoute(s Settings) (inference.Route, error) {
	rt, err := ResolveRuntime(s.Provider, s.Model, s.Config)
	if err != nil {
		return inference.Route{}, err
	}
	switch s.Provider {
	case "openai", "anthropic", "google-gemini":
		return inference.Route{Provider: s.Provider, Model: rt.Model}, nil
	case "azure-openai":
		resource, err := requiredConfig(s.Config, "resourceName", "Azure resource name")
		if err != nil {
			return inference.Route{}, err
		}
		return inference.Route{Provider: "azure-openai", Model: rt.Model, Options: map[string]string{
			"resource_name": resource, "deployment_name": rt.Model, "api_version": AxAzureAPIVersion,
		}}, nil
	}
	provider := axProfiles[s.Provider]
	if provider == "" {
		provider = inference.OpenAICompatible
	}
	return inference.Route{Provider: provider, Model: rt.Model, URL: rt.BaseURL}, nil
}

type TokenFunc func(ctx context.Context) (string, error)

var (
	adcMu     sync.Mutex
	adcSource oauth2.TokenSource
)

// GoogleToken is getGoogleToken(): an access token from Application Default
// Credentials.
func GoogleToken(ctx context.Context) (string, error) {
	adcMu.Lock()
	source := adcSource
	adcMu.Unlock()
	if source == nil {
		creds, err := google.FindDefaultCredentials(ctx, "https://www.googleapis.com/auth/cloud-platform")
		if err != nil {
			return "", err
		}
		adcMu.Lock()
		adcSource, source = creds.TokenSource, creds.TokenSource
		adcMu.Unlock()
	}
	token, err := source.Token()
	if err != nil {
		return "", err
	}
	if token.AccessToken == "" {
		return "", errors.New("Failed to obtain Google access token. Use gcloud ADC or GOOGLE_APPLICATION_CREDENTIALS.")
	}
	return token.AccessToken, nil
}

// GatewayKey is gatewayKey() in packages/llm/src/inference.ts.
func GatewayKey(ctx context.Context, s Settings, token TokenFunc) (string, error) {
	if s.Provider == "vertex" {
		return token(ctx)
	}
	if key := s.Secrets["apiKey"]; key != "" {
		return key, nil
	}
	return "none", nil
}

const FallbackKeyEnv = "FALLBACK_MODEL_API_KEY"

func recordKey(ctx context.Context, provider, model string, config map[string]any, secrets map[string]string, token TokenFunc) (string, string, error) {
	if !IsPrimaryProvider(provider) {
		return "", "", fmt.Errorf("Unsupported primary provider: %s", provider)
	}
	rt, err := ResolveRuntime(provider, model, config)
	if err != nil {
		return "", "", err
	}
	switch rt.CredentialSource {
	case "google-adc":
		key, err := token(ctx)
		return key, rt.KeyEnv, err
	case "stored-api-key":
		return secrets["apiKey"], rt.KeyEnv, nil
	}
	return "", rt.KeyEnv, nil
}

// AgentModelKeys is loadAgentModelKeys() in packages/llm/src/host-provision.ts:
// the keys one agent run hands to serve, by variable name.
func AgentModelKeys(ctx context.Context, db DB, secretsDir, orgID string, token TokenFunc) (map[string]string, error) {
	_, primaryRec, fallbackRec, routeErr := TaskRouteRecords(ctx, db, orgID, "turn")
	keys := map[string]string{}
	var (
		provider, model string
		config          map[string]any
		secrets         map[string]string
		err             error
	)
	if routeErr == nil && primaryRec != nil {
		if secrets, err = DecryptSecrets(secretsDir, primaryRec.Sealed); err != nil {
			return nil, err
		}
		provider, model, config = primaryRec.Provider, primaryRec.Model, primaryRec.Config
	} else {
		row, err := loadStored(ctx, db, secretsDir, orgID, "primary")
		if err != nil || row == nil || !row.Enabled {
			return keys, nil
		}
		provider, config, secrets = row.Provider, row.Config, row.Secrets
		if row.Model != nil {
			model = *row.Model
		}
	}
	key, keyEnv, err := recordKey(ctx, provider, model, config, secrets, token)
	if err != nil {
		return nil, err
	}
	if routeErr == nil && fallbackRec != nil {
		if fbSecrets, err := DecryptSecrets(secretsDir, fallbackRec.Sealed); err == nil {
			if fbKey, _, err := recordKey(ctx, fallbackRec.Provider, fallbackRec.Model, fallbackRec.Config, fbSecrets, token); err == nil && fbKey != "" {
				keys[FallbackKeyEnv] = fbKey
			}
		}
	}
	if key == "" {
		return keys, nil
	}
	keys["MODEL_API_KEY"] = key
	if env := os.Getenv("OPENNEKO_AGENT_MODEL_KEY_ENV"); env != "" {
		keyEnv = env
	}
	if keyEnv != "" {
		keys[keyEnv] = key
	}
	return keys, nil
}

// Gateway runs one host model call.
type Gateway func(ctx context.Context, request inference.Request, key, fallbackKey string) (inference.Result, error)

// Embedder runs one embedding call.
type Embedder func(ctx context.Context, request embedding.Request, key string) (embedding.Result, error)

type frame struct {
	Type        string `json:"type"`
	Generate    any    `json:"generate,omitempty"`
	Embed       any    `json:"embed,omitempty"`
	ModelKey    string `json:"model_key,omitempty"`
	FallbackKey string `json:"fallback_key,omitempty"`
}

func serveCall(ctx context.Context, socket string, start frame, label string, out any) error {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", socket)
	if err != nil {
		return fmt.Errorf("openneko serve unavailable: %w", err)
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	line, err := json.Marshal(start)
	if err != nil {
		return err
	}
	if _, err := conn.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("openneko serve unavailable: %w", err)
	}
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 64<<10), 64<<20)
	got := false
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) == "" {
			continue
		}
		var f struct {
			Type   string          `json:"type"`
			Result json.RawMessage `json:"result"`
			Code   *int            `json:"code"`
			Error  string          `json:"error"`
		}
		if json.Unmarshal(scanner.Bytes(), &f) != nil {
			return errors.New("openneko serve wrote a line that is not JSON")
		}
		switch f.Type {
		case "result":
			if len(f.Result) > 0 && string(f.Result) != "null" {
				if err := json.Unmarshal(f.Result, out); err != nil {
					return err
				}
				got = true
			}
		case "exit":
			if f.Code != nil && *f.Code != 0 {
				if f.Error != "" {
					return errors.New(f.Error)
				}
				return fmt.Errorf("%s call failed (code %d)", label, *f.Code)
			}
			if !got {
				return fmt.Errorf("openneko serve returned no %s result", label)
			}
			return nil
		}
	}
	if ctx.Err() != nil {
		return fmt.Errorf("%s call timed out", label)
	}
	return fmt.Errorf("openneko serve returned no %s result", label)
}

// ServeGateway sends a generate frame to serve's socket. With no socket the
// call runs in this process.
func ServeGateway(socket string) Gateway {
	if socket == "" {
		return inference.Generate
	}
	return func(ctx context.Context, request inference.Request, key, fallbackKey string) (inference.Result, error) {
		var out inference.Result
		err := serveCall(ctx, socket, frame{Type: "generate", Generate: request, ModelKey: key, FallbackKey: fallbackKey}, "model", &out)
		return out, err
	}
}

func ServeEmbedder(socket string) Embedder {
	if socket == "" {
		return embedding.Embed
	}
	return func(ctx context.Context, request embedding.Request, key string) (embedding.Result, error) {
		var out embedding.Result
		err := serveCall(ctx, socket, frame{Type: "embed", Embed: request, ModelKey: key}, "embedding", &out)
		return out, err
	}
}

// Program is a hostProgram(): a static Ax signature and its instruction.
type Program struct {
	Signature   string
	Instruction string
}

// Usage is the AgentTokenUsage of one host call, priced.
type Usage struct {
	Coverage                                    string
	Input, Output, Total, CacheRead, CacheWrite int64
	Calls                                       int64
	CostStatus                                  string
	CostUSD                                     *float64
	CostSource, PricingVersion                  string
}

type Result struct {
	Outputs  map[string]any
	Usage    Usage
	Provider string
	Model    string
	Fallback bool
}

// HostCalls holds what a host model call needs. Spend records the ledger
// row; a nil Spend records nothing.
type HostCalls struct {
	DB         DB
	SecretsDir string
	Gateway    Gateway
	Token      TokenFunc
	Spend      func(ctx context.Context, orgID, task string, served RoutedModel, usage Usage)
	Now        func() time.Time
}

const callTimeout = 10 * time.Minute

// Generate is generate() in packages/llm/src/inference.ts: the task's route
// and fallback, then one spend ledger row.
func (h HostCalls) Generate(ctx context.Context, orgID, task string, program Program, values map[string]any) (Result, error) {
	route, err := ResolveTaskRoute(ctx, h.DB, h.SecretsDir, orgID, task)
	if err != nil {
		return Result{}, err
	}
	return h.OnRoute(ctx, orgID, task, route, program, values)
}

// OnRoute calls the route. An empty task records no spend, as the provider
// test does.
func (h HostCalls) OnRoute(ctx context.Context, orgID, task string, route TaskRoute, program Program, values map[string]any) (Result, error) {
	primary, err := AxRoute(route.Primary.Settings)
	if err != nil {
		return Result{}, err
	}
	request := inference.Request{Route: primary, Signature: program.Signature, Instruction: program.Instruction, Values: values}
	var fallbackRoute *inference.Route
	if route.Fallback != nil {
		fr, err := AxRoute(route.Fallback.Settings)
		if err != nil {
			return Result{}, err
		}
		fallbackRoute = &fr
		request.Fallback = fallbackRoute
	}
	key, err := GatewayKey(ctx, route.Primary.Settings, h.Token)
	if err != nil {
		return Result{}, err
	}
	fallbackKey := ""
	if route.Fallback != nil {
		if fallbackKey, err = GatewayKey(ctx, route.Fallback.Settings, h.Token); err != nil {
			return Result{}, err
		}
	}
	callCtx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	result, err := h.Gateway(callCtx, request, key, fallbackKey)
	if err != nil {
		return Result{}, err
	}
	served, servedRoute := route.Primary, primary
	byFallback := result.Fallback && route.Fallback != nil
	if byFallback {
		served, servedRoute = *route.Fallback, *fallbackRoute
	}
	now := time.Now()
	if h.Now != nil {
		now = h.Now()
	}
	usage := PriceUsage(result.Usage, served.Price, servedRoute, now)
	if orgID != "" && task != "" && h.Spend != nil {
		h.Spend(ctx, orgID, task, served, usage)
	}
	outputs := result.Outputs
	if outputs == nil {
		outputs = map[string]any{}
	}
	return Result{Outputs: outputs, Usage: usage, Provider: served.Settings.Provider, Model: served.Settings.Model, Fallback: byFallback}, nil
}

var providerTest = Program{`prompt:string -> reply:string "the word READY"`, "Reply with READY only."}

// TestPrimary is testPrimaryProvider(): one call on the settings, with no
// task route and no spend row.
func (h HostCalls) TestPrimary(ctx context.Context, orgID string, s Settings) error {
	result, err := h.OnRoute(ctx, orgID, "", TaskRoute{Task: "default", Primary: RoutedModel{Ref: "default", Name: "default", Settings: s}}, providerTest, map[string]any{"prompt": "Reply with READY only."})
	if err != nil {
		return err
	}
	reply, _ := result.Outputs["reply"].(string)
	if raw, ok := result.Outputs["reply"]; ok && reply == "" && raw != nil {
		reply = fmt.Sprint(raw)
	}
	if strings.TrimSpace(reply) == "" {
		return errors.New("Provider test returned an empty response")
	}
	return nil
}
