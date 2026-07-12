package contracttest

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/agensfield/scriba/internal/pricing"
)

func TestDifferentialReceiptAndCanonicalExpectedProjection(t *testing.T) {
	root := filepath.Join("..", "..", "testdata", "contracts", "differential")
	receiptData, err := os.ReadFile(filepath.Join(root, "codex-gpt-5.6.receipt.json"))
	if err != nil {
		t.Fatal(err)
	}
	var receipt struct {
		SchemaVersion int    `json:"schema_version"`
		Timezone      string `json:"timezone"`
		Tools         []struct {
			Name       string   `json:"name"`
			Repository string   `json:"repository"`
			Commit     string   `json:"commit"`
			Version    string   `json:"version"`
			Args       []string `json:"args"`
		} `json:"tools"`
		Expected string `json:"scriba_expected"`
		Policies []struct {
			Peer     string `json:"peer"`
			Decision string `json:"decision"`
			Reason   string `json:"reason"`
		} `json:"policies"`
	}
	if err := json.Unmarshal(receiptData, &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.SchemaVersion != 1 || receipt.Timezone != "UTC" || len(receipt.Tools) != 2 || len(receipt.Policies) != 2 {
		t.Fatalf("incomplete differential receipt: %+v", receipt)
	}
	for _, tool := range receipt.Tools {
		if tool.Name == "" || tool.Repository == "" || tool.Commit == "" || tool.Version == "" || len(tool.Args) == 0 {
			t.Fatalf("incomplete tool metadata: %+v", tool)
		}
	}
	for _, policy := range receipt.Policies {
		if policy.Peer == "" || policy.Decision == "" || policy.Reason == "" {
			t.Fatalf("incomplete policy: %+v", policy)
		}
	}

	expectedData, err := os.ReadFile(filepath.Join(root, receipt.Expected))
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(expectedData))
	decoder.UseNumber()
	var expected any
	if err := decoder.Decode(&expected); err != nil {
		t.Fatal(err)
	}
	canonical, err := CanonicalJSON(expected)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(expectedData, canonical) {
		t.Fatalf("expected projection is not canonical JSON\nwant: %s\ngot:  %s", canonical, expectedData)
	}
	var projection struct {
		Cases []struct {
			Model        string `json:"model"`
			InputTokens  int64  `json:"input_tokens"`
			OutputTokens int64  `json:"output_tokens"`
			RateTier     string `json:"rate_tier"`
			CostUSD      string `json:"cost_usd"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(expectedData, &projection); err != nil {
		t.Fatal(err)
	}
	if len(projection.Cases) != 9 {
		t.Fatalf("differential cases = %d, want 9", len(projection.Cases))
	}
	for _, c := range projection.Cases {
		modelPricing, ok := pricing.Lookup(c.Model)
		if !ok {
			t.Fatalf("missing pricing for %s", c.Model)
		}
		wantTier := "short"
		if c.InputTokens > modelPricing.LongContextThreshold {
			wantTier = "long"
		}
		if c.RateTier != wantTier {
			t.Fatalf("%s input %d tier=%s want=%s", c.Model, c.InputTokens, c.RateTier, wantTier)
		}
		want, err := strconv.ParseFloat(c.CostUSD, 64)
		if err != nil {
			t.Fatal(err)
		}
		got, ok := pricing.Cost(c.Model, pricing.Usage{InputTokens: c.InputTokens, OutputTokens: c.OutputTokens}, pricing.StandardSpeedMultiplier)
		if !ok || math.Abs(got-want) > 1e-12 {
			t.Fatalf("%s input %d cost=%0.12f want=%s", c.Model, c.InputTokens, got, c.CostUSD)
		}
	}
}
