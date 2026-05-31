package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

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
}

type Service struct {
	cfg               BotConfig
	controller        Controller
	offsets           OffsetStore
	deliveries        DeliveryStore
	radar             radar.Client
	bot               *tgbot.Bot
	botRef            string
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
	svc := &Service{cfg: cfg, controller: controller, offsets: offsets, deliveries: deliveries, radar: radarClient, botRef: "default"}
	var options []tgbot.Option
	if offsets != nil {
		if offset, ok, err := offsets.GetTelegramOffset(context.Background(), svc.botRef); err != nil {
			return nil, err
		} else if ok {
			options = append(options, tgbot.WithInitialOffset(offset+1))
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
	_ = s.RegisterCommands(ctx)
	go s.retryDeliveries(ctx)
	s.bot.Start(ctx)
}

func (s *Service) RegisterCommands(ctx context.Context) error {
	if s.bot == nil {
		return nil
	}
	_, err := s.bot.SetMyCommands(ctx, &tgbot.SetMyCommandsParams{Commands: []models.BotCommand{
		{Command: "status", Description: "server health and polling state"},
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

func (s *Service) handleUpdate(ctx context.Context, b *tgbot.Bot, update *models.Update) {
	if s.offsets != nil {
		defer func() { _ = s.offsets.SetTelegramOffset(ctx, s.botRef, update.ID) }()
	}
	if !s.authorized(update) {
		return
	}
	if update.CallbackQuery != nil {
		s.handleCallback(ctx, update.CallbackQuery)
		return
	}
	if update.Message == nil || update.Message.Text == "" {
		return
	}
	text := strings.TrimSpace(update.Message.Text)
	reply, markup := s.handleCommand(ctx, text)
	if reply != "" {
		_, _ = s.send(ctx, reply, markup)
	}
	_ = b
}

func (s *Service) handleCommand(ctx context.Context, text string) (string, models.ReplyMarkup) {
	command := strings.Fields(text)
	if len(command) == 0 {
		return "", nil
	}
	switch strings.ToLower(strings.Split(command[0], "@")[0]) {
	case "/start":
		return "Scriba is alive.\n\n" + helpText(), mainKeyboard()
	case "/status":
		interval, err := s.controller.PollInterval(ctx)
		if err != nil {
			return "status: alive\npoll interval: unknown (" + err.Error() + ")", nil
		}
		last := "none yet"
		if event, ok, err := s.controller.LastResetEvent(ctx); err == nil && ok {
			last = fmt.Sprintf("%s · %s -> %s", event.ResetKind, formatTime(event.PreviousResetAt), formatTime(event.CurrentResetAt))
		}
		return "Scriba status\nalive: yes\npoll interval: " + interval.String() + "\nlast reset: " + last, mainKeyboard()
	case "/settings":
		interval, _ := s.controller.PollInterval(ctx)
		return settingsText(interval), settingsKeyboard(interval)
	case "/limits":
		result, err := s.controller.RefreshNow(ctx)
		if errors.Is(err, server.ErrRefreshInProgress) {
			return "refresh already in progress. hold the line.", nil
		}
		if err != nil {
			return "refresh failed: " + err.Error(), nil
		}
		return RenderLimits(result.Observation), nil
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
	return s.bot.SendMessage(ctx, &tgbot.SendMessageParams{
		ChatID:      s.cfg.ChatID,
		Text:        text,
		ReplyMarkup: markup,
	})
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
			{Text: "Settings", CallbackData: "quick:settings"},
		},
	}}
}

func settingsText(interval time.Duration) string {
	return "Polling interval\ncurrent: " + interval.String() + "\n\nChoose how often Scriba polls live Codex limits."
}

func helpText() string {
	return strings.Join([]string{
		"Commands",
		"/status - server health and polling state",
		"/limits - show current Codex limits",
		"/refresh - force a live Codex poll",
		"/lastreset - show the latest reset event",
		"/settings - change poll interval",
		"/radar - public reset radar",
	}, "\n")
}

func RenderBaseline(notice server.BaselineNotice) string {
	return "Scriba is alive.\nStarted tracking Codex limits.\n\n" + renderAccount(notice.Account) + "\n\n" + renderWindows(notice.Windows, "current")
}

func RenderLimits(obs resetwatch.Observation) string {
	return "Codex limits\n" + renderAccount(obs.Account) + "\n\n" + renderWindows(obs.Windows, "current")
}

func RenderReset(event resetwatch.Event) string {
	var b strings.Builder
	b.WriteString("Codex Reset Notification\n")
	if joke := resetwatch.JokeText(event.JokeID); joke != "" {
		b.WriteString(joke)
		b.WriteString("\n")
	}
	b.WriteString(renderAccount(event.Account))
	b.WriteString("\n\n")
	b.WriteString("trigger: ")
	b.WriteString(event.PrimaryTriggerLabel)
	b.WriteString(" (")
	b.WriteString(event.ResetKind)
	b.WriteString(")\n")
	b.WriteString("before reset: ")
	b.WriteString(formatTime(event.PreviousResetAt))
	b.WriteString("\nafter reset:  ")
	b.WriteString(formatTime(event.CurrentResetAt))

	prev := windowsFromSnapshot(event.PreviousSnapshotJSON)
	current := windowsFromSnapshot(event.CurrentSnapshotJSON)
	if len(prev) > 0 || len(current) > 0 {
		b.WriteString("\n\n")
		b.WriteString(renderBeforeAfter(prev, current))
	}
	return b.String()
}

func renderAccount(account resetwatch.Account) string {
	var parts []string
	if account.Label != "" {
		parts = append(parts, account.Label)
	}
	if account.Email != "" {
		parts = append(parts, account.Email)
	}
	if account.Plan != "" {
		parts = append(parts, account.Plan)
	}
	if len(parts) == 0 {
		return "account: unknown"
	}
	return "account: " + strings.Join(parts, " · ")
}

func renderWindows(windows []resetwatch.Window, rowLabel string) string {
	byLabel := map[string]resetwatch.Window{}
	for _, window := range windows {
		byLabel[window.Label] = window
	}
	labels := []string{resetwatch.LabelWeeklyLimit, resetwatch.LabelFiveHour, resetwatch.LabelSparkWeekly, resetwatch.LabelSparkFive, resetwatch.LabelReviewWeek}
	var b strings.Builder
	for _, label := range labels {
		window, ok := byLabel[label]
		if !ok {
			continue
		}
		b.WriteString(sectionLabel(label))
		b.WriteString("\n")
		b.WriteString(renderWindow(rowLabel, window, ""))
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
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
		b.WriteString(sectionLabel(label))
		b.WriteString("\n")
		if beforeOK {
			b.WriteString(renderWindow("before", before, "  "))
			b.WriteString("\n")
		}
		if afterOK {
			b.WriteString(renderWindow("after", after, "  "))
			b.WriteString("\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func renderWindow(label string, window resetwatch.Window, prefix string) string {
	percent := 0.0
	if window.UsedPercent != nil {
		percent = *window.UsedPercent
	}
	return fmt.Sprintf("%s%-7s %s %3.0f%%  reset %s", prefix, label, bar(percent), percent, formatTime(window.ResetAt))
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
	filled := int(percent / 10)
	if filled < 0 {
		filled = 0
	}
	if filled > 10 {
		filled = 10
	}
	return strings.Repeat("█", filled) + strings.Repeat("░", 10-filled)
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	return t.Local().Format("Mon 15:04")
}
