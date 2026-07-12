// Package pricing calculates token costs from Scriba's reviewed offline catalog.
package pricing

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
)

const (
	StandardSpeedMultiplier          = 1.0
	FastSpeedMultiplier              = 2.0
	OpenAILongContextThreshold int64 = 272_000
)

type Rates struct{ Input, CachedInput, Output, CacheWrite float64 }
type ModelPricing struct {
	Model                string
	Short, Long          Rates
	LongContextThreshold int64
}
type Usage struct{ InputTokens, CachedInputTokens, OutputTokens, ReasoningOutputTokens int64 }

type decimalRates struct{ Input, CachedInput, Output, CacheWrite string }
type catalogModel struct {
	Model                string       `json:"model"`
	Aliases              []string     `json:"aliases"`
	LongContextThreshold int64        `json:"long_context_threshold"`
	Short                decimalRates `json:"short"`
	Long                 decimalRates `json:"long"`
}
type catalogFile struct {
	SchemaVersion       int            `json:"schema_version"`
	EffectiveDate       string         `json:"effective_date"`
	Sources             []string       `json:"sources"`
	SourceReceipt       string         `json:"source_receipt"`
	SourceReceiptSHA256 string         `json:"source_receipt_sha256"`
	Models              []catalogModel `json:"models"`
}

//go:embed catalog.json
var catalogJSON []byte

var models, aliases = mustLoadCatalog(catalogJSON)

func Lookup(model string) (ModelPricing, bool) {
	name := normalizeModel(model)
	if canonical, ok := aliases[name]; ok {
		name = canonical
	}
	value, ok := models[name]
	return value, ok
}

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
	if speedMultiplier <= 0 || math.IsNaN(speedMultiplier) || math.IsInf(speedMultiplier, 0) {
		speedMultiplier = StandardSpeedMultiplier
	}
	cost := (float64(input-cached)*rates.Input + float64(cached)*rates.CachedInput + float64(output)*rates.Output) * speedMultiplier
	return cost, true
}

// CheckCatalog validates a catalog and returns deterministic canonical JSON.
func CheckCatalog(data []byte) ([]byte, error) {
	var catalog catalogFile
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&catalog); err != nil {
		return nil, fmt.Errorf("decode catalog: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return nil, fmt.Errorf("decode catalog: trailing data")
	}
	if catalog.SchemaVersion != 1 || catalog.EffectiveDate == "" || len(catalog.Sources) == 0 || catalog.SourceReceipt == "" || len(catalog.SourceReceiptSHA256) != 64 || len(catalog.Models) == 0 {
		return nil, fmt.Errorf("missing catalog provenance or unsupported schema")
	}
	seen := map[string]bool{}
	for _, model := range catalog.Models {
		name := normalizeModel(model.Model)
		if name == "" || name != model.Model || seen[name] {
			return nil, fmt.Errorf("invalid model %q", model.Model)
		}
		seen[name] = true
	}
	for _, model := range catalog.Models {
		if model.LongContextThreshold <= 0 {
			return nil, fmt.Errorf("invalid model %q", model.Model)
		}
		for _, alias := range model.Aliases {
			alias = normalizeModel(alias)
			if alias == "" || seen[alias] {
				return nil, fmt.Errorf("duplicate/empty alias %q", alias)
			}
			seen[alias] = true
		}
		for _, rates := range []decimalRates{model.Short, model.Long} {
			for _, value := range []string{rates.Input, rates.CachedInput, rates.Output, rates.CacheWrite} {
				n, err := strconv.ParseFloat(value, 64)
				if err != nil || n < 0 || math.IsNaN(n) || math.IsInf(n, 0) {
					return nil, fmt.Errorf("invalid rate %q for %s", value, model.Model)
				}
			}
		}
	}
	canonical, err := json.MarshalIndent(catalog, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(canonical, '\n'), nil
}

func mustLoadCatalog(data []byte) (map[string]ModelPricing, map[string]string) {
	canonical, err := CheckCatalog(data)
	if err != nil {
		panic(err)
	}
	var catalog catalogFile
	if err := json.Unmarshal(canonical, &catalog); err != nil {
		panic(err)
	}
	result, aliasMap := map[string]ModelPricing{}, map[string]string{}
	for _, item := range catalog.Models {
		pricing := ModelPricing{Model: item.Model, Short: parseRates(item.Short), Long: parseRates(item.Long), LongContextThreshold: item.LongContextThreshold}
		result[item.Model] = pricing
		for _, alias := range item.Aliases {
			aliasMap[alias] = item.Model
		}
	}
	return result, aliasMap
}
func parseRates(r decimalRates) Rates {
	parse := func(s string) float64 { v, _ := strconv.ParseFloat(s, 64); return v }
	return Rates{parse(r.Input), parse(r.CachedInput), parse(r.Output), parse(r.CacheWrite)}
}
func normalizeModel(model string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	for _, p := range []string{"openai/", "openai:"} {
		model = strings.TrimPrefix(model, p)
	}
	model = strings.TrimSuffix(model, "-latest")
	return trimDateSuffix(model)
}
func trimDateSuffix(model string) string {
	if len(model) > 11 {
		s := model[len(model)-11:]
		if s[0] == '-' && allDigits(s[1:5]) && s[5] == '-' && allDigits(s[6:8]) && s[8] == '-' && allDigits(s[9:]) {
			return model[:len(model)-11]
		}
	}
	if len(model) > 9 {
		s := model[len(model)-9:]
		if s[0] == '-' && allDigits(s[1:]) {
			return model[:len(model)-9]
		}
	}
	return model
}
func allDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, c := range value {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
