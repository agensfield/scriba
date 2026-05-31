package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"math"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dustin/go-humanize"
	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/agensfield/scriba/internal/radar"
	"github.com/agensfield/scriba/internal/remote"
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
	Stats(context.Context) (server.Stats, error)
}

type OffsetStore interface {
	GetTelegramOffset(context.Context, string) (int64, bool, error)
	SetTelegramOffset(context.Context, string, int64) error
}

type DeliveryStore interface {
	EnsureDelivery(context.Context, string, string) (store.Delivery, error)
	MarkDeliveryAttempt(context.Context, string, string, bool, string, string) error
	PendingDeliveries(context.Context, string, int) ([]store.Delivery, error)
	LoadResetEvent(context.Context, string) (resetwatch.Event, bool, error)
	EnsureWarningDelivery(context.Context, string, string) (store.Delivery, error)
	MarkWarningDeliveryAttempt(context.Context, string, string, bool, string, string) error
	PendingWarningDeliveries(context.Context, string, int) ([]store.Delivery, error)
	LoadWarningEvent(context.Context, string) (resetwatch.WarningEvent, bool, error)
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
		{Command: "stats", Description: "storage and delivery stats"},
		{Command: "limits", Description: "show current Codex limits"},
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
		interval, err := s.controller.PollInterval(ctx)
		if err != nil {
			return "<b>Scriba status</b>\nalive: yes\npoll interval: unknown\n<code>" + html.EscapeString(err.Error()) + "</code>", nil
		}
		last := "none yet"
		if event, ok, err := s.controller.LastResetEvent(ctx); err == nil && ok {
			last = fmt.Sprintf("%s · %s -> %s", event.ResetKind, formatTime(event.PreviousResetAt), formatTime(event.CurrentResetAt))
		}
		return "<b>Scriba status</b>\n<pre>" + html.EscapeString(fmt.Sprintf("%-14s %s\n%-14s %s\n%-14s %s\n%-14s %s", "alive", "yes", "poll interval", interval.String(), "last reset", last, "details", "/stats")) + "</pre>", mainKeyboard()
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
	s.answerCallback(ctx, query.ID, "poll interval updated: "+interval.String())
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
	if parseMode(text) == "" {
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
		return models.InlineKeyboardButton{Text: text, CallbackData: "settings:poll:" + interval.String()}
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
			{Text: "Refresh", CallbackData: "quick:refresh"},
		},
		{
			{Text: "Stats", CallbackData: "quick:stats"},
			{Text: "Radar", CallbackData: "quick:radar"},
		},
		{
			{Text: "Settings", CallbackData: "quick:settings"},
		},
	}}
}

func settingsText(interval time.Duration) string {
	return "<b>Polling interval</b>\ncurrent: <code>" + html.EscapeString(interval.String()) + "</code>\n\nChoose how often Scriba polls live Codex limits."
}

func helpText() string {
	return strings.Join([]string{
		"<b>Commands</b>",
		"<code>/status</code> server health and polling state",
		"<code>/stats</code> storage and delivery stats",
		"<code>/limits</code> current Codex limits",
		"<code>/refresh</code> force a live poll",
		"<code>/lastreset</code> latest reset event",
		"<code>/settings</code> polling settings",
		"<code>/radar</code> public reset radar",
	}, "\n")
}

func RenderBaseline(notice server.BaselineNotice) string {
	return "<b>Scriba is alive</b>\nstarted tracking Codex limits.\n" + renderFreshness(notice.ObservedAt) + "\n\n" + renderAccount(notice.Account) + "\n\n" + renderWindows(notice.Windows, "current")
}

func RenderLimits(obs resetwatch.Observation) string {
	return "<b>Codex limits</b>\n" + renderFreshness(obs.ObservedAt) + "\n\n" + renderAccount(obs.Account) + "\n\n" + renderWindows(obs.Windows, "current")
}

func RenderReset(event resetwatch.Event) string {
	var b strings.Builder
	b.WriteString("<b>Codex reset notification</b>\n")
	if joke := resetwatch.JokeText(event.JokeID); joke != "" {
		b.WriteString(html.EscapeString(joke))
		b.WriteString("\n")
	}
	b.WriteString(renderAccount(event.Account))
	b.WriteString("\n\n")
	b.WriteString("<b>Trigger</b>\n")
	b.WriteString("<pre>")
	b.WriteString(html.EscapeString(fmt.Sprintf("%-8s %s", "window", sectionLabel(event.PrimaryTriggerLabel))))
	b.WriteString("\n")
	b.WriteString(html.EscapeString(fmt.Sprintf("%-8s %s", "kind", event.ResetKind)))
	b.WriteString("\n")
	b.WriteString(html.EscapeString(fmt.Sprintf("%-8s %s", "before", formatTime(event.PreviousResetAt))))
	b.WriteString("\n")
	b.WriteString(html.EscapeString(fmt.Sprintf("%-8s %s", "after", formatTime(event.CurrentResetAt))))
	if !event.DetectedAt.IsZero() {
		b.WriteString("\n")
		b.WriteString(html.EscapeString(fmt.Sprintf("%-8s %s", "seen", formatFreshTime(event.DetectedAt))))
	}
	b.WriteString("</pre>")

	prev := windowsFromSnapshot(event.PreviousSnapshotJSON)
	current := windowsFromSnapshot(event.CurrentSnapshotJSON)
	if len(prev) > 0 || len(current) > 0 {
		b.WriteString("\n\n")
		b.WriteString(renderBeforeAfter(prev, current))
	}
	return b.String()
}

func RenderLimitWarning(warning resetwatch.WarningEvent) string {
	var b strings.Builder
	b.WriteString("<b>Codex limit warning</b>\n")
	b.WriteString(renderAccount(warning.Account))
	b.WriteString("\n\n")
	b.WriteString("<b>")
	b.WriteString(html.EscapeString(sectionLabel(warning.Label)))
	b.WriteString("</b>\n")
	b.WriteString("<pre>")
	b.WriteString(html.EscapeString(fmt.Sprintf("%-10s %s", "left", formatPercent(warning.RemainingPercent))))
	b.WriteString("\n")
	b.WriteString(html.EscapeString(fmt.Sprintf("%-10s %d%%", "checkpoint", warning.ThresholdRemaining)))
	b.WriteString("\n")
	b.WriteString(html.EscapeString(fmt.Sprintf("%-10s %s", "used", formatPercent(warning.UsedPercent))))
	b.WriteString("\n")
	b.WriteString(html.EscapeString(fmt.Sprintf("%-10s %s", "reset", formatTime(warning.ResetAt))))
	if !warning.DetectedAt.IsZero() {
		b.WriteString("\n")
		b.WriteString(html.EscapeString(fmt.Sprintf("%-10s %s", "seen", formatFreshTime(warning.DetectedAt))))
	}
	b.WriteString("</pre>")
	return b.String()
}

func RenderStats(stats server.Stats, environment string, telegramEnabled bool) string {
	var b strings.Builder
	b.WriteString("<b>Scriba stats</b>\n")
	b.WriteString(renderRuntimeStats(stats, environment, telegramEnabled))
	if stats.Store.LatestObservation != nil {
		b.WriteString("\n\n")
		b.WriteString(renderObservationStats(*stats.Store.LatestObservation))
	}
	b.WriteString("\n\n")
	b.WriteString(renderStorageStats(stats.Store))
	b.WriteString("\n\n")
	b.WriteString(renderDeliveryStats("Reset deliveries", stats.Store.ResetDeliveries))
	b.WriteString("\n\n")
	b.WriteString(renderDeliveryStats("Warning deliveries", stats.Store.WarningDeliveries))
	if stats.Store.LastReset != nil || stats.Store.LastWarning != nil {
		b.WriteString("\n\n")
		b.WriteString(renderRecentStats(stats.Store))
	}
	return b.String()
}

func renderRuntimeStats(stats server.Stats, environment string, telegramEnabled bool) string {
	rows := []string{
		fmt.Sprintf("%-12s %s", "poll", stats.PollInterval.String()),
		fmt.Sprintf("%-12s %dd", "retention", stats.ObservationRetentionDays),
	}
	if environment != "" {
		rows = append(rows, fmt.Sprintf("%-12s %s", "env", environment))
		rows = append(rows, fmt.Sprintf("%-12s %t", "telegram", telegramEnabled))
	}
	return "<pre>" + html.EscapeString(strings.Join(rows, "\n")) + "</pre>"
}

func renderObservationStats(latest store.ObservationSummary) string {
	rows := []string{
		fmt.Sprintf("%-12s %s", "latest", formatFreshTime(latest.ObservedAt)),
		fmt.Sprintf("%-12s %d", "latest win", latest.Windows),
		fmt.Sprintf("%-12s %s", "account", latest.AccountLabel),
	}
	if latest.AccountEmail != "" || latest.AccountPlan != "" {
		account := latest.AccountEmail
		if latest.AccountEmail != "" && latest.AccountPlan != "" {
			account += " · "
		}
		account += latest.AccountPlan
		rows = append(rows, fmt.Sprintf("%-12s %s", "plan", account))
	}
	return "<b>Observation</b>\n<pre>" + html.EscapeString(strings.Join(rows, "\n")) + "</pre>"
}

func renderStorageStats(stats store.Stats) string {
	counts := stats.Counts
	rows := []string{
		fmt.Sprintf("%-13s %s", "db", formatBytes(stats.DBFiles.TotalBytes)),
		fmt.Sprintf("%-13s %s", "main", formatBytes(stats.DBFiles.MainBytes)),
		fmt.Sprintf("%-13s %s", "wal", formatBytes(stats.DBFiles.WALBytes)),
		fmt.Sprintf("%-13s %d", "accounts", counts["accounts"]),
		fmt.Sprintf("%-13s %d", "stored polls", counts["limit_observations"]),
		fmt.Sprintf("%-13s %d", "stored win", counts["observed_windows"]),
		fmt.Sprintf("%-13s %d", "tracked win", counts["limit_windows"]),
		fmt.Sprintf("%-13s %d", "resets", counts["reset_events"]),
		fmt.Sprintf("%-13s %d", "warnings", counts["limit_warning_events"]),
	}
	return "<b>Storage</b>\n<pre>" + html.EscapeString(strings.Join(rows, "\n")) + "</pre>"
}

func renderDeliveryStats(title string, counts map[string]store.DeliveryCounts) string {
	rows := make([]string, 0, len(counts)+1)
	for _, status := range []string{"pending", "failed", "delivered"} {
		count := counts[status]
		rows = append(rows, fmt.Sprintf("%-10s %3d  attempts %d", status, count.Count, count.Attempts))
	}
	return "<b>" + html.EscapeString(title) + "</b>\n<pre>" + html.EscapeString(strings.Join(rows, "\n")) + "</pre>"
}

func renderRecentStats(stats store.Stats) string {
	rows := []string{}
	if stats.LastReset != nil {
		rows = append(rows, fmt.Sprintf("%-8s %s · %s · %s", "reset", sectionLabel(stats.LastReset.Trigger), stats.LastReset.Kind, formatFreshTime(stats.LastReset.DetectedAt)))
	}
	if stats.LastWarning != nil {
		rows = append(rows, fmt.Sprintf("%-8s %s · %d%% left · %s", "warning", sectionLabel(stats.LastWarning.Label), stats.LastWarning.ThresholdRemaining, formatFreshTime(stats.LastWarning.DetectedAt)))
	}
	return "<b>Recent</b>\n<pre>" + html.EscapeString(strings.Join(rows, "\n")) + "</pre>"
}

func formatBytes(value int64) string {
	if value <= 0 {
		return "0 B"
	}
	return humanize.Bytes(uint64(value))
}

func renderFreshness(t time.Time) string {
	if t.IsZero() {
		return "<i>observed unknown</i>"
	}
	return "<i>observed " + html.EscapeString(formatFreshTime(t)) + "</i>"
}

func formatFreshTime(t time.Time) string {
	return humanize.Time(t) + " · " + t.Local().Format("Mon 15:04:05")
}

func formatPercent(value float64) string {
	return fmt.Sprintf("%.0f%%", value)
}

func renderAccount(account resetwatch.Account) string {
	label := account.Label
	if label == "" {
		label = "unknown"
	}
	var b strings.Builder
	b.WriteString("<b>Account</b> ")
	b.WriteString(html.EscapeString(label))
	if account.Email != "" || account.Plan != "" {
		b.WriteString("\n")
		b.WriteString("<code>")
		if account.Email != "" {
			b.WriteString(html.EscapeString(account.Email))
		}
		if account.Email != "" && account.Plan != "" {
			b.WriteString(" · ")
		}
		if account.Plan != "" {
			b.WriteString(html.EscapeString(account.Plan))
		}
		b.WriteString("</code>")
	}
	return b.String()
}

func renderWindows(windows []resetwatch.Window, rowLabel string) string {
	byLabel := map[string]resetwatch.Window{}
	for _, window := range windows {
		byLabel[window.Label] = window
	}
	primaryLabels := []string{resetwatch.LabelWeeklyLimit, resetwatch.LabelFiveHour}
	secondaryLabels := []string{resetwatch.LabelSparkWeekly, resetwatch.LabelSparkFive, resetwatch.LabelReviewWeek}
	var b strings.Builder
	if section := renderWindowSection("Primary", primaryLabels, byLabel, rowLabel); section != "" {
		b.WriteString(section)
	}
	if section := renderWindowSection("Secondary", secondaryLabels, byLabel, rowLabel); section != "" {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(section)
	}
	return b.String()
}

func renderBeforeAfter(prev, current []resetwatch.Window) string {
	labels := []string{resetwatch.LabelWeeklyLimit, resetwatch.LabelFiveHour, resetwatch.LabelSparkWeekly, resetwatch.LabelSparkFive, resetwatch.LabelReviewWeek}
	prevByLabel := mapWindows(prev)
	currentByLabel := mapWindows(current)
	var b strings.Builder
	for _, label := range labels {
		before, beforeOK := prevByLabel[label]
		after, afterOK := currentByLabel[label]
		if !beforeOK && !afterOK {
			continue
		}
		b.WriteString("<b>")
		b.WriteString(html.EscapeString(sectionLabel(label)))
		b.WriteString("</b>\n")
		b.WriteString("<pre>")
		if beforeOK {
			b.WriteString(renderWindow("before", before, ""))
			b.WriteString("\n")
		}
		if afterOK {
			b.WriteString(renderWindow("after", after, ""))
			b.WriteString("\n")
		}
		b.WriteString("</pre>")
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func renderWindowSection(title string, labels []string, byLabel map[string]resetwatch.Window, rowLabel string) string {
	var rows []string
	for _, label := range labels {
		window, ok := byLabel[label]
		if !ok {
			continue
		}
		rows = append(rows, renderWindow(rowLabel, window, sectionLabel(label)))
	}
	if len(rows) == 0 {
		return ""
	}
	return "<b>" + html.EscapeString(title) + "</b>\n<pre>" + html.EscapeString(strings.Join(rows, "\n\n")) + "</pre>"
}

func renderWindow(label string, window resetwatch.Window, prefix string) string {
	percent := 0.0
	if window.UsedPercent != nil {
		percent = *window.UsedPercent
	}
	title := prefix
	if title == "" {
		title = label
	}
	return fmt.Sprintf("%-13s %s %3.0f%% used\n%-13s %s", title, bar(percent), percent, "reset", formatTime(window.ResetAt))
}

func sectionLabel(label string) string {
	switch label {
	case resetwatch.LabelWeeklyLimit:
		return "Weekly"
	case resetwatch.LabelFiveHour:
		return "5h"
	case resetwatch.LabelSparkWeekly:
		return "Spark weekly"
	case resetwatch.LabelSparkFive:
		return "Spark 5h"
	case resetwatch.LabelReviewWeek:
		return "Review weekly"
	default:
		return label
	}
}

func mapWindows(windows []resetwatch.Window) map[string]resetwatch.Window {
	byLabel := make(map[string]resetwatch.Window, len(windows))
	for _, window := range windows {
		byLabel[window.Label] = window
	}
	return byLabel
}

func windowsFromSnapshot(snapshot []byte) []resetwatch.Window {
	if len(snapshot) == 0 {
		return nil
	}
	var result remote.ProbeResult
	if err := unmarshalSnapshot(snapshot, &result); err != nil {
		return nil
	}
	return resetwatch.FromMetricLines(result.Lines)
}

func unmarshalSnapshot(data []byte, target any) error {
	return json.Unmarshal(data, target)
}

func bar(percent float64) string {
	const width = 10
	filled := int(math.Ceil(percent / (100 / width)))
	if filled < 0 {
		filled = 0
	}
	if filled > width {
		filled = width
	}
	return strings.Repeat("▰", filled) + strings.Repeat("▱", width-filled)
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

func formatTime(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	return t.Local().Format("Mon 15:04")
}
