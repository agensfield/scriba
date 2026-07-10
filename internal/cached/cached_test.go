package cached

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/agensfield/scriba/internal/cache"
	"github.com/agensfield/scriba/internal/local"
	"github.com/agensfield/scriba/internal/model"
)

func TestScanCodexIgnoresPreviousParserCacheVersion(t *testing.T) {
	root := t.TempDir()
	session := filepath.Join(root, "session.jsonl")
	data := `{"type":"turn_context","payload":{"model":"gpt-5.6-luna"}}
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
	if err := db.SaveFileEvents("codex", session, fingerprint.Size, fingerprint.MtimeMs, stale, model.ScannerStats{Files: 1, Events: 1}); err != nil {
		t.Fatal(err)
	}

	events, _, err := ScanCodex(db, []string{root})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].TotalTokens != 110 || events[0].EffectiveTokens != 30 || events[0].Model != "gpt-5.6-luna" {
		t.Fatalf("events = %+v", events)
	}
}
