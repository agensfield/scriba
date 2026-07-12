package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/agensfield/scriba/internal/resetwatch"
)

func TestOpenReadOnlyDoesNotCreateOrMutate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.db")
	if _, err := OpenReadOnly(path); err == nil {
		t.Fatal("OpenReadOnly created a missing database")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("missing database was created: %v", err)
	}

	s := openTestStore(t)
	before, err := os.Stat(s.path)
	if err != nil {
		t.Fatal(err)
	}
	ro, err := OpenReadOnly(s.path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ro.Close() })
	if _, err := ro.db.Exec(`create table should_not_exist (id integer)`); err == nil {
		t.Fatal("read-only store accepted a write")
	}
	after, err := os.Stat(s.path)
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() != before.Size() {
		t.Fatalf("read-only open changed database size: %d -> %d", before.Size(), after.Size())
	}
}

func TestLoadBudgetHistoryIsolatedAndChronological(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	used := 10.0
	period := int64((5 * time.Hour) / time.Millisecond)
	for _, tc := range []struct {
		provider, account string
		at                time.Time
		used              float64
	}{
		{"codex", "wanted", base.Add(2 * time.Hour), 30}, {"codex", "other", base.Add(time.Hour), 20}, {"claude", "wanted", base.Add(time.Hour), 20}, {"codex", "wanted", base.Add(time.Hour), 10},
	} {
		used = tc.used
		obs := resetwatch.Observation{ProviderID: tc.provider, Account: resetwatch.Account{Ref: tc.account}, ObservedAt: tc.at, Windows: []resetwatch.Window{{Label: "5h limit", UsedPercent: &used, ResetAt: base.Add(5 * time.Hour), PeriodDurationMs: &period}}}
		if _, err := s.ApplyDecision(ctx, obs, resetwatch.Decision{}); err != nil {
			t.Fatal(err)
		}
	}
	history, err := s.LoadBudgetHistory(ctx, "codex", "wanted", base.Add(30*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 {
		t.Fatalf("got %d observations", len(history))
	}
	if !history[0].ObservedAt.Equal(base.Add(time.Hour)) || !history[1].ObservedAt.Equal(base.Add(2*time.Hour)) {
		t.Fatalf("not chronological: %#v", history)
	}
	if *history[0].Windows[0].UsedPercent != 10 || *history[1].Windows[0].UsedPercent != 30 {
		t.Fatalf("history leaked across scope: %#v", history)
	}
}
