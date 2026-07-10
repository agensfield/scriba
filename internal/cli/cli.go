package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/agensfield/scriba/internal/bench"
	"github.com/agensfield/scriba/internal/buildinfo"
	"github.com/agensfield/scriba/internal/cache"
	"github.com/agensfield/scriba/internal/cached"
	"github.com/agensfield/scriba/internal/config"
	"github.com/agensfield/scriba/internal/doctor"
	"github.com/agensfield/scriba/internal/local"
	"github.com/agensfield/scriba/internal/local/claude"
	localcodex "github.com/agensfield/scriba/internal/local/codex"
	"github.com/agensfield/scriba/internal/model"
	"github.com/agensfield/scriba/internal/privacy"
	"github.com/agensfield/scriba/internal/remote"
	remotecodex "github.com/agensfield/scriba/internal/remote/codex"
	"github.com/agensfield/scriba/internal/render"
	"github.com/agensfield/scriba/internal/reports"
	"github.com/agensfield/scriba/internal/status"
	"github.com/agensfield/scriba/internal/telegram"
	"github.com/agensfield/scriba/internal/updater"
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
	timezone        string
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
	check           bool
	out             string
	statePath       string
	env             string
}

func Run(args []string) int {
	if len(args) > 0 && (args[0] == "--version" || args[0] == "-v" || args[0] == "version") {
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
			Flags: []string{"json", "config", "cache-dir", "no-cache", "no-remote", "fast", "redact", "timezone"},
		})
		if err != nil {
			return err
		}
		return runStatus(opts)
	case "claude", "codex":
		if len(args) < 2 || isHelpArg(args[1]) {
			fmt.Println(groupHelp(args[0]))
			return nil
		}
		if args[0] == "codex" && args[1] == "limits" {
			opts, _, err := parse(args[2:], flagSpec{
				Use:   "scriba codex limits [flags]",
				Flags: []string{"json", "config", "cache-dir", "fast", "redact"},
			})
			if err != nil {
				return err
			}
			return runCodexLimits(opts)
		}
		if args[0] == "codex" && (args[1] == "profile" || args[1] == "profile-stats") {
			opts, _, err := parse(args[2:], flagSpec{
				Use:   "scriba codex profile [flags]",
				Flags: []string{"json", "config", "cache-dir", "redact"},
			})
			if err != nil {
				return err
			}
			return runCodexProfile(opts)
		}
		if args[0] == "codex" && (args[1] == "reset-grants" || args[1] == "grants") {
			opts, _, err := parse(args[2:], flagSpec{
				Use:   "scriba codex reset-grants [flags]",
				Flags: []string{"json", "config", "cache-dir", "redact"},
			})
			if err != nil {
				return err
			}
			return runCodexResetGrants(opts)
		}
		opts, _, err := parse(args[2:], flagSpec{
			Use:   fmt.Sprintf("scriba %s %s [flags]", args[0], args[1]),
			Flags: []string{"json", "config", "cache-dir", "no-cache", "no-remote", "redact", "since", "until", "timezone"},
		})
		if err != nil {
			return err
		}
		return runReport(args[0], args[1], opts)
	case "schema":
		return printJSON(map[string]any{"schemaVersion": model.SchemaVersion, "commands": commands()}, false)
	case "update", "upgrade":
		opts, rest, err := parse(args[1:], flagSpec{
			Use:   "scriba update [flags]",
			Flags: []string{"json", "check"},
		})
		if err != nil {
			return err
		}
		if len(rest) > 0 {
			return fmt.Errorf("scriba update does not accept positional arguments")
		}
		return runUpdate(opts)
	case "config":
		if len(args) < 2 || isHelpArg(args[1]) {
			fmt.Println(groupHelp("config"))
			return nil
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
		if len(args) < 2 || isHelpArg(args[1]) {
			fmt.Println(groupHelp("cache"))
			return nil
		}
		opts, _, err := parse(args[2:], flagSpec{
			Use:   fmt.Sprintf("scriba cache %s [flags]", args[1]),
			Flags: []string{"json", "config", "cache-dir", "redact"},
		})
		if err != nil {
			return err
		}
		return runCache(args[1], opts)
	case "telegram":
		if len(args) < 2 || isHelpArg(args[1]) {
			fmt.Println(groupHelp("telegram"))
			return nil
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
	case "server":
		if len(args) < 2 || isHelpArg(args[1]) {
			fmt.Println(groupHelp("server"))
			return nil
		}
		opts, _, err := parse(args[2:], flagSpec{
			Use:   fmt.Sprintf("scriba server %s [flags]", args[1]),
			Flags: []string{"json", "config", "state-path", "env", "redact"},
		})
		if err != nil {
			return err
		}
		return runServer(args[1], opts)
	case "bench":
		if len(args) < 2 || isHelpArg(args[1]) {
			fmt.Println(groupHelp("bench"))
			return nil
		}
		if args[1] != "ccusage" {
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

func isHelpArg(arg string) bool {
	return arg == "--help" || arg == "-h" || arg == "help"
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
	"timezone":          {Name: "timezone", Value: "zone", Usage: "calendar timezone (IANA name, UTC, or local)"},
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
	"check":             {Name: "check", Usage: "check for updates without installing"},
	"out":               {Name: "out", Value: "path", Usage: "output path"},
	"state-path":        {Name: "state-path", Value: "path", Usage: "server sqlite path"},
	"env":               {Name: "env", Value: "name", Usage: "server environment"},
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
		case "timezone":
			fs.StringVar(&opts.timezone, name, "", flagHelp[name].Usage)
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
		case "check":
			fs.BoolVar(&opts.check, name, false, flagHelp[name].Usage)
		case "out":
			fs.StringVar(&opts.out, name, "", flagHelp[name].Usage)
		case "state-path":
			fs.StringVar(&opts.statePath, name, "", flagHelp[name].Usage)
		case "env":
			fs.StringVar(&opts.env, name, "", flagHelp[name].Usage)
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
	if opts.timezone != "" {
		cfg.Timezone = opts.timezone
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
		events, stats, err = localcodex.Scan(cfg.Providers.Codex.Paths)
	}
	if err != nil {
		return err
	}
	location, err := reports.Location(cfg.Timezone)
	if err != nil {
		return fmt.Errorf("invalid timezone %q: %w", cfg.Timezone, err)
	}
	filtered := reports.ApplyFiltersIn(events, reports.Filters{Since: opts.since, Until: opts.until}, location)
	payload := map[string]any{"providerId": provider, "stats": stats, "timezone": location.String()}
	var rows any
	var limits *codexLimitsPayload
	switch command {
	case "summary":
		rows = reports.DailyIn(filtered, true, location)
		if provider == "codex" && !opts.noRemote {
			remoteLimits, err := liveCodexLimitsPayload()
			if err != nil {
				return err
			}
			limits = &remoteLimits
		}
	case "daily":
		rows = reports.DailyIn(filtered, true, location)
	case "weekly":
		rows = reports.WeeklyIn(filtered, true, location)
	case "monthly":
		rows = reports.MonthlyIn(filtered, true, location)
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
	human := render.Report(title(provider)+" "+title(command), rows)
	if limits != nil {
		payload["limits"] = limits
		human += "\n\n" + render.CodexLimits(limits.Lines, false)
	}
	return output(opts, payload, human)
}

func runCodexLimits(opts options) error {
	if opts.fast {
		cfg, err := load(opts)
		if err != nil {
			return err
		}
		c, err := cache.Open(cfg.CacheDir)
		if err != nil {
			return err
		}
		defer func() { _ = c.Close() }()
		snapshot, err := c.LoadStatusSnapshot()
		if err != nil {
			return err
		}
		if snapshot == nil {
			return fmt.Errorf("no cached status snapshot found; run `scriba status` first")
		}
		payload, err := codexLimitsFromSnapshot(*snapshot)
		if err != nil {
			return err
		}
		return output(opts, payload, render.CodexLimits(payload.Lines, true))
	}
	payload, err := liveCodexLimitsPayload()
	if err != nil {
		return err
	}
	return output(opts, payload, render.CodexLimits(payload.Lines, false))
}

func runCodexProfile(opts options) error {
	profile, err := remotecodex.FetchProfile(context.Background(), nil)
	if err != nil {
		return err
	}
	profile.SchemaVersion = model.SchemaVersion
	return output(opts, profile, renderCodexProfile(profile))
}

func runCodexResetGrants(opts options) error {
	payload, err := liveCodexLimitsPayload()
	if err != nil {
		return err
	}
	return output(opts, resetGrantsPayload(payload), renderResetGrants(payload))
}

func renderCodexProfile(profile remotecodex.ProfileResult) string {
	var b strings.Builder
	b.WriteString(cliHeader("Codex profile"))
	b.WriteString("\n")
	identity := profile.Profile.DisplayName
	if identity == "" {
		identity = profile.Profile.Username
	}
	if identity == "" && profile.AuthState.Email != "" {
		identity = profile.AuthState.Email
	}
	if identity != "" {
		b.WriteString(cliBold(identity))
		if profile.Profile.Username != "" && profile.Profile.Username != identity {
			fmt.Fprintf(&b, " %s", cliMuted("@"+profile.Profile.Username))
		}
		b.WriteString("\n")
	}
	if !profile.AuthState.OK {
		if profile.AuthState.Error != "" {
			fmt.Fprintf(&b, "%s\n", cliYellow(profile.AuthState.Error))
		} else {
			fmt.Fprintf(&b, "%s\n", cliYellow("profile unavailable"))
		}
		return strings.TrimRight(b.String(), "\n")
	}
	freshness := []string{}
	if profile.Metadata.StatsAsOf != "" {
		freshness = append(freshness, "stats as of "+profile.Metadata.StatsAsOf)
	}
	if profile.Metadata.GeneratedAt != "" {
		freshness = append(freshness, "generated "+formatCLIStatsTimeString(profile.Metadata.GeneratedAt))
	}
	if len(freshness) > 0 {
		fmt.Fprintf(&b, "%s\n", cliMuted(strings.Join(freshness, " · ")))
	}
	if profile.Metadata.StatsError != nil {
		fmt.Fprintf(&b, "%s %v\n", cliYellow("stats error"), profile.Metadata.StatsError)
	}
	stats := profile.Stats
	b.WriteString("\n")
	b.WriteString(cliBold("Overview"))
	b.WriteString("\n")
	writeRows(&b, []string{
		fmt.Sprintf("%-13s %s", "tokens", cliGreen(compactTokens(stats.LifetimeTokens))+" lifetime"),
		fmt.Sprintf("%-13s %s", "peak day", compactTokens(stats.PeakDailyTokens)),
		fmt.Sprintf("%-13s %s", "streak", fmt.Sprintf("%dd current · %dd best", stats.CurrentStreakDays, stats.LongestStreakDays)),
		fmt.Sprintf("%-13s %s", "longest turn", humanDurationSeconds(stats.LongestRunningTurnSec)),
	})
	b.WriteString("\n")
	b.WriteString(cliBold("Usage style"))
	b.WriteString("\n")
	writeRows(&b, []string{
		fmt.Sprintf("%-13s %s", "reasoning", fmt.Sprintf("%s · %s", emptyAsUnset(stats.MostUsedReasoningEffort), formatPercent(stats.MostUsedReasoningEffortPct))),
		fmt.Sprintf("%-13s %s", "fast mode", formatPercent(stats.FastModeUsagePercentage)),
		fmt.Sprintf("%-13s %s", "threads", humanInt(stats.TotalThreads)),
		fmt.Sprintf("%-13s %s", "skills", fmt.Sprintf("%s uses · %s unique", humanInt(stats.TotalSkillsUsed), humanInt(stats.UniqueSkillsUsed))),
	})
	if workspace := workspaceRank(stats); workspace != "" {
		fmt.Fprintf(&b, "%-13s %s\n", "workspace", workspace)
	}
	if daily := renderUsageBuckets("Daily activity", stats.DailyUsageBuckets, 14); daily != "" {
		b.WriteString("\n")
		b.WriteString(daily)
	}
	if weekly := renderUsageBuckets("Weekly activity", stats.WeeklyUsageBuckets, 8); weekly != "" {
		b.WriteString("\n")
		b.WriteString(weekly)
	}
	if top := renderTopInvocations(stats.TopInvocations, 5); top != "" {
		b.WriteString("\n")
		b.WriteString(top)
	}
	return strings.TrimRight(b.String(), "\n")
}

func workspaceRank(stats remotecodex.ProfileStats) string {
	if stats.WorkspaceRank == nil || stats.WorkspaceTotalUserCount == nil {
		return ""
	}
	return fmt.Sprintf("#%d of %d", *stats.WorkspaceRank, *stats.WorkspaceTotalUserCount)
}

func renderUsageBuckets(title string, buckets []remotecodex.UsageBucket, limit int) string {
	if len(buckets) == 0 {
		return ""
	}
	if limit > 0 && len(buckets) > limit {
		buckets = buckets[len(buckets)-limit:]
	}
	var maxTokens int64
	for _, bucket := range buckets {
		if bucket.Tokens > maxTokens {
			maxTokens = bucket.Tokens
		}
	}
	var b strings.Builder
	b.WriteString(cliBold(title))
	b.WriteString("\n")
	for _, bucket := range buckets {
		fmt.Fprintf(&b, "%-10s %s %10s\n", bucket.StartDate, usageBar(bucket.Tokens, maxTokens, 16), compactTokens(bucket.Tokens))
	}
	return b.String()
}

func usageBar(tokens, maxTokens int64, width int) string {
	if width <= 0 {
		width = 16
	}
	if maxTokens <= 0 || tokens <= 0 {
		return cliMuted(strings.Repeat("·", width))
	}
	filled := int((tokens*int64(width) + maxTokens - 1) / maxTokens)
	if filled < 1 {
		filled = 1
	}
	if filled > width {
		filled = width
	}
	return cliGreen(strings.Repeat("█", filled)) + cliMuted(strings.Repeat("░", width-filled))
}

func renderTopInvocations(invocations []remotecodex.Invocation, limit int) string {
	if len(invocations) == 0 {
		return ""
	}
	if limit > 0 && len(invocations) > limit {
		invocations = invocations[:limit]
	}
	var b strings.Builder
	b.WriteString(cliBold("Top invocations"))
	b.WriteString("\n")
	for i, invocation := range invocations {
		name := invocationName(invocation)
		fmt.Fprintf(&b, "%s %-24s %s\n", cliMuted(fmt.Sprintf("%d.", i+1)), truncateCLI(name, 24), humanInt(invocation.UsageCount))
	}
	return b.String()
}

func invocationName(invocation remotecodex.Invocation) string {
	switch {
	case invocation.SkillName != "":
		return invocation.SkillName
	case invocation.PluginName != "":
		return invocation.PluginName
	case invocation.SkillID != "":
		return invocation.SkillID
	case invocation.PluginID != "":
		return invocation.PluginID
	case invocation.Type != "":
		return invocation.Type
	default:
		return "unknown"
	}
}

func compactTokens(value int64) string {
	negative := value < 0
	if negative {
		value = -value
	}
	var text string
	switch {
	case value >= 1_000_000_000:
		text = fmt.Sprintf("%.1fB", float64(value)/1_000_000_000)
	case value >= 1_000_000:
		text = fmt.Sprintf("%.1fM", float64(value)/1_000_000)
	case value >= 1_000:
		text = fmt.Sprintf("%.1fK", float64(value)/1_000)
	default:
		text = fmt.Sprint(value)
	}
	if negative {
		return "-" + text
	}
	return text
}

func humanDurationSeconds(seconds int64) string {
	if seconds <= 0 {
		return "unknown"
	}
	return time.Duration(seconds * int64(time.Second)).Round(time.Second).String()
}

func formatPercent(value float64) string {
	return fmt.Sprintf("%.1f%%", value)
}

func runUpdate(opts options) error {
	check, err := updater.CheckLatest(context.Background(), buildinfo.Version)
	if err != nil {
		return err
	}
	human := renderUpdateCheck(check)
	if opts.check || check.Status == updater.Current || check.Status == updater.Ahead {
		return output(opts, check, human)
	}
	if !check.SelfUpdateSupported {
		return errors.New(check.SelfUpdateReason)
	}
	fmt.Println(human)
	fmt.Printf("installing %s...\n", check.Latest)
	if err := updater.Install(context.Background(), check.Latest, os.Stdout, os.Stderr); err != nil {
		return err
	}
	fmt.Printf("installed scriba %s\n", check.Latest)
	return nil
}

func renderUpdateCheck(check updater.Check) string {
	lines := []string{
		"Scriba update",
		fmt.Sprintf("%-13s %s", "current", emptyAsUnset(check.Current)),
		fmt.Sprintf("%-13s %s", "latest", emptyAsUnset(check.Latest)),
		fmt.Sprintf("%-13s %s", "status", emptyAsUnset(check.StatusText)),
		fmt.Sprintf("%-13s %s", "install", emptyAsUnset(check.InstallManager)),
	}
	if check.InstallPath != "" {
		lines = append(lines, fmt.Sprintf("%-13s %s", "path", check.InstallPath))
	}
	if check.SelfUpdateReason != "" {
		lines = append(lines, fmt.Sprintf("%-13s %s", "self-update", check.SelfUpdateReason))
	} else if check.SelfUpdateSupported {
		lines = append(lines, fmt.Sprintf("%-13s %s", "self-update", "supported"))
	}
	if check.UpdateCommand != "" {
		lines = append(lines, fmt.Sprintf("%-13s %s", "command", check.UpdateCommand))
	}
	return strings.Join(lines, "\n")
}

func liveCodexLimitsPayload() (codexLimitsPayload, error) {
	result, err := remotecodex.Probe(true)
	if err != nil {
		return codexLimitsPayload{}, err
	}
	return codexLimitsPayload{
		SchemaVersion: model.SchemaVersion,
		ProviderID:    result.ProviderID,
		Source:        "chatgpt-codex-backend",
		Mode:          "live",
		Lines:         filterCodexLimitLines(result.Lines),
		ResetCredits:  result.ResetCredits,
		Provenance:    result.Provenance,
		AuthState:     result.AuthState,
	}, nil
}

type codexLimitsPayload struct {
	SchemaVersion string                   `json:"schemaVersion"`
	ProviderID    string                   `json:"providerId"`
	Source        string                   `json:"source"`
	Mode          string                   `json:"mode"`
	GeneratedAt   string                   `json:"generatedAt,omitempty"`
	Lines         []model.MetricLine       `json:"lines"`
	ResetCredits  []remote.ResetCredit     `json:"resetCredits,omitempty"`
	Provenance    []model.SourceProvenance `json:"provenance,omitempty"`
	AuthState     any                      `json:"authState,omitempty"`
}

func codexLimitsFromSnapshot(snapshot model.StatusSnapshot) (codexLimitsPayload, error) {
	for _, provider := range snapshot.Providers {
		if provider.ProviderID != "codex" {
			continue
		}
		return codexLimitsPayload{
			SchemaVersion: snapshot.SchemaVersion,
			ProviderID:    provider.ProviderID,
			Source:        "status-cache",
			Mode:          "fast",
			GeneratedAt:   snapshot.GeneratedAt,
			Lines:         filterCodexLimitLines(provider.Lines),
			Provenance:    provider.Provenance,
		}, nil
	}
	return codexLimitsPayload{}, fmt.Errorf("cached status snapshot has no codex provider")
}

func filterCodexLimitLines(lines []model.MetricLine) []model.MetricLine {
	var filtered []model.MetricLine
	for _, line := range lines {
		label := strings.ToLower(line.Label)
		if line.Type == "progress" ||
			label == "plan" ||
			strings.Contains(label, "limit") ||
			strings.Contains(label, "spark") ||
			strings.Contains(label, "review") ||
			strings.Contains(label, "credit") ||
			strings.Contains(label, "grant") {
			filtered = append(filtered, line)
		}
	}
	return filtered
}

func resetGrantsPayload(payload codexLimitsPayload) map[string]any {
	return map[string]any{
		"schemaVersion": payload.SchemaVersion,
		"providerId":    payload.ProviderID,
		"source":        payload.Source,
		"mode":          payload.Mode,
		"authState":     payload.AuthState,
		"resetCredits":  payload.ResetCredits,
		"summary":       resetGrantSummary(payload),
	}
}

func resetGrantSummary(payload codexLimitsPayload) map[string]any {
	summary := map[string]any{
		"available": len(payload.ResetCredits),
	}
	for _, line := range payload.Lines {
		if strings.EqualFold(line.Label, "Reset grants") {
			if count, ok := numericValue(line.Value); ok {
				summary["available"] = int(count)
			}
		}
		if strings.EqualFold(line.Label, "Grant expiry") && line.Value != nil {
			summary["earliestExpiresAt"] = line.Value
		}
	}
	return summary
}

func renderResetGrants(payload codexLimitsPayload) string {
	return renderResetGrantsAt(payload, time.Now().UTC())
}

func renderResetGrantsAt(payload codexLimitsPayload, now time.Time) string {
	summary := resetGrantSummary(payload)
	var b strings.Builder
	b.WriteString(cliHeader("Codex reset grants"))
	b.WriteString("\n")
	fmt.Fprintf(&b, "%s %s", cliGreen(fmt.Sprint(summary["available"])), cliGreen("available"))
	if expiresAt, ok := summary["earliestExpiresAt"]; ok {
		fmt.Fprintf(&b, " · %s %s", cliMuted("earliest expires"), formatGrantExpiry(fmt.Sprint(expiresAt), now))
	}
	b.WriteString("\n")
	if len(payload.ResetCredits) == 0 {
		fmt.Fprintf(&b, "\n%s", cliYellow("No available reset grants found."))
		return strings.TrimRight(b.String(), "\n")
	}
	for i, credit := range payload.ResetCredits {
		title := credit.Title
		if title == "" {
			title = "Reset grant"
		}
		fmt.Fprintf(&b, "\n%s %s\n", cliMuted(fmt.Sprintf("%d.", i+1)), cliBold(title))
		writeGrantRow(&b, "expires", formatGrantExpiry(credit.ExpiresAt, now))
		writeGrantRow(&b, "granted", cliValue(formatGrantTime(credit.GrantedAt)))
		if credit.Status != "" {
			status := cliValue(credit.Status)
			if strings.EqualFold(credit.Status, "available") {
				status = cliGreen(credit.Status)
			}
			writeGrantRow(&b, "status", status)
		}
		if credit.ID != "" {
			writeGrantRow(&b, "id", cliMuted(credit.ID))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func writeGrantRow(b *strings.Builder, name string, value string) {
	padding := 8 - len(name)
	if padding < 1 {
		padding = 1
	}
	fmt.Fprintf(b, "   %s%s %s\n", cliLabel(name), strings.Repeat(" ", padding), value)
}

func formatGrantExpiry(value string, now time.Time) string {
	formatted := formatGrantTime(value)
	parsed, ok := parseGrantTime(value)
	if !ok {
		return formatted
	}
	return fmt.Sprintf("%s %s", cliValue(formatted), cliGreen("("+relativeGrantTime(parsed, now)+")"))
}

func formatGrantTime(value string) string {
	if strings.TrimSpace(value) == "" {
		return "unset"
	}
	parsed, ok := parseGrantTime(value)
	if !ok {
		return value
	}
	return parsed.Local().Format("2006-01-02 15:04 MST")
}

func parseGrantTime(value string) (time.Time, bool) {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil {
		parsed, err = time.Parse(time.RFC3339, strings.TrimSpace(value))
	}
	return parsed, err == nil
}

func relativeGrantTime(target, now time.Time) string {
	delta := target.Sub(now.UTC())
	if delta < 0 {
		delta = -delta
		if delta < time.Hour {
			return "expired now"
		}
		return "expired " + roundedGrantDuration(delta) + " ago"
	}
	if delta < time.Hour {
		return "within 1h"
	}
	return "in " + roundedGrantDuration(delta)
}

func roundedGrantDuration(delta time.Duration) string {
	if delta < 48*time.Hour {
		hours := int(delta.Round(time.Hour).Hours())
		if hours < 1 {
			hours = 1
		}
		return fmt.Sprintf("%dh", hours)
	}
	days := int(delta.Round(24*time.Hour).Hours() / 24)
	if days < 1 {
		days = 1
	}
	return fmt.Sprintf("%dd", days)
}

func numericValue(value any) (float64, bool) {
	switch v := value.(type) {
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case float64:
		return v, true
	case json.Number:
		parsed, err := v.Float64()
		return parsed, err == nil
	default:
		return 0, false
	}
}

func cliHeader(text string) string { return cliANSI("96;1", text) }
func cliLabel(text string) string  { return cliANSI("36", text) }
func cliMuted(text string) string  { return cliANSI("94", text) }
func cliGreen(text string) string  { return cliANSI("32;1", text) }
func cliYellow(text string) string { return cliANSI("33;1", text) }
func cliBold(text string) string   { return cliANSI("1", text) }
func cliValue(text string) string  { return text }

func cliANSI(code, text string) string {
	if text == "" {
		return ""
	}
	return "\x1b[" + code + "m" + text + "\x1b[0m"
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
		return output(opts, map[string]any{"ok": true, "cacheDir": dir}, fmt.Sprintf("cache reset · %s", dir))
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
			return output(opts, payload, renderCacheStatus(payload))
		}
		if command == "vacuum" {
			result := c.Vacuum()
			payload := withOK(result)
			return output(opts, payload, renderCacheVacuum(result))
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
		payload := map[string]any{"ok": true, "pruned": pruned, "remaining": status.FileEvents}
		return output(opts, payload, fmt.Sprintf("cache pruned · %d files removed", pruned))
	default:
		return fmt.Errorf("unknown cache command: %s", command)
	}
}

func renderCacheStatus(status cache.Status) string {
	var b strings.Builder
	b.WriteString(cliHeader("Scriba cache"))
	b.WriteString("\n")
	writeAlignedRows(&b, []string{
		fmt.Sprintf("%-12s %s", "dir", status.CacheDir),
		fmt.Sprintf("%-12s %s", "database", status.DatabasePath),
		fmt.Sprintf("%-12s %s", "size", humanBytes(status.SizeBytes)),
		fmt.Sprintf("%-12s %d", "schema", status.SchemaVersion),
		fmt.Sprintf("%-12s %s", "wal", cacheWALStatus(status.WAL)),
	})
	if len(status.ScanStats) > 0 {
		b.WriteString("\n")
		b.WriteString(cliBold("Scans"))
		b.WriteString("\n")
		for _, scan := range status.ScanStats {
			fmt.Fprintf(&b, "%-12s %s · %s files · %s events\n",
				scan.ProviderID,
				formatCLIStatsTimeString(scan.UpdatedAt),
				humanInt(int64(scan.Stats.Files)),
				humanInt(int64(scan.Stats.Events)),
			)
		}
	}
	if len(status.FileEvents) > 0 {
		b.WriteString("\n")
		b.WriteString(cliBold("Cached files"))
		b.WriteString("\n")
		for _, entry := range status.FileEvents {
			fmt.Fprintf(&b, "%-12s %s files · updated %s\n", entry.ProviderID, humanInt(int64(entry.Files)), formatCLIStatsTimeString(entry.UpdatedAt))
		}
	}
	if len(status.Snapshots) > 0 {
		b.WriteString("\n")
		b.WriteString(cliBold("Snapshots"))
		b.WriteString("\n")
		for _, snapshot := range status.Snapshots {
			fmt.Fprintf(&b, "%-12s %s\n", snapshot.Name, formatCLIStatsTimeString(snapshot.UpdatedAt))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func renderCacheVacuum(result cache.VacuumResult) string {
	return fmt.Sprintf("cache vacuumed · %s -> %s · reclaimed %s", humanBytes(result.BeforeBytes), humanBytes(result.AfterBytes), humanBytes(result.ReclaimedBytes))
}

func cacheWALStatus(info cache.WALInfo) string {
	if !info.Enabled {
		return cliYellow("disabled")
	}
	return cliGreen(info.Mode) + cliMuted(fmt.Sprintf(" · busy timeout %dms", info.BusyTimeoutMs))
}

func writeAlignedRows(b *strings.Builder, rows []string) {
	for _, row := range rows {
		b.WriteString(row)
		b.WriteString("\n")
	}
}

func humanBytes(value int64) string {
	if value <= 0 {
		return "0 B"
	}
	units := []string{"B", "KB", "MB", "GB", "TB"}
	v := float64(value)
	unit := 0
	for v >= 1000 && unit < len(units)-1 {
		v /= 1000
		unit++
	}
	if unit == 0 {
		return fmt.Sprintf("%d %s", value, units[unit])
	}
	return fmt.Sprintf("%.1f %s", v, units[unit])
}

func humanInt(value int64) string {
	negative := value < 0
	if negative {
		value = -value
	}
	text := fmt.Sprint(value)
	for i := len(text) - 3; i > 0; i -= 3 {
		text = text[:i] + "," + text[i:]
	}
	if negative {
		return "-" + text
	}
	return text
}

func formatCLIStatsTimeString(value string) string {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil {
		parsed, err = time.Parse(time.RFC3339, strings.TrimSpace(value))
	}
	if err != nil {
		return emptyAsUnset(value)
	}
	return formatCLIStatsTime(parsed)
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
		"server":        cfg.Server,
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

func title(value string) string {
	if value == "" {
		return value
	}
	return strings.ToUpper(value[:1]) + value[1:]
}

func commands() map[string][]string {
	return map[string][]string{
		"root":     {"doctor", "status", "claude", "codex", "schema", "config", "cache", "bench", "telegram", "server", "update", "version"},
		"claude":   {"summary", "daily", "weekly", "monthly", "sessions", "session", "blocks"},
		"codex":    {"summary", "daily", "weekly", "monthly", "sessions", "session", "limits", "reset-grants", "profile"},
		"config":   {"path", "show", "init", "telegram"},
		"cache":    {"status", "reset", "prune", "vacuum"},
		"bench":    {"ccusage"},
		"telegram": {"alerts", "reset"},
		"server":   {"run", "status", "health", "stats", "refresh", "radar", "prune"},
	}
}

func groupHelp(group string) string {
	switch group {
	case "claude":
		return `scriba claude - Local Claude Code usage reports.

Commands:
  scriba claude summary
  scriba claude daily
  scriba claude weekly
  scriba claude monthly
  scriba claude sessions
  scriba claude blocks

Common flags:
  --since time       start date or timestamp
  --until time       end date or timestamp
  --timezone zone    calendar timezone (default: local)
  --json             emit JSON

Examples:
  scriba claude weekly
  scriba claude sessions --since 2026-06-01`
	case "codex":
		return `scriba codex - Local Codex reports and live ChatGPT/Codex limit checks.

Commands:
  scriba codex summary
  scriba codex daily
  scriba codex weekly
  scriba codex monthly
  scriba codex sessions
  scriba codex limits
  scriba codex reset-grants
  scriba codex profile

Live commands:
  limits           fetch current Codex windows from ChatGPT/Codex auth
  reset-grants     show available reset grants and their expirations
  profile          show ChatGPT/Codex profile token activity

Common flags:
  --since time       start date or timestamp
  --until time       end date or timestamp
  --timezone zone    calendar timezone (default: local)
  --json             emit JSON

Examples:
  scriba codex summary
  scriba codex limits --json
  scriba codex reset-grants
  scriba codex profile`
	case "config":
		return `scriba config - Manage Scriba configuration.

Commands:
  scriba config path
  scriba config show
  scriba config init
  scriba config telegram

Examples:
  scriba config init
  scriba config telegram --enable --chat-id "$TELEGRAM_CHAT_ID" --bot-token-env SCRIBA_TELEGRAM_BOT_TOKEN`
	case "cache":
		return `scriba cache - Inspect and maintain the local derived cache.

Commands:
  scriba cache status
  scriba cache reset
  scriba cache prune
  scriba cache vacuum

Cache deletion is safe; source logs and provider APIs remain authoritative.`
	case "telegram":
		return `scriba telegram - Legacy one-shot Telegram helpers.

Commands:
  scriba telegram alerts
  scriba telegram reset

Use scriba server run for the resident Telegram bot.`
	case "server":
		return `scriba server - Run and inspect the resident Codex limit watcher.

Commands:
  scriba server run
  scriba server status
  scriba server health
  scriba server stats
  scriba server refresh
  scriba server radar
  scriba server prune

Examples:
  scriba server run --env prod
  scriba server health --env prod
  scriba server refresh --env prod --json`
	case "bench":
		return `scriba bench - Benchmark helpers.

Commands:
  scriba bench ccusage`
	default:
		return help()
	}
}

func help() string {
	return `scriba - Fast local usage tracking for Claude Code and Codex.

Usage:
  scriba [command] [flags]

Commands:
  status            combined local usage and remote limit snapshot
  doctor            auth, paths, cache, and provider diagnostics
  claude            Claude Code usage reports
  codex             Codex usage reports, live limits, reset grants, profile
  server            resident Codex watcher and Telegram bot
  update            check or install the latest tagged release
  config            config file and Telegram settings
  cache             derived cache maintenance
  telegram          legacy one-shot Telegram helpers
  bench             comparison helpers

Common flows:
  scriba
  scriba doctor
  scriba codex limits
  scriba codex reset-grants
  scriba codex profile
  scriba update --check
  scriba server run --env prod

Use "scriba <command> --help" for command-specific help.
Use --json for automation and agents.
`
}
