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
	// TODO(contract): a reset snapshot should be treated as fresh usage. Current
	// subtraction clamps it to zero and drops the event, so freezing that output
	// would turn a known undercount into a compatibility promise.
	t.Skip("known defect: cumulative counter reset is dropped")
}

func TestContractNumericAndLongContextBoundaries(t *testing.T) {
	tests := []struct {
		name  string
		input int64
	}{
		{"numeric-boundary.jsonl", 9007199254740991},
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
