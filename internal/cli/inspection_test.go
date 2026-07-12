package cli

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agensfield/scriba/internal/server/store"
)

func TestInspectionDispatchRejectsPositionalsUnknownFlagsAndLimits(t *testing.T) {
	for _, test := range []struct {
		name string
		fn   func() error
	}{
		{"policy positional", func() error { return dispatchPolicy([]string{"explain", "extra"}) }},
		{"policy flag", func() error { return dispatchPolicy([]string{"explain", "--status", "pending"}) }},
		{"outbox positional", func() error { return dispatchOutbox([]string{"list", "extra"}) }},
		{"outbox flag", func() error { return dispatchOutbox([]string{"list", "--rule", "x"}) }},
		{"outbox limit", func() error { return dispatchOutbox([]string{"list", "--limit", "1001", "--state-path", "missing"}) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := test.fn(); err == nil {
				t.Fatal("unexpected success")
			}
		})
	}
}

func TestInspectionHelpAndSchemaPublishCommands(t *testing.T) {
	if !containsCommand(commands()["policy"], "explain") || !containsCommand(commands()["root"], "outbox") || !containsCommand(commands()["outbox"], "list") {
		t.Fatal("inspection commands missing from command schema")
	}
	for _, want := range []string{"scriba policy explain", "scriba outbox list"} {
		if !strings.Contains(groupHelp(strings.Fields(want)[1]), want) {
			t.Fatalf("help missing %q", want)
		}
	}
}

func TestInspectionPayloadSchemaVersionsAndJSONFields(t *testing.T) {
	policyData, err := json.Marshal(policyExplainPayload{SchemaVersion: policyExplainSchemaVersion, Evaluations: []policyEvaluationPayload{}})
	if err != nil || !strings.Contains(string(policyData), `"schemaVersion":"scriba.policy-explain.v1"`) || !strings.Contains(string(policyData), `"evaluations":[]`) {
		t.Fatalf("policy payload=%s err=%v", policyData, err)
	}
	outboxData, err := json.Marshal(outboxListPayload{SchemaVersion: outboxListSchemaVersion, Messages: []outboxMessagePayload{}})
	if err != nil || !strings.Contains(string(outboxData), `"schemaVersion":"scriba.outbox-list.v1"`) || !strings.Contains(string(outboxData), `"messages":[]`) {
		t.Fatalf("outbox payload=%s err=%v", outboxData, err)
	}
}

func TestRunOutboxListDoesNotMutateDatabaseBytesOrMtime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.sqlite")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`insert into notification_outbox(id,event_kind,source,event_id,target,payload_version,payload_json,status,attempts,available_at,lease_token,lease_expires_at,created_at,updated_at) values
('leased-cli','reset','test','event-cli','telegram:test',1,'{}','leased',3,'2026-07-12T01:00:00Z','lease-proof','2026-07-12T03:00:00Z','2026-07-12T01:00:00Z','2026-07-12T01:00:00Z')`)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	beforeBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	beforeInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := runOutboxList(options{statePath: path, status: "leased", target: "telegram:test", limit: 10}); err != nil {
		t.Fatal(err)
	}
	if err := runPolicyExplain(options{statePath: path, limit: 10}); err != nil {
		t.Fatal(err)
	}
	afterBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	afterInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(afterBytes) != string(beforeBytes) {
		t.Fatal("outbox list changed database bytes")
	}
	if !afterInfo.ModTime().Equal(beforeInfo.ModTime()) {
		t.Fatalf("outbox list changed mtime: %s -> %s", beforeInfo.ModTime(), afterInfo.ModTime())
	}
	if afterInfo.Size() != beforeInfo.Size() {
		t.Fatalf("outbox list changed size: %d -> %d", beforeInfo.Size(), afterInfo.Size())
	}
}

func TestInspectionRedactsOpenErrors(t *testing.T) {
	err := runOutboxList(options{statePath: "/Users/arda/private.sqlite", redact: true, limit: 1})
	if err == nil || strings.Contains(err.Error(), "/Users/arda") || !strings.Contains(err.Error(), "/Users/[redacted]") {
		t.Fatalf("error=%v", err)
	}
}

func TestOutboxDTOIncludesAttemptsStatusAndLease(t *testing.T) {
	expires := time.Date(2026, 7, 12, 3, 0, 0, 0, time.UTC)
	dto := outboxMessageDTO(store.OutboxMessage{ID: "x", Status: "leased", Attempts: 3, LeaseToken: "proof", LeaseExpiresAt: &expires, PayloadJSON: `{}`})
	if dto.Status != "leased" || dto.Attempts != 3 || dto.LeaseToken != "proof" || dto.LeaseExpiresAt != "2026-07-12T03:00:00Z" {
		t.Fatalf("dto=%#v", dto)
	}
}

func TestInspectionRedactionRemovesInternalIdentifiersAndPayloads(t *testing.T) {
	policyPayload := redactPolicyExplain(policyExplainPayload{Evaluations: []policyEvaluationPayload{{
		SubjectKey: "RateLimitResetCredit_secret", AccountRef: "acct-secret", ConfigHash: "hash-secret",
		State: json.RawMessage(`{"knownGrantIdentities":["credit-secret"]}`), Evaluation: json.RawMessage(`[{"subject":"secret"}]`),
	}}})
	policyJSON, err := json.Marshal(policyPayload)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"RateLimitResetCredit_secret", "acct-secret", "hash-secret", "credit-secret"} {
		if strings.Contains(string(policyJSON), secret) {
			t.Fatalf("policy redaction leaked %q: %s", secret, policyJSON)
		}
	}

	outboxPayload := redactOutboxList(outboxListPayload{Messages: []outboxMessagePayload{{
		ProfileRef: "profile-secret", AccountRef: "acct-secret", Target: "telegram:123", Payload: json.RawMessage(`{"creditId":"credit-secret"}`),
		LeaseToken: "lease-secret", ProviderMessageID: "message-secret", LastError: "/Users/arda/secret",
	}}})
	outboxJSON, err := json.Marshal(outboxPayload)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"profile-secret", "acct-secret", "telegram:123", "credit-secret", "lease-secret", "message-secret", "/Users/arda/secret"} {
		if strings.Contains(string(outboxJSON), secret) {
			t.Fatalf("outbox redaction leaked %q: %s", secret, outboxJSON)
		}
	}
}
