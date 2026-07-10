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
	if events[0].TotalTokens != 14 || events[1].TotalTokens != 10 {
		t.Fatalf("totals = %d, %d; want 14, 10", events[0].TotalTokens, events[1].TotalTokens)
	}
	if events[1].InputTokens != 5 || events[1].OutputTokens != 5 || events[1].CachedInputTokens != 1 || events[1].ReasoningOutputTokens != 1 {
		t.Fatalf("delta event = %+v", events[1])
	}
	if events[0].Model != "gpt-5.4" || events[0].SessionID != "2026/05/16/session" {
		t.Fatalf("metadata = %+v", events[0])
	}
	if events[0].UncachedInputTokens != 8 || events[0].EffectiveTokens != 12 || events[1].UncachedInputTokens != 4 || events[1].EffectiveTokens != 9 {
		t.Fatalf("effective usage = %+v, %+v", events[0], events[1])
	}
}

func TestParseFileUsesLastUsageAndTracksModelChanges(t *testing.T) {
	root := t.TempDir()
	session := filepath.Join(root, "session.jsonl")
	data := `{"type":"turn_context","payload":{"model":"gpt-5.5"}}
{"type":"event_msg","timestamp":"2026-07-09T20:00:00Z","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":300000,"cached_input_tokens":280000,"output_tokens":1000,"reasoning_output_tokens":200,"total_tokens":999999},"total_token_usage":{"input_tokens":300000,"cached_input_tokens":280000,"output_tokens":1000,"total_tokens":301000}}}}
{"type":"turn_context","payload":{"model":"gpt-5.6-sol"}}
{"type":"event_msg","timestamp":"2026-07-09T20:01:00Z","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":100,"cached_input_tokens":120,"output_tokens":10,"reasoning_output_tokens":20,"total_tokens":999999},"total_token_usage":{"input_tokens":300100,"cached_input_tokens":280100,"output_tokens":1010,"total_tokens":301110}}}}
`
	if err := os.WriteFile(session, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}

	events, _, err := ParseFile(root, session)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2", len(events))
	}
	if events[0].Model != "gpt-5.5" || events[1].Model != "gpt-5.6-sol" {
		t.Fatalf("models = %q, %q", events[0].Model, events[1].Model)
	}
	if events[0].TotalTokens != 301000 || events[0].EffectiveTokens != 21000 {
		t.Fatalf("first usage = %+v", events[0])
	}
	if events[1].CachedInputTokens != 100 || events[1].ReasoningOutputTokens != 10 || events[1].TotalTokens != 110 || events[1].EffectiveTokens != 10 {
		t.Fatalf("clamped usage = %+v", events[1])
	}
	if events[0].CostUSD == nil || events[1].CostUSD == nil {
		t.Fatalf("expected known model costs: %+v, %+v", events[0].CostUSD, events[1].CostUSD)
	}
}
