package setup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/open-neko/neko/apps/openneko/internal/prompt"
)

// Config carries everything the onboarding flow needs. Headless callers set the
// override fields and Headless=true; interactive callers leave them empty.
type Config struct {
	Mode    string // demo | prod | dev — picks the data-source default
	BaseURL string // web app URL, for the browser handoff message

	Headless bool // run from flags without prompting

	AdminPassword    string
	Backend          string
	Provider         string
	ProviderKey      string
	Model            string
	DataURL          string
	ResearchProvider string
	ResearchKey      string
	NoResearch       bool
}

// Outcome reports how onboarding ended so the caller can print the right
// next-step message.
type Outcome struct {
	Configured bool // ran through Finish in the terminal
	Skipped    bool // deferred to the browser (chosen, or no TTY + no flags)
}

// errBrowser is returned by any step when the operator opts out to the browser.
var errBrowser = errors.New("browser handoff")

var forbiddenPasswords = map[string]bool{"secret": true, "password": true, "postgres": true}

const minPasswordLen = 8

// Run executes onboarding against the web app. Headless runs from Config
// fields; interactive prompts step by step and can bail to the browser at any
// point (partial progress persists server-side, so resuming in the browser just
// continues).
func Run(ctx context.Context, c *Client, out io.Writer, cfg Config) (Outcome, error) {
	if cfg.Headless {
		return runHeadless(ctx, c, cfg)
	}
	return runInteractive(ctx, c, out, cfg)
}

// ----- interactive -----

func runInteractive(ctx context.Context, c *Client, out io.Writer, cfg Config) (Outcome, error) {
	fmt.Fprintln(out)
	fmt.Fprintln(out, "OpenNeko is up. Configure it now in the terminal, or finish in your browser?")
	choice, err := prompt.Visible("  [t] terminal  ·  [b] browser  (t): ")
	if err != nil {
		return Outcome{}, err
	}
	if strings.EqualFold(strings.TrimSpace(choice), "b") {
		return browserHandoff(out, cfg), nil
	}
	fmt.Fprintln(out, "  (type b at any prompt to switch to the browser)")

	err = stepsInteractive(ctx, c, out, cfg)
	if errors.Is(err, errBrowser) {
		return browserHandoff(out, cfg), nil
	}
	if err != nil {
		return Outcome{}, err
	}
	return Outcome{Configured: true}, nil
}

func stepsInteractive(ctx context.Context, c *Client, out io.Writer, cfg Config) error {
	// 1. Admin DB password (only when still the bootstrap default).
	changed, err := c.PasswordChanged(ctx)
	if err != nil {
		return err
	}
	if !changed {
		fmt.Fprintln(out, "\n1. Choose a database password (min 8 chars; you won't enter it again).")
		pw, err := promptNewPassword(out)
		if err != nil {
			return err
		}
		if err := c.ChangePassword(ctx, pw); err != nil {
			return err
		}
		fmt.Fprintln(out, "   ✓ password set")
	}

	// 2. Data source.
	ds, err := c.GetDataSource(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintln(out, "\n2. Connect your data (GraphJin base URL).")
	for {
		root, err := askLine("   GraphJin URL", defaultDataURL(cfg, ds))
		if err != nil {
			return err
		}
		gql, mcp := DeriveEndpoints(root)
		mcpOK, err := c.TestDataSource(ctx, gql, mcp)
		if err != nil {
			fmt.Fprintf(out, "   ✗ %s — try again.\n", err)
			continue
		}
		if err := c.SaveDataSource(ctx, gql, mcp, "primary"); err != nil {
			fmt.Fprintf(out, "   ✗ %s — try again.\n", err)
			continue
		}
		if mcpOK {
			fmt.Fprintln(out, "   ✓ data source saved")
		} else {
			fmt.Fprintln(out, "   ✓ data source saved (MCP unreachable — fine for the agent path)")
		}
		break
	}

	// 3. Agent backend + primary provider.
	agent, err := c.GetAgent(ctx)
	if err != nil {
		return err
	}
	prov, err := c.GetProvider(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintln(out, "\n3. Choose the agent + model provider.")
	backend, err := chooseOption(out, "   Agent backend", toOptions(agent.Options), agent.Agent.Backend)
	if err != nil {
		return err
	}
	var provider string
	if backend == "claude-agent" {
		provider = "anthropic"
		fmt.Fprintln(out, "   Provider: anthropic (locked by Claude Agent backend)")
	} else {
		provider, err = chooseOption(out, "   Provider", prov.Options.Primary, prov.Primary.Provider)
		if err != nil {
			return err
		}
	}
	model, err := askLine("   Model", modelDefault(prov.Defaults.Primary, provider, prov.Primary))
	if err != nil {
		return err
	}
	config, secrets, err := collectFields(prov.Fields.Primary[provider])
	if err != nil {
		return err
	}
	cap := agent.Agent.GlobalCap
	if cap == 0 {
		cap = agent.Defaults.GlobalCap
	}
	draft := ProviderDraft{Scope: "primary", Provider: provider, Model: model, Enabled: true, Config: config, Secrets: secrets}
	fmt.Fprintln(out, "   validating key…")
	if err := c.TestProvider(ctx, draft); err != nil {
		return fmt.Errorf("provider key test failed: %w", err)
	}
	if err := c.SaveAgent(ctx, backend, cap); err != nil {
		return err
	}
	if err := c.SaveProvider(ctx, draft); err != nil {
		return err
	}
	fmt.Fprintln(out, "   ✓ agent + provider saved")

	// 4. Research (optional).
	fmt.Fprintln(out, "\n4. Industry research (optional).")
	enable, err := askYesNo("   Enable industry research?", false)
	if err != nil {
		return err
	}
	if enable {
		rprovider, err := chooseOption(out, "   Research provider", filterOut(prov.Options.Research, "disabled"), researchDefault(prov.Options.Research))
		if err != nil {
			return err
		}
		rmodel, err := askLine("   Model", prov.Defaults.Research[rprovider])
		if err != nil {
			return err
		}
		rconfig, rsecrets, err := collectFields(prov.Fields.Research[rprovider])
		if err != nil {
			return err
		}
		rdraft := ProviderDraft{Scope: "research", Provider: rprovider, Model: rmodel, Enabled: true, Config: rconfig, Secrets: rsecrets}
		fmt.Fprintln(out, "   validating key…")
		if err := c.TestProvider(ctx, rdraft); err != nil {
			return fmt.Errorf("research key test failed: %w", err)
		}
		if err := c.SaveProvider(ctx, rdraft); err != nil {
			return err
		}
		fmt.Fprintln(out, "   ✓ research enabled")
	} else if err := c.SaveProvider(ctx, disabledResearch()); err != nil {
		return err
	}

	// 5. Finish.
	return c.Finish(ctx)
}

// ----- headless -----

func runHeadless(ctx context.Context, c *Client, cfg Config) (Outcome, error) {
	changed, err := c.PasswordChanged(ctx)
	if err != nil {
		return Outcome{}, err
	}
	if !changed {
		if cfg.AdminPassword == "" {
			return Outcome{}, errors.New("setup: --admin-password is required (database still has the bootstrap default)")
		}
		if err := validatePassword(cfg.AdminPassword); err != nil {
			return Outcome{}, err
		}
		if err := c.ChangePassword(ctx, cfg.AdminPassword); err != nil {
			return Outcome{}, err
		}
	}

	ds, err := c.GetDataSource(ctx)
	if err != nil {
		return Outcome{}, err
	}
	gql, mcp := DeriveEndpoints(defaultDataURL(cfg, ds))
	if _, err := c.TestDataSource(ctx, gql, mcp); err != nil {
		return Outcome{}, fmt.Errorf("data source test failed: %w", err)
	}
	if err := c.SaveDataSource(ctx, gql, mcp, "primary"); err != nil {
		return Outcome{}, err
	}

	agent, err := c.GetAgent(ctx)
	if err != nil {
		return Outcome{}, err
	}
	prov, err := c.GetProvider(ctx)
	if err != nil {
		return Outcome{}, err
	}
	backend := cfg.Backend
	if backend == "" {
		backend = agent.Agent.Backend
	}
	provider := cfg.Provider
	if backend == "claude-agent" {
		provider = "anthropic"
	}
	if provider == "" {
		return Outcome{}, errors.New("setup: --provider is required")
	}
	if cfg.ProviderKey == "" {
		return Outcome{}, errors.New("setup: --provider-key is required")
	}
	model := cfg.Model
	if model == "" {
		model = modelDefault(prov.Defaults.Primary, provider, prov.Primary)
	}
	secretKey := secretFieldKey(prov.Fields.Primary[provider])
	cap := agent.Agent.GlobalCap
	if cap == 0 {
		cap = agent.Defaults.GlobalCap
	}
	draft := ProviderDraft{
		Scope: "primary", Provider: provider, Model: model, Enabled: true,
		Config: map[string]string{}, Secrets: map[string]string{secretKey: cfg.ProviderKey},
	}
	if err := c.TestProvider(ctx, draft); err != nil {
		return Outcome{}, fmt.Errorf("provider key test failed: %w", err)
	}
	if err := c.SaveAgent(ctx, backend, cap); err != nil {
		return Outcome{}, err
	}
	if err := c.SaveProvider(ctx, draft); err != nil {
		return Outcome{}, err
	}

	if !cfg.NoResearch && cfg.ResearchProvider != "" && cfg.ResearchKey != "" {
		rmodel := prov.Defaults.Research[cfg.ResearchProvider]
		rkey := secretFieldKey(prov.Fields.Research[cfg.ResearchProvider])
		rdraft := ProviderDraft{
			Scope: "research", Provider: cfg.ResearchProvider, Model: rmodel, Enabled: true,
			Config: map[string]string{}, Secrets: map[string]string{rkey: cfg.ResearchKey},
		}
		if err := c.TestProvider(ctx, rdraft); err != nil {
			return Outcome{}, fmt.Errorf("research key test failed: %w", err)
		}
		if err := c.SaveProvider(ctx, rdraft); err != nil {
			return Outcome{}, err
		}
	} else if err := c.SaveProvider(ctx, disabledResearch()); err != nil {
		return Outcome{}, err
	}

	if err := c.Finish(ctx); err != nil {
		return Outcome{}, err
	}
	return Outcome{Configured: true}, nil
}

// ----- shared helpers -----

func browserHandoff(out io.Writer, cfg Config) Outcome {
	fmt.Fprintf(out, "\nFinish setup in your browser: %s\n", cfg.BaseURL)
	return Outcome{Skipped: true}
}

func defaultDataURL(cfg Config, ds *DataSource) string {
	if ds != nil && ds.Source == "org" && ds.GraphqlURL != "" {
		return deriveRoot(ds.GraphqlURL)
	}
	if cfg.DataURL != "" {
		return cfg.DataURL
	}
	if cfg.Mode == "demo" {
		return "http://graphjin:8080"
	}
	return "http://host.docker.internal:8080"
}

func modelDefault(defaults map[string]string, provider string, current ProviderConfig) string {
	if d := defaults[provider]; d != "" {
		return d
	}
	if current.Provider == provider && current.Model != "" {
		return current.Model
	}
	return ""
}

func researchDefault(opts []ProviderOption) string {
	for _, o := range opts {
		if o.Value != "disabled" {
			return o.Value
		}
	}
	return ""
}

func disabledResearch() ProviderDraft {
	return ProviderDraft{Scope: "research", Provider: "disabled", Model: "", Enabled: false, Config: map[string]string{}, Secrets: map[string]string{}}
}

func filterOut(opts []ProviderOption, drop string) []ProviderOption {
	out := make([]ProviderOption, 0, len(opts))
	for _, o := range opts {
		if o.Value != drop {
			out = append(out, o)
		}
	}
	return out
}

func toOptions(in []AgentBackendOption) []ProviderOption {
	out := make([]ProviderOption, len(in))
	for i, o := range in {
		out[i] = ProviderOption{Value: o.Value, Label: o.Label, Description: o.Description}
	}
	return out
}

// secretFieldKey returns the key of the first secret-kind field, defaulting to
// "apiKey" when the provider declares none explicitly.
func secretFieldKey(fields []Field) string {
	for _, f := range fields {
		if f.Kind == "secret" {
			return f.Key
		}
	}
	return "apiKey"
}

func validatePassword(pw string) error {
	if len(pw) < minPasswordLen {
		return fmt.Errorf("password must be at least %d characters", minPasswordLen)
	}
	if forbiddenPasswords[strings.ToLower(pw)] {
		return errors.New("that password is too common; pick something else")
	}
	return nil
}

// ----- prompt helpers -----

// askLine reads a visible line with a default; "b" bails to the browser.
func askLine(label, def string) (string, error) {
	suffix := ""
	if def != "" {
		suffix = fmt.Sprintf(" [%s]", def)
	}
	v, err := prompt.Visible(fmt.Sprintf("%s%s: ", label, suffix))
	if err != nil {
		return "", err
	}
	v = strings.TrimSpace(v)
	if strings.EqualFold(v, "b") {
		return "", errBrowser
	}
	if v == "" {
		return def, nil
	}
	return v, nil
}

func askYesNo(label string, def bool) (bool, error) {
	hint := "y/N"
	if def {
		hint = "Y/n"
	}
	v, err := prompt.Visible(fmt.Sprintf("%s (%s): ", label, hint))
	if err != nil {
		return false, err
	}
	v = strings.TrimSpace(strings.ToLower(v))
	if v == "b" {
		return false, errBrowser
	}
	if v == "" {
		return def, nil
	}
	return v == "y" || v == "yes", nil
}

// chooseOption prints the options and reads a value (accepts the value itself
// or its 1-based index); empty keeps def.
func chooseOption(out io.Writer, label string, opts []ProviderOption, def string) (string, error) {
	for i, o := range opts {
		marker := " "
		if o.Value == def {
			marker = "*"
		}
		fmt.Fprintf(out, "     %s [%d] %s — %s\n", marker, i+1, o.Value, o.Description)
	}
	v, err := askLine(label, def)
	if err != nil {
		return "", err
	}
	for i, o := range opts {
		if v == o.Value || v == fmt.Sprintf("%d", i+1) {
			return o.Value, nil
		}
	}
	if v == def {
		return def, nil
	}
	return "", fmt.Errorf("unknown option %q", v)
}

func promptNewPassword(out io.Writer) (string, error) {
	for {
		pw, err := prompt.Hidden("   New password: ")
		if err != nil {
			return "", err
		}
		if err := validatePassword(pw); err != nil {
			fmt.Fprintf(out, "   ✗ %s\n", err)
			continue
		}
		confirm, err := prompt.Hidden("   Confirm password: ")
		if err != nil {
			return "", err
		}
		if pw != confirm {
			fmt.Fprintln(out, "   ✗ passwords don't match")
			continue
		}
		return pw, nil
	}
}

// collectFields prompts each provider field, routing secret-kind fields through
// a hidden prompt and the rest into the config map.
func collectFields(fields []Field) (config, secrets map[string]string, err error) {
	config = map[string]string{}
	secrets = map[string]string{}
	for _, f := range fields {
		label := "   " + f.Label
		if f.Kind == "secret" {
			v, err := prompt.Hidden(label + ": ")
			if err != nil {
				return nil, nil, err
			}
			secrets[f.Key] = v
			continue
		}
		v, err := askLine(label, f.Placeholder)
		if err != nil {
			return nil, nil, err
		}
		config[f.Key] = v
	}
	return config, secrets, nil
}
