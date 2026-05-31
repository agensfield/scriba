package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
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
}

type OffsetStore interface {
	GetTelegramOffset(context.Context, string) (int64, bool, error)
	SetTelegramOffset(context.Context, string, int64) error
}

type DeliveryStore interface {
	EnsureDelivery(context.Context, string, string) (store.Delivery, error)
	MarkDeliveryAttempt(context.Context, string, string, bool, string) error
}

type Service struct {
	cfg        BotConfig
	controller Controller
	offsets    OffsetStore
	deliveries DeliveryStore
	radar      radar.Client
	bot        *tgbot.Bot
	botRef     string
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
	s.bot.Start(ctx)
}

func (s *Service) NotifyBaseline(ctx context.Context, notice server.BaselineNotice) error {
	return s.send(ctx, RenderBaseline(notice), nil)
}

func (s *Service) NotifyReset(ctx context.Context, event resetwatch.Event) error {
	target := s.target()
	if s.deliveries != nil {
		if _, err := s.deliveries.EnsureDelivery(ctx, event.ID, target); err != nil {
			return err
		}
	}
	err := s.send(ctx, RenderReset(event), nil)
	if s.deliveries == nil {
		return err
	}
	if err != nil {
		_ = s.deliveries.MarkDeliveryAttempt(ctx, event.ID, target, false, err.Error())
		return err
	}
	return s.deliveries.MarkDeliveryAttempt(ctx, event.ID, target, true, "")
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
		_ = s.send(ctx, reply, markup)
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
		return "i'm alive. started tracking Codex limits.", nil
	case "/status":
		interval, err := s.controller.PollInterval(ctx)
		if err != nil {
			return "status: alive\npoll interval: unknown (" + err.Error() + ")", nil
		}
		return "status: alive\npoll interval: " + interval.String(), nil
	case "/settings":
		interval, _ := s.controller.PollInterval(ctx)
		return "settings\npoll interval: " + interval.String(), settingsKeyboard()
	case "/refresh", "/limits":
		result, err := s.controller.RefreshNow(ctx)
		if errors.Is(err, server.ErrRefreshInProgress) {
			return "refresh already in progress. hold the line.", nil
		}
		if err != nil {
			return "refresh failed: " + err.Error(), nil
		}
		return RenderLimits(result.Observation), nil
	case "/radar":
		current, err := s.radar.Fetch(ctx)
		if err != nil {
			return "radar failed: " + err.Error(), nil
		}
		return s.radar.RenderText(current), nil
	default:
		return "commands: /status /limits /refresh /radar /settings", nil
	}
}

func (s *Service) handleCallback(ctx context.Context, query *models.CallbackQuery) {
	if !strings.HasPrefix(query.Data, "settings:poll:") {
		return
	}
	raw := strings.TrimPrefix(query.Data, "settings:poll:")
	interval, err := time.ParseDuration(raw)
	if err != nil {
		_ = s.send(ctx, "bad interval: "+raw, settingsKeyboard())
		return
	}
	if err := s.controller.SetPollInterval(ctx, interval); err != nil {
		_ = s.send(ctx, "could not update interval: "+err.Error(), settingsKeyboard())
		return
	}
	_ = s.send(ctx, "poll interval updated: "+interval.String(), settingsKeyboard())
}

func (s *Service) send(ctx context.Context, text string, markup models.ReplyMarkup) error {
	if s.bot == nil {
		return nil
	}
	_, err := s.bot.SendMessage(ctx, &tgbot.SendMessageParams{
		ChatID:      s.cfg.ChatID,
		Text:        text,
		ReplyMarkup: markup,
	})
	return err
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

func settingsKeyboard() models.InlineKeyboardMarkup {
	return models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{
			{Text: "1m", CallbackData: "settings:poll:1m"},
			{Text: "5m", CallbackData: "settings:poll:5m"},
			{Text: "10m", CallbackData: "settings:poll:10m"},
			{Text: "30m", CallbackData: "settings:poll:30m"},
		},
	}}
}

func RenderBaseline(notice server.BaselineNotice) string {
	return "Codex usage tracker is alive\n" + renderAccount(notice.Account) + "\n\n" + renderWindows(notice.Windows)
}

func RenderLimits(obs resetwatch.Observation) string {
	return "Codex limits\n" + renderAccount(obs.Account) + "\n\n" + renderWindows(obs.Windows)
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

func renderWindows(windows []resetwatch.Window) string {
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
		b.WriteString(renderWindow(label, window, ""))
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
		b.WriteString(label)
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
	return fmt.Sprintf("%s%-8s %s %3.0f%% reset %s", prefix, label, bar(percent), percent, formatTime(window.ResetAt))
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
	return strings.Repeat("#", filled) + strings.Repeat(".", 10-filled)
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	return t.Local().Format("Mon 15:04")
}
