// Package pricing calculates token costs for models whose prices Scriba knows.
package pricing

import "strings"

const (
	// StandardSpeedMultiplier prices tokens at the published standard tier.
	StandardSpeedMultiplier = 1.0
	// FastSpeedMultiplier is the fallback used when fast pricing has no
	// model-specific multiplier.
	FastSpeedMultiplier = 2.0
	// OpenAILongContextThreshold is the largest input that remains in the
	// short-context tier. A request enters the long tier only above this value.
	OpenAILongContextThreshold int64 = 272_000
)

// Rates contains per-token prices in USD. CacheWrite is retained as pricing
// metadata even though Codex currently reports no cache-write token bucket.
type Rates struct {
	Input       float64
	CachedInput float64
	Output      float64
	CacheWrite  float64
}

// ModelPricing describes the standard short- and long-context rates for a
// model. LongContextThreshold applies to total input, including cached input.
type ModelPricing struct {
	Model                string
	Short                Rates
	Long                 Rates
	LongContextThreshold int64
}

// Usage is one request's Codex token usage. InputTokens includes cached input;
// ReasoningOutputTokens is informational because it is already included in
// OutputTokens and must not be billed a second time.
type Usage struct {
	InputTokens           int64
	CachedInputTokens     int64
	OutputTokens          int64
	ReasoningOutputTokens int64
}

var models = map[string]ModelPricing{
	"gpt-5.6-sol": {
		Model:                "gpt-5.6-sol",
		Short:                Rates{Input: 5e-6, CachedInput: 0.5e-6, Output: 30e-6, CacheWrite: 6.25e-6},
		Long:                 Rates{Input: 10e-6, CachedInput: 1e-6, Output: 45e-6, CacheWrite: 12.5e-6},
		LongContextThreshold: OpenAILongContextThreshold,
	},
	"gpt-5.6-terra": {
		Model:                "gpt-5.6-terra",
		Short:                Rates{Input: 2.5e-6, CachedInput: 0.25e-6, Output: 15e-6, CacheWrite: 3.125e-6},
		Long:                 Rates{Input: 5e-6, CachedInput: 0.5e-6, Output: 22.5e-6, CacheWrite: 6.25e-6},
		LongContextThreshold: OpenAILongContextThreshold,
	},
	"gpt-5.6-luna": {
		Model:                "gpt-5.6-luna",
		Short:                Rates{Input: 1e-6, CachedInput: 0.1e-6, Output: 6e-6, CacheWrite: 1.25e-6},
		Long:                 Rates{Input: 2e-6, CachedInput: 0.2e-6, Output: 9e-6, CacheWrite: 2.5e-6},
		LongContextThreshold: OpenAILongContextThreshold,
	},
	"gpt-5.5": {
		Model:                "gpt-5.5",
		Short:                Rates{Input: 5e-6, CachedInput: 0.5e-6, Output: 30e-6, CacheWrite: 5e-6},
		Long:                 Rates{Input: 10e-6, CachedInput: 1e-6, Output: 45e-6, CacheWrite: 10e-6},
		LongContextThreshold: OpenAILongContextThreshold,
	},
	"gpt-5.4": {
		Model:                "gpt-5.4",
		Short:                Rates{Input: 2.5e-6, CachedInput: 0.25e-6, Output: 15e-6, CacheWrite: 2.5e-6},
		Long:                 Rates{Input: 5e-6, CachedInput: 0.5e-6, Output: 22.5e-6, CacheWrite: 5e-6},
		LongContextThreshold: OpenAILongContextThreshold,
	},
}

// Lookup returns pricing only for an exact known model, after removing common
// transport decorations and a trailing release date. It deliberately does not
// guess a model from a family prefix.
func Lookup(model string) (ModelPricing, bool) {
	name := normalizeModel(model)
	value, ok := models[name]
	return value, ok
}

// Cost returns a request's USD cost and whether the model's pricing is known.
// speedMultiplier is normally StandardSpeedMultiplier or FastSpeedMultiplier.
func Cost(model string, usage Usage, speedMultiplier float64) (float64, bool) {
	modelPricing, ok := Lookup(model)
	if !ok {
		return 0, false
	}

	input := max(usage.InputTokens, 0)
	cached := min(max(usage.CachedInputTokens, 0), input)
	output := max(usage.OutputTokens, 0)
	rates := modelPricing.Short
	if input > modelPricing.LongContextThreshold {
		rates = modelPricing.Long
	}
	if speedMultiplier <= 0 {
		speedMultiplier = StandardSpeedMultiplier
	}

	nonCached := input - cached
	cost := float64(nonCached)*rates.Input +
		float64(cached)*rates.CachedInput +
		float64(output)*rates.Output
	return cost * speedMultiplier, true
}

func normalizeModel(model string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	for _, prefix := range []string{"openai/", "openai:"} {
		model = strings.TrimPrefix(model, prefix)
	}
	model = strings.TrimSuffix(model, "-latest")
	model = trimDateSuffix(model)
	return model
}

func trimDateSuffix(model string) string {
	if len(model) > 11 {
		suffix := model[len(model)-11:]
		if suffix[0] == '-' && allDigits(suffix[1:5]) && suffix[5] == '-' &&
			allDigits(suffix[6:8]) && suffix[8] == '-' && allDigits(suffix[9:]) {
			return model[:len(model)-11]
		}
	}
	if len(model) > 9 {
		suffix := model[len(model)-9:]
		if suffix[0] == '-' && allDigits(suffix[1:]) {
			return model[:len(model)-9]
		}
	}
	return model
}

func allDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}
