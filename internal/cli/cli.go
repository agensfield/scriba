package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/agensfield/scriba/internal/bench"
	"github.com/agensfield/scriba/internal/buildinfo"
	"github.com/agensfield/scriba/internal/cache"
	"github.com/agensfield/scriba/internal/cached"
	"github.com/agensfield/scriba/internal/config"
	"github.com/agensfield/scriba/internal/doctor"
	"github.com/agensfield/scriba/internal/local"
	"github.com/agensfield/scriba/internal/local/claude"
	"github.com/agensfield/scriba/internal/local/codex"
	"github.com/agensfield/scriba/internal/model"
	"github.com/agensfield/scriba/internal/privacy"
	"github.com/agensfield/scriba/internal/render"
	"github.com/agensfield/scriba/internal/reports"
	"github.com/agensfield/scriba/internal/status"
	"github.com/agensfield/scriba/internal/telegram"
)

type options struct {
	jsonOut         bool
	config          string
	cacheDir        string
	botToken        string
	botTokenEnv     string
	chatID          string
	noCache         bool
	noRemote        bool
	fast            bool
	redact          bool
	since           string
	until           string
	sessionPercent  float64
	weeklyPercent   float64
	send            bool
	enable          bool
	disable         bool
	refresh         bool
	includeErrors   bool
	noIncludeErrors bool
	provider        string
	label           string
	message         string
	execute         bool
	out             string
}

func Run(args []string) int {
	if len(args) > 0 && (args[0] == "--version" || args[0] == "-v") {
		fmt.Println(buildinfo.Version)
		return 0
	}
	if len(args) == 0 {
		args = []string{"status"}
	}
	if err := dispatch(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func dispatch(args []string) error {
	switch args[0] {
	case "doctor":
		opts, _, err := parse(args[1:], flagSpec{
			Use:   "scriba doctor [flags]",
			Flags: []string{"json", "config", "cache-dir", "no-remote", "redact"},
		})
		if err != nil {
			return err
		}
		return runDoctor(opts)
	case "status":
		opts, _, err := parse(args[1:], flagSpec{
			Use:   "scriba status [flags]",
			Flags: []string{"json", "config", "cache-dir", "no-cache", "no-remote", "fast", "redact"},
		})
		if err != nil {
			return err
		}
		return runStatus(opts)
	case "claude", "codex":
		if len(args) < 2 {
			return fmt.Errorf("missing %s report command", args[0])
		}
		opts, _, err := parse(args[2:], flagSpec{
			Use:   fmt.Sprintf("scriba %s %s [flags]", args[0], args[1]),
			Flags: []string{"json", "config", "cache-dir", "no-cache", "redact", "since", "until"},
		})
		if err != nil {
			return err
		}
		return runReport(args[0], args[1], opts)
	case "schema":
		return printJSON(map[string]any{"schemaVersion": model.SchemaVersion, "commands": commands()}, false)
	case "config":
		if len(args) < 2 {
			return fmt.Errorf("missing config command")
		}
		opts, _, err := parse(args[2:], flagSpec{
			Use: fmt.Sprintf("scriba config %s [flags]", args[1]),
			Flags: []string{
				"json", "config", "redact", "enable", "disable", "bot-token", "bot-token-env",
				"chat-id", "session-percent", "weekly-percent", "include-errors", "no-include-errors",
			},
		})
		if err != nil {
			return err
		}
		return runConfig(args[1], opts)
	case "cache":
		if len(args) < 2 {
			return fmt.Errorf("missing cache command")
		}
		opts, _, err := parse(args[2:], flagSpec{
			Use:   fmt.Sprintf("scriba cache %s [flags]", args[1]),
			Flags: []string{"config", "cache-dir", "redact"},
		})
		if err != nil {
			return err
		}
		return runCache(args[1], opts)
	case "telegram":
		if len(args) < 2 {
			return fmt.Errorf("unknown telegram command")
		}
		if args[1] == "alerts" {
			opts, _, err := parse(args[2:], flagSpec{
				Use:   "scriba telegram alerts [flags]",
				Flags: []string{"json", "config", "cache-dir", "no-remote", "redact", "send", "refresh"},
			})
			if err != nil {
				return err
			}
			return runTelegram(opts)
		}
		if args[1] == "reset" {
			opts, _, err := parse(args[2:], flagSpec{
				Use:   "scriba telegram reset [flags]",
				Flags: []string{"json", "config", "redact", "send", "provider", "label", "message"},
			})
			if err != nil {
				return err
			}
			return runTelegramReset(opts)
		}
		return fmt.Errorf("unknown telegram command")
	case "bench":
		if len(args) < 2 || args[1] != "ccusage" {
			return fmt.Errorf("unknown bench command")
		}
		opts, _, err := parse(args[2:], flagSpec{
			Use:   "scriba bench ccusage [flags]",
			Flags: []string{"json", "redact", "provider", "execute", "out"},
		})
		if err != nil {
			return err
		}
		return runBench(opts)
	case "--help", "-h", "help":
		fmt.Println(help())
		return nil
	default:
		return fmt.Errorf("unknown command: %s", args[0])
	}
}

type flagSpec struct {
	Use   string
	Flags []string
}

type flagMeta struct {
	Name    string
	Value   string
	Usage   string
	Default string
}

var flagHelp = map[string]flagMeta{
	"json":              {Name: "json", Usage: "emit JSON"},
	"config":            {Name: "config", Value: "path", Usage: "config path"},
	"cache-dir":         {Name: "cache-dir", Value: "dir", Usage: "cache dir"},
	"no-cache":          {Name: "no-cache", Usage: "disable cache"},
	"no-remote":         {Name: "no-remote", Usage: "skip remote provider probes"},
	"fast":              {Name: "fast", Usage: "read cached status only"},
	"redact":            {Name: "redact", Usage: "redact output"},
	"since":             {Name: "since", Value: "time", Usage: "start date or timestamp"},
	"until":             {Name: "until", Value: "time", Usage: "end date or timestamp"},
	"send":              {Name: "send", Usage: "send alerts"},
	"enable":            {Name: "enable", Usage: "enable feature"},
	"disable":           {Name: "disable", Usage: "disable feature"},
	"refresh":           {Name: "refresh", Usage: "refresh status before evaluating alerts"},
	"bot-token":         {Name: "bot-token", Value: "token", Usage: "telegram bot token"},
	"bot-token-env":     {Name: "bot-token-env", Value: "env", Usage: "telegram bot token environment variable"},
	"chat-id":           {Name: "chat-id", Value: "id", Usage: "telegram chat id"},
	"session-percent":   {Name: "session-percent", Value: "n", Usage: "session alert percentage"},
	"weekly-percent":    {Name: "weekly-percent", Value: "n", Usage: "weekly alert percentage"},
	"include-errors":    {Name: "include-errors", Usage: "include provider errors in telegram alerts"},
	"no-include-errors": {Name: "no-include-errors", Usage: "exclude provider errors from telegram alerts"},
	"provider":          {Name: "provider", Value: "name", Usage: "benchmark provider", Default: "all"},
	"label":             {Name: "label", Value: "text", Usage: "alert label"},
	"message":           {Name: "message", Value: "text", Usage: "telegram message"},
	"execute":           {Name: "execute", Usage: "execute benchmark"},
	"out":               {Name: "out", Value: "path", Usage: "output path"},
}

func parse(args []string, spec flagSpec) (options, []string, error) {
	opts := options{provider: "all"}
	fs := flag.NewFlagSet("scriba", flag.ContinueOnError)
	for _, name := range spec.Flags {
		switch name {
		case "json":
			fs.BoolVar(&opts.jsonOut, name, false, flagHelp[name].Usage)
		case "config":
			fs.StringVar(&opts.config, name, "", flagHelp[name].Usage)
		case "cache-dir":
			fs.StringVar(&opts.cacheDir, name, "", flagHelp[name].Usage)
		case "bot-token":
			fs.StringVar(&opts.botToken, name, "", flagHelp[name].Usage)
		case "bot-token-env":
			fs.StringVar(&opts.botTokenEnv, name, "", flagHelp[name].Usage)
		case "chat-id":
			fs.StringVar(&opts.chatID, name, "", flagHelp[name].Usage)
		case "no-cache":
			fs.BoolVar(&opts.noCache, name, false, flagHelp[name].Usage)
		case "no-remote":
			fs.BoolVar(&opts.noRemote, name, false, flagHelp[name].Usage)
		case "fast":
			fs.BoolVar(&opts.fast, name, false, flagHelp[name].Usage)
		case "redact":
			fs.BoolVar(&opts.redact, name, false, flagHelp[name].Usage)
		case "since":
			fs.StringVar(&opts.since, name, "", flagHelp[name].Usage)
		case "until":
			fs.StringVar(&opts.until, name, "", flagHelp[name].Usage)
		case "session-percent":
			fs.Float64Var(&opts.sessionPercent, name, 0, flagHelp[name].Usage)
		case "weekly-percent":
			fs.Float64Var(&opts.weeklyPercent, name, 0, flagHelp[name].Usage)
		case "send":
			fs.BoolVar(&opts.send, name, false, flagHelp[name].Usage)
		case "enable":
			fs.BoolVar(&opts.enable, name, false, flagHelp[name].Usage)
		case "disable":
			fs.BoolVar(&opts.disable, name, false, flagHelp[name].Usage)
		case "refresh":
			fs.BoolVar(&opts.refresh, name, false, flagHelp[name].Usage)
		case "include-errors":
			fs.BoolVar(&opts.includeErrors, name, false, flagHelp[name].Usage)
		case "no-include-errors":
			fs.BoolVar(&opts.noIncludeErrors, name, false, flagHelp[name].Usage)
		case "provider":
			fs.StringVar(&opts.provider, name, "all", flagHelp[name].Usage)
		case "label":
			fs.StringVar(&opts.label, name, "", flagHelp[name].Usage)
		case "message":
			fs.StringVar(&opts.message, name, "", flagHelp[name].Usage)
		case "execute":
			fs.BoolVar(&opts.execute, name, false, flagHelp[name].Usage)
		case "out":
			fs.StringVar(&opts.out, name, "", flagHelp[name].Usage)
		}
	}
	fs.SetOutput(os.Stdout)
	fs.Usage = func() { printUsage(spec) }
	err := fs.Parse(args)
	return opts, fs.Args(), err
}

func printUsage(spec flagSpec) {
	_, _ = fmt.Fprintf(os.Stdout, "Usage: %s\n\nFlags:\n", spec.Use)
	for _, name := range spec.Flags {
		meta := flagHelp[name]
		flagName := "--" + meta.Name
		if meta.Value != "" {
			flagName += " " + meta.Value
		}
		line := fmt.Sprintf("  %-18s %s", flagName, meta.Usage)
		if meta.Default != "" {
			line += fmt.Sprintf(" (default %q)", meta.Default)
		}
		_, _ = fmt.Fprintln(os.Stdout, line)
	}
}

func load(opts options) (config.Config, error) {
	cfg, err := config.Load(opts.config)
	if err != nil {
		return cfg, err
	}
	if opts.cacheDir != "" {
		cfg.CacheDir = opts.cacheDir
	}
	return cfg, config.Validate(cfg)
}

func runStatus(opts options) error {
	cfg, err := load(opts)
	if err != nil {
		return err
	}
	if !opts.noCache {
		c, err := cache.Open(cfg.CacheDir)
		if err != nil {
			return err
		}
		defer func() { _ = c.Close() }()
		if opts.fast {
			snapshot, err := c.LoadStatusSnapshot()
			if err != nil {
				return err
			}
			if snapshot == nil {
				return fmt.Errorf("no cached status snapshot found; run `scriba status` first")
			}
			return output(opts, *snapshot, render.Status(*snapshot))
		}
		built, err := status.Build(cfg, c, !opts.noRemote)
		if err != nil {
			if snapshot, loadErr := c.LoadStatusSnapshot(); loadErr == nil && snapshot != nil {
				stale := status.MarkStale(*snapshot, err)
				return output(opts, stale, render.Status(stale))
			}
			return err
		}
		if err := status.Save(c, built); err != nil {
			return err
		}
		return output(opts, built.Snapshot, render.Status(built.Snapshot))
	}
	built, err := status.Build(cfg, nil, !opts.noRemote)
	if err != nil {
		return err
	}
	return output(opts, built.Snapshot, render.Status(built.Snapshot))
}

func runDoctor(opts options) error {
	cfg, err := load(opts)
	if err != nil {
		return err
	}
	c, err := cache.Open(cfg.CacheDir)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	payload, err := doctor.Build(cfg, c, !opts.noRemote)
	if err != nil {
		return err
	}
	return output(opts, payload, render.DoctorPayload(payload))
}

func runReport(provider, command string, opts options) error {
	cfg, err := load(opts)
	if err != nil {
		return err
	}
	var events []model.LocalUsageEvent
	var stats model.ScannerStats
	if !opts.noCache {
		c, err := cache.Open(cfg.CacheDir)
		if err != nil {
			return err
		}
		defer func() { _ = c.Close() }()
		if provider == "claude" {
			events, stats, err = cached.ScanClaude(c, cfg.Providers.Claude.Paths)
		} else {
			events, stats, err = cached.ScanCodex(c, cfg.Providers.Codex.Paths)
		}
		if err != nil {
			return err
		}
	} else if provider == "claude" {
		events, stats, err = claude.Scan(cfg.Providers.Claude.Paths)
	} else {
		events, stats, err = codex.Scan(cfg.Providers.Codex.Paths)
	}
	if err != nil {
		return err
	}
	filtered := reports.ApplyFilters(events, reports.Filters{Since: opts.since, Until: opts.until})
	payload := map[string]any{"providerId": provider, "stats": stats}
	var rows any
	switch command {
	case "summary":
		rows = reports.Daily(filtered, true)
	case "daily":
		rows = reports.Daily(filtered, true)
	case "weekly":
		rows = reports.Weekly(filtered, true)
	case "monthly":
		rows = reports.Monthly(filtered, true)
	case "sessions", "session":
		rows = reports.Sessions(filtered, true)
	case "blocks":
		if provider != "claude" {
			return fmt.Errorf("blocks is only available for claude")
		}
		rows = reports.Blocks(filtered)
	default:
		return fmt.Errorf("unknown report: %s", command)
	}
	payload["rows"] = rows
	return output(opts, payload, render.Report(title(provider)+" "+title(command), rowCount(rows)))
}

func runCache(command string, opts options) error {
	cfg, err := load(opts)
	if err != nil {
		return err
	}
	switch command {
	case "reset":
		dir, err := cache.Reset(cfg.CacheDir)
		if err != nil {
			return err
		}
		return printJSON(map[string]any{"ok": true, "cacheDir": dir}, opts.redact)
	case "status", "prune", "vacuum":
		c, err := cache.Open(cfg.CacheDir)
		if err != nil {
			return err
		}
		defer func() { _ = c.Close() }()
		if command == "status" {
			payload, err := c.Status()
			if err != nil {
				return err
			}
			return printJSON(payload, opts.redact)
		}
		if command == "vacuum" {
			return printJSON(withOK(c.Vacuum()), opts.redact)
		}
		existing := map[string]struct{}{}
		dirs := append([]string{}, cfg.Providers.Claude.Paths...)
		dirs = append(dirs, cfg.Providers.Codex.Paths...)
		for _, dir := range dirs {
			files, _ := local.WalkJSONLFiles(dir)
			for _, file := range files {
				existing[file] = struct{}{}
			}
		}
		pruned, err := c.Prune(existing)
		if err != nil {
			return err
		}
		status, _ := c.Status()
		return printJSON(map[string]any{"ok": true, "pruned": pruned, "remaining": status.FileEvents}, opts.redact)
	default:
		return fmt.Errorf("unknown cache command: %s", command)
	}
}

func runConfig(command string, opts options) error {
	path := opts.config
	if path == "" {
		path = config.DefaultPath()
	}
	switch command {
	case "path":
		fmt.Println(path)
		return nil
	case "show":
		cfg, err := config.Load(opts.config)
		if err != nil {
			return err
		}
		if opts.jsonOut {
			return printJSON(privacy.Redact(cfg), false)
		}
		fmt.Println(path)
		return nil
	case "init":
		cfg, err := config.Load(opts.config)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err := config.Save(opts.config, cfg); err != nil {
			return err
		}
		return output(opts, configSummary(path, cfg), "config initialized: "+path)
	case "telegram":
		cfg, err := config.Load(opts.config)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		changed := applyTelegramConfig(&cfg, opts)
		if changed {
			if err := config.Save(opts.config, cfg); err != nil {
				return err
			}
		}
		return output(opts, telegramSummary(path, cfg.Telegram), telegramHumanSummary(path, cfg.Telegram, changed))
	default:
		return fmt.Errorf("unknown config command: %s", command)
	}
}

func applyTelegramConfig(cfg *config.Config, opts options) bool {
	changed := false
	if opts.enable {
		cfg.Telegram.Enabled = true
		changed = true
	}
	if opts.disable {
		cfg.Telegram.Enabled = false
		changed = true
	}
	if opts.botToken != "" {
		cfg.Telegram.BotToken = opts.botToken
		changed = true
	}
	if opts.botTokenEnv != "" {
		cfg.Telegram.BotTokenEnv = opts.botTokenEnv
		changed = true
	}
	if opts.chatID != "" {
		cfg.Telegram.ChatID = opts.chatID
		changed = true
	}
	if opts.sessionPercent > 0 {
		cfg.Telegram.Alerts.SessionPercent = opts.sessionPercent
		changed = true
	}
	if opts.weeklyPercent > 0 {
		cfg.Telegram.Alerts.WeeklyPercent = opts.weeklyPercent
		changed = true
	}
	if opts.includeErrors {
		cfg.Telegram.Alerts.IncludeErrors = true
		changed = true
	}
	if opts.noIncludeErrors {
		cfg.Telegram.Alerts.IncludeErrors = false
		changed = true
	}
	return changed
}

func configSummary(path string, cfg config.Config) map[string]any {
	return map[string]any{
		"path":          path,
		"schemaVersion": cfg.SchemaVersion,
		"telegram":      telegramSummary(path, cfg.Telegram),
	}
}

func telegramSummary(path string, cfg config.TelegramConfig) map[string]any {
	return map[string]any{
		"path":           path,
		"enabled":        cfg.Enabled,
		"hasBotToken":    cfg.BotToken != "",
		"botTokenEnv":    cfg.BotTokenEnv,
		"chatId":         cfg.ChatID,
		"sessionPercent": cfg.Alerts.SessionPercent,
		"weeklyPercent":  cfg.Alerts.WeeklyPercent,
		"includeErrors":  cfg.Alerts.IncludeErrors,
	}
}

func telegramHumanSummary(path string, cfg config.TelegramConfig, changed bool) string {
	state := "disabled"
	if cfg.Enabled {
		state = "enabled"
	}
	prefix := "telegram config"
	if changed {
		prefix = "telegram config updated"
	}
	return fmt.Sprintf("%s · %s · chat %s · %s", prefix, state, emptyAsUnset(cfg.ChatID), path)
}

func emptyAsUnset(value string) string {
	if value == "" {
		return "unset"
	}
	return value
}

func runTelegram(opts options) error {
	cfg, err := load(opts)
	if err != nil {
		return err
	}
	snapshot, err := telegramSnapshot(cfg, opts)
	if err != nil {
		return err
	}
	alerts := telegram.Evaluate(snapshot, cfg.Telegram)
	sent := 0
	if opts.send && len(alerts) > 0 {
		token := cfg.Telegram.BotToken
		if token == "" {
			token = os.Getenv(cfg.Telegram.BotTokenEnv)
		}
		if token == "" {
			return fmt.Errorf("missing Telegram bot token; set telegram.botToken or env %s", cfg.Telegram.BotTokenEnv)
		}
		if cfg.Telegram.ChatID == "" {
			return fmt.Errorf("missing telegram.chatId in Scriba config")
		}
		sent, err = telegram.Send(token, cfg.Telegram.ChatID, alerts)
		if err != nil {
			return err
		}
	}
	return output(opts, map[string]any{"generatedAt": snapshot.GeneratedAt, "enabled": cfg.Telegram.Enabled, "alerts": alerts, "sent": sent}, fmt.Sprintf("%d telegram alerts", len(alerts)))
}

func telegramSnapshot(cfg config.Config, opts options) (model.StatusSnapshot, error) {
	c, err := cache.Open(cfg.CacheDir)
	if err != nil {
		return model.StatusSnapshot{}, err
	}
	defer func() { _ = c.Close() }()
	if !opts.refresh {
		if snapshot, err := c.LoadStatusSnapshot(); err == nil && snapshot != nil {
			return *snapshot, nil
		}
	}
	built, err := status.Build(cfg, c, !opts.noRemote)
	if err != nil {
		return model.StatusSnapshot{}, err
	}
	if err := status.Save(c, built); err != nil {
		return model.StatusSnapshot{}, err
	}
	return built.Snapshot, nil
}

func runTelegramReset(opts options) error {
	cfg, err := load(opts)
	if err != nil {
		return err
	}
	message := strings.TrimSpace(opts.message)
	if message == "" {
		return fmt.Errorf("missing --message for telegram reset")
	}
	alert := telegram.Alert{
		ProviderID: opts.provider,
		Label:      opts.label,
		Severity:   "reset",
		Message:    message,
	}
	sent := 0
	if opts.send && cfg.Telegram.Enabled {
		token := cfg.Telegram.BotToken
		if token == "" {
			token = os.Getenv(cfg.Telegram.BotTokenEnv)
		}
		if token == "" {
			return fmt.Errorf("missing Telegram bot token; set telegram.botToken or env %s", cfg.Telegram.BotTokenEnv)
		}
		if cfg.Telegram.ChatID == "" {
			return fmt.Errorf("missing telegram.chatId in Scriba config")
		}
		sent, err = telegram.Send(token, cfg.Telegram.ChatID, []telegram.Alert{alert})
		if err != nil {
			return err
		}
	}
	return output(opts, map[string]any{"enabled": cfg.Telegram.Enabled, "alert": alert, "sent": sent}, "telegram reset alert")
}

func runBench(opts options) error {
	payload := bench.Build(opts.provider, opts.execute)
	if opts.out != "" {
		data, _ := json.MarshalIndent(privacy.Redact(payload), "", "  ")
		if err := os.MkdirAll(filepath.Dir(opts.out), 0o700); err != nil && filepath.Dir(opts.out) != "." {
			return err
		}
		if err := os.WriteFile(opts.out, append(data, '\n'), 0o600); err != nil {
			return err
		}
	}
	return output(opts, payload, fmt.Sprintf("ccusage benchmark plan · %d commands", len(payload.Commands)))
}

func output(opts options, value any, human string) error {
	if opts.redact {
		value = privacy.Redact(value)
	}
	if opts.jsonOut {
		return printJSON(value, false)
	}
	fmt.Println(human)
	return nil
}

func printJSON(value any, redact bool) error {
	if redact {
		value = privacy.Redact(value)
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}

func withOK(value any) map[string]any {
	data, _ := json.Marshal(value)
	var out map[string]any
	_ = json.Unmarshal(data, &out)
	out["ok"] = true
	return out
}

func rowCount(value any) int {
	data, _ := json.Marshal(value)
	var rows []any
	_ = json.Unmarshal(data, &rows)
	return len(rows)
}

func title(value string) string {
	if value == "" {
		return value
	}
	return strings.ToUpper(value[:1]) + value[1:]
}

func commands() map[string][]string {
	return map[string][]string{
		"root":     {"doctor", "status", "claude", "codex", "schema", "config", "cache", "bench", "telegram"},
		"claude":   {"summary", "daily", "weekly", "monthly", "sessions", "session", "blocks"},
		"codex":    {"summary", "daily", "weekly", "monthly", "sessions", "session"},
		"config":   {"path", "show", "init", "telegram"},
		"cache":    {"status", "reset", "prune", "vacuum"},
		"bench":    {"ccusage"},
		"telegram": {"alerts", "reset"},
	}
}

func help() string {
	return `scriba - Fast local usage tracking for Claude Code and Codex.

Commands:
  scriba [status]
  scriba doctor
  scriba claude daily|weekly|monthly|sessions|blocks
  scriba codex daily|weekly|monthly|sessions
  scriba config path|show|init|telegram
  scriba cache status|reset|prune|vacuum
  scriba telegram alerts|reset
  scriba bench ccusage
`
}
