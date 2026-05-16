package claude

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestScanDeduplicatesUsageRecords(t *testing.T) {
	root := t.TempDir()
	session := filepath.Join(root, "project-one", "session-a.jsonl")
	if err := os.MkdirAll(filepath.Dir(session), 0o700); err != nil {
		t.Fatal(err)
	}
	line := `{"timestamp":"2026-05-16T10:00:00.000Z","sessionId":"session-a","requestId":"req-1","costUSD":0.42,"message":{"id":"msg-1","model":"claude-sonnet-4","usage":{"input_tokens":10,"output_tokens":20,"cache_creation_input_tokens":3,"cache_read_input_tokens":4}}}`
	if err := os.WriteFile(session, []byte(fmt.Sprintf("%s\n%s\n{bad-json\n", line, line)), 0o600); err != nil {
		t.Fatal(err)
	}

	events, stats, err := Scan([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	if stats.Events != 1 || stats.Duplicates != 1 || stats.InvalidLines != 1 {
		t.Fatalf("stats = %+v, want one event, duplicate, and invalid line", stats)
	}
	event := events[0]
	if event.TotalTokens != 37 || event.CachedInputTokens != 4 || event.Project != "project-one" {
		t.Fatalf("event = %+v", event)
	}
}
