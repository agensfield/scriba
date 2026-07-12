package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/agensfield/scriba/internal/server/store"
	"github.com/go-telegram/bot/models"
)

const maxGetUpdatesResponse = 16 << 20

type stagingHTTPClient struct {
	next interface {
		Do(*http.Request) (*http.Response, error)
	}
	store  UpdateStore
	botRef string
}
type telegramEnvelope struct {
	OK     bool              `json:"ok"`
	Result []json.RawMessage `json:"result"`
}

func (c *stagingHTTPClient) Do(req *http.Request) (*http.Response, error) {
	resp, err := c.next.Do(req)
	if err != nil {
		return nil, err
	}
	if !strings.HasSuffix(req.URL.Path, "/getUpdates") || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp, nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxGetUpdatesResponse+1))
	_ = resp.Body.Close()
	if err != nil {
		return nil, err
	}
	if len(body) > maxGetUpdatesResponse {
		return nil, errors.New("telegram getUpdates response exceeds limit")
	}
	var env telegramEnvelope
	if err = json.Unmarshal(body, &env); err != nil {
		return responseWithBody(resp, body), nil
	}
	if !env.OK {
		return responseWithBody(resp, body), nil
	}
	updates := make([]store.TelegramUpdateInput, 0, len(env.Result))
	for _, raw := range env.Result {
		var id struct {
			ID int64 `json:"update_id"`
		}
		if err = json.Unmarshal(raw, &id); err != nil {
			return nil, fmt.Errorf("decode telegram update id: %w", err)
		}
		updates = append(updates, store.TelegramUpdateInput{UpdateID: id.ID, RawJSON: string(raw)})
	}
	if err = c.store.StageTelegramUpdates(req.Context(), c.botRef, updates, time.Now()); err != nil {
		return nil, fmt.Errorf("stage telegram updates: %w", err)
	}
	return responseWithBody(resp, body), nil
}
func responseWithBody(resp *http.Response, body []byte) *http.Response {
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	return resp
}

func (s *Service) processTelegramUpdates(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if err := s.drainTelegramUpdates(ctx); err != nil && !errors.Is(err, context.Canceled) {
			s.logger.Warn("telegram inbox drain failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-s.updateWake:
		case <-ticker.C:
		}
	}
}
func (s *Service) drainTelegramUpdates(ctx context.Context) error {
	if s.updates == nil {
		return nil
	}
	for {
		rows, err := s.updates.DueTelegramUpdates(ctx, s.botRef, time.Now(), 100)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		for _, row := range rows {
			if err = ctx.Err(); err != nil {
				return err
			}
			var update models.Update
			if err = json.Unmarshal([]byte(row.RawJSON), &update); err != nil {
				_, markErr := s.updates.MarkTelegramUpdateDead(ctx, s.botRef, row.UpdateID, "malformed update: "+err.Error(), time.Now())
				if markErr != nil {
					return markErr
				}
				continue
			}
			err = s.dispatchUpdate(ctx, &update)
			if err == nil {
				_, err = s.updates.MarkTelegramUpdateProcessed(ctx, s.botRef, row.UpdateID, time.Now())
			} else if ctx.Err() != nil {
				return ctx.Err()
			} else {
				_, err = s.updates.MarkTelegramUpdateFailure(ctx, s.botRef, row.UpdateID, err.Error(), time.Now())
			}
			if err != nil {
				return err
			}
		}
		if len(rows) < 100 {
			return nil
		}
	}
}
