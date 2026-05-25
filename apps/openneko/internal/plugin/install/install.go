package install

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/open-neko/neko/apps/openneko/internal/plugin/manifest"
	"github.com/open-neko/neko/apps/openneko/internal/plugin/marketplace"
	"github.com/open-neko/neko/apps/openneko/internal/secrets"
)

type TrustedMarketplace struct {
	Name string
	URL  string
}

// EnvPromptFunc resolves a missing required env value. The CLI supplies a
// TTY-hidden prompt; the worker (future) will supply a web-UI modal.
type EnvPromptFunc func(plugin string, req marketplace.EnvRequirement) (string, error)

// NpmRunner runs `npm <args>` in cwd. Defaults to a real npm subprocess; tests
// override.
type NpmRunner func(ctx context.Context, args []string, cwd string) error

type Options struct {
	RepoRoot            string
	Spec                string
	Version             string
	Unverified          bool
	TrustedMarketplaces []TrustedMarketplace
	SecretsConfigDir    string
	Client              marketplace.Client
	NpmRunner           NpmRunner
	EnvPrompt           EnvPromptFunc
	// InstallDir overrides where `npm install` runs and where node_modules
	// is looked up. Defaults to RepoRoot. Env var OPENNEKO_PLUGIN_INSTALL_DIR
	// also wins when set, useful inside the worker container which installs
	// plugins to /var/lib/openneko/plugins/ to avoid clashing with the
	// pre-existing pnpm-managed workspace at /app.
	InstallDir string
	// SkillsInstallDir overrides where bundled skill halves are copied
	// (declared via package.json's openneko.skill). Default ~/.openneko/
	// skills/. The worker's skill-doctor (M8) walks this dir.
	SkillsInstallDir string
}

// installDir resolves the directory used for npm install + node_modules
// reads. Precedence: explicit option > env var > RepoRoot.
func installDir(opts Options) string {
	if opts.InstallDir != "" {
		return opts.InstallDir
	}
	if v := os.Getenv("OPENNEKO_PLUGIN_INSTALL_DIR"); v != "" {
		return v
	}
	return opts.RepoRoot
}

type Result struct {
	Name          string
	Version       string
	Integrity     string
	Network       []string
	Marketplace   string
	Source        string // "marketplace" | "unverified"
	EnvSaved      []string
	EnvAlreadySet []string
	// SkillInstalledAt is the absolute path under SkillsInstallDir where
	// the package's bundled skill half landed, or empty when the package
	// declared no skill.
	SkillInstalledAt string
}

type ParsedSpec struct {
	Name           string
	MarketplaceRef string
}

func ParseSpec(spec string) (ParsedSpec, error) {
	if spec == "" {
		return ParsedSpec{}, errors.New("install: package name required")
	}
	at := strings.LastIndex(spec, "@")
	if at <= 0 {
		return ParsedSpec{Name: spec}, nil
	}
	name := spec[:at]
	mref := spec[at+1:]
	if mref == "" {
		return ParsedSpec{Name: spec}, nil
	}
	return ParsedSpec{Name: name, MarketplaceRef: mref}, nil
}

func Run(ctx context.Context, opts Options) (*Result, error) {
	if opts.NpmRunner == nil {
		opts.NpmRunner = realNpm
	}
	if opts.Client == nil {
		opts.Client = marketplace.NewClient()
	}
	if opts.Unverified {
		return runUnverified(ctx, opts)
	}
	return runMarketplace(ctx, opts)
}

func runMarketplace(ctx context.Context, opts Options) (*Result, error) {
	parsed, err := ParseSpec(opts.Spec)
	if err != nil {
		return nil, err
	}

	targets := opts.TrustedMarketplaces
	if parsed.MarketplaceRef != "" {
		var match *TrustedMarketplace
		for i, m := range opts.TrustedMarketplaces {
			if m.Name == parsed.MarketplaceRef || m.URL == parsed.MarketplaceRef {
				match = &opts.TrustedMarketplaces[i]
				break
			}
		}
		if match == nil {
			return nil, fmt.Errorf(`install: marketplace %q not trusted — add it first with `+"`openneko marketplace add <url>`", parsed.MarketplaceRef)
		}
		targets = []TrustedMarketplace{*match}
	}

	type hit struct {
		mp     *marketplace.Marketplace
		name   string
		url    string
		plugin *marketplace.Plugin
	}
	var hits []hit
	for _, m := range targets {
		mp, err := opts.Client.Fetch(ctx, m.URL)
		if err != nil {
			return nil, fmt.Errorf("install: failed to fetch marketplace %s (%s): %w", m.Name, m.URL, err)
		}
		if p := marketplace.FindPlugin(mp, parsed.Name); p != nil {
			hits = append(hits, hit{mp: mp, name: m.Name, url: m.URL, plugin: p})
		}
	}
	if len(hits) == 0 {
		names := make([]string, len(targets))
		for i, t := range targets {
			names[i] = t.Name
		}
		return nil, fmt.Errorf(`install: plugin %q not found in any trusted marketplace (%s)`, parsed.Name, strings.Join(names, ", "))
	}
	if len(hits) > 1 {
		choices := make([]string, len(hits))
		for i, h := range hits {
			choices[i] = parsed.Name + "@" + h.name
		}
		return nil, fmt.Errorf("install: plugin %q is listed in multiple trusted marketplaces. Pick one:\n  %s", parsed.Name, strings.Join(choices, "\n  "))
	}

	chosen := hits[0]
	version, err := marketplace.PickInstallVersion(chosen.plugin, opts.Version)
	if err != nil {
		return nil, err
	}

	envSaved, envAlreadySet, err := resolveRequiredEnv(parsed.Name, version, opts)
	if err != nil {
		return nil, err
	}

	if err := opts.NpmRunner(ctx, []string{"install", parsed.Name + "@" + version.Version}, installDir(opts)); err != nil {
		return nil, err
	}

	m, err := manifest.Read(opts.RepoRoot)
	if err != nil {
		return nil, err
	}
	if m == nil {
		empty := manifest.Empty()
		m = &empty
	}
	entry := manifest.Entry{
		Name:      parsed.Name,
		Version:   version.Version,
		Integrity: version.Integrity,
		Permissions: manifest.Permissions{
			Network: nilToEmpty(version.Permissions.Network),
			Env:     convertEnv(version.Permissions.Env),
		},
		Capabilities: convertCapabilities(version.Capabilities),
		Marketplace:  chosen.name,
	}
	updated := manifest.Upsert(*m, entry)
	if err := manifest.Write(opts.RepoRoot, updated); err != nil {
		return nil, err
	}

	skillInstalledAt, err := copyBundledSkill(parsed.Name, installDir(opts), opts.SkillsInstallDir)
	if err != nil {
		return nil, err
	}

	return &Result{
		Name:             parsed.Name,
		Version:          version.Version,
		Integrity:        version.Integrity,
		Network:          entry.Permissions.Network,
		Marketplace:      chosen.name,
		Source:           "marketplace",
		EnvSaved:         envSaved,
		EnvAlreadySet:    envAlreadySet,
		SkillInstalledAt: skillInstalledAt,
	}, nil
}

func resolveRequiredEnv(pluginName string, version *marketplace.Version, opts Options) (saved, alreadySet []string, err error) {
	required := make([]marketplace.EnvRequirement, 0)
	for _, r := range version.Permissions.Env {
		if r.Required == nil || *r.Required {
			required = append(required, r)
		}
	}
	if len(required) == 0 {
		return nil, nil, nil
	}

	store, err := secrets.Read(opts.SecretsConfigDir)
	if err != nil {
		return nil, nil, err
	}
	existing := map[string]bool{}
	for _, k := range secrets.ListKeysForPlugin(store, pluginName) {
		existing[k] = true
	}
	missing := []marketplace.EnvRequirement{}
	for _, r := range required {
		if existing[r.Key] {
			alreadySet = append(alreadySet, r.Key)
		} else {
			missing = append(missing, r)
		}
	}
	if len(missing) == 0 {
		return nil, alreadySet, nil
	}
	if opts.EnvPrompt == nil {
		return nil, nil, errors.New("install: env prompt required but not supplied")
	}
	updated := store
	for _, req := range missing {
		val, err := opts.EnvPrompt(pluginName, req)
		if err != nil {
			return nil, nil, err
		}
		if val == "" {
			return nil, nil, fmt.Errorf(`install: required env %q not supplied for %s`, req.Key, pluginName)
		}
		updated, err = secrets.Set(updated, pluginName, req.Key, val)
		if err != nil {
			return nil, nil, err
		}
		saved = append(saved, req.Key)
	}
	if err := secrets.Write(updated, opts.SecretsConfigDir); err != nil {
		return nil, nil, err
	}
	return saved, alreadySet, nil
}

func runUnverified(ctx context.Context, opts Options) (*Result, error) {
	parsed, err := ParseSpec(opts.Spec)
	if err != nil {
		return nil, err
	}
	spec := parsed.Name
	if opts.Version != "" {
		spec = parsed.Name + "@" + opts.Version
	}
	if err := opts.NpmRunner(ctx, []string{"install", spec}, installDir(opts)); err != nil {
		return nil, err
	}
	meta, err := readPackageMeta(parsed.Name, installDir(opts))
	if err != nil {
		return nil, fmt.Errorf("--unverified install: cannot read package.json for %s after install: %w", parsed.Name, err)
	}
	if meta.Openneko == nil || meta.Openneko.Capabilities == nil {
		return nil, fmt.Errorf("--unverified install: %s package.json must declare openneko.capabilities", parsed.Name)
	}
	integrity := meta.Integrity
	if integrity == "" {
		integrity = "sha512-unknown"
	}
	caps := convertOpennekoCapabilities(meta.Openneko.Capabilities)
	network := []string{}
	envReq := []manifest.EnvRequirement{}
	if meta.Openneko.Permissions != nil {
		network = nilToEmpty(meta.Openneko.Permissions.Network)
		envReq = convertOpennekoEnv(meta.Openneko.Permissions.Env)
	}
	entry := manifest.Entry{
		Name:         parsed.Name,
		Version:      meta.Version,
		Integrity:    integrity,
		Permissions:  manifest.Permissions{Network: network, Env: envReq},
		Capabilities: caps,
	}
	m, err := manifest.Read(opts.RepoRoot)
	if err != nil {
		return nil, err
	}
	if m == nil {
		empty := manifest.Empty()
		m = &empty
	}
	if err := manifest.Write(opts.RepoRoot, manifest.Upsert(*m, entry)); err != nil {
		return nil, err
	}
	skillInstalledAt, err := copyBundledSkill(parsed.Name, installDir(opts), opts.SkillsInstallDir)
	if err != nil {
		return nil, err
	}
	return &Result{
		Name:             parsed.Name,
		Version:          entry.Version,
		Integrity:        entry.Integrity,
		Network:          entry.Permissions.Network,
		Source:           "unverified",
		SkillInstalledAt: skillInstalledAt,
	}, nil
}

// copyBundledSkill is the Go mirror of plugin-install/run-install.ts's
// copyBundledSkill. After npm install completes, look for an
// openneko.skill folder declared in the installed package's
// package.json; copy it under the operator's ~/.openneko/skills/
// directory keyed by the skill's frontmatter name. Returns the
// destination path on success, empty string when the package declared
// no skill, or an error on filesystem failure.
func copyBundledSkill(pluginName, installRoot, skillsInstallDir string) (string, error) {
	meta, err := readPackageMeta(pluginName, installRoot)
	if err != nil {
		// readPackageMeta only errors when package.json is unreadable.
		// For --unverified that already errored upstream; for marketplace
		// installs the lack of package.json is a separate bug. Treat as
		// "no skill to copy" rather than failing.
		return "", nil
	}
	if meta.Openneko == nil || meta.Openneko.Skill == "" {
		return "", nil
	}
	pkgRoot := filepath.Join(installRoot, "node_modules", pluginName)
	skillSrc := filepath.Join(pkgRoot, meta.Openneko.Skill)
	if info, err := os.Stat(skillSrc); err != nil || !info.IsDir() {
		// Declared but missing on disk — skip silently.
		return "", nil
	}

	skillName := unscopedName(pluginName)
	skillMd := filepath.Join(skillSrc, "SKILL.md")
	if frontmatterName, err := readSkillFrontmatterName(skillMd); err == nil && frontmatterName != "" {
		skillName = frontmatterName
	}

	dest := filepath.Join(resolveSkillsInstallDir(skillsInstallDir), skillName)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", err
	}
	// Remove any prior copy so re-installs reflect upstream changes.
	_ = os.RemoveAll(dest)
	if err := copyDir(skillSrc, dest); err != nil {
		return "", fmt.Errorf("copy skill: %w", err)
	}
	return dest, nil
}

func unscopedName(pkg string) string {
	if i := strings.LastIndex(pkg, "/"); i >= 0 {
		return pkg[i+1:]
	}
	return pkg
}

func resolveSkillsInstallDir(override string) string {
	if override != "" {
		return override
	}
	if v := os.Getenv("OPENNEKO_SKILLS_DIR"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	if home == "" {
		home = os.TempDir()
	}
	return filepath.Join(home, ".openneko", "skills")
}

// readSkillFrontmatterName extracts the `name:` field from a
// SKILL.md's YAML frontmatter — the absolute minimum the install
// path needs. Mirrors extractNameFromFrontmatter on the TS side.
func readSkillFrontmatterName(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	lines := strings.Split(string(raw), "\n")
	if len(lines) == 0 || strings.TrimRight(lines[0], "\r") != "---" {
		return "", nil
	}
	for i := 1; i < len(lines); i++ {
		line := strings.TrimRight(lines[i], "\r")
		if line == "---" {
			return "", nil
		}
		const prefix = "name:"
		if strings.HasPrefix(line, prefix) {
			val := strings.TrimSpace(line[len(prefix):])
			val = strings.Trim(val, `"' `)
			return val, nil
		}
	}
	return "", nil
}

// copyDir mirrors a directory tree. Filesystem-only, no symlinks.
func copyDir(src, dst string) error {
	return filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode())
	})
}

type pkgEnv struct {
	Key         string `json:"key"`
	Required    *bool  `json:"required,omitempty"`
	Secret      *bool  `json:"secret,omitempty"`
	Description string `json:"description"`
}

type pkgPermissions struct {
	Network []string `json:"network,omitempty"`
	Env     []pkgEnv `json:"env,omitempty"`
}

type pkgCapabilities struct {
	Action  *manifest.ActionCapability  `json:"action,omitempty"`
	Auth    *manifest.AuthCapability    `json:"auth,omitempty"`
	Channel *manifest.ChannelCapability `json:"channel,omitempty"`
}

type pkgOpenneko struct {
	Runner       string           `json:"runner,omitempty"`
	Skill        string           `json:"skill,omitempty"`
	Permissions  *pkgPermissions  `json:"permissions,omitempty"`
	Capabilities *pkgCapabilities `json:"capabilities,omitempty"`
}

type pkgMeta struct {
	Version   string       `json:"version"`
	Integrity string       `json:"_integrity,omitempty"`
	Openneko  *pkgOpenneko `json:"openneko,omitempty"`
}

func readPackageMeta(name, cwd string) (*pkgMeta, error) {
	file := filepath.Join(cwd, "node_modules", name, "package.json")
	raw, err := os.ReadFile(file)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("package.json missing at %s", file)
		}
		return nil, err
	}
	var meta pkgMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return nil, err
	}
	return &meta, nil
}

func realNpm(ctx context.Context, args []string, cwd string) error {
	cmd := exec.CommandContext(ctx, "npm", args...)
	cmd.Dir = cwd
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

func nilToEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func convertEnv(in []marketplace.EnvRequirement) []manifest.EnvRequirement {
	out := make([]manifest.EnvRequirement, len(in))
	for i, r := range in {
		out[i] = manifest.EnvRequirement{
			Key:         r.Key,
			Required:    r.Required,
			Secret:      r.Secret,
			Description: r.Description,
		}
	}
	return out
}

func convertCapabilities(c marketplace.Capabilities) manifest.Capabilities {
	var out manifest.Capabilities
	if c.Action != nil {
		acts := make([]manifest.ActionDeclaration, len(c.Action.Kinds))
		for i, a := range c.Action.Kinds {
			acts[i] = manifest.ActionDeclaration{
				Kind:        a.Kind,
				Description: a.Description,
				DefaultMode: a.DefaultMode,
			}
		}
		out.Action = &manifest.ActionCapability{Kinds: acts}
	}
	if c.Auth != nil {
		out.Auth = &manifest.AuthCapability{ProviderLabel: c.Auth.ProviderLabel}
	}
	if c.Channel != nil {
		out.Channel = &manifest.ChannelCapability{
			ProviderLabel: c.Channel.ProviderLabel,
			Profile:       c.Channel.Profile,
			Directions:    c.Channel.Directions,
			Ingress:       c.Channel.Ingress,
		}
	}
	return out
}

func convertOpennekoCapabilities(c *pkgCapabilities) manifest.Capabilities {
	if c == nil {
		return manifest.Capabilities{}
	}
	var out manifest.Capabilities
	if c.Action != nil {
		out.Action = c.Action
	}
	if c.Auth != nil {
		out.Auth = c.Auth
	}
	if c.Channel != nil {
		out.Channel = c.Channel
	}
	return out
}

func convertOpennekoEnv(in []pkgEnv) []manifest.EnvRequirement {
	out := make([]manifest.EnvRequirement, len(in))
	for i, r := range in {
		out[i] = manifest.EnvRequirement{
			Key:         r.Key,
			Required:    r.Required,
			Secret:      r.Secret,
			Description: r.Description,
		}
	}
	return out
}
