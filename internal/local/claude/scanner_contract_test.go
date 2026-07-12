package claude

import (
	"path/filepath"
	"testing"
)

func claudeContractRoot() string {
	return filepath.Join("..", "..", "..", "testdata", "contracts", "claude")
}

func TestContractCanonicalDuplicateAndCacheBuckets(t *testing.T) {
	events, stats, err := Scan([]string{claudeContractRoot()})
	if err != nil {
		t.Fatal(err)
	}
	// The directory corpus intentionally also contains malformed and API-error fixtures.
	if len(events) != 4 || stats.Duplicates != 1 || stats.InvalidLines != 3 {
		t.Fatalf("events=%d stats=%+v", len(events), stats)
	}
	var canonical = events[0]
	for _, event := range events {
		if event.UniqueKey == "m1:r1" {
			canonical = event
		}
	}
	if canonical.TotalTokens != 19 || canonical.CacheCreationTokens != 3 || canonical.CacheReadTokens != 4 || canonical.CachedInputTokens != 4 {
		t.Fatalf("event=%+v", canonical)
	}
}

func TestContractNumericBoundaryIsExactAndOverflowIsRejected(t *testing.T) {
	root := claudeContractRoot()
	events, stats, err := ParseFile(root, filepath.Join(root, "numeric-boundary.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].InputTokens != 9007199254740993 || events[0].TotalTokens != 9007199254740994 || stats.InvalidLines != 1 {
		t.Fatalf("events=%+v stats=%+v", events, stats)
	}
}

func TestContractMalformedTruncatedAndErrorRecord(t *testing.T) {
	root := claudeContractRoot()
	events, stats, err := ParseFile(root, filepath.Join(root, "malformed-truncated.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || stats.InvalidLines != 2 || events[0].Model != "unknown" {
		t.Fatalf("events=%+v stats=%+v", events, stats)
	}
	errorEvents, _, err := ParseFile(root, filepath.Join(root, "error.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(errorEvents) != 1 || errorEvents[0].TotalTokens != 6 {
		t.Fatalf("error events=%+v", errorEvents)
	}
	// Policy note: isApiErrorMessage currently does not suppress billed usage;
	// this fixture freezes that accounting behavior, not API success semantics.
}
