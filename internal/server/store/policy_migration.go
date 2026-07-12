package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

const PolicySchemaVersion = 8

const policySchemaSQL = `
create unique index if not exists idx_accounts_provider_ref on accounts(provider_id,account_ref);
create table if not exists policy_states (
 rule_id text not null check(length(rule_id)>0),
 subject_key text not null check(length(subject_key)>0),
 rule_kind text not null check(rule_kind in ('remaining_checkpoint','reset_transition','grant_available','grant_expiry_checkpoint')),
 provider_id text not null check(length(provider_id)>0),
 account_ref text not null,
 policy_revision text not null check(length(policy_revision)>0),
 config_hash text not null check(length(config_hash)>0),
 state_json text not null check(json_valid(state_json)),
 evaluation_json text not null check(json_valid(evaluation_json)),
 observed_at text not null check(length(observed_at)>0),
 created_at text not null check(length(created_at)>0),
 updated_at text not null check(length(updated_at)>0),
 primary key(provider_id,account_ref,policy_revision,config_hash,rule_id,subject_key),
 foreign key(provider_id,account_ref) references accounts(provider_id,account_ref)
);
create index if not exists idx_policy_states_account on policy_states(provider_id,account_ref,policy_revision,config_hash,rule_kind,rule_id,subject_key);

create table if not exists policy_events (
 id text primary key check(length(id)>0),
 semantic_key text not null check(length(semantic_key)>0),
 event_kind text not null check(length(event_kind)>0),
 semantic_event_id text not null check(length(semantic_event_id)>0),
 rule_id text not null check(length(rule_id)>0),
 subject_key text not null check(length(subject_key)>0),
 rule_kind text not null check(rule_kind in ('remaining_checkpoint','reset_transition','grant_available','grant_expiry_checkpoint')),
 provider_id text not null check(length(provider_id)>0),
 account_ref text not null,
 policy_revision text not null check(length(policy_revision)>0),
 config_hash text not null check(length(config_hash)>0),
 payload_version integer not null check(payload_version>0),
 payload_json text not null check(json_valid(payload_json)),
 detected_at text not null check(length(detected_at)>0),
 created_at text not null check(length(created_at)>0),
 foreign key(provider_id,account_ref) references accounts(provider_id,account_ref)
);
create unique index if not exists idx_policy_events_semantic_key on policy_events(semantic_key);
create unique index if not exists idx_policy_events_correlation on policy_events(event_kind,semantic_event_id);
create index if not exists idx_policy_events_account on policy_events(provider_id,account_ref,policy_revision,config_hash,detected_at,id);
create index if not exists idx_policy_events_rule on policy_events(rule_id,subject_key,detected_at,id);`

func (s *Store) migratePolicy(ctx context.Context) (retErr error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	var version sql.NullInt64
	if err = conn.QueryRowContext(ctx, `select max(version) from schema_migrations`).Scan(&version); err != nil {
		return err
	}
	if version.Valid && version.Int64 >= PolicySchemaVersion {
		return validatePolicySchema(ctx, conn)
	}
	if _, err = conn.ExecContext(ctx, `begin immediate`); err != nil {
		return err
	}
	defer func() {
		if retErr != nil {
			_, _ = conn.ExecContext(context.Background(), `rollback`)
		}
	}()
	if err = conn.QueryRowContext(ctx, `select max(version) from schema_migrations`).Scan(&version); err != nil {
		return err
	}
	if version.Valid && version.Int64 >= PolicySchemaVersion {
		if err = validatePolicySchema(ctx, conn); err != nil {
			return err
		}
		_, err = conn.ExecContext(ctx, `commit`)
		return err
	}
	if _, err = conn.ExecContext(ctx, policySchemaSQL); err != nil {
		return err
	}
	if err = validatePolicySchema(ctx, conn); err != nil {
		return err
	}
	if _, err = conn.ExecContext(ctx, `insert into schema_migrations(version,applied_at) values(?,?)`, PolicySchemaVersion, formatTime(time.Now())); err != nil {
		return err
	}
	if _, err = conn.ExecContext(ctx, `commit`); err != nil {
		return err
	}
	return nil
}

type policySchemaQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func validatePolicySchema(ctx context.Context, db policySchemaQuerier) error {
	requiredColumns := map[string][]string{
		"policy_states": {"rule_id", "subject_key", "rule_kind", "provider_id", "account_ref", "policy_revision", "config_hash", "state_json", "evaluation_json", "observed_at", "created_at", "updated_at"},
		"policy_events": {"id", "semantic_key", "event_kind", "semantic_event_id", "rule_id", "subject_key", "rule_kind", "provider_id", "account_ref", "policy_revision", "config_hash", "payload_version", "payload_json", "detected_at", "created_at"},
	}
	for table, required := range requiredColumns {
		rows, err := db.QueryContext(ctx, `pragma table_info(`+table+`)`)
		if err != nil {
			return fmt.Errorf("validate policy schema table %s: %w", table, err)
		}
		found := map[string]bool{}
		for rows.Next() {
			var cid, notNull, pk int
			var name, typ string
			var defaultValue any
			if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
				_ = rows.Close()
				return fmt.Errorf("validate policy schema table %s: %w", table, err)
			}
			found[name] = true
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("validate policy schema table %s: %w", table, err)
		}
		var missing []string
		for _, column := range required {
			if !found[column] {
				missing = append(missing, column)
			}
		}
		if len(missing) > 0 {
			return fmt.Errorf("invalid policy schema: table %s missing columns %s", table, strings.Join(missing, ","))
		}
	}
	requiredTableFragments := map[string][]string{
		"policy_states": {
			"primary key(provider_id,account_ref,policy_revision,config_hash,rule_id,subject_key)",
			"foreign key(provider_id,account_ref) references accounts(provider_id,account_ref)",
			"check(json_valid(state_json))", "check(json_valid(evaluation_json))",
		},
		"policy_events": {
			"id text primary key", "foreign key(provider_id,account_ref) references accounts(provider_id,account_ref)",
			"check(json_valid(payload_json))", "check(payload_version>0)",
		},
	}
	for table, fragments := range requiredTableFragments {
		rows, err := db.QueryContext(ctx, `select sql from sqlite_master where type='table' and name=?`, table)
		if err != nil {
			return fmt.Errorf("validate policy schema definition %s: %w", table, err)
		}
		var definition string
		if !rows.Next() {
			_ = rows.Close()
			return fmt.Errorf("invalid policy schema: missing table %s", table)
		}
		if err := rows.Scan(&definition); err != nil {
			_ = rows.Close()
			return fmt.Errorf("validate policy schema definition %s: %w", table, err)
		}
		_ = rows.Close()
		normalized := strings.ToLower(strings.Join(strings.Fields(definition), " "))
		normalized = strings.NewReplacer(" ", "", "\n", "", "\t", "").Replace(normalized)
		for _, fragment := range fragments {
			want := strings.NewReplacer(" ", "", "\n", "", "\t", "").Replace(strings.ToLower(fragment))
			if !strings.Contains(normalized, want) {
				return fmt.Errorf("invalid policy schema: table %s missing constraint %s", table, fragment)
			}
		}
	}
	requiredIndexes := map[string]struct {
		columns []string
		unique  bool
	}{
		"idx_accounts_provider_ref":      {[]string{"provider_id", "account_ref"}, true},
		"idx_policy_states_account":      {[]string{"provider_id", "account_ref", "policy_revision", "config_hash", "rule_kind", "rule_id", "subject_key"}, false},
		"idx_policy_events_semantic_key": {[]string{"semantic_key"}, true},
		"idx_policy_events_correlation":  {[]string{"event_kind", "semantic_event_id"}, true},
		"idx_policy_events_account":      {[]string{"provider_id", "account_ref", "policy_revision", "config_hash", "detected_at", "id"}, false},
		"idx_policy_events_rule":         {[]string{"rule_id", "subject_key", "detected_at", "id"}, false},
	}
	for name, required := range requiredIndexes {
		rows, err := db.QueryContext(ctx, `select name from pragma_index_info(?) order by seqno`, name)
		if err != nil {
			return fmt.Errorf("validate policy schema index %s: %w", name, err)
		}
		var columns []string
		for rows.Next() {
			var column string
			if err := rows.Scan(&column); err != nil {
				_ = rows.Close()
				return fmt.Errorf("validate policy schema index %s: %w", name, err)
			}
			columns = append(columns, column)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("validate policy schema index %s: %w", name, err)
		}
		if strings.Join(columns, ",") != strings.Join(required.columns, ",") {
			return fmt.Errorf("invalid policy schema: index %s columns are %s, want %s", name, strings.Join(columns, ","), strings.Join(required.columns, ","))
		}
		rows, err = db.QueryContext(ctx, `select sql from sqlite_master where type='index' and name=?`, name)
		if err != nil {
			return fmt.Errorf("validate policy schema index %s: %w", name, err)
		}
		var definition string
		if !rows.Next() {
			_ = rows.Close()
			return fmt.Errorf("invalid policy schema: missing index %s", name)
		}
		if err := rows.Scan(&definition); err != nil {
			_ = rows.Close()
			return fmt.Errorf("validate policy schema index %s: %w", name, err)
		}
		_ = rows.Close()
		isUnique := strings.HasPrefix(strings.ToLower(strings.TrimSpace(definition)), "create unique index")
		if isUnique != required.unique {
			return fmt.Errorf("invalid policy schema: index %s unique=%t, want %t", name, isUnique, required.unique)
		}
	}
	return nil
}
