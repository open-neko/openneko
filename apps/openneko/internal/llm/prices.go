package llm

import (
	"math"
	"strings"
	"time"

	"github.com/open-neko/openneko/apps/ax-harness/catalog"
	"github.com/open-neko/openneko/apps/ax-harness/inference"
)

const PriceOverridesVersion = "overrides-2026-10"

// PricesVersion is MODEL_PRICES_VERSION in packages/llm/src/spend/model-prices.ts.
func PricesVersion() string {
	return "ax-go@" + catalog.Version() + "+" + PriceOverridesVersion
}

type override struct {
	provider string
	models   []string
	from     time.Time
	rates    catalog.Rates
}

func flash(input, output, cacheRead float64) catalog.Rates {
	m := func(usd float64) int64 { return int64(math.Round(usd * 1_000_000)) }
	r := catalog.Rates{Prompt: m(input), Completion: m(output), CacheRead: m(cacheRead)}
	r.CacheWrite = r.Prompt
	r.LongPrompt, r.LongCompletion, r.LongCacheRead, r.LongCacheWrite = r.Prompt, r.Completion, r.CacheRead, r.CacheWrite
	return r
}

var overrides = []override{
	{"google-gemini", []string{"gemini-3.7-flash", "gemini-3.8-flash"}, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), flash(0.75, 3.75, 0.075)},
	{"google-gemini", []string{"gemini-3.7-flash", "gemini-3.8-flash"}, time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC), flash(1.5, 7.5, 0.15)},
}

func catalogProviders(provider string) []string {
	switch provider = strings.ToLower(provider); provider {
	case "openai-compatible", "azure-openai":
		return []string{"openai"}
	case "googlegeminiai":
		return []string{"google-gemini"}
	case "vertex-ai":
		return []string{"google-gemini", "anthropic"}
	}
	return []string{provider}
}

func overrideFor(provider, model string, at time.Time) (catalog.Rates, bool) {
	var found *catalog.Rates
	for _, name := range catalogProviders(provider) {
		for i := range overrides {
			o := &overrides[i]
			if o.provider != name || at.Before(o.from) {
				continue
			}
			for _, m := range o.models {
				if m == model {
					found = &o.rates
				}
			}
		}
	}
	if found == nil {
		return catalog.Rates{}, false
	}
	return *found, true
}

// TokenCostUSD is estimateTokenCost(): the override, then the Ax catalog.
func TokenCostUSD(provider, model string, u Usage, at time.Time) (float64, string, bool) {
	rates, ok := overrideFor(provider, model, at)
	source := "price_override"
	if !ok {
		if rates, ok = catalog.Lookup(provider, model); !ok {
			return 0, "", false
		}
		source = "ax_model_catalog"
	}
	prompt, completion, read, write := rates.Prompt, rates.Completion, rates.CacheRead, rates.CacheWrite
	if rates.LongContextThreshold > 0 && u.Input+u.CacheRead+u.CacheWrite > rates.LongContextThreshold {
		prompt, completion, read, write = rates.LongPrompt, rates.LongCompletion, rates.LongCacheRead, rates.LongCacheWrite
	}
	extra := max(0, u.Total-u.Input-u.Output-u.CacheRead-u.CacheWrite)
	micros := float64(u.Input)*float64(prompt) + float64(u.CacheRead)*float64(read) +
		float64(u.CacheWrite)*float64(write) + float64(u.Output+extra)*float64(completion)
	return micros / 1e12, source, true
}

// RecordCostUSD is estimateRecordCost(): cache tokens bill as input.
func RecordCostUSD(p Price, u Usage) float64 {
	input := u.Input + u.CacheRead + u.CacheWrite
	output := max(u.Output, u.Total-input)
	return (float64(input)*p.InputUSDPer1M + float64(output)*p.OutputUSDPer1M) / 1_000_000
}

// PriceUsage is gatewayUsage() then priceHostUsage(): the model record's
// price wins, then the catalog; a model with neither stays unpriced.
func PriceUsage(raw inference.Usage, price *Price, route inference.Route, at time.Time) Usage {
	if raw.Coverage == "unavailable" || raw.Reported == 0 {
		return Usage{Coverage: "unavailable", CostStatus: "unknown"}
	}
	u := Usage{Coverage: raw.Coverage, Input: raw.InputTokens, Output: raw.OutputTokens, Total: raw.TotalTokens, CacheRead: raw.CacheReadTokens, CacheWrite: raw.CacheWriteTokens}
	var usd float64
	if price != nil {
		usd, u.CostSource, u.PricingVersion = RecordCostUSD(*price, u), "model_record", "model_record"
	} else {
		var ok bool
		if usd, u.CostSource, ok = TokenCostUSD(route.ProviderName(), route.Model, u, at); !ok {
			u.CostStatus = "unknown"
			return u
		}
		u.PricingVersion = PricesVersion()
	}
	u.CostUSD = &usd
	u.CostStatus = "estimated"
	if usd == 0 {
		u.CostStatus = "included"
	}
	return u
}
