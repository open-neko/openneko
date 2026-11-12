// Package llm holds the host's model settings: the provider catalogue, the
// provider runtime, the primary provider and the model records.
package llm

import (
	"strings"
	"unicode/utf16"
)

type ProviderOption struct {
	Value       string `json:"value"`
	Label       string `json:"label"`
	Description string `json:"description"`
}

type Field struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Kind        string `json:"kind"`
	Required    *bool  `json:"required,omitempty"`
	Placeholder string `json:"placeholder,omitempty"`
	Help        string `json:"help,omitempty"`
}

var PrimaryProviders = []ProviderOption{
	{"openai", "OpenAI", "Direct OpenAI API key"},
	{"anthropic", "Anthropic", "Claude via Anthropic API"},
	{"google-gemini", "Google Gemini", "Gemini via Google AI API"},
	{"azure-openai", "Azure OpenAI", "Azure resource + deployment"},
	{"mistral", "Mistral", "Mistral API"},
	{"groq", "Groq", "Groq API"},
	{"cohere", "Cohere", "Cohere API"},
	{"together", "Together", "Together API"},
	{"deepseek", "DeepSeek", "DeepSeek API"},
	{"ollama", "Ollama", "Local or remote Ollama endpoint"},
	{"openai-compatible", "OpenAI-compatible URL", "Any server that speaks the OpenAI chat API"},
	{"huggingface", "Hugging Face", "Hugging Face Inference API"},
	{"openrouter", "OpenRouter", "OpenRouter API"},
	{"reka", "Reka", "Reka API"},
	{"x-grok", "xAI Grok", "xAI Grok API"},
	{"vertex", "Vertex MaaS", "Vertex OpenAI-compatible endpoint via Google ADC"},
}

var defaultModels = map[string]string{
	"openai":            "gpt-4.1-mini",
	"anthropic":         "claude-opus-4-7",
	"google-gemini":     "gemini-3.6-flash",
	"azure-openai":      "gpt-4.1",
	"mistral":           "mistral-large-latest",
	"groq":              "llama-3.3-70b-versatile",
	"cohere":            "command-a-03-2025",
	"together":          "meta-llama/Llama-3.3-70B-Instruct-Turbo",
	"deepseek":          "deepseek-chat",
	"ollama":            "llama3.2",
	"openai-compatible": "",
	"huggingface":       "meta-llama/Llama-3.3-70B-Instruct",
	"openrouter":        "openai/gpt-4.1-mini",
	"reka":              "reka-core",
	"x-grok":            "grok-3-mini",
	"vertex":            "zai-org/glm-5-maas",
}

var yes, no = new(true), new(false)

func (f Field) IsRequired() bool { return f.Required != nil && *f.Required }

func IsPrimaryProvider(provider string) bool {
	_, ok := defaultModels[provider]
	return ok
}

func DefaultModel(provider string) string { return defaultModels[provider] }

func Fields(provider string) []Field {
	switch provider {
	case "ollama":
		return []Field{{
			Key: "url", Label: "Base URL", Kind: "url", Required: yes,
			Placeholder: "http://host.docker.internal:11434/v1",
			Help:        "Use host.docker.internal for Ollama running on the OpenNeko host, or a remote OpenAI-compatible URL.",
		}}
	case "openai-compatible":
		return []Field{
			{
				Key: "url", Label: "Base URL", Kind: "url", Required: yes,
				Placeholder: "https://models.example.com/v1",
				Help:        "The address before /chat/completions. Use host.docker.internal for a server on the OpenNeko host.",
			},
			{Key: "apiKey", Label: "API key", Kind: "secret", Required: no, Placeholder: "Leave empty when the server needs no key"},
		}
	case "azure-openai":
		return []Field{
			{Key: "apiKey", Label: "API key", Kind: "secret", Required: yes, Placeholder: "your-azure-key"},
			{Key: "resourceName", Label: "Resource name", Kind: "text", Required: yes, Placeholder: "my-openai-resource"},
			{Key: "deploymentName", Label: "Deployment name", Kind: "text", Required: yes, Placeholder: "gpt-4.1"},
		}
	case "vertex":
		return []Field{
			{
				Key: "projectId", Label: "GCP project ID", Kind: "text", Required: yes, Placeholder: "my-gcp-project",
				Help: "OpenNeko uses host-side Google Application Default Credentials to mint a short-lived token; the agent sandbox never receives the credential.",
			},
			{Key: "region", Label: "Region", Kind: "text", Required: yes, Placeholder: "global"},
		}
	}
	return []Field{{Key: "apiKey", Label: "API key", Kind: "secret", Required: yes, Placeholder: "Paste your provider key"}}
}

type Task struct {
	Task        string `json:"task"`
	Label       string `json:"label"`
	Description string `json:"description"`
}

var DefaultRoute = Task{"default", "Default", "Every task with no route of its own."}

var Tasks = []Task{
	{"turn", "Agent turns", "Questions, workflows, metric runs and channel replies."},
	{"classify", "Question classifier", "Sorts a new question before the agent starts."},
	{"compact", "Conversation compaction", "Shortens a long conversation so the agent can go on."},
	{"bot_instructions", "Bot instructions", "Keeps a bot's standing instructions up to date."},
	{"bot_names", "Bot names", "Suggests a name for a new bot."},
	{"skill_lessons", "Skill lessons", "Proposes a skill change from a finished run."},
	{"research_mission", "Research mission", "Writes the company mission during setup."},
}

func IsTask(task string) bool {
	for _, t := range Tasks {
		if t.Task == task {
			return true
		}
	}
	return false
}

// MaskSecret counts UTF-16 units, as the TypeScript maskSecret does.
func MaskSecret(value string) string {
	units := utf16.Encode([]rune(value))
	n := len(units)
	if n == 0 {
		return ""
	}
	if n <= 8 {
		return strings.Repeat("•", n)
	}
	middle := min(max(4, n-7), 12)
	return string(utf16.Decode(units[:3])) + strings.Repeat("•", middle) + string(utf16.Decode(units[n-4:]))
}

var ResearchProviders = []ProviderOption{
	{"disabled", "Disabled", "Skip industry research"},
	{"perplexity", "Perplexity", "Perplexity chat completions"},
}

func IsResearchProvider(provider string) bool {
	return provider == "disabled" || provider == "perplexity"
}

func ResearchFields(provider string) []Field {
	if provider == "perplexity" {
		return []Field{{Key: "apiKey", Label: "API key", Kind: "secret", Required: yes, Placeholder: "pplx-..."}}
	}
	return []Field{}
}

// FormErrors is validateConfig() in apps/web/src/lib/provider-settings.ts.
func FormErrors(scope, provider, model string, enabled bool, config map[string]any, secrets map[string]string) []string {
	if scope == "research" && (!enabled || provider == "disabled") {
		return nil
	}
	if model == "" {
		return []string{"Model is required."}
	}
	fields := ResearchFields(provider)
	if scope == "primary" {
		fields = Fields(provider)
	}
	var errs []string
	for _, f := range fields {
		value := configString(config, f.Key)
		if f.Kind == "secret" {
			value = secrets[f.Key]
		}
		if f.IsRequired() && value == "" {
			errs = append(errs, f.Label+" is required.")
		}
	}
	if len(errs) == 0 && scope == "primary" {
		if _, err := ResolveRuntime(provider, model, config); err != nil {
			errs = append(errs, err.Error())
		}
	}
	return errs
}
