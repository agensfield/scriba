package agentcontext

import (
	"errors"
	"testing"
)

func TestEventCursorStrictCanonicalParsing(t *testing.T) {
	for _, valid := range []string{"v1.0000000000000000", "v1.0000000000000001", "v1.7fffffffffffffff"} {
		seq, err := parseEventCursor(valid)
		if err != nil || formatEventCursor(seq) != valid {
			t.Fatalf("%q: seq=%d err=%v", valid, seq, err)
		}
	}
	for _, invalid := range []string{"", "v1.", "v2.0000000000000001", "v1.01", "v1.-000000000000001", "v1.000000000000000A", "v1.8000000000000000"} {
		_, err := parseEventCursor(invalid)
		var pageErr *EventPageError
		if !errors.As(err, &pageErr) || pageErr.ReasonCode != "invalid_cursor" {
			t.Fatalf("%q: %v", invalid, err)
		}
	}
}

func TestEventsRejectsImplicitModeAndUnboundedLimits(t *testing.T) {
	svc := New(Config{})
	for _, tc := range []EventPageRequest{{Limit: 1}, {Mode: "replay", Cursor: "v1.0000000000000000"}, {Mode: "replay", Cursor: "v1.0000000000000000", Limit: 101}} {
		_, err := svc.Events(t.Context(), tc)
		var pageErr *EventPageError
		if !errors.As(err, &pageErr) {
			t.Fatalf("request %+v: %v", tc, err)
		}
	}
}

func TestCursorExpiryUsesPerAccountPruneFloorAcrossGlobalSequenceGaps(t *testing.T) {
	// This account consumed sequence 7, another account owns 8 and 9, and its
	// next retained event is 10. Seven remains a valid cursor; six does not.
	if cursorExpired(7, 7) {
		t.Fatal("cursor at the account prune floor expired")
	}
	if !cursorExpired(6, 7) {
		t.Fatal("cursor before the account prune floor remained valid")
	}
	if cursorExpired(7, 7) {
		t.Fatal("cursor at a fully pruned account floor expired")
	}
	if cursorExpired(7, 0) {
		t.Fatal("global sequence gap without an account prune floor expired")
	}
}
