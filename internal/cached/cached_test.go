package cached

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/agensfield/scriba/internal/cache"
	"github.com/agensfield/scriba/internal/local"
	"github.com/agensfield/scriba/internal/model"
)

func TestScanCodexRepricesWhenCatalogFingerprintChanges(t *testing.T) {
	root := t.TempDir()
	session := filepath.Join(root, "session.jsonl")
	data := `{"type":"turn_context","payload":{"model":"gpt-6-astra"}}
{"type":"event_msg","timestamp":"2026-07-10T10:00:00Z","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":100,"cached_input_tokens":80,"output_tokens":10,"total_tokens":999}}}}
`
	if err := os.WriteFile(session, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}

	db, err := cache.Open(filepath.Join(t.TempDir(), "cache.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	fingerprint, err := local.FileFingerprint(session)
	if err != nil {
		t.Fatal(err)
	}
	stale := []model.LocalUsageEvent{{ProviderID: "codex", TotalTokens: 999}}
	if err := db.SaveFileEvents("codex-v4", session, fingerprint.Size, fingerprint.MtimeMs, stale, model.ScannerStats{Files: 1, Events: 1}); err != nil {
		t.Fatal(err)
	}

	events, _, err := ScanCodex(db, []string{root})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].TotalTokens != 110 || events[0].EffectiveTokens != 30 || events[0].Model != "gpt-6-astra" {
		t.Fatalf("events = %+v", events)
	}
	if events[0].CostUSD == nil || events[0].PricingState != "calculated" {
		t.Fatalf("Astra pricing was not refreshed: costUSD=%s pricingState=%q event=%+v", formatCost(events[0].CostUSD), events[0].PricingState, events[0])
	}
	const want = 0.00078
	got := *events[0].CostUSD
	// arm64 may fuse the final multiply-add, moving the IEEE-754 result by one ULP.
	if !withinOneULP(got, want) {
		t.Fatalf("Astra pricing was not refreshed: costUSD=%s want=%.18g (bits=0x%016x) event=%+v", formatCost(events[0].CostUSD), want, math.Float64bits(want), events[0])
	}
}

func formatCost(cost *float64) string {
	if cost == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%.18g (bits=0x%016x)", *cost, math.Float64bits(*cost))
}

func withinOneULP(got, want float64) bool {
	if math.IsNaN(got) || math.IsNaN(want) {
		return false
	}
	if got == want {
		return true
	}
	return got == math.Nextafter(want, math.Inf(1)) || got == math.Nextafter(want, math.Inf(-1))
}

func TestScanClaudeIgnoresPreviousParserCacheVersion(t *testing.T) {
	root := t.TempDir()
	session := filepath.Join(root, "session.jsonl")
	data := `{"timestamp":"2026-07-10T10:00:00Z","sessionId":"fresh","requestId":"r1","message":{"id":"m1","model":"claude-sonnet-4","usage":{"input_tokens":10,"output_tokens":2}}}` + "\n"
	if err := os.WriteFile(session, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := cache.Open(filepath.Join(t.TempDir(), "cache.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	fingerprint, err := local.FileFingerprint(session)
	if err != nil {
		t.Fatal(err)
	}
	stale := []model.LocalUsageEvent{{ProviderID: "claude", SessionID: "stale", TotalTokens: 999}}
	if err := db.SaveFileEvents("claude", session, fingerprint.Size, fingerprint.MtimeMs, stale, model.ScannerStats{Files: 1, Events: 1}); err != nil {
		t.Fatal(err)
	}
	events, _, err := ScanClaude(db, []string{root})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].SessionID != "fresh" || events[0].TotalTokens != 12 {
		t.Fatalf("events = %+v", events)
	}
}
