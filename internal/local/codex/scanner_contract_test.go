package codex

import (
	"path/filepath"
	"testing"
)

func contractFixture(name string) string {
	return filepath.Join("..", "..", "..", "testdata", "contracts", "codex", name)
}

func TestContractCumulativeMalformedAndTruncated(t *testing.T) {
	events, stats, err := ParseFile(filepath.Dir(contractFixture("cumulative-malformed-truncated.jsonl")), contractFixture("cumulative-malformed-truncated.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || stats.Lines != 5 || stats.InvalidLines != 0 {
		t.Fatalf("events=%d stats=%+v", len(events), stats)
	}
	// Non-candidate malformed lines are intentionally ignored by the scanner's
	// cheap token_count/turn_context prefilter and do not increment InvalidLines.
	if events[0].TotalTokens != 13 || events[1].TotalTokens != 7 || events[1].CachedInputTokens != 1 || events[1].ReasoningOutputTokens != 1 {
		t.Fatalf("events=%+v", events)
	}
}

func TestContractLastUsageModelConflictAndCompactionMarker(t *testing.T) {
	events, stats, err := ParseFile(filepath.Dir(contractFixture("last-model-switch.jsonl")), contractFixture("last-model-switch.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || stats.InvalidLines != 0 {
		t.Fatalf("events=%d stats=%+v", len(events), stats)
	}
	if events[0].Model != "gpt-5.5" || events[1].Model != "gpt-5.4" {
		t.Fatalf("model precedence changed: %+v", events)
	}
}

func TestContractCounterReset(t *testing.T) {
	events, stats, err := ParseFile(filepath.Dir(contractFixture("counter-reset.jsonl")), contractFixture("counter-reset.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || stats.Events != 2 {
		t.Fatalf("events=%d stats=%+v", len(events), stats)
	}
	if events[0].InputTokens != 100 || events[0].OutputTokens != 20 || events[0].TotalTokens != 120 {
		t.Fatalf("first event=%+v", events[0])
	}
	if events[1].InputTokens != 5 || events[1].CachedInputTokens != 0 || events[1].OutputTokens != 2 || events[1].ReasoningOutputTokens != 0 || events[1].TotalTokens != 7 {
		t.Fatalf("reset event=%+v", events[1])
	}
}

func TestContractNumericAndLongContextBoundaries(t *testing.T) {
	tests := []struct {
		name  string
		input int64
	}{
		{"numeric-boundary.jsonl", 9007199254740991},
		{"numeric-exact-2p53-plus-1.jsonl", 9007199254740993},
		{"long-context-271999.jsonl", 271999},
		{"long-context-272000.jsonl", 272000},
		{"long-context-272001.jsonl", 272001},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			events, _, err := ParseFile(filepath.Dir(contractFixture(tt.name)), contractFixture(tt.name))
			if err != nil {
				t.Fatal(err)
			}
			if len(events) != 1 || events[0].InputTokens != tt.input || events[0].TotalTokens != tt.input+1 {
				t.Fatalf("events=%+v", events)
			}
		})
	}
}

func TestContractRejectsFractionalAndOverflowTokenCounts(t *testing.T) {
	events, stats, err := ParseFile(filepath.Dir(contractFixture("numeric-invalid.jsonl")), contractFixture("numeric-invalid.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 || stats.InvalidLines != 2 {
		t.Fatalf("events=%+v stats=%+v", events, stats)
	}
}

func TestContractModelPrecedenceIsDeterministic(t *testing.T) {
	for i := 0; i < 100; i++ {
		events, _, err := ParseFile(filepath.Dir(contractFixture("model-conflict.jsonl")), contractFixture("model-conflict.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		if len(events) != 1 || events[0].Model != "gpt-5.4" {
			t.Fatalf("iteration %d events=%+v", i, events)
		}
	}
}
