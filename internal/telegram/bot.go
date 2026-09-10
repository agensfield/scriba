package telegram

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/agensfield/scriba/internal/accounts"
	"github.com/agensfield/scriba/internal/budget"
	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/agensfield/scriba/internal/radar"
	"github.com/agensfield/scriba/internal/remote"
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
	Accounts(context.Context) ([]store.Account, error)
	LatestObservationForAccount(context.Context, string) (resetwatch.Observation, bool, error)
	CodexActivityForAccount(context.Context, string) (server.CodexActivityResult, error)
	PlanCodexReset(context.Context, string) (server.CodexResetPlan, error)
	ConsumeCodexReset(context.Context, string, remotecodex.ResetAccountPin, remote.ResetCredit, string) (remotecodex.RateLimitResetResult, error)
	Stats(context.Context) (server.Stats, error)
	Health(context.Context) (server.Health, error)
}

type OffsetStore interface {
	GetTelegramOffset(context.Context, string) (int64, bool, error)
	SetTelegramOffset(context.Context, string, int64) error
}

type UpdateStore interface {
	StageTelegramUpdates(context.Context, string, []store.TelegramUpdateInput, time.Time) error
	DueTelegramUpdates(context.Context, string, time.Time, int) ([]store.TelegramUpdate, error)
	MarkTelegramUpdateProcessed(context.Context, string, int64, time.Time) (bool, error)
	MarkTelegramUpdateFailure(context.Context, string, int64, string, time.Time) (bool, error)
	MarkTelegramUpdateDead(context.Context, string, int64, string, time.Time) (bool, error)
}

type DeliveryStore interface {
	ClaimOutboxForTarget(context.Context, string, time.Time, time.Duration, int) ([]store.OutboxMessage, error)
	FinishOutboxSuccess(context.Context, string, string, string, time.Time) (bool, error)
	FinishOutboxFailure(context.Context, store.OutboxMessage, string, time.Time) (bool, error)
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
	pendingResets     map[string]*pendingCodexReset
	apiTimeout        time.Duration
	updates           UpdateStore
	updateWake        chan struct{}
	outboxWake        chan struct{}
	startBot          func(context.Context)
	retryLoop         func(context.Context)
	inboxLoop         func(context.Context)
}

type pendingCodexReset struct {
	Account   server.AccountIdentity
	Plan      remotecodex.RateLimitResetPlan
	RequestID string
	ChatID    int64
	UserID    int64
	ExpiresAt time.Time
	InFlight  bool
	Cancelled bool
	Result    *remotecodex.RateLimitResetResult
}

const (
	telegramPollTimeout = 30 * time.Second
	telegramHTTPTimeout = 35 * time.Second
	telegramAPITimeout  = 12 * time.Second
	resetConfirmTTL     = 10 * time.Minute
)

func NewBotService(cfg BotConfig, controller Controller, offsets OffsetStore, deliveries DeliveryStore, radarClient radar.Client) (*Service, error) {
	return newBotService(cfg, controller, offsets, deliveries, radarClient)
}

func newBotService(cfg BotConfig, controller Controller, offsets OffsetStore, deliveries DeliveryStore, radarClient radar.Client, extraOptions ...tgbot.Option) (*Service, error) {
	if strings.TrimSpace(cfg.Token) == "" {
		return nil, errors.New("telegram bot token is required")
	}
	if cfg.ChatID == 0 {
		return nil, errors.New("telegram chat id is required")
	}
	svc := &Service{cfg: cfg, controller: controller, offsets: offsets, deliveries: deliveries, radar: radarClient, botRef: "default", logger: slog.Default(), apiTimeout: telegramAPITimeout, updateWake: make(chan struct{}, 1), outboxWake: make(chan struct{}, 1)}
	if inbox, ok := offsets.(UpdateStore); ok {
		svc.updates = inbox
	}
	var options []tgbot.Option
	if offsets != nil {
		if offset, ok, err := offsets.GetTelegramOffset(context.Background(), svc.botRef); err != nil {
			return nil, err
		} else if ok {
			// go-telegram/bot sends lastUpdateID+1 to getUpdates internally.
			options = append(options, tgbot.WithInitialOffset(offset))
		}
	}
	client := tgbot.HttpClient(&http.Client{Timeout: telegramHTTPTimeout})
	options = append(options,
		tgbot.WithAllowedUpdates(tgbot.AllowedUpdates{"message", "callback_query"}),
		tgbot.WithDefaultHandler(svc.handleUpdate),
	)
	options = append(options, extraOptions...)
	// This must remain the final HTTP-client option: otherwise an option can
	// silently bypass the durable getUpdates barrier.
	if svc.updates != nil {
		client = &stagingHTTPClient{next: client, store: svc.updates, botRef: svc.botRef}
	}
	options = append(options, tgbot.WithHTTPClient(telegramPollTimeout, client))
	b, err := tgbot.New(cfg.Token, options...)
	if err != nil {
		return nil, err
	}
	svc.bot = b
	svc.startBot = b.Start
	svc.retryLoop = svc.retryDeliveries
	svc.inboxLoop = svc.processTelegramUpdates
	return svc, nil
}

func (s *Service) Start(ctx context.Context) {
	if err := s.RegisterCommands(ctx); err != nil {
		s.logger.Warn("telegram command registration failed", "error", err)
	}
	retryDone := make(chan struct{})
	inboxDone := make(chan struct{})
	workerCtx, cancelWorkers := context.WithCancel(ctx)
	go func() {
		defer close(retryDone)
		s.retryLoop(workerCtx)
	}()
	go func() { defer close(inboxDone); s.inboxLoop(workerCtx) }()
	s.startBot(ctx)
	cancelWorkers()
	<-retryDone
	<-inboxDone
}

func (s *Service) RegisterCommands(ctx context.Context) error {
	if s.bot == nil {
		return nil
	}
	ctx, cancel := s.apiContext(ctx)
	defer cancel()
	_, err := s.bot.SetMyCommands(ctx, &tgbot.SetMyCommandsParams{Commands: []models.BotCommand{
		{Command: "status", Description: "server health and polling state"},
		{Command: "health", Description: "poll/auth health check"},
		{Command: "stats", Description: "storage and delivery stats"},
		{Command: "limits", Description: "show current Codex limits"},
		{Command: "grants", Description: "show detailed Codex reset grants"},
		{Command: "reset", Description: "preview and confirm a Codex limit reset"},
		{Command: "activity", Description: "show Codex account activity"},
		{Command: "accounts", Description: "list known Codex accounts"},
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
	s.wakeOutbox()
	return nil
}

func (s *Service) NotifyLimitWarning(ctx context.Context, warning resetwatch.WarningEvent) error {
	s.wakeOutbox()
	return nil
}

func (s *Service) NotifyPacingWarning(ctx context.Context, warning budget.PacingAlert) error {
	s.wakeOutbox()
	return nil
}

func (s *Service) NotifyGrantExpiryWarning(ctx context.Context, warning resetwatch.GrantExpiryWarning) error {
	s.wakeOutbox()
	return nil
}

func (s *Service) NotifyResetGrant(ctx context.Context, event resetwatch.ResetGrantEvent) error {
	s.wakeOutbox()
	return nil
}

func (s *Service) NotifyRadarProbability(ctx context.Context, alert radar.ProbabilityAlert) error {
	s.wakeOutbox()
	return nil
}

func (s *Service) NotifyHealth(ctx context.Context, notice server.HealthNotice) error {
	_, err := s.send(ctx, RenderHealthNotice(notice), nil)
	return err
}

func (s *Service) handleUpdate(ctx context.Context, b *tgbot.Bot, update *models.Update) {
	if s.updates != nil {
		select {
		case s.updateWake <- struct{}{}:
		default:
		}
		return
	}
	if err := s.dispatchUpdate(ctx, update); err != nil {
		s.logger.Warn("telegram update failed", "update_id", update.ID, "error", err)
		return
	}
	if s.offsets != nil {
		_ = s.offsets.SetTelegramOffset(ctx, s.botRef, update.ID)
	}
	_ = b
}

func (s *Service) dispatchUpdate(ctx context.Context, update *models.Update) error {
	if !s.authorized(update) {
		s.logUnauthorized(update)
		return nil
	}
	if update.CallbackQuery != nil {
		s.logger.Info("telegram callback received", "update_id", update.ID, "kind", callbackKind(update.CallbackQuery.Data))
		return s.handleCallback(ctx, update.CallbackQuery)
	}
	if update.Message == nil || update.Message.Text == "" {
		return nil
	}
	text := strings.TrimSpace(update.Message.Text)
	s.logger.Info("telegram command received", "update_id", update.ID, "command", commandName(text))
	chatID, userID, _, _ := updateIdentity(update)
	reply, markup := s.handleCommandFor(ctx, text, chatID, userID)
	if reply != "" {
		if _, err := s.send(ctx, reply, markup); err != nil {
			s.logger.Warn("telegram command reply failed", "update_id", update.ID, "command", commandName(text), "error", err)
			return err
		}
	}
	return nil
}

func (s *Service) handleCommand(ctx context.Context, text string) (string, models.ReplyMarkup) {
	return s.handleCommandFor(ctx, text, s.cfg.ChatID, 0)
}

func (s *Service) handleCommandFor(ctx context.Context, text string, chatID, userID int64) (string, models.ReplyMarkup) {
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
		selector, err := commandAccount(command)
		if err != nil {
			return "usage: /limits [account]", nil
		}
		obs, account, stale, ok, err := s.cachedAccountObservation(ctx, selector)
		if err != nil {
			return accountCommandError("limits", err), nil
		}
		if !ok {
			return "no cached limits yet. use /refresh to fetch live Codex limits.", nil
		}
		return renderSelectedAccount(account, stale, RenderLimits(obs)), accountKeyboard(account.ID)
	case "/grants":
		selector, err := commandAccount(command)
		if err != nil {
			return "usage: /grants [account]", nil
		}
		obs, account, stale, ok, err := s.cachedAccountObservation(ctx, selector)
		if err != nil {
			return accountCommandError("reset grants", err), nil
		}
		if !ok {
			return "no cached reset grants yet. use /refresh to fetch live Codex limits.", nil
		}
		return renderSelectedAccount(account, stale, RenderResetGrantDetails(obs)), accountKeyboard(account.ID)
	case "/reset":
		selector, err := commandAccount(command)
		if err != nil {
			return "usage: /reset [account]", nil
		}
		return s.beginCodexReset(ctx, selector, chatID, userID)
	case "/activity":
		selector, err := commandAccount(command)
		if err != nil {
			return "usage: /activity [account]", nil
		}
		result, err := s.controller.CodexActivityForAccount(ctx, selector)
		if err != nil {
			return accountCommandError("activity", err), nil
		}
		if !telegramAccountIDPattern.MatchString(result.Account.ID) {
			return "activity failed.", nil
		}
		return renderSelectedAccount(result.Account, false, RenderActivity(result.Activity)), accountKeyboard(result.Account.ID)
	case "/accounts":
		if len(command) != 1 {
			return "usage: /accounts", nil
		}
		known, err := s.controller.Accounts(ctx)
		if err != nil {
			return "accounts failed.", nil
		}
		text, _ := RenderAccountsPage(known, 0)
		return text, accountsKeyboard(known, 0)
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

func (s *Service) handleCallback(ctx context.Context, query *models.CallbackQuery) error {
	if strings.HasPrefix(query.Data, "reset:v1:") {
		return s.handleResetCallback(ctx, query)
	}
	if strings.HasPrefix(query.Data, "accounts:v1:") {
		return s.handleAccountCallback(ctx, query)
	}
	switch query.Data {
	case "quick:home":
		_ = s.answerCallback(ctx, query.ID, "main menu")
		return s.editCallbackMessage(ctx, query, helpText(), mainKeyboard())
	case "quick:accounts":
		_ = s.answerCallback(ctx, query.ID, "loading accounts")
		reply, markup := s.handleCommand(ctx, "/accounts")
		return s.editCallbackMessage(ctx, query, reply, markup)
	case "quick:limits":
		_ = s.answerCallback(ctx, query.ID, "refreshing limits")
		reply, _ := s.handleCommand(ctx, "/limits")
		_, err := s.send(ctx, reply, mainKeyboard())
		return err
	case "quick:activity":
		_ = s.answerCallback(ctx, query.ID, "loading activity")
		reply, _ := s.handleCommand(ctx, "/activity")
		_, err := s.send(ctx, reply, mainKeyboard())
		return err
	case "quick:grants":
		_ = s.answerCallback(ctx, query.ID, "loading reset grants")
		reply, _ := s.handleCommand(ctx, "/grants")
		_, err := s.send(ctx, reply, mainKeyboard())
		return err
	case "quick:reset":
		_ = s.answerCallback(ctx, query.ID, "preparing reset confirmation")
		chatID, _ := callbackChatID(query)
		reply, markup := s.beginCodexReset(ctx, "", chatID, query.From.ID)
		return s.editCallbackMessage(ctx, query, reply, markup)
	case "quick:health":
		_ = s.answerCallback(ctx, query.ID, "checking health")
		reply, _ := s.handleCommand(ctx, "/health")
		_, err := s.send(ctx, reply, mainKeyboard())
		return err
	case "quick:refresh":
		_ = s.answerCallback(ctx, query.ID, "forcing refresh")
		reply, _ := s.handleCommand(ctx, "/refresh")
		_, err := s.send(ctx, reply, nil)
		return err
	case "quick:radar":
		_ = s.answerCallback(ctx, query.ID, "checking radar")
		reply, _ := s.handleCommand(ctx, "/radar")
		_, err := s.send(ctx, reply, nil)
		return err
	case "quick:stats":
		_ = s.answerCallback(ctx, query.ID, "loading stats")
		reply, _ := s.handleCommand(ctx, "/stats")
		_, err := s.send(ctx, reply, mainKeyboard())
		return err
	case "quick:settings":
		interval, _ := s.controller.PollInterval(ctx)
		_ = s.answerCallback(ctx, query.ID, "settings")
		return s.editCallbackMessage(ctx, query, settingsText(interval), settingsKeyboard(interval))
	}
	if !strings.HasPrefix(query.Data, "settings:poll:") {
		return s.answerCallback(ctx, query.ID, "expired or invalid control")
	}
	raw := strings.TrimPrefix(query.Data, "settings:poll:")
	interval, err := time.ParseDuration(raw)
	if err != nil {
		return s.answerCallback(ctx, query.ID, "bad interval")
	}
	if err := s.controller.SetPollInterval(ctx, interval); err != nil {
		return s.answerCallback(ctx, query.ID, "could not update interval")
	}
	_ = s.answerCallback(ctx, query.ID, "poll interval updated: "+server.FormatDuration(interval))
	return s.editCallbackMessage(ctx, query, settingsText(interval), settingsKeyboard(interval))
}

func (s *Service) cachedAccountObservation(ctx context.Context, selector string) (resetwatch.Observation, store.Account, bool, bool, error) {
	obs, ok, err := s.controller.LatestObservationForAccount(ctx, selector)
	if err != nil || !ok {
		return obs, store.Account{}, false, ok, err
	}
	known, err := s.controller.Accounts(ctx)
	if err != nil {
		return resetwatch.Observation{}, store.Account{}, false, false, err
	}
	accountID := store.AccountID(obs.ProviderID, obs.Account.Ref)
	account, found := accountByID(known, accountID)
	if !found {
		return resetwatch.Observation{}, store.Account{}, false, false, accounts.ErrAccountNotFound
	}
	stale := false
	if health, healthErr := s.controller.Health(ctx); healthErr == nil {
		for _, item := range health.Accounts {
			if item.Account.ID == account.ID {
				stale = item.IsStale
				return obs, account, stale, true, nil
			}
		}
		stale = health.StaleAfter > 0 && time.Since(account.LastSeenAt) > health.StaleAfter
	}
	return obs, account, stale, true, nil
}

func accountCommandError(action string, err error) string {
	var bindingErr *remotecodex.AccountBindingError
	switch {
	case errors.Is(err, accounts.ErrAccountNotFound):
		return "unknown account. use /accounts to list known accounts."
	case errors.Is(err, accounts.ErrCredentialsUnavailable):
		return action + " unavailable: this account has no usable credentials."
	case errors.As(err, &bindingErr):
		return action + " unavailable: account credentials changed. try again."
	default:
		return action + " failed."
	}
}

func (s *Service) handleAccountCallback(ctx context.Context, query *models.CallbackQuery) error {
	action, value, ok := parseAccountCallback(query.Data)
	if !ok {
		return s.answerCallback(ctx, query.ID, "expired or invalid control")
	}
	if action == "list" {
		page, err := strconv.Atoi(value)
		if err != nil {
			return s.answerCallback(ctx, query.ID, "invalid account page")
		}
		known, err := s.controller.Accounts(ctx)
		if err != nil {
			return s.answerCallback(ctx, query.ID, "accounts unavailable")
		}
		text, pages := RenderAccountsPage(known, page)
		if text == "" || page >= pages {
			return s.answerCallback(ctx, query.ID, "account page expired")
		}
		_ = s.answerCallback(ctx, query.ID, "accounts")
		return s.editCallbackMessage(ctx, query, text, accountsKeyboard(known, page))
	}

	accountID := value
	known, err := s.controller.Accounts(ctx)
	if err != nil {
		return s.answerCallback(ctx, query.ID, "account unavailable")
	}
	account, found := accountByID(known, accountID)
	if !found {
		_ = s.answerCallback(ctx, query.ID, "expired or invalid control")
		return s.editCallbackMessage(ctx, query, "account unavailable.", accountsBackKeyboard())
	}
	if action == "open" {
		_ = s.answerCallback(ctx, query.ID, "account")
		return s.editCallbackMessage(ctx, query, renderAccountLanding(account), accountKeyboard(accountID))
	}

	command := map[string]string{"limits": "/limits ", "grants": "/grants ", "reset": "/reset ", "activity": "/activity "}[action]
	if command == "" {
		return s.answerCallback(ctx, query.ID, "expired or invalid control")
	}
	_ = s.answerCallback(ctx, query.ID, "loading "+action)
	chatID, _ := callbackChatID(query)
	reply, markup := s.handleCommandFor(ctx, command+accountID, chatID, query.From.ID)
	if markup == nil {
		markup = accountKeyboard(accountID)
	}
	return s.editCallbackMessage(ctx, query, reply, markup)
}

func (s *Service) beginCodexReset(ctx context.Context, selector string, chatID, userID int64) (string, models.ReplyMarkup) {
	resolved, err := s.controller.PlanCodexReset(ctx, selector)
	if err != nil {
		return accountCommandError("reset preview", err), nil
	}
	if !telegramAccountIDPattern.MatchString(resolved.Account.ID) {
		return "reset preview failed.", nil
	}
	requestID, err := remotecodex.NewRateLimitResetRequestID()
	if err != nil {
		return "reset preview failed.", nil
	}
	token := strings.ReplaceAll(requestID, "-", "")[:20]
	account := server.AccountIdentity{ID: resolved.Account.ID, DisplayName: resolved.Account.DisplayName()}
	pending := &pendingCodexReset{Account: account, Plan: resolved.Plan, RequestID: requestID, ChatID: chatID, UserID: userID, ExpiresAt: time.Now().Add(resetConfirmTTL)}
	s.mu.Lock()
	if s.pendingResets == nil {
		s.pendingResets = map[string]*pendingCodexReset{}
	}
	s.prunePendingResetsLocked(time.Now())
	s.pendingResets[token] = pending
	s.mu.Unlock()
	return RenderCodexResetConfirmation(account, resolved.Plan), resetConfirmationKeyboard(token)
}

func (s *Service) handleResetCallback(ctx context.Context, query *models.CallbackQuery) error {
	action, token, ok := parseResetCallback(query.Data)
	if !ok {
		return s.answerCallback(ctx, query.ID, "expired or invalid reset control")
	}
	chatID, ok := callbackChatID(query)
	if !ok {
		return s.answerCallback(ctx, query.ID, "reset confirmation unavailable")
	}
	now := time.Now()
	s.mu.Lock()
	s.prunePendingResetsLocked(now)
	pending := s.pendingResets[token]
	if pending == nil || pending.ChatID != chatID || pending.UserID != query.From.ID {
		s.mu.Unlock()
		return s.answerCallback(ctx, query.ID, "expired or invalid reset control")
	}
	if action == "cancel" {
		account := pending.Account
		if pending.InFlight {
			s.mu.Unlock()
			return s.answerCallback(ctx, query.ID, "reset already in progress")
		}
		var result *remotecodex.RateLimitResetResult
		if pending.Result != nil {
			value := *pending.Result
			result = &value
		} else {
			pending.Cancelled = true
		}
		s.mu.Unlock()
		if result != nil {
			_ = s.answerCallback(ctx, query.ID, "reset already completed")
			return s.editCallbackMessage(ctx, query, RenderCodexResetResult(account, *result), accountKeyboard(account.ID))
		}
		_ = s.answerCallback(ctx, query.ID, "reset cancelled")
		return s.editCallbackMessage(ctx, query, RenderCodexResetCancelled(account), accountKeyboard(account.ID))
	}
	if pending.Cancelled {
		s.mu.Unlock()
		return s.answerCallback(ctx, query.ID, "reset confirmation was cancelled")
	}
	if pending.Result != nil {
		result := *pending.Result
		account := pending.Account
		s.mu.Unlock()
		_ = s.answerCallback(ctx, query.ID, "reset already completed")
		return s.editCallbackMessage(ctx, query, RenderCodexResetResult(account, result), accountKeyboard(account.ID))
	}
	if pending.InFlight {
		s.mu.Unlock()
		return s.answerCallback(ctx, query.ID, "reset already in progress")
	}
	pending.InFlight = true
	account, accountPin, credit, requestID := pending.Account, pending.Plan.AccountPin, pending.Plan.Credit, pending.RequestID
	s.mu.Unlock()

	result, err := s.controller.ConsumeCodexReset(ctx, account.ID, accountPin, credit, requestID)
	s.mu.Lock()
	pending.InFlight = false
	var accountBindingErr *remotecodex.ResetAccountBindingError
	accountChanged := errors.As(err, &accountBindingErr)
	if err == nil {
		pending.Result = &result
	} else if accountChanged {
		delete(s.pendingResets, token)
	}
	s.mu.Unlock()
	if accountChanged {
		_ = s.answerCallback(ctx, query.ID, "account changed; preview reset again")
		return s.editCallbackMessage(ctx, query, RenderCodexResetAccountChanged(account), accountKeyboard(account.ID))
	}
	if err != nil {
		_ = s.answerCallback(ctx, query.ID, "reset failed; confirmation remains retryable")
		return s.editCallbackMessage(ctx, query, RenderCodexResetRetry(account), resetConfirmationKeyboard(token))
	}
	_ = s.answerCallback(ctx, query.ID, resetCallbackAnswer(result.Outcome))
	return s.editCallbackMessage(ctx, query, RenderCodexResetResult(account, result), accountKeyboard(account.ID))
}

func (s *Service) prunePendingResetsLocked(now time.Time) {
	for token, pending := range s.pendingResets {
		if now.After(pending.ExpiresAt) {
			delete(s.pendingResets, token)
		}
	}
}

func (s *Service) answerCallback(ctx context.Context, id, text string) error {
	if s.bot == nil {
		return nil
	}
	ctx, cancel := s.apiContext(ctx)
	defer cancel()
	_, err := s.bot.AnswerCallbackQuery(ctx, &tgbot.AnswerCallbackQueryParams{CallbackQueryID: id, Text: text, CacheTime: 1})
	return err
}

func (s *Service) editCallbackMessage(ctx context.Context, query *models.CallbackQuery, text string, markup models.ReplyMarkup) error {
	if s.bot == nil || query.Message.Message == nil {
		_, err := s.send(ctx, text, markup)
		return err
	}
	editCtx, cancel := s.apiContext(ctx)
	_, err := s.bot.EditMessageText(editCtx, &tgbot.EditMessageTextParams{
		ChatID:      query.Message.Message.Chat.ID,
		MessageID:   query.Message.Message.ID,
		Text:        text,
		ParseMode:   parseMode(text),
		ReplyMarkup: markup,
	})
	cancel()
	if err == nil || strings.Contains(strings.ToLower(err.Error()), "message is not modified") {
		return nil
	}
	if isUncertainSendError(err) {
		return err
	}
	_, sendErr := s.send(ctx, text, markup)
	return sendErr
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
		case <-s.outboxWake:
		}
	}
}

func (s *Service) retryDeliveriesOnce(ctx context.Context) {
	target := s.target()
	for range 50 {
		deliveries, err := s.deliveries.ClaimOutboxForTarget(ctx, target, time.Now(), time.Minute, 1)
		if err != nil {
			s.logger.Warn("telegram outbox claim failed", "target", target, "error", err)
			return
		}
		if len(deliveries) == 0 {
			return
		}
		delivery := deliveries[0]
		payload, err := store.DecodeOutboxPayload(delivery)
		var text string
		var keyboard models.ReplyMarkup
		if err == nil {
			switch event := payload.(type) {
			case resetwatch.Event:
				text = RenderReset(event)
			case resetwatch.WarningEvent:
				text = RenderLimitWarning(event)
			case budget.PacingAlert:
				text = RenderPacingWarning(event)
			case resetwatch.GrantExpiryWarning:
				text = RenderGrantExpiryWarning(event)
			case resetwatch.ResetGrantEvent:
				text = RenderResetGrant(event)
			case radar.ProbabilityAlert:
				text, keyboard = RenderRadarProbability(event), mainKeyboard()
			default:
				err = fmt.Errorf("unsupported outbox payload type %T", payload)
			}
		}
		var message *models.Message
		if err == nil {
			message, err = s.send(ctx, text, keyboard)
		}
		if err != nil {
			finished, finishErr := s.deliveries.FinishOutboxFailure(ctx, delivery, err.Error(), time.Now())
			if finishErr != nil || !finished {
				s.logger.Warn("telegram outbox failure fence rejected", "outbox_id", delivery.ID, "finished", finished, "error", finishErr)
			}
			continue
		}
		messageID := ""
		if message != nil {
			messageID = strconv.Itoa(message.ID)
		}
		finished, finishErr := s.deliveries.FinishOutboxSuccess(ctx, delivery.ID, delivery.LeaseToken, messageID, time.Now())
		if finishErr != nil || !finished {
			s.logger.Warn("telegram outbox success fence rejected", "outbox_id", delivery.ID, "finished", finished, "error", finishErr)
		}
	}
}

func (s *Service) wakeOutbox() {
	select {
	case s.outboxWake <- struct{}{}:
	default:
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
	sendCtx, cancel := s.apiContext(ctx)
	message, err := s.bot.SendMessage(sendCtx, &tgbot.SendMessageParams{
		ChatID:      s.cfg.ChatID,
		Text:        text,
		ParseMode:   parseMode(text),
		ReplyMarkup: markup,
	})
	cancel()
	if err == nil {
		s.logger.Info("telegram send completed", "duration", time.Since(started).Round(time.Millisecond), "formatted", parseMode(text) != "")
		return message, nil
	}
	if parseMode(text) == "" || isUncertainSendError(err) {
		return message, err
	}
	s.logger.Warn("telegram formatted send failed; retrying plain text", "error", err)
	sendCtx, cancel = s.apiContext(ctx)
	message, err = s.bot.SendMessage(sendCtx, &tgbot.SendMessageParams{
		ChatID:      s.cfg.ChatID,
		Text:        stripTelegramHTML(text),
		ReplyMarkup: markup,
	})
	cancel()
	if err == nil {
		s.logger.Info("telegram send completed", "duration", time.Since(started).Round(time.Millisecond), "formatted", false, "fallback", true)
	}
	return message, err
}

func (s *Service) apiContext(parent context.Context) (context.Context, context.CancelFunc) {
	timeout := s.apiTimeout
	if timeout <= 0 {
		timeout = telegramAPITimeout
	}
	return context.WithTimeout(parent, timeout)
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
	chatID, userID, chatType, ok := updateIdentity(update)
	if !ok || chatID != s.cfg.ChatID {
		return false
	}
	if len(s.cfg.AllowedUserIDs) == 0 {
		return chatType == models.ChatTypePrivate
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

func updateIdentity(update *models.Update) (chatID int64, userID int64, chatType models.ChatType, ok bool) {
	if update.Message != nil {
		chatID = update.Message.Chat.ID
		chatType = update.Message.Chat.Type
		if update.Message.From != nil {
			userID = update.Message.From.ID
		}
		return chatID, userID, chatType, true
	}
	if update.CallbackQuery != nil {
		userID = update.CallbackQuery.From.ID
		if msg := update.CallbackQuery.Message.Message; msg != nil {
			return msg.Chat.ID, userID, msg.Chat.Type, true
		}
		if msg := update.CallbackQuery.Message.InaccessibleMessage; msg != nil {
			return msg.Chat.ID, userID, msg.Chat.Type, true
		}
	}
	return 0, 0, "", false
}

func (s *Service) logUnauthorized(update *models.Update) {
	chatID, userID, _, ok := updateIdentity(update)
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

func callbackKind(data string) string {
	quick := map[string]bool{"quick:home": true, "quick:accounts": true, "quick:limits": true, "quick:activity": true, "quick:grants": true, "quick:reset": true, "quick:health": true, "quick:refresh": true, "quick:radar": true, "quick:stats": true, "quick:settings": true}
	if quick[data] {
		return data
	}
	if strings.HasPrefix(data, "settings:poll:") {
		return "settings:poll"
	}
	if _, _, ok := parseAccountCallback(data); ok {
		return "accounts:v1"
	}
	if _, _, ok := parseResetCallback(data); ok {
		return "reset:v1"
	}
	return "unknown"
}

var resetCallbackTokenPattern = regexp.MustCompile(`^[0-9a-f]{20}$`)

func parseResetCallback(data string) (string, string, bool) {
	parts := strings.Split(data, ":")
	if len(parts) != 4 || parts[0] != "reset" || parts[1] != "v1" {
		return "", "", false
	}
	if parts[2] != "confirm" && parts[2] != "cancel" {
		return "", "", false
	}
	if !resetCallbackTokenPattern.MatchString(parts[3]) {
		return "", "", false
	}
	return parts[2], parts[3], true
}

func callbackChatID(query *models.CallbackQuery) (int64, bool) {
	if query == nil {
		return 0, false
	}
	if query.Message.Message != nil {
		return query.Message.Message.Chat.ID, true
	}
	if query.Message.InaccessibleMessage != nil {
		return query.Message.InaccessibleMessage.Chat.ID, true
	}
	return 0, false
}

func resetCallbackAnswer(outcome string) string {
	switch outcome {
	case remotecodex.ResetOutcomeReset:
		return "Codex limits reset"
	case remotecodex.ResetOutcomeNothingToReset:
		return "nothing eligible to reset"
	case remotecodex.ResetOutcomeNoCredit:
		return "no reset credit available"
	case remotecodex.ResetOutcomeAlreadyRedeemed:
		return "reset already completed"
	default:
		return "reset response received"
	}
}

func resetConfirmationKeyboard(token string) models.InlineKeyboardMarkup {
	return models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: "Confirm reset", CallbackData: "reset:v1:confirm:" + token}},
		{{Text: "Cancel", CallbackData: "reset:v1:cancel:" + token}},
	}}
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
			{Text: "Reset limits", CallbackData: "quick:reset"},
			{Text: "Refresh", CallbackData: "quick:refresh"},
		},
		{{Text: "Activity", CallbackData: "quick:activity"}},
		{
			{Text: "Radar", CallbackData: "quick:radar"},
			{Text: "Health", CallbackData: "quick:health"},
		},
		{
			{Text: "Stats", CallbackData: "quick:stats"},
			{Text: "Settings", CallbackData: "quick:settings"},
		},
		{{Text: "Accounts", CallbackData: "quick:accounts"}},
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
		"<code>/accounts</code> known Codex accounts",
		"<code>/limits [account]</code> stored Codex limits",
		"<code>/grants [account]</code> detailed Codex reset grants",
		"<code>/reset [account]</code> preview and confirm a Codex limit reset",
		"<code>/activity [account]</code> live Codex activity",
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
