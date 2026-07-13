# Scriba Operations

## User service

Install the committed user units, reload systemd, and enable the resident
server plus its daily verified backup:

```sh
install -Dm0644 deploy/systemd/scriba.service ~/.config/systemd/user/scriba.service
install -Dm0644 deploy/systemd/scriba-backup.service ~/.config/systemd/user/scriba-backup.service
install -Dm0644 deploy/systemd/scriba-backup.timer ~/.config/systemd/user/scriba-backup.timer
systemctl --user daemon-reload
systemctl --user enable --now scriba.service scriba-backup.timer
systemctl --user start scriba-backup.service
systemctl --user status scriba.service scriba-backup.timer
journalctl --user -u scriba.service -u scriba-backup.service --since today
```

The service sandbox leaves the config and home directory read-only. Scriba can
write only its state, cache, and Codex authentication directories; the backup
job can write only under `~/.local/state`. Both units use an owner-only umask,
restricted address families, a read-only system and home baseline, and the
process restrictions supported by unprivileged user managers. Validate edits
on the target system before reloading:

```sh
systemd-analyze --user verify \
  ~/.config/systemd/user/scriba.service \
  ~/.config/systemd/user/scriba-backup.service \
  ~/.config/systemd/user/scriba-backup.timer
systemd-analyze --user security scriba.service
systemd-analyze --user security scriba-backup.service
```

User services stop at logout unless lingering is enabled. An administrator can
make the server and persistent timer independent of interactive SSH sessions:

```sh
sudo loginctl enable-linger "$USER"
loginctl show-user "$USER" -p Linger
```

## Backup and restore

`scriba server backup` uses SQLite `VACUUM INTO`, validates `quick_check`,
checks the supported schema and required tables, computes SHA-256, fsyncs the
candidate, promotes it atomically, and retains the newest 14 matching backups
by default. The timer runs daily at 03:17 local time with up to 15 minutes of
random delay and catches up after downtime via `Persistent=true`.

Local backups default to `~/.local/state/scriba/backups`. They protect against
database corruption and operator mistakes, not host loss. Copy verified
backups to an independently controlled off-host destination when Scriba data
becomes irreplaceable.

Restore only while the service is stopped. Preserve the failed live database,
verify the selected backup independently, install it with owner-only
permissions, then start and inspect health before deleting either copy:

```sh
systemctl --user stop scriba.service
cp -p ~/.local/state/scriba/server.sqlite ~/.local/state/scriba/server.sqlite.failed
sqlite3 "$BACKUP" 'pragma quick_check; select max(version) from schema_migrations;'
install -m0600 "$BACKUP" ~/.local/state/scriba/server.sqlite
systemctl --user start scriba.service
scriba server health --env prod --json
journalctl --user -u scriba.service --since -5m
```

Do not restore over a running SQLite database. Keep the pre-restore copy until
health, schema, queue state, Telegram ingress, and at least one normal poll have
been verified.
