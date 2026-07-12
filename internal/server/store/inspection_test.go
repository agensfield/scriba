package store

import (
	"context"
	"testing"
	"time"
)

func TestListPolicyEvaluationsFiltersOrdersAndBounds(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if _, err := s.db.Exec(`insert into accounts(account_ref,provider_id,label,email,plan,updated_at) values
('acct-a','codex','A','','pro','2026-07-12T00:00:00Z'),
('acct-b','other','B','','pro','2026-07-12T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	insert := `insert into policy_states(rule_id,subject_key,rule_kind,provider_id,account_ref,policy_revision,config_hash,state_json,evaluation_json,observed_at,created_at,updated_at) values(?,?,?,?,?,?,?,?,?,?,?,?)`
	for _, row := range [][]any{
		{"rule.b", "subject", "remaining_checkpoint", "codex", "acct-a", "rev", "hash", `{"b":2}`, `[{"reason":"new"}]`, "2026-07-12T02:00:00Z", "2026-07-12T02:00:00Z", "2026-07-12T02:00:00Z"},
		{"rule.a", "subject", "reset_transition", "codex", "acct-a", "rev", "hash", `{"a":1}`, `[{"reason":"old"}]`, "2026-07-12T01:00:00Z", "2026-07-12T01:00:00Z", "2026-07-12T01:00:00Z"},
		{"rule.a", "subject", "reset_transition", "other", "acct-b", "rev", "hash", `{}`, `[]`, "2026-07-12T03:00:00Z", "2026-07-12T03:00:00Z", "2026-07-12T03:00:00Z"},
	} {
		if _, err := s.db.Exec(insert, row...); err != nil {
			t.Fatal(err)
		}
	}

	got, err := s.ListPolicyEvaluations(ctx, PolicyEvaluationFilter{ProviderID: "codex", AccountRef: "acct-a", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].RuleID != "rule.b" || got[1].RuleID != "rule.a" || got[0].StateJSON != `{"b":2}` || got[0].EvaluationJSON != `[{"reason":"new"}]` {
		t.Fatalf("unexpected evaluations: %#v", got)
	}
	got, err = s.ListPolicyEvaluations(ctx, PolicyEvaluationFilter{RuleID: "rule.a", Limit: 1})
	if err != nil || len(got) != 1 || got[0].ProviderID != "other" {
		t.Fatalf("rule filter: %#v err=%v", got, err)
	}
	for _, limit := range []int{0, InspectionMaxLimit + 1} {
		if _, err := s.ListPolicyEvaluations(ctx, PolicyEvaluationFilter{Limit: limit}); err == nil {
			t.Fatalf("limit %d accepted", limit)
		}
	}
}

func TestListOutboxFiltersWithoutClaimingOrMutating(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 12, 4, 0, 0, 0, time.UTC)
	_, err := s.db.Exec(`insert into notification_outbox(id,event_kind,source,event_id,target,payload_version,payload_json,status,attempts,available_at,lease_token,lease_expires_at,created_at,updated_at) values
('pending-a','reset','test','e1','telegram:a',1,'{}','pending',0,?,null,null,?,?),
('leased-b','reset','test','e2','telegram:b',1,'{}','leased',3,?,'token-b',?,?,?)`, formatTime(now), formatTime(now), formatTime(now), formatTime(now.Add(-time.Hour)), formatTime(now.Add(time.Hour)), formatTime(now.Add(-time.Hour)), formatTime(now.Add(-time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	var before string
	if err := s.db.QueryRow(`select group_concat(id||':'||status||':'||attempts||':'||coalesce(lease_token,'')||':'||coalesce(lease_expires_at,'')||':'||updated_at,'|') from notification_outbox order by id`).Scan(&before); err != nil {
		t.Fatal(err)
	}

	got, err := s.ListOutbox(ctx, OutboxFilter{Status: "leased", Target: "telegram:b", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "leased-b" || got[0].Attempts != 3 || got[0].LeaseToken != "token-b" {
		t.Fatalf("unexpected outbox: %#v", got)
	}
	got, err = s.ListOutbox(ctx, OutboxFilter{ID: "pending-a", Limit: 1})
	if err != nil || len(got) != 1 || got[0].Status != "pending" {
		t.Fatalf("id filter: %#v err=%v", got, err)
	}
	var after string
	if err := s.db.QueryRow(`select group_concat(id||':'||status||':'||attempts||':'||coalesce(lease_token,'')||':'||coalesce(lease_expires_at,'')||':'||updated_at,'|') from notification_outbox order by id`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("inspection mutated outbox\nbefore=%s\nafter=%s", before, after)
	}
	for _, limit := range []int{-1, InspectionMaxLimit + 1} {
		if _, err := s.ListOutbox(ctx, OutboxFilter{Limit: limit}); err == nil {
			t.Fatalf("limit %d accepted", limit)
		}
	}
}
