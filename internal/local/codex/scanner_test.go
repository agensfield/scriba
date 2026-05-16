package codex

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseFileUsesTotalUsageDeltas(t *testing.T) {
	root := t.TempDir()
	session := filepath.Join(root, "2026", "05", "16", "session.jsonl")
	if err := os.MkdirAll(filepath.Dir(session), 0o700); err != nil {
		t.Fatal(err)
	}
	data := `{"type":"turn_context","payload":{"model":"gpt-5.4"}}
{"type":"event_msg","timestamp":"2026-05-16T10:00:00.000Z","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":10,"cached_input_tokens":2,"output_tokens":4,"reasoning_output_tokens":1,"total_tokens":16}}}}
{"type":"event_msg","timestamp":"2026-05-16T10:10:00.000Z","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":15,"cached_input_tokens":3,"output_tokens":9,"reasoning_output_tokens":2,"total_tokens":27}}}}
`
	if err := os.WriteFile(session, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}

	events, stats, err := ParseFile(root, session)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || stats.Events != 2 {
		t.Fatalf("events = %d stats = %+v, want two events", len(events), stats)
	}
	if events[0].TotalTokens != 16 || events[1].TotalTokens != 11 {
		t.Fatalf("totals = %d, %d; want 16, 11", events[0].TotalTokens, events[1].TotalTokens)
	}
	if events[1].InputTokens != 5 || events[1].OutputTokens != 5 || events[1].CachedInputTokens != 1 || events[1].ReasoningOutputTokens != 1 {
		t.Fatalf("delta event = %+v", events[1])
	}
	if events[0].Model != "gpt-5.4" || events[0].SessionID != "2026/05/16/session" {
		t.Fatalf("metadata = %+v", events[0])
	}
}
