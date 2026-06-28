package store

const schemaSQL = `
create table if not exists schema_migrations (
  version integer primary key,
  applied_at text not null
);

create table if not exists accounts (
  account_ref text primary key,
  provider_id text not null,
  label text not null,
  email text not null,
  plan text not null,
  updated_at text not null
);

create table if not exists limit_observations (
  id text primary key,
  provider_id text not null,
  account_ref text not null,
  observed_at text not null,
  snapshot_json text not null,
  created_at text not null,
  foreign key (account_ref) references accounts(account_ref)
);

create table if not exists observed_windows (
  observation_id text not null,
  label text not null,
  used_percent real,
  reset_at text not null,
  period_duration_ms integer,
  primary key (observation_id, label),
  foreign key (observation_id) references limit_observations(id) on delete cascade
);

create table if not exists limit_windows (
  account_ref text not null,
  label text not null,
  stable_reset_at text not null,
  last_seen_reset_at text not null,
  last_observed_at text not null,
  last_snapshot_json text not null,
  updated_at text not null,
  primary key (account_ref, label),
  foreign key (account_ref) references accounts(account_ref)
);

create table if not exists reset_events (
  id text primary key,
  provider_id text not null,
  account_ref text not null,
  account_label text not null,
  account_email text not null,
  account_plan text not null,
  primary_trigger_label text not null,
  secondary_trigger_labels_json text not null,
  reset_kind text not null,
  previous_reset_at text not null,
  current_reset_at text not null,
  previous_snapshot_json text not null,
  current_snapshot_json text not null,
  joke_id text not null,
  detected_at text not null,
  created_at text not null,
  foreign key (account_ref) references accounts(account_ref)
);

create table if not exists notification_deliveries (
  id text primary key,
  event_id text not null,
  target text not null,
  status text not null,
  attempts integer not null default 0,
  last_attempt_at text,
  next_attempt_at text,
  delivered_at text,
  provider_message_id text,
  last_error text,
  created_at text not null,
  updated_at text not null,
  unique (event_id, target),
  foreign key (event_id) references reset_events(id)
);

create table if not exists limit_warning_events (
  id text primary key,
  provider_id text not null,
  account_ref text not null,
  account_label text not null,
  account_email text not null,
  account_plan text not null,
  label text not null,
  threshold_remaining integer not null,
  used_percent real not null,
  remaining_percent real not null,
  reset_at text not null,
  snapshot_json text not null,
  detected_at text not null,
  created_at text not null,
  foreign key (account_ref) references accounts(account_ref)
);

create table if not exists limit_warning_deliveries (
  id text primary key,
  warning_id text not null,
  target text not null,
  status text not null,
  attempts integer not null default 0,
  last_attempt_at text,
  next_attempt_at text,
  delivered_at text,
  provider_message_id text,
  last_error text,
  created_at text not null,
  updated_at text not null,
  unique (warning_id, target),
  foreign key (warning_id) references limit_warning_events(id)
);

create table if not exists reset_grant_warning_events (
  id text primary key,
  provider_id text not null,
  account_ref text not null,
  account_label text not null,
  account_email text not null,
  account_plan text not null,
  credit_id text not null,
  credit_title text not null,
  threshold_days integer not null,
  expires_at text not null,
  snapshot_json text not null,
  detected_at text not null,
  created_at text not null,
  foreign key (account_ref) references accounts(account_ref)
);

create table if not exists reset_grant_warning_deliveries (
  id text primary key,
  warning_id text not null,
  target text not null,
  status text not null,
  attempts integer not null default 0,
  last_attempt_at text,
  next_attempt_at text,
  delivered_at text,
  provider_message_id text,
  last_error text,
  created_at text not null,
  updated_at text not null,
  unique (warning_id, target),
  foreign key (warning_id) references reset_grant_warning_events(id)
);

create table if not exists reset_grant_tracking_state (
  account_ref text primary key,
  provider_id text not null,
  available_count integer not null,
  last_observed_at text not null,
  created_at text not null,
  updated_at text not null,
  foreign key (account_ref) references accounts(account_ref)
);

create table if not exists reset_grant_events (
  id text primary key,
  provider_id text not null,
  account_ref text not null,
  account_label text not null,
  account_email text not null,
  account_plan text not null,
  credit_id text not null,
  credit_title text not null,
  reset_type text not null,
  granted_at text,
  expires_at text not null,
  available_count integer not null,
  snapshot_json text not null,
  detected_at text not null,
  created_at text not null,
  foreign key (account_ref) references accounts(account_ref)
);

create table if not exists reset_grant_deliveries (
  id text primary key,
  event_id text not null,
  target text not null,
  status text not null,
  attempts integer not null default 0,
  last_attempt_at text,
  next_attempt_at text,
  delivered_at text,
  provider_message_id text,
  last_error text,
  created_at text not null,
  updated_at text not null,
  unique (event_id, target),
  foreign key (event_id) references reset_grant_events(id)
);

create table if not exists radar_alert_events (
  id text primary key,
  milestone integer not null,
  probability_24h real not null,
  probability_48h real not null,
  level text not null,
  expected_window text not null,
  reasoning_summary text not null,
  checked_at text not null,
  detected_at text not null,
  snapshot_json text not null,
  created_at text not null
);

create table if not exists radar_alert_deliveries (
  id text primary key,
  alert_id text not null,
  target text not null,
  status text not null,
  attempts integer not null default 0,
  last_attempt_at text,
  next_attempt_at text,
  delivered_at text,
  provider_message_id text,
  last_error text,
  created_at text not null,
  updated_at text not null,
  unique (alert_id, target),
  foreign key (alert_id) references radar_alert_events(id)
);

create table if not exists server_settings (
  key text primary key,
  value text not null,
  updated_at text not null
);

create table if not exists telegram_offsets (
  bot_ref text primary key,
  last_update_id integer not null,
  updated_at text not null
);

create index if not exists idx_limit_observations_account_time
  on limit_observations(account_ref, observed_at);

create index if not exists idx_reset_events_account_detected
  on reset_events(account_ref, detected_at);

create index if not exists idx_notification_deliveries_status
  on notification_deliveries(status, created_at);

create index if not exists idx_limit_warning_events_account_detected
  on limit_warning_events(account_ref, detected_at);

create index if not exists idx_limit_warning_deliveries_status
  on limit_warning_deliveries(status, created_at);

create index if not exists idx_reset_grant_warning_events_account_detected
  on reset_grant_warning_events(account_ref, detected_at);

create index if not exists idx_reset_grant_warning_deliveries_status
  on reset_grant_warning_deliveries(status, created_at);

create index if not exists idx_reset_grant_events_account_detected
  on reset_grant_events(account_ref, detected_at);

create index if not exists idx_reset_grant_deliveries_status
  on reset_grant_deliveries(status, created_at);

create index if not exists idx_radar_alert_events_detected
  on radar_alert_events(detected_at);

create index if not exists idx_radar_alert_deliveries_status
  on radar_alert_deliveries(status, created_at);
`
