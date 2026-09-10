package store

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// TestCopiedSchema12To13 is an opt-in drill. SCRIBA_SCHEMA13_COPY must name a
// disposable, verified schema-12 copy. The supplied copy is opened read-only;
// migration runs on a second test-owned copy.
func TestCopiedSchema12To13(t *testing.T) {
	source := os.Getenv("SCRIBA_SCHEMA13_COPY")
	if source == "" {
		t.Skip("SCRIBA_SCHEMA13_COPY is unset")
	}
	info, err := os.Stat(source)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatalf("copy unavailable or not regular: %v", err)
	}
	beforeDB, err := sql.Open("sqlite", sqliteReadOnlyDSN(source))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = beforeDB.Close() }()
	var version int
	if err = beforeDB.QueryRow(`select max(version) from schema_migrations`).Scan(&version); err != nil || version != 12 {
		t.Fatalf("copy must be schema 12, got %d: %v", version, err)
	}
	checks := copiedDBChecks(t, beforeDB)
	working := filepath.Join(t.TempDir(), "schema13-drill.sqlite")
	copySQLiteFile(t, source, working)
	migrated, err := Open(working)
	if err != nil {
		t.Fatal(err)
	}
	if err = migrated.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(working)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	after := copiedDBChecks(t, reopened.db)
	for name, before := range checks {
		if got := after[name]; got != before {
			t.Fatalf("preservation check failed for %s", name)
		}
		t.Logf("%s: count=%d hash=match", name, before.count)
	}
	var integrity string
	if err = reopened.db.QueryRow(`pragma integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatalf("integrity check failed: %v", err)
	}
	var foreignKeys int
	if err = reopened.db.QueryRow(`select count(*) from pragma_foreign_key_check`).Scan(&foreignKeys); err != nil || foreignKeys != 0 {
		t.Fatalf("foreign key check failed: count=%d err=%v", foreignKeys, err)
	}
	if err = reopened.db.QueryRow(`select max(version) from schema_migrations`).Scan(&version); err != nil || version != 13 {
		t.Fatalf("reopened schema=%d err=%v", version, err)
	}
	t.Logf("schema=13 integrity=ok foreign_keys=ok")
}

type copiedCheck struct {
	count int
	hash  [32]byte
}

func copiedDBChecks(t *testing.T, db *sql.DB) map[string]copiedCheck {
	t.Helper()
	tables := []string{
		"limit_observations", "observed_windows", "limit_windows",
		"reset_events", "notification_deliveries",
		"limit_warning_events", "limit_warning_deliveries",
		"reset_grant_warning_events", "reset_grant_warning_deliveries",
		"reset_grant_events", "reset_grant_deliveries", "reset_grant_tracking_state",
		"radar_alert_events", "radar_alert_deliveries",
		"policy_states", "policy_events", "policy_event_replay",
		"pacing_alert_states", "pacing_warning_events",
		"server_settings", "telegram_offsets", "telegram_updates",
	}
	out := make(map[string]copiedCheck, len(tables)+1)
	out["accounts"] = hashQuery(t, db, `select account_ref,provider_id,label,email,plan,updated_at from accounts order by account_ref`)
	for _, table := range tables {
		out[table] = hashQuery(t, db, `select * from `+table+` order by rowid`)
	}
	// profile_ref is deliberately excluded from the cross-version projection.
	out["notification_outbox"] = hashQuery(t, db, `select id,event_kind,source,account_ref,event_id,target,payload_version,payload_json,status,attempts,available_at,lease_token,lease_expires_at,delivered_at,provider_message_id,last_error,dead_lettered_at,created_at,updated_at from notification_outbox order by id`)
	return out
}

func hashQuery(t *testing.T, db *sql.DB, query string) copiedCheck {
	t.Helper()
	rows, err := db.Query(query)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.New()
	count := 0
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err = rows.Scan(pointers...); err != nil {
			t.Fatal(err)
		}
		for _, value := range values {
			_, _ = fmt.Fprintf(h, "%T:%v\x00", value, value)
		}
		count++
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return copiedCheck{count: count, hash: sum}
}

func copySQLiteFile(t *testing.T, source, destination string) {
	t.Helper()
	in, err := os.Open(source) // #nosec G304 -- explicit operator-provided copy path.
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) // #nosec G304 -- test-owned temp path.
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.Copy(out, in); err != nil {
		_ = out.Close()
		t.Fatal(err)
	}
	if err = out.Close(); err != nil {
		t.Fatal(err)
	}
}
