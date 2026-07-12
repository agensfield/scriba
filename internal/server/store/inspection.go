package store

import (
	"context"
	"errors"
	"strings"
	"time"
)

const InspectionMaxLimit = 1000

type PolicyEvaluationFilter struct {
	ProviderID string
	AccountRef string
	RuleID     string
	Limit      int
}

type PolicyEvaluation struct {
	RuleID, SubjectKey, RuleKind, ProviderID, AccountRef  string
	PolicyRevision, ConfigHash, StateJSON, EvaluationJSON string
	ObservedAt, CreatedAt, UpdatedAt                      time.Time
}

func (s *Store) ListPolicyEvaluations(ctx context.Context, filter PolicyEvaluationFilter) ([]PolicyEvaluation, error) {
	if err := validateInspectionLimit(filter.Limit); err != nil {
		return nil, err
	}
	query := `select rule_id,subject_key,rule_kind,provider_id,account_ref,policy_revision,config_hash,state_json,evaluation_json,observed_at,created_at,updated_at from policy_states where 1=1`
	args := make([]any, 0, 4)
	query, args = appendStringFilter(query, args, "provider_id", filter.ProviderID)
	query, args = appendStringFilter(query, args, "account_ref", filter.AccountRef)
	query, args = appendStringFilter(query, args, "rule_id", filter.RuleID)
	query += ` order by observed_at desc,provider_id,account_ref,rule_id,subject_key,policy_revision,config_hash limit ?`
	args = append(args, filter.Limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]PolicyEvaluation, 0)
	for rows.Next() {
		var v PolicyEvaluation
		var observed, created, updated string
		if err := rows.Scan(&v.RuleID, &v.SubjectKey, &v.RuleKind, &v.ProviderID, &v.AccountRef, &v.PolicyRevision, &v.ConfigHash, &v.StateJSON, &v.EvaluationJSON, &observed, &created, &updated); err != nil {
			return nil, err
		}
		v.ObservedAt, v.CreatedAt, v.UpdatedAt = parseDBTime(observed), parseDBTime(created), parseDBTime(updated)
		out = append(out, v)
	}
	return out, rows.Err()
}

type OutboxFilter struct {
	ID, Status, Target string
	Limit              int
}

func (s *Store) ListOutbox(ctx context.Context, filter OutboxFilter) ([]OutboxMessage, error) {
	if err := validateInspectionLimit(filter.Limit); err != nil {
		return nil, err
	}
	query := `select id,event_kind,source,coalesce(profile_ref,''),coalesce(account_ref,''),event_id,target,payload_version,payload_json,status,attempts,available_at,coalesce(lease_token,''),lease_expires_at,delivered_at,coalesce(provider_message_id,''),coalesce(last_error,''),dead_lettered_at,created_at,updated_at from notification_outbox where 1=1`
	args := make([]any, 0, 4)
	query, args = appendStringFilter(query, args, "id", filter.ID)
	query, args = appendStringFilter(query, args, "status", filter.Status)
	query, args = appendStringFilter(query, args, "target", filter.Target)
	query += ` order by created_at desc,id limit ?`
	args = append(args, filter.Limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]OutboxMessage, 0)
	for rows.Next() {
		v, err := scanOutbox(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func validateInspectionLimit(limit int) error {
	if limit < 1 || limit > InspectionMaxLimit {
		return errors.New("inspection limit must be between 1 and 1000")
	}
	return nil
}

func appendStringFilter(query string, args []any, column, value string) (string, []any) {
	if strings.TrimSpace(value) == "" {
		return query, args
	}
	return query + " and " + column + "=?", append(args, value)
}
