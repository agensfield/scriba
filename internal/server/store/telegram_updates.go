package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type TelegramUpdate struct {
	BotRef, RawJSON, Status, LastError string
	UpdateID, Attempts                 int64
	AvailableAt, CreatedAt, UpdatedAt  time.Time
}

type TelegramUpdateInput struct {
	UpdateID int64
	RawJSON  string
}
type TelegramUpdateStats struct{ Pending, Processed, Dead int }

// StageTelegramUpdates durably records a successful getUpdates batch and advances
// the polling high-water in the same transaction. Existing v6 offsets are the
// assumed-processed high-water; Telegram history is intentionally not backfilled.
func (s *Store) StageTelegramUpdates(ctx context.Context, botRef string, updates []TelegramUpdateInput, now time.Time) error {
	if botRef == "" {
		return errors.New("telegram bot ref is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var high int64
	if err = tx.QueryRowContext(ctx, `select coalesce((select last_update_id from telegram_offsets where bot_ref=?),-1)`, botRef).Scan(&high); err != nil {
		return err
	}
	assumedProcessed := high
	for _, u := range updates {
		if u.UpdateID < 0 || u.RawJSON == "" {
			return errors.New("invalid telegram update")
		}
		if u.UpdateID > assumedProcessed {
			if _, err = tx.ExecContext(ctx, `insert into telegram_updates(bot_ref,update_id,raw_json,status,attempts,available_at,created_at,updated_at) values(?,?,?,'pending',0,?,?,?) on conflict(bot_ref,update_id) do nothing`, botRef, u.UpdateID, u.RawJSON, formatTime(now), formatTime(now), formatTime(now)); err != nil {
				return err
			}
		}
		if u.UpdateID > high {
			high = u.UpdateID
		}
	}
	if len(updates) > 0 {
		if _, err = tx.ExecContext(ctx, `insert into telegram_offsets(bot_ref,last_update_id,updated_at) values(?,?,?) on conflict(bot_ref) do update set last_update_id=excluded.last_update_id,updated_at=excluded.updated_at where excluded.last_update_id>telegram_offsets.last_update_id`, botRef, high, formatTime(now)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) DueTelegramUpdates(ctx context.Context, botRef string, now time.Time, limit int) ([]TelegramUpdate, error) {
	if limit < 1 || limit > 1000 {
		return nil, errors.New("telegram update limit must be between 1 and 1000")
	}
	rows, err := s.db.QueryContext(ctx, `select bot_ref,update_id,raw_json,status,attempts,available_at,coalesce(last_error,''),created_at,updated_at from telegram_updates where bot_ref=? and status='pending' and available_at<=? order by available_at,update_id limit ?`, botRef, formatTime(now), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TelegramUpdate
	for rows.Next() {
		var u TelegramUpdate
		var a, c, m string
		if err = rows.Scan(&u.BotRef, &u.UpdateID, &u.RawJSON, &u.Status, &u.Attempts, &a, &u.LastError, &c, &m); err != nil {
			return nil, err
		}
		u.AvailableAt = parseDBTime(a)
		u.CreatedAt = parseDBTime(c)
		u.UpdatedAt = parseDBTime(m)
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *Store) MarkTelegramUpdateProcessed(ctx context.Context, botRef string, id int64, now time.Time) (bool, error) {
	r, e := s.db.ExecContext(ctx, `update telegram_updates set status='processed',processed_at=?,last_error=null,updated_at=? where bot_ref=? and update_id=? and status='pending'`, formatTime(now), formatTime(now), botRef, id)
	return changed(r, e)
}
func (s *Store) MarkTelegramUpdateFailure(ctx context.Context, botRef string, id int64, message string, now time.Time) (bool, error) {
	var attempts int64
	if err := s.db.QueryRowContext(ctx, `select attempts from telegram_updates where bot_ref=? and update_id=? and status='pending'`, botRef, id).Scan(&attempts); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	r, e := s.db.ExecContext(ctx, `update telegram_updates set attempts=attempts+1,available_at=?,last_error=?,updated_at=? where bot_ref=? and update_id=? and status='pending'`, formatTime(now.Add(TelegramUpdateBackoff(attempts+1))), message, formatTime(now), botRef, id)
	return changed(r, e)
}
func (s *Store) MarkTelegramUpdateDead(ctx context.Context, botRef string, id int64, message string, now time.Time) (bool, error) {
	r, e := s.db.ExecContext(ctx, `update telegram_updates set status='dead',attempts=attempts+1,dead_at=?,last_error=?,updated_at=? where bot_ref=? and update_id=? and status='pending'`, formatTime(now), message, formatTime(now), botRef, id)
	return changed(r, e)
}
func TelegramUpdateBackoff(attempt int64) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	return time.Second * time.Duration(1<<min(attempt-1, 6))
}
func (s *Store) TelegramUpdateStats(ctx context.Context, botRef string) (TelegramUpdateStats, error) {
	var v TelegramUpdateStats
	e := s.db.QueryRowContext(ctx, `select count(*) filter(where status='pending'),count(*) filter(where status='processed'),count(*) filter(where status='dead') from telegram_updates where bot_ref=?`, botRef).Scan(&v.Pending, &v.Processed, &v.Dead)
	return v, e
}
