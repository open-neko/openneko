package llm

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

type Endpoint struct {
	Host string `json:"host"`
	Port int    `json:"port,omitempty"`
}

// Runtime is resolveProviderRuntime() in packages/llm/src/provider-runtime.ts.
type Runtime struct {
	Model            string
	BaseURL          string
	KeyEnv           string
	CredentialSource string
	Endpoint         Endpoint
}

func configString(config map[string]any, key string) string {
	s, _ := config[key].(string)
	return strings.TrimSpace(s)
}

func requiredConfig(config map[string]any, key, label string) (string, error) {
	if v := configString(config, key); v != "" {
		return v, nil
	}
	return "", fmt.Errorf("%s is required.", label)
}

func parseHTTPURL(raw string) (*url.URL, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Host == "" || u.Hostname() == "" {
		return nil, false
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	return u, true
}

func endpointFromURL(baseURL string) (Endpoint, error) {
	u, ok := parseHTTPURL(baseURL)
	if !ok {
		return Endpoint{}, fmt.Errorf("Invalid provider Base URL: %s", baseURL)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return Endpoint{}, fmt.Errorf("Provider Base URL must use http or https: %s", baseURL)
	}
	e := Endpoint{Host: u.Hostname()}
	if strings.Contains(e.Host, ":") {
		e.Host = "[" + e.Host + "]"
	}
	if p := u.Port(); p != "" {
		e.Port, _ = strconv.Atoi(p)
		if (u.Scheme == "http" && e.Port == 80) || (u.Scheme == "https" && e.Port == 443) {
			e.Port = 0
		}
	}
	if e.Port == 0 && u.Scheme == "http" {
		e.Port = 80
	}
	return e, nil
}

func fixedRuntime(model, baseURL, keyEnv string) (Runtime, error) {
	e, err := endpointFromURL(baseURL)
	return Runtime{Model: model, BaseURL: baseURL, KeyEnv: keyEnv, CredentialSource: "stored-api-key", Endpoint: e}, err
}

var (
	dnsLabel  = regexp.MustCompile(`(?i)^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
	projectID = regexp.MustCompile(`(?i)^[a-z0-9][a-z0-9-.:]{0,126}$`)
)

func azureBaseURL(config map[string]any) (string, error) {
	resource, err := requiredConfig(config, "resourceName", "Azure resource name")
	if err != nil {
		return "", err
	}
	if !dnsLabel.MatchString(resource) {
		return "", errors.New("Azure resource name contains invalid characters.")
	}
	return "https://" + resource + ".openai.azure.com/openai/v1", nil
}

func VertexBaseURL(config map[string]any) (string, error) {
	project, err := requiredConfig(config, "projectId", "GCP project ID")
	if err != nil {
		return "", err
	}
	region, err := requiredConfig(config, "region", "Vertex region")
	if err != nil {
		return "", err
	}
	if !projectID.MatchString(project) {
		return "", errors.New("GCP project ID contains invalid characters.")
	}
	if !dnsLabel.MatchString(region) {
		return "", errors.New("Vertex region contains invalid characters.")
	}
	host := region + "-aiplatform.googleapis.com"
	if region == "global" {
		host = "aiplatform.googleapis.com"
	}
	return "https://" + host + "/v1beta1/projects/" + url.PathEscape(project) + "/locations/" + url.PathEscape(region) + "/endpoints/openapi", nil
}

// localBaseURL sends a loopback address to host.docker.internal: openneko
// serve calls the model from its own container.
func localBaseURL(config map[string]any, label, defaultPath string) (string, error) {
	configured, err := requiredConfig(config, "url", label+" Base URL")
	if err != nil {
		return "", err
	}
	u, ok := parseHTTPURL(configured)
	if !ok {
		return "", fmt.Errorf("Invalid %s Base URL: %s", label, configured)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("%s Base URL must use http or https.", label)
	}
	if u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(configured, "#") {
		return "", fmt.Errorf("%s Base URL must not carry credentials, a query or a fragment.", label)
	}
	if p := u.Port(); (p == "80" && u.Scheme == "http") || (p == "443" && u.Scheme == "https") {
		u.Host = strings.TrimSuffix(u.Host, ":"+p)
	}
	switch u.Hostname() {
	case "localhost", "127.0.0.1", "::1":
		if p := u.Port(); p != "" {
			u.Host = "host.docker.internal:" + p
		} else {
			u.Host = "host.docker.internal"
		}
	}
	if defaultPath != "" && (u.Path == "/" || u.Path == "") {
		u.Path, u.RawPath = defaultPath, ""
	}
	return strings.TrimSuffix(u.String(), "/"), nil
}

var fixedProviders = map[string][2]string{
	"openai":        {"https://api.openai.com/v1", "OPENAI_API_KEY"},
	"anthropic":     {"https://api.anthropic.com", "ANTHROPIC_API_KEY"},
	"google-gemini": {"https://generativelanguage.googleapis.com/v1beta", "GEMINI_API_KEY"},
	"mistral":       {"https://api.mistral.ai/v1", "MISTRAL_API_KEY"},
	"groq":          {"https://api.groq.com/openai/v1", "GROQ_API_KEY"},
	"cohere":        {"https://api.cohere.ai/compatibility/v1", "COHERE_API_KEY"},
	"together":      {"https://api.together.ai/v1", "TOGETHER_API_KEY"},
	"deepseek":      {"https://api.deepseek.com/v1", "DEEPSEEK_API_KEY"},
	"huggingface":   {"https://router.huggingface.co/v1", "HF_TOKEN"},
	"openrouter":    {"https://openrouter.ai/api/v1", "OPENROUTER_API_KEY"},
	"reka":          {"https://api.reka.ai/v1", "REKA_API_KEY"},
	"x-grok":        {"https://api.x.ai/v1", "XAI_API_KEY"},
}

func ResolveRuntime(provider, model string, config map[string]any) (Runtime, error) {
	if fixed, ok := fixedProviders[provider]; ok {
		return fixedRuntime(model, fixed[0], fixed[1])
	}
	switch provider {
	case "azure-openai":
		deployment, err := requiredConfig(config, "deploymentName", "Azure deployment name")
		if err != nil {
			return Runtime{}, err
		}
		base, err := azureBaseURL(config)
		if err != nil {
			return Runtime{}, err
		}
		return fixedRuntime(deployment, base, "AZURE_FOUNDRY_API_KEY")
	case "ollama", "openai-compatible":
		label, path, keyEnv, source := "Ollama", "/v1", "", "none"
		if provider == "openai-compatible" {
			label, path, keyEnv, source = "OpenAI-compatible", "", "OPENAI_COMPATIBLE_API_KEY", "stored-api-key"
		}
		base, err := localBaseURL(config, label, path)
		if err != nil {
			return Runtime{}, err
		}
		e, err := endpointFromURL(base)
		return Runtime{Model: model, BaseURL: base, KeyEnv: keyEnv, CredentialSource: source, Endpoint: e}, err
	case "vertex":
		base, err := VertexBaseURL(config)
		if err != nil {
			return Runtime{}, err
		}
		e, err := endpointFromURL(base)
		return Runtime{Model: model, BaseURL: base, KeyEnv: "VERTEX_ACCESS_TOKEN", CredentialSource: "google-adc", Endpoint: e}, err
	}
	return Runtime{}, fmt.Errorf("unknown provider %q", provider)
}

// Settings is a ResolvedPrimaryProviderConfig in packages/llm/src/llm.ts.
type Settings struct {
	Source   string
	Provider string
	Model    string
	Enabled  bool
	Label    *string
	Config   map[string]any
	Secrets  map[string]string
}

func (s Settings) Validate() error {
	switch s.Provider {
	case "ollama":
		if configString(s.Config, "url") == "" {
			return errors.New("OLLAMA provider requires a Base URL")
		}
	case "openai-compatible":
		if configString(s.Config, "url") == "" {
			return errors.New("An OpenAI-compatible provider needs a Base URL")
		}
		if strings.TrimSpace(s.Model) == "" {
			return errors.New("An OpenAI-compatible provider needs a model name")
		}
	case "azure-openai":
		if s.Secrets["apiKey"] == "" {
			return errors.New("Azure OpenAI requires an API key")
		}
		if configString(s.Config, "resourceName") == "" {
			return errors.New("Azure OpenAI requires a resource name")
		}
		if configString(s.Config, "deploymentName") == "" {
			return errors.New("Azure OpenAI requires a deployment name")
		}
	case "vertex":
		if configString(s.Config, "projectId") == "" {
			return errors.New("Vertex requires a GCP project ID")
		}
	default:
		if s.Secrets["apiKey"] == "" {
			return fmt.Errorf("%s requires an API key", s.Provider)
		}
	}
	return nil
}
