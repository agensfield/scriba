package pricing

import (
	"math"
	"testing"
)

func TestLookupPublishedRates(t *testing.T) {
	tests := []struct {
		model string
		short Rates
		long  Rates
	}{
		{"gpt-5.6-sol", Rates{5e-6, 0.5e-6, 30e-6, 6.25e-6}, Rates{10e-6, 1e-6, 45e-6, 12.5e-6}},
		{"gpt-5.6-terra", Rates{2.5e-6, 0.25e-6, 15e-6, 3.125e-6}, Rates{5e-6, 0.5e-6, 22.5e-6, 6.25e-6}},
		{"gpt-5.6-luna", Rates{1e-6, 0.1e-6, 6e-6, 1.25e-6}, Rates{2e-6, 0.2e-6, 9e-6, 2.5e-6}},
		{"gpt-5.5", Rates{5e-6, 0.5e-6, 30e-6, 0}, Rates{10e-6, 1e-6, 45e-6, 0}},
		{"gpt-5.4", Rates{2.5e-6, 0.25e-6, 15e-6, 0}, Rates{5e-6, 0.5e-6, 22.5e-6, 0}},
	}

	for _, test := range tests {
		t.Run(test.model, func(t *testing.T) {
			got, ok := Lookup(test.model)
			if !ok {
				t.Fatal("pricing missing")
			}
			if got.Model != test.model || got.Short != test.short || got.Long != test.long {
				t.Fatalf("unexpected pricing: %#v", got)
			}
			if got.LongContextThreshold != OpenAILongContextThreshold {
				t.Fatalf("threshold = %d", got.LongContextThreshold)
			}
		})
	}
}

func TestCostUsesWholeRequestTierAboveThreshold(t *testing.T) {
	usage := Usage{InputTokens: 272_000, CachedInputTokens: 72_000, OutputTokens: 1_000}
	short, ok := Cost("gpt-5.6-sol", usage, StandardSpeedMultiplier)
	if !ok {
		t.Fatal("pricing missing")
	}
	wantShort := 200_000*5e-6 + 72_000*0.5e-6 + 1_000*30e-6
	closeEnough(t, short, wantShort)

	usage.InputTokens++
	long, ok := Cost("gpt-5.6-sol", usage, StandardSpeedMultiplier)
	if !ok {
		t.Fatal("pricing missing")
	}
	wantLong := 200_001*10e-6 + 72_000*1e-6 + 1_000*45e-6
	closeEnough(t, long, wantLong)
}

func TestCostExactForEachPublishedModelAndTier(t *testing.T) {
	shortUsage := Usage{InputTokens: 100_000, CachedInputTokens: 20_000, OutputTokens: 2_000}
	longUsage := Usage{InputTokens: 300_000, CachedInputTokens: 20_000, OutputTokens: 2_000}
	tests := []struct {
		model     string
		wantShort float64
		wantLong  float64
	}{
		{
			"gpt-5.6-sol",
			80_000*5e-6 + 20_000*0.5e-6 + 2_000*30e-6,
			280_000*10e-6 + 20_000*1e-6 + 2_000*45e-6,
		},
		{
			"gpt-5.6-terra",
			80_000*2.5e-6 + 20_000*0.25e-6 + 2_000*15e-6,
			280_000*5e-6 + 20_000*0.5e-6 + 2_000*22.5e-6,
		},
		{
			"gpt-5.6-luna",
			80_000*1e-6 + 20_000*0.1e-6 + 2_000*6e-6,
			280_000*2e-6 + 20_000*0.2e-6 + 2_000*9e-6,
		},
		{
			"gpt-5.5",
			80_000*5e-6 + 20_000*0.5e-6 + 2_000*30e-6,
			280_000*10e-6 + 20_000*1e-6 + 2_000*45e-6,
		},
		{
			"gpt-5.4",
			80_000*2.5e-6 + 20_000*0.25e-6 + 2_000*15e-6,
			280_000*5e-6 + 20_000*0.5e-6 + 2_000*22.5e-6,
		},
	}
	for _, test := range tests {
		t.Run(test.model, func(t *testing.T) {
			gotShort, ok := Cost(test.model, shortUsage, StandardSpeedMultiplier)
			if !ok {
				t.Fatal("pricing missing")
			}
			closeEnough(t, gotShort, test.wantShort)

			gotLong, ok := Cost(test.model, longUsage, StandardSpeedMultiplier)
			if !ok {
				t.Fatal("pricing missing")
			}
			closeEnough(t, gotLong, test.wantLong)
		})
	}
}

func TestCostClampsCachedInputAndDoesNotRebillReasoning(t *testing.T) {
	usage := Usage{
		InputTokens:           100,
		CachedInputTokens:     150,
		OutputTokens:          10,
		ReasoningOutputTokens: 9,
	}
	got, ok := Cost("gpt-5.6-luna", usage, StandardSpeedMultiplier)
	if !ok {
		t.Fatal("pricing missing")
	}
	closeEnough(t, got, 100*0.1e-6+10*6e-6)
}

func TestCostAppliesSpeedMultiplier(t *testing.T) {
	usage := Usage{InputTokens: 100, OutputTokens: 10}
	standard, _ := Cost("gpt-5.6-terra", usage, StandardSpeedMultiplier)
	fast, _ := Cost("gpt-5.6-terra", usage, FastSpeedMultiplier)
	closeEnough(t, fast, standard*2)

	defaulted, _ := Cost("gpt-5.6-terra", usage, 0)
	closeEnough(t, defaulted, standard)
}

func TestLookupNormalizesOnlySafeDecorations(t *testing.T) {
	known := []string{
		" GPT-5.6-SOL ",
		"openai/gpt-5.6-sol",
		"openai:gpt-5.6-sol-latest",
		"gpt-5.6-sol-2026-07-10",
		"gpt-5.6-sol-20260710",
	}
	for _, model := range known {
		if got, ok := Lookup(model); !ok || got.Model != "gpt-5.6-sol" {
			t.Errorf("Lookup(%q) = %#v, %v", model, got, ok)
		}
	}

	unknown := []string{"gpt-5.6", "gpt-5.6-solar", "gpt-5.6-sol-codex", ""}
	for _, model := range unknown {
		if _, ok := Lookup(model); ok {
			t.Errorf("Lookup(%q) unexpectedly succeeded", model)
		}
		if cost, ok := Cost(model, Usage{InputTokens: 1}, 1); ok || cost != 0 {
			t.Errorf("Cost(%q) = %v, %v", model, cost, ok)
		}
	}
}

func TestCostClampsNegativeTokenCounts(t *testing.T) {
	got, ok := Cost("gpt-5.4", Usage{InputTokens: -1, CachedInputTokens: 10, OutputTokens: -2}, 1)
	if !ok || got != 0 {
		t.Fatalf("Cost() = %v, %v", got, ok)
	}
}

func TestGPT56BoundaryGoldensEveryCacheAndSpeedMix(t *testing.T) {
	models := []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna"}
	for _, model := range models {
		pricing, _ := Lookup(model)
		for _, input := range []int64{271_999, 272_000, 272_001} {
			for _, cached := range []int64{0, input / 2, input} {
				for _, speed := range []float64{StandardSpeedMultiplier, FastSpeedMultiplier} {
					rates := pricing.Short
					if input > 272_000 {
						rates = pricing.Long
					}
					want := (float64(input-cached)*rates.Input + float64(cached)*rates.CachedInput + 17*rates.Output) * speed
					got, ok := Cost(model, Usage{InputTokens: input, CachedInputTokens: cached, OutputTokens: 17}, speed)
					if !ok {
						t.Fatalf("missing %s", model)
					}
					closeEnough(t, got, want)
				}
			}
		}
	}
}

func TestCostProperties(t *testing.T) {
	for _, model := range []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna"} {
		previous := 0.0
		for input := int64(0); input < 400_000; input += 997 {
			got, _ := Cost(model, Usage{InputTokens: input, CachedInputTokens: input / 3, OutputTokens: 41}, 1)
			if got < 0 || math.IsNaN(got) || math.IsInf(got, 0) || got < previous {
				t.Fatalf("invalid/nonmonotonic cost %s input=%d: %g after %g", model, input, got, previous)
			}
			previous = got
			clamped, _ := Cost(model, Usage{InputTokens: input, CachedInputTokens: input + 1, OutputTokens: 41}, 1)
			allCached, _ := Cost(model, Usage{InputTokens: input, CachedInputTokens: input, OutputTokens: 41}, 1)
			closeEnough(t, clamped, allCached)
		}
	}
}

func FuzzCost(f *testing.F) {
	f.Add("gpt-5.6-sol", int64(272_000), int64(12), int64(3), float64(1))
	f.Add("gpt-5.6-luna", int64(-1), int64(99), int64(-4), float64(2))
	f.Fuzz(func(t *testing.T, model string, input, cached, output int64, speed float64) {
		got, ok := Cost(model, Usage{InputTokens: input, CachedInputTokens: cached, OutputTokens: output}, speed)
		if !ok {
			if got != 0 {
				t.Fatalf("unknown model cost %g", got)
			}
			return
		}
		if got < 0 || math.IsNaN(got) || math.IsInf(got, 0) {
			t.Fatalf("invalid cost %g", got)
		}
	})
}

func closeEnough(t *testing.T, got, want float64) {
	t.Helper()
	if difference := got - want; difference < -1e-12 || difference > 1e-12 {
		t.Fatalf("cost = %.12f, want %.12f", got, want)
	}
}
