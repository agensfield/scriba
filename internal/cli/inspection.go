package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/agensfield/scriba/internal/privacy"
	"github.com/agensfield/scriba/internal/server/store"
)

const (
	policyExplainSchemaVersion = "scriba.policy-explain.v1"
	outboxListSchemaVersion    = "scriba.outbox-list.v1"
)

type policyExplainPayload struct {
	SchemaVersion string                    `json:"schemaVersion"`
	Environment   string                    `json:"environment"`
	Evaluations   []policyEvaluationPayload `json:"evaluations"`
}

type policyEvaluationPayload struct {
	RuleID         string          `json:"ruleId"`
	SubjectKey     string          `json:"subjectKey"`
	RuleKind       string          `json:"ruleKind"`
	ProviderID     string          `json:"providerId"`
	AccountRef     string          `json:"accountRef"`
	PolicyRevision string          `json:"policyRevision"`
	ConfigHash     string          `json:"configHash"`
	State          json.RawMessage `json:"state"`
	Evaluation     json.RawMessage `json:"evaluation"`
	ObservedAt     string          `json:"observedAt"`
	CreatedAt      string          `json:"createdAt"`
	UpdatedAt      string          `json:"updatedAt"`
}

type outboxListPayload struct {
	SchemaVersion string                 `json:"schemaVersion"`
	Environment   string                 `json:"environment"`
	Messages      []outboxMessagePayload `json:"messages"`
}

type outboxMessagePayload struct {
	ID                string          `json:"id"`
	EventKind         string          `json:"eventKind"`
	Source            string          `json:"source"`
	ProfileRef        string          `json:"profileRef,omitempty"`
	AccountRef        string          `json:"accountRef,omitempty"`
	EventID           string          `json:"eventId"`
	Target            string          `json:"target"`
	PayloadVersion    int             `json:"payloadVersion"`
	Payload           json.RawMessage `json:"payload"`
	Status            string          `json:"status"`
	Attempts          int             `json:"attempts"`
	AvailableAt       string          `json:"availableAt"`
	LeaseToken        string          `json:"leaseToken,omitempty"`
	LeaseExpiresAt    string          `json:"leaseExpiresAt,omitempty"`
	DeliveredAt       string          `json:"deliveredAt,omitempty"`
	ProviderMessageID string          `json:"providerMessageId,omitempty"`
	LastError         string          `json:"lastError,omitempty"`
	DeadLetteredAt    string          `json:"deadLetteredAt,omitempty"`
	CreatedAt         string          `json:"createdAt"`
	UpdatedAt         string          `json:"updatedAt"`
}

func dispatchOutbox(args []string) error {
	if len(args) == 0 || isHelpArg(args[0]) {
		fmt.Println(groupHelp("outbox"))
		return nil
	}
	if args[0] != "list" {
		return fmt.Errorf("unknown outbox command: %s", args[0])
	}
	opts, rest, err := parse(args[1:], flagSpec{
		Use:   "scriba outbox list [flags]",
		Flags: []string{"json", "config", "state-path", "env", "redact", "id", "status", "target", "limit"},
	})
	if err != nil {
		return err
	}
	if len(rest) != 0 {
		return fmt.Errorf("scriba outbox list does not accept positional arguments")
	}
	return runOutboxList(opts)
}

func openInspectionStore(opts options) (*store.Store, string, error) {
	cfg, err := load(opts)
	if err != nil {
		return nil, "", inspectionError(opts, err)
	}
	if opts.statePath != "" {
		cfg.Server.StatePath = opts.statePath
	}
	if opts.env != "" {
		cfg.Server.Environment = opts.env
	}
	st, err := store.OpenReadOnly(resolveServerStatePath(cfg.Server.StatePath))
	if err != nil {
		return nil, "", inspectionError(opts, err)
	}
	return st, cfg.Server.Environment, nil
}

func runPolicyExplain(opts options) error {
	st, environment, err := openInspectionStore(opts)
	if err != nil {
		return inspectionError(opts, err)
	}
	defer func() { _ = st.Close() }()
	rows, err := st.ListPolicyEvaluations(context.Background(), store.PolicyEvaluationFilter{
		ProviderID: inspectionProvider(opts.provider), AccountRef: opts.account, RuleID: opts.rule, Limit: opts.limit,
	})
	if err != nil {
		return inspectionError(opts, err)
	}
	payload := policyExplainPayload{SchemaVersion: policyExplainSchemaVersion, Environment: environment, Evaluations: make([]policyEvaluationPayload, 0, len(rows))}
	for _, row := range rows {
		payload.Evaluations = append(payload.Evaluations, policyEvaluationPayload{
			RuleID: row.RuleID, SubjectKey: row.SubjectKey, RuleKind: row.RuleKind, ProviderID: row.ProviderID,
			AccountRef: row.AccountRef, PolicyRevision: row.PolicyRevision, ConfigHash: row.ConfigHash,
			State: json.RawMessage(row.StateJSON), Evaluation: json.RawMessage(row.EvaluationJSON),
			ObservedAt: formatInspectionTime(row.ObservedAt), CreatedAt: formatInspectionTime(row.CreatedAt), UpdatedAt: formatInspectionTime(row.UpdatedAt),
		})
	}
	if opts.redact {
		payload = redactPolicyExplain(payload)
	}
	return output(opts, payload, renderPolicyExplain(payload))
}

func runOutboxList(opts options) error {
	st, environment, err := openInspectionStore(opts)
	if err != nil {
		return inspectionError(opts, err)
	}
	defer func() { _ = st.Close() }()
	rows, err := st.ListOutbox(context.Background(), store.OutboxFilter{ID: opts.id, Status: opts.status, Target: opts.target, Limit: opts.limit})
	if err != nil {
		return inspectionError(opts, err)
	}
	payload := outboxListPayload{SchemaVersion: outboxListSchemaVersion, Environment: environment, Messages: make([]outboxMessagePayload, 0, len(rows))}
	for _, row := range rows {
		payload.Messages = append(payload.Messages, outboxMessageDTO(row))
	}
	if opts.redact {
		payload = redactOutboxList(payload)
	}
	return output(opts, payload, renderOutboxList(payload))
}

func outboxMessageDTO(row store.OutboxMessage) outboxMessagePayload {
	return outboxMessagePayload{
		ID: row.ID, EventKind: row.EventKind, Source: row.Source, ProfileRef: row.ProfileRef, AccountRef: row.AccountRef,
		EventID: row.EventID, Target: row.Target, PayloadVersion: row.PayloadVersion, Payload: json.RawMessage(row.PayloadJSON),
		Status: row.Status, Attempts: row.Attempts, AvailableAt: formatInspectionTime(row.AvailableAt), LeaseToken: row.LeaseToken,
		LeaseExpiresAt: formatOptionalInspectionTime(row.LeaseExpiresAt), DeliveredAt: formatOptionalInspectionTime(row.DeliveredAt),
		ProviderMessageID: row.ProviderMessageID, LastError: row.LastError, DeadLetteredAt: formatOptionalInspectionTime(row.DeadLetteredAt),
		CreatedAt: formatInspectionTime(row.CreatedAt), UpdatedAt: formatInspectionTime(row.UpdatedAt),
	}
}

func formatInspectionTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }
func formatOptionalInspectionTime(value *time.Time) string {
	if value == nil {
		return ""
	}
	return formatInspectionTime(*value)
}

func renderPolicyExplain(payload policyExplainPayload) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n%s · %d evaluations", cliHeader("Policy explanations"), payload.Environment, len(payload.Evaluations))
	for _, row := range payload.Evaluations {
		account, decision := row.AccountRef, compactJSON(row.Evaluation)
		fmt.Fprintf(&b, "\n\n%s  %s\n  subject   %s\n  account   %s/%s\n  observed  %s", cliBold(row.RuleID), row.RuleKind, row.SubjectKey, row.ProviderID, account, row.ObservedAt)
		fmt.Fprintf(&b, "\n  decision  %s", decision)
	}
	return b.String()
}

func redactPolicyExplain(payload policyExplainPayload) policyExplainPayload {
	for i := range payload.Evaluations {
		payload.Evaluations[i].SubjectKey = "[redacted]"
		payload.Evaluations[i].AccountRef = "[redacted]"
		payload.Evaluations[i].ConfigHash = "[redacted]"
		payload.Evaluations[i].State = json.RawMessage(`"[redacted]"`)
		payload.Evaluations[i].Evaluation = json.RawMessage(`"[redacted]"`)
	}
	return payload
}

func redactOutboxList(payload outboxListPayload) outboxListPayload {
	for i := range payload.Messages {
		payload.Messages[i].ProfileRef = "[redacted]"
		payload.Messages[i].AccountRef = "[redacted]"
		payload.Messages[i].Target = "[redacted]"
		payload.Messages[i].Payload = json.RawMessage(`"[redacted]"`)
		payload.Messages[i].LeaseToken = "[redacted]"
		payload.Messages[i].ProviderMessageID = "[redacted]"
		payload.Messages[i].LastError = "[redacted]"
	}
	return payload
}

func renderOutboxList(payload outboxListPayload) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n%s · %d messages", cliHeader("Notification outbox"), payload.Environment, len(payload.Messages))
	for _, row := range payload.Messages {
		fmt.Fprintf(&b, "\n\n%s  %s\n  event     %s\n  target    %s\n  attempts  %d\n  available %s", cliBold(row.ID), row.Status, row.EventKind, row.Target, row.Attempts, row.AvailableAt)
		if row.LeaseToken != "" {
			fmt.Fprintf(&b, "\n  lease     active until %s", row.LeaseExpiresAt)
		}
	}
	return b.String()
}

func compactJSON(data json.RawMessage) string {
	var b bytes.Buffer
	if err := json.Compact(&b, data); err != nil {
		return string(data)
	}
	return b.String()
}

func inspectionProvider(value string) string {
	if value == "all" {
		return ""
	}
	return value
}

func inspectionError(opts options, err error) error {
	if err == nil || !opts.redact {
		return err
	}
	return errors.New(fmt.Sprint(privacy.Redact(err.Error())))
}
