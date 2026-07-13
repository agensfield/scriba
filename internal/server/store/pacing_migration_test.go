package store

import (
	"context"
	"testing"
)

func TestPacingSchemaMigrationFromV11IsIdempotent(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.db.Exec(`drop table pacing_warning_events; drop table pacing_alert_states; delete from schema_migrations where version=12`); err != nil {
		t.Fatal(err)
	}
	if err := s.migratePacingAlerts(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.migratePacingAlerts(context.Background()); err != nil {
		t.Fatal(err)
	}
	var version, states, events, migrations int
	if err := s.db.QueryRow(`select (select max(version)),(select count(*) from pacing_alert_states),(select count(*) from pacing_warning_events),(select count(*) from schema_migrations where version=12) from schema_migrations`).Scan(&version, &states, &events, &migrations); err != nil {
		t.Fatal(err)
	}
	if version != 12 || states != 0 || events != 0 || migrations != 1 {
		t.Fatalf("version=%d states=%d events=%d migrations=%d", version, states, events, migrations)
	}
}

func TestPacingSchemaMigrationRejectsMalformedStampedV12(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.db.Exec(`drop table pacing_alert_states; create table pacing_alert_states(x text)`); err != nil {
		t.Fatal(err)
	}
	if err := s.migratePacingAlerts(context.Background()); err == nil {
		t.Fatal("malformed schema accepted")
	}
}

func TestPacingSchemaMigrationFailureRollsBackPartialObjects(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.db.Exec(`drop table pacing_warning_events; drop table pacing_alert_states; delete from schema_migrations where version=12; create table pacing_warning_events(blocker text)`); err != nil {
		t.Fatal(err)
	}
	if err := s.migratePacingAlerts(context.Background()); err == nil {
		t.Fatal("conflicting table migrated")
	}
	var version, states int
	if err := s.db.QueryRow(`select (select max(version)),(select count(*) from sqlite_master where type='table' and name='pacing_alert_states') from schema_migrations`).Scan(&version, &states); err != nil {
		t.Fatal(err)
	}
	if version != 11 || states != 0 {
		t.Fatalf("version=%d partial_states=%d", version, states)
	}
}
