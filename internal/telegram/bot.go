package telegram

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/agensfield/scriba/internal/radar"
	remotecodex "github.com/agensfield/scriba/internal/remote/codex"
	"github.com/agensfield/scriba/internal/resetwatch"
	"github.com/agensfield/scriba/internal/server"
	"github.com/agensfield/scriba/internal/server/store"
)

type BotConfig struct {
	Token          string
	ChatID         int64
	AllowedUserIDs []int64
}

type Controller interface {
	RefreshNow(context.Context) (server.PollResult, error)
	PollInterval(context.Context) (time.Duration, error)
	SetPollInterval(context.Context, time.Duration) error
	LastResetEvent(context.Context) (resetwatch.Event, bool, error)
	LatestObservation(context.Context) (resetwatch.Observation, bool, error)
	CodexProfile(context.Context) (remotecodex.ProfileResult, error)
	Stats(context.Context) (server.Stats, error)
	Health(context.Context) (server.Health, error)
}

type OffsetStore interface {
	GetTelegramOffset(context.Context, string) (int64, bool, error)
	SetTelegramOffset(context.Context, string, int64) error
}

type DeliveryStore interface {
	EnsureDelivery(context.Context, string, string) (store.Delivery, error)
	MarkDeliverySending(context.Context, string, string) error
	MarkDeliveryAttempt(context.Context, string, string, bool, string, string) error
	PendingDeliveries(context.Context, string, int) ([]store.Delivery, error)
	LoadResetEvent(context.Context, string) (resetwatch.Event, bool, error)
	EnsureWarningDelivery(context.Context, string, string) (store.Delivery, error)
	MarkWarningDeliverySending(context.Context, string, string) error
	MarkWarningDeliveryAttempt(context.Context, string, string, bool, string, string) error
	PendingWarningDeliveries(context.Context, string, int) ([]store.Delivery, error)
	LoadWarningEvent(context.Context, string) (resetwatch.WarningEvent, bool, error)
	EnsureGrantExpiryWarningDelivery(context.Context, string, string) (store.Delivery, error)
	MarkGrantExpiryWarningDeliverySending(context.Context, string, string) error
	MarkGrantExpiryWarningDeliveryAttempt(context.Context, string, string, bool, string, string) error
	PendingGrantExpiryWarningDeliveries(context.Context, string, int) ([]store.Delivery, error)
	LoadGrantExpiryWarningEvent(context.Context, string) (resetwatch.GrantExpiryWarning, bool, error)
	EnsureResetGrantDelivery(context.Context, string, string) (store.Delivery, error)
	MarkResetGrantDeliverySending(context.Context, string, string) error
	MarkResetGrantDeliveryAttempt(context.Context, string, string, bool, string, string) error
	PendingResetGrantDeliveries(context.Context, string, int) ([]store.Delivery, error)
	LoadResetGrantEvent(context.Context, string) (resetwatch.ResetGrantEvent, bool, error)
	EnsureRadarAlertDelivery(context.Context, string, string) (store.Delivery, error)
	MarkRadarAlertDeliverySending(context.Context, string, string) error
	MarkRadarAlertDeliveryAttempt(context.Context, string, string, bool, string, string) error
	PendingRadarAlertDeliveries(context.Context, string, int) ([]store.Delivery, error)
	LoadRadarAlertEvent(context.Context, string) (radar.ProbabilityAlert, bool, error)
}

type Service struct {
	cfg               BotConfig
	controller        Controller
	offsets           OffsetStore
	deliveries        DeliveryStore
	radar             radar.Client
	bot               *tgbot.Bot
	botRef            string
	logger            *slog.Logger
	mu                sync.Mutex
	lastManualRefresh time.Time
}

func NewBotService(cfg BotConfig, controller Controller, offsets OffsetStore, deliveries DeliveryStore, radarClient radar.Client) (*Service, error) {
	if strings.TrimSpace(cfg.Token) == "" {
		return nil, errors.New("telegram bot token is required")
	}
	if cfg.ChatID == 0 {
		return nil, errors.New("telegram chat id is required")
	}
	svc := &Service{cfg: cfg, controller: controller, offsets: offsets, deliveries: deliveries, radar: radarClient, botRef: "default", logger: slog.Default()}
	var options []tgbot.Option
	if offsets != nil {
		if offset, ok, err := offsets.GetTelegramOffset(context.Background(), svc.botRef); err != nil {
			return nil, err
		} else if ok {
			// go-telegram/bot sends lastUpdateID+1 to getUpdates internally.
			options = append(options, tgbot.WithInitialOffset(offset))
		}
	}
	options = append(options,
		tgbot.WithAllowedUpdates(tgbot.AllowedUpdates{"message", "callback_query"}),
		tgbot.WithDefaultHandler(svc.handleUpdate),
	)
	b, err := tgbot.New(cfg.Token, options...)
	if err != nil {
		return nil, err
	}
	svc.bot = b
	return svc, nil
}

func (s *Service) Start(ctx context.Context) {
	if err := s.RegisterCommands(ctx); err != nil {
		s.logger.Warn("telegram command registration failed", "error", err)
	}
	go s.retryDeliveries(ctx)
	s.bot.Start(ctx)
}

func (s *Service) RegisterCommands(ctx context.Context) error {
	if s.bot == nil {
		return nil
	}
	_, err := s.bot.SetMyCommands(ctx, &tgbot.SetMyCommandsParams{Commands: []models.BotCommand{
		{Command: "status", Description: "server health and polling state"},
		{Command: "health", Description: "poll/auth health check"},
		{Command: "stats", Description: "storage and delivery stats"},
		{Command: "limits", Description: "show current Codex limits"},
		{Command: "grants", Description: "show detailed Codex reset grants"},
		{Command: "profile", Description: "show Codex profile stats"},
		{Command: "refresh", Description: "force a live Codex poll"},
		{Command: "lastreset", Description: "show the latest reset event"},
		{Command: "settings", Description: "change runtime settings"},
		{Command: "radar", Description: "check public reset radar"},
		{Command: "help", Description: "show commands"},
	}})
	return err
}

func (s *Service) NotifyBaseline(ctx context.Context, notice server.BaselineNotice) error {
	_, err := s.send(ctx, RenderBaseline(notice), mainKeyboard())
	return err
}

func (s *Service) NotifyReset(ctx context.Context, event resetwatch.Event) error {
	target := s.target()
	if s.deliveries != nil {
		if _, err := s.deliveries.EnsureDelivery(ctx, event.ID, target); err != nil {
			return err
		}
		if err := s.deliveries.MarkDeliverySending(ctx, event.ID, target); err != nil {
			return err
		}
	}
	message, err := s.send(ctx, RenderReset(event), nil)
	if s.deliveries == nil {
		return err
	}
	if err != nil {
		_ = s.deliveries.MarkDeliveryAttempt(ctx, event.ID, target, false, err.Error(), "")
		return err
	}
	messageID := ""
	if message != nil {
		messageID = strconv.Itoa(message.ID)
	}
	return s.deliveries.MarkDeliveryAttempt(ctx, event.ID, target, true, "", messageID)
}

func (s *Service) NotifyLimitWarning(ctx context.Context, warning resetwatch.WarningEvent) error {
	target := s.target()
	if s.deliveries != nil {
		if _, err := s.deliveries.EnsureWarningDelivery(ctx, warning.ID, target); err != nil {
			return err
		}
		if err := s.deliveries.MarkWarningDeliverySending(ctx, warning.ID, target); err != nil {
			return err
		}
	}
	message, err := s.send(ctx, RenderLimitWarning(warning), nil)
	if s.deliveries == nil {
		return err
	}
	if err != nil {
		_ = s.deliveries.MarkWarningDeliveryAttempt(ctx, warning.ID, target, false, err.Error(), "")
		return err
	}
	messageID := ""
	if message != nil {
		messageID = strconv.Itoa(message.ID)
	}
	return s.deliveries.MarkWarningDeliveryAttempt(ctx, warning.ID, target, true, "", messageID)
}

func (s *Service) NotifyGrantExpiryWarning(ctx context.Context, warning resetwatch.GrantExpiryWarning) error {
	target := s.target()
	if s.deliveries != nil {
		if _, err := s.deliveries.EnsureGrantExpiryWarningDelivery(ctx, warning.ID, target); err != nil {
			return err
		}
		if err := s.deliveries.MarkGrantExpiryWarningDeliverySending(ctx, warning.ID, target); err != nil {
			return err
		}
	}
	message, err := s.send(ctx, RenderGrantExpiryWarning(warning), nil)
	if s.deliveries == nil {
		return err
	}
	if err != nil {
		_ = s.deliveries.MarkGrantExpiryWarningDeliveryAttempt(ctx, warning.ID, target, false, err.Error(), "")
		return err
	}
	messageID := ""
	if message != nil {
		messageID = strconv.Itoa(message.ID)
	}
	return s.deliveries.MarkGrantExpiryWarningDeliveryAttempt(ctx, warning.ID, target, true, "", messageID)
}

func (s *Service) NotifyResetGrant(ctx context.Context, event resetwatch.ResetGrantEvent) error {
	target := s.target()
	if s.deliveries != nil {
		if _, err := s.deliveries.EnsureResetGrantDelivery(ctx, event.ID, target); err != nil {
			return err
		}
		if err := s.deliveries.MarkResetGrantDeliverySending(ctx, event.ID, target); err != nil {
			return err
		}
	}
	message, err := s.send(ctx, RenderResetGrant(event), nil)
	if s.deliveries == nil {
		return err
	}
	if err != nil {
		_ = s.deliveries.MarkResetGrantDeliveryAttempt(ctx, event.ID, target, false, err.Error(), "")
		return err
	}
	messageID := ""
	if message != nil {
		messageID = strconv.Itoa(message.ID)
	}
	return s.deliveries.MarkResetGrantDeliveryAttempt(ctx, event.ID, target, true, "", messageID)
}

func (s *Service) NotifyRadarProbability(ctx context.Context, alert radar.ProbabilityAlert) error {
	target := s.target()
	if s.deliveries != nil {
		if _, err := s.deliveries.EnsureRadarAlertDelivery(ctx, alert.ID, target); err != nil {
			return err
		}
		if err := s.deliveries.MarkRadarAlertDeliverySending(ctx, alert.ID, target); err != nil {
			return err
		}
	}
	message, err := s.send(ctx, RenderRadarProbability(alert), mainKeyboard())
	if s.deliveries == nil {
		return err
	}
	if err != nil {
		_ = s.deliveries.MarkRadarAlertDeliveryAttempt(ctx, alert.ID, target, false, err.Error(), "")
		return err
	}
	messageID := ""
	if message != nil {
		messageID = strconv.Itoa(message.ID)
	}
	return s.deliveries.MarkRadarAlertDeliveryAttempt(ctx, alert.ID, target, true, "", messageID)
}

func (s *Service) NotifyHealth(ctx context.Context, notice server.HealthNotice) error {
	_, err := s.send(ctx, RenderHealthNotice(notice), nil)
	return err
}

func (s *Service) handleUpdate(ctx context.Context, b *tgbot.Bot, update *models.Update) {
	handled := false
	defer func() {
		if handled && s.offsets != nil {
			if err := s.offsets.SetTelegramOffset(ctx, s.botRef, update.ID); err != nil {
				s.logger.Warn("telegram offset persist failed", "update_id", update.ID, "error", err)
			}
		}
	}()
	if !s.authorized(update) {
		s.logUnauthorized(update)
		handled = true
		return
	}
	if update.CallbackQuery != nil {
		s.logger.Info("telegram callback received", "update_id", update.ID, "data", update.CallbackQuery.Data)
		s.handleCallback(ctx, update.CallbackQuery)
		handled = true
		return
	}
	if update.Message == nil || update.Message.Text == "" {
		handled = true
		return
	}
	text := strings.TrimSpace(update.Message.Text)
	s.logger.Info("telegram command received", "update_id", update.ID, "command", commandName(text))
	reply, markup := s.handleCommand(ctx, text)
	if reply != "" {
		if _, err := s.send(ctx, reply, markup); err != nil {
			s.logger.Warn("telegram command reply failed", "update_id", update.ID, "command", commandName(text), "error", err)
			return
		}
	}
	handled = true
	_ = b
}

func (s *Service) handleCommand(ctx context.Context, text string) (string, models.ReplyMarkup) {
	command := strings.Fields(text)
	if len(command) == 0 {
		return "", nil
	}
	switch strings.ToLower(strings.Split(command[0], "@")[0]) {
	case "/start":
		return "<b>Scriba is alive</b>\nremote Codex limit watch is running.\n\n" + helpText(), mainKeyboard()
	case "/status":
		health, err := s.controller.Health(ctx)
		if err != nil {
			return "<b>Scriba status</b>\nalive: yes\nhealth: unknown\n<code>" + html.EscapeString(err.Error()) + "</code>", nil
		}
		last := "none yet"
		if event, ok, err := s.controller.LastResetEvent(ctx); err == nil && ok {
			last = fmt.Sprintf("%s · %s -> %s", event.ResetKind, formatTime(event.PreviousResetAt), formatTime(event.CurrentResetAt))
		}
		return "<b>Scriba status</b>\n<pre>" + html.EscapeString(fmt.Sprintf("%-14s %s\n%-14s %s\n%-14s %s\n%-14s %s\n%-14s %s\n%-14s %s", "alive", "yes", "version", health.Version, "health", health.Status, "poll interval", server.FormatDuration(health.PollInterval), "last reset", last, "details", "/health · /stats")) + "</pre>", mainKeyboard()
	case "/health":
		health, err := s.controller.Health(ctx)
		if err != nil {
			return "health failed: " + err.Error(), nil
		}
		return RenderHealth(health), mainKeyboard()
	case "/stats":
		stats, err := s.controller.Stats(ctx)
		if err != nil {
			return "stats failed: " + err.Error(), nil
		}
		return RenderStats(stats, "", false), mainKeyboard()
	case "/settings":
		interval, _ := s.controller.PollInterval(ctx)
		return settingsText(interval), settingsKeyboard(interval)
	case "/limits":
		obs, ok, err := s.controller.LatestObservation(ctx)
		if err != nil {
			return "limits failed: " + err.Error(), nil
		}
		if !ok {
			return "no cached limits yet. use /refresh to fetch live Codex limits.", nil
		}
		return RenderLimits(obs), nil
	case "/grants":
		obs, ok, err := s.controller.LatestObservation(ctx)
		if err != nil {
			return "reset grants failed: " + err.Error(), nil
		}
		if !ok {
			return "no cached reset grants yet. use /refresh to fetch live Codex limits.", nil
		}
		return RenderResetGrantDetails(obs), mainKeyboard()
	case "/profile":
		profile, err := s.controller.CodexProfile(ctx)
		if err != nil {
			return "profile failed: " + err.Error(), nil
		}
		return RenderProfile(profile), mainKeyboard()
	case "/refresh":
		if retryAfter := s.manualRefreshRetryAfter(); retryAfter > 0 {
			return "refresh rate-limited. try again in " + retryAfter.Round(time.Second).String(), nil
		}
		result, err := s.controller.RefreshNow(ctx)
		if errors.Is(err, server.ErrRefreshInProgress) {
			return "refresh already in progress. hold the line.", nil
		}
		if err != nil {
			return "refresh failed: " + err.Error(), nil
		}
		s.markManualRefresh()
		return RenderLimits(result.Observation), nil
	case "/lastreset":
		event, ok, err := s.controller.LastResetEvent(ctx)
		if err != nil {
			return "last reset failed: " + err.Error(), nil
		}
		if !ok {
			return "no reset events recorded yet.", nil
		}
		return RenderReset(event), nil
	case "/radar":
		current, err := s.radar.Fetch(ctx)
		if err != nil {
			return "radar failed: " + err.Error(), nil
		}
		return s.radar.RenderText(current), nil
	case "/help":
		return helpText(), mainKeyboard()
	default:
		return helpText(), mainKeyboard()
	}
}

func (s *Service) handleCallback(ctx context.Context, query *models.CallbackQuery) {
	switch query.Data {
	case "quick:limits":
		s.answerCallback(ctx, query.ID, "refreshing limits")
		reply, _ := s.handleCommand(ctx, "/limits")
		_, _ = s.send(ctx, reply, nil)
		return
	case "quick:profile":
		s.answerCallback(ctx, query.ID, "loading profile")
		reply, _ := s.handleCommand(ctx, "/profile")
		_, _ = s.send(ctx, reply, mainKeyboard())
		return
	case "quick:grants":
		s.answerCallback(ctx, query.ID, "loading reset grants")
		reply, _ := s.handleCommand(ctx, "/grants")
		_, _ = s.send(ctx, reply, mainKeyboard())
		return
	case "quick:health":
		s.answerCallback(ctx, query.ID, "checking health")
		reply, _ := s.handleCommand(ctx, "/health")
		_, _ = s.send(ctx, reply, mainKeyboard())
		return
	case "quick:refresh":
		s.answerCallback(ctx, query.ID, "forcing refresh")
		reply, _ := s.handleCommand(ctx, "/refresh")
		_, _ = s.send(ctx, reply, nil)
		return
	case "quick:radar":
		s.answerCallback(ctx, query.ID, "checking radar")
		reply, _ := s.handleCommand(ctx, "/radar")
		_, _ = s.send(ctx, reply, nil)
		return
	case "quick:stats":
		s.answerCallback(ctx, query.ID, "loading stats")
		reply, _ := s.handleCommand(ctx, "/stats")
		_, _ = s.send(ctx, reply, mainKeyboard())
		return
	case "quick:settings":
		interval, _ := s.controller.PollInterval(ctx)
		s.answerCallback(ctx, query.ID, "settings")
		s.editCallbackMessage(ctx, query, settingsText(interval), settingsKeyboard(interval))
		return
	}
	if !strings.HasPrefix(query.Data, "settings:poll:") {
		return
	}
	raw := strings.TrimPrefix(query.Data, "settings:poll:")
	interval, err := time.ParseDuration(raw)
	if err != nil {
		s.answerCallback(ctx, query.ID, "bad interval")
		return
	}
	if err := s.controller.SetPollInterval(ctx, interval); err != nil {
		s.answerCallback(ctx, query.ID, "could not update interval")
		return
	}
	s.answerCallback(ctx, query.ID, "poll interval updated: "+server.FormatDuration(interval))
	s.editCallbackMessage(ctx, query, settingsText(interval), settingsKeyboard(interval))
}

func (s *Service) answerCallback(ctx context.Context, id, text string) {
	if s.bot == nil {
		return
	}
	_, _ = s.bot.AnswerCallbackQuery(ctx, &tgbot.AnswerCallbackQueryParams{CallbackQueryID: id, Text: text, CacheTime: 1})
}

func (s *Service) editCallbackMessage(ctx context.Context, query *models.CallbackQuery, text string, markup models.ReplyMarkup) {
	if s.bot == nil || query.Message.Message == nil {
		_, _ = s.send(ctx, text, markup)
		return
	}
	_, err := s.bot.EditMessageText(ctx, &tgbot.EditMessageTextParams{
		ChatID:      query.Message.Message.Chat.ID,
		MessageID:   query.Message.Message.ID,
		Text:        text,
		ParseMode:   parseMode(text),
		ReplyMarkup: markup,
	})
	if err != nil {
		_, _ = s.send(ctx, text, markup)
	}
}

func (s *Service) retryDeliveries(ctx context.Context) {
	if s.deliveries == nil {
		return
	}
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		s.retryDeliveriesOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Service) retryDeliveriesOnce(ctx context.Context) {
	target := s.target()
	deliveries, err := s.deliveries.PendingDeliveries(ctx, target, 10)
	if err != nil {
		return
	}
	for _, delivery := range deliveries {
		event, ok, err := s.deliveries.LoadResetEvent(ctx, delivery.EventID)
		if err != nil || !ok {
			continue
		}
		if err := s.deliveries.MarkDeliverySending(ctx, delivery.EventID, target); err != nil {
			continue
		}
		message, err := s.send(ctx, RenderReset(event), nil)
		if err != nil {
			_ = s.deliveries.MarkDeliveryAttempt(ctx, delivery.EventID, target, false, err.Error(), "")
			continue
		}
		messageID := ""
		if message != nil {
			messageID = strconv.Itoa(message.ID)
		}
		_ = s.deliveries.MarkDeliveryAttempt(ctx, delivery.EventID, target, true, "", messageID)
	}
	warningDeliveries, err := s.deliveries.PendingWarningDeliveries(ctx, target, 10)
	if err != nil {
		return
	}
	for _, delivery := range warningDeliveries {
		warning, ok, err := s.deliveries.LoadWarningEvent(ctx, delivery.EventID)
		if err != nil || !ok {
			continue
		}
		if err := s.deliveries.MarkWarningDeliverySending(ctx, delivery.EventID, target); err != nil {
			continue
		}
		message, err := s.send(ctx, RenderLimitWarning(warning), nil)
		if err != nil {
			_ = s.deliveries.MarkWarningDeliveryAttempt(ctx, delivery.EventID, target, false, err.Error(), "")
			continue
		}
		messageID := ""
		if message != nil {
			messageID = strconv.Itoa(message.ID)
		}
		_ = s.deliveries.MarkWarningDeliveryAttempt(ctx, delivery.EventID, target, true, "", messageID)
	}
	grantWarningDeliveries, err := s.deliveries.PendingGrantExpiryWarningDeliveries(ctx, target, 10)
	if err != nil {
		return
	}
	for _, delivery := range grantWarningDeliveries {
		warning, ok, err := s.deliveries.LoadGrantExpiryWarningEvent(ctx, delivery.EventID)
		if err != nil || !ok {
			continue
		}
		if err := s.deliveries.MarkGrantExpiryWarningDeliverySending(ctx, delivery.EventID, target); err != nil {
			continue
		}
		message, err := s.send(ctx, RenderGrantExpiryWarning(warning), nil)
		if err != nil {
			_ = s.deliveries.MarkGrantExpiryWarningDeliveryAttempt(ctx, delivery.EventID, target, false, err.Error(), "")
			continue
		}
		messageID := ""
		if message != nil {
			messageID = strconv.Itoa(message.ID)
		}
		_ = s.deliveries.MarkGrantExpiryWarningDeliveryAttempt(ctx, delivery.EventID, target, true, "", messageID)
	}
	resetGrantDeliveries, err := s.deliveries.PendingResetGrantDeliveries(ctx, target, 10)
	if err != nil {
		return
	}
	for _, delivery := range resetGrantDeliveries {
		event, ok, err := s.deliveries.LoadResetGrantEvent(ctx, delivery.EventID)
		if err != nil || !ok {
			continue
		}
		if err := s.deliveries.MarkResetGrantDeliverySending(ctx, delivery.EventID, target); err != nil {
			continue
		}
		message, err := s.send(ctx, RenderResetGrant(event), nil)
		if err != nil {
			_ = s.deliveries.MarkResetGrantDeliveryAttempt(ctx, delivery.EventID, target, false, err.Error(), "")
			continue
		}
		messageID := ""
		if message != nil {
			messageID = strconv.Itoa(message.ID)
		}
		_ = s.deliveries.MarkResetGrantDeliveryAttempt(ctx, delivery.EventID, target, true, "", messageID)
	}
	radarDeliveries, err := s.deliveries.PendingRadarAlertDeliveries(ctx, target, 10)
	if err != nil {
		return
	}
	for _, delivery := range radarDeliveries {
		alert, ok, err := s.deliveries.LoadRadarAlertEvent(ctx, delivery.EventID)
		if err != nil || !ok {
			continue
		}
		if err := s.deliveries.MarkRadarAlertDeliverySending(ctx, delivery.EventID, target); err != nil {
			continue
		}
		message, err := s.send(ctx, RenderRadarProbability(alert), mainKeyboard())
		if err != nil {
			_ = s.deliveries.MarkRadarAlertDeliveryAttempt(ctx, delivery.EventID, target, false, err.Error(), "")
			continue
		}
		messageID := ""
		if message != nil {
			messageID = strconv.Itoa(message.ID)
		}
		_ = s.deliveries.MarkRadarAlertDeliveryAttempt(ctx, delivery.EventID, target, true, "", messageID)
	}
}

func (s *Service) manualRefreshRetryAfter() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastManualRefresh.IsZero() {
		return 0
	}
	next := s.lastManualRefresh.Add(20 * time.Second)
	if time.Now().Before(next) {
		return time.Until(next)
	}
	return 0
}

func (s *Service) markManualRefresh() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastManualRefresh = time.Now()
}

func (s *Service) send(ctx context.Context, text string, markup models.ReplyMarkup) (*models.Message, error) {
	if s.bot == nil {
		return nil, nil
	}
	started := time.Now()
	message, err := s.bot.SendMessage(ctx, &tgbot.SendMessageParams{
		ChatID:      s.cfg.ChatID,
		Text:        text,
		ParseMode:   parseMode(text),
		ReplyMarkup: markup,
	})
	if err == nil {
		s.logger.Info("telegram send completed", "duration", time.Since(started).Round(time.Millisecond), "formatted", parseMode(text) != "")
		return message, nil
	}
	if parseMode(text) == "" || isUncertainSendError(err) {
		return message, err
	}
	s.logger.Warn("telegram formatted send failed; retrying plain text", "error", err)
	message, err = s.bot.SendMessage(ctx, &tgbot.SendMessageParams{
		ChatID:      s.cfg.ChatID,
		Text:        stripTelegramHTML(text),
		ReplyMarkup: markup,
	})
	if err == nil {
		s.logger.Info("telegram send completed", "duration", time.Since(started).Round(time.Millisecond), "formatted", false, "fallback", true)
	}
	return message, err
}

func isUncertainSendError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "context deadline exceeded") ||
		strings.Contains(message, "client.timeout") ||
		strings.Contains(message, "i/o timeout") ||
		strings.Contains(message, "timeout awaiting")
}

func (s *Service) authorized(update *models.Update) bool {
	chatID, userID, ok := updateIdentity(update)
	if !ok || chatID != s.cfg.ChatID {
		return false
	}
	if len(s.cfg.AllowedUserIDs) == 0 {
		return true
	}
	for _, allowed := range s.cfg.AllowedUserIDs {
		if userID == allowed {
			return true
		}
	}
	return false
}

func (s *Service) target() string {
	return "telegram:" + strconv.FormatInt(s.cfg.ChatID, 10)
}

func updateIdentity(update *models.Update) (chatID int64, userID int64, ok bool) {
	if update.Message != nil {
		chatID = update.Message.Chat.ID
		if update.Message.From != nil {
			userID = update.Message.From.ID
		}
		return chatID, userID, true
	}
	if update.CallbackQuery != nil {
		userID = update.CallbackQuery.From.ID
		if msg := update.CallbackQuery.Message.Message; msg != nil {
			return msg.Chat.ID, userID, true
		}
	}
	return 0, 0, false
}

func (s *Service) logUnauthorized(update *models.Update) {
	chatID, userID, ok := updateIdentity(update)
	if !ok {
		s.logger.Warn("telegram update ignored without identity", "update_id", update.ID)
		return
	}
	s.logger.Warn("telegram update ignored by allowlist", "update_id", update.ID, "chat_id", chatID, "user_id", userID)
}

func commandName(text string) string {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return ""
	}
	command := strings.ToLower(strings.Split(fields[0], "@")[0])
	if !strings.HasPrefix(command, "/") {
		return "message"
	}
	return command
}

func settingsKeyboard(current time.Duration) models.InlineKeyboardMarkup {
	button := func(label string, interval time.Duration) models.InlineKeyboardButton {
		text := label
		if current == interval {
			text = "· " + label + " ·"
		}
		return models.InlineKeyboardButton{Text: text, CallbackData: "settings:poll:" + server.FormatDuration(interval)}
	}
	return models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{
			button("1m", time.Minute),
			button("2m", 2*time.Minute),
			button("5m", 5*time.Minute),
		},
		{
			button("10m", 10*time.Minute),
			button("15m", 15*time.Minute),
		},
	}}
}

func mainKeyboard() models.InlineKeyboardMarkup {
	return models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{
			{Text: "Limits", CallbackData: "quick:limits"},
			{Text: "Grants", CallbackData: "quick:grants"},
		},
		{
			{Text: "Profile", CallbackData: "quick:profile"},
			{Text: "Refresh", CallbackData: "quick:refresh"},
		},
		{
			{Text: "Radar", CallbackData: "quick:radar"},
			{Text: "Health", CallbackData: "quick:health"},
		},
		{
			{Text: "Stats", CallbackData: "quick:stats"},
			{Text: "Settings", CallbackData: "quick:settings"},
		},
	}}
}

func settingsText(interval time.Duration) string {
	return "<b>Polling interval</b>\ncurrent: <code>" + html.EscapeString(server.FormatDuration(interval)) + "</code>\n\nChoose how often Scriba polls live Codex limits."
}

func helpText() string {
	return strings.Join([]string{
		"<b>Commands</b>",
		"<code>/status</code> server health and polling state",
		"<code>/health</code> poll/auth health check",
		"<code>/stats</code> storage and delivery stats",
		"<code>/limits</code> current Codex limits",
		"<code>/grants</code> detailed Codex reset grants",
		"<code>/profile</code> Codex profile stats",
		"<code>/refresh</code> force a live poll",
		"<code>/lastreset</code> latest reset event",
		"<code>/settings</code> polling settings",
		"<code>/radar</code> public reset radar",
	}, "\n")
}

func parseMode(text string) models.ParseMode {
	if strings.Contains(text, "<b>") || strings.Contains(text, "<code>") || strings.Contains(text, "<pre>") || strings.Contains(text, "<blockquote") {
		return models.ParseModeHTML
	}
	return ""
}

var telegramHTMLTagPattern = regexp.MustCompile(`</?(?:b|strong|i|em|u|ins|s|strike|del|span|tg-spoiler|a|code|pre|blockquote)(?:\s+[^>]*)?>`)

func stripTelegramHTML(text string) string {
	stripped := telegramHTMLTagPattern.ReplaceAllString(text, "")
	return html.UnescapeString(stripped)
}
