package agent

// TokenPrice is a trusted upper-bound price for one approved model route.
// InputMicrosPerMillion must cover the most expensive input treatment on that
// route, including cache writes; admission does not assume cache discounts.
type TokenPrice struct {
	InputMicrosPerMillion  int64 `json:"input_micros_per_million"`
	OutputMicrosPerMillion int64 `json:"output_micros_per_million"`
}

// CostSummary is the durable admission charge, in micros of the host's
// accounting currency. Missing provider usage retains its reservation.
type CostSummary struct {
	PricingVersion string `json:"pricing_version"`
	ChargedMicros  int64  `json:"charged_micros"`
	BudgetMicros   int64  `json:"budget_micros"`
}

const maxTokenPriceMicrosPerMillion int64 = 1_000_000_000

func (p TokenPrice) Valid() bool {
	return p.InputMicrosPerMillion > 0 && p.InputMicrosPerMillion <= maxTokenPriceMicrosPerMillion &&
		p.OutputMicrosPerMillion > 0 && p.OutputMicrosPerMillion <= maxTokenPriceMicrosPerMillion
}

func (p TokenPrice) Reservation(tokens int64) int64 {
	if tokens <= 0 || !p.Valid() {
		return 0
	}
	rate := p.InputMicrosPerMillion
	if p.OutputMicrosPerMillion > rate {
		rate = p.OutputMicrosPerMillion
	}
	return millionthCeil(tokens, rate)
}

// Observed conservatively prices all reported tokens. Tokens not explained by
// the provider's input/output split use the higher rate, never a zero charge.
func (p TokenPrice) Observed(input, output, total int64) int64 {
	if !p.Valid() || input < 0 || output < 0 || total < 0 {
		return 0
	}
	extra := total - input - output
	if extra < 0 {
		extra = 0
	}
	return millionthCeil(input, p.InputMicrosPerMillion) +
		millionthCeil(output, p.OutputMicrosPerMillion) + p.Reservation(extra)
}

// Prices are bounded at 1e9 micros per million and usage at 1e12 tokens, so
// the quotient/remainder products below fit in int64 without floating point.
func millionthCeil(tokens, rate int64) int64 {
	if tokens <= 0 || rate <= 0 {
		return 0
	}
	const million int64 = 1_000_000
	whole := tokens / million * rate
	remainder := tokens % million * rate
	return whole + (remainder+million-1)/million
}
