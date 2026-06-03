package radar

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/dustin/go-humanize"
)

const (
	DefaultURL = "https://codexradar.com/current.json"
	BackupURL  = "https://codex-reset-radar.pages.dev/current.json"
)

type Client struct {
	URL        string
	BackupURL  string
	HTTPClient *http.Client
	Now        func() time.Time
}

type Current struct {
	SchemaVersion     string         `json:"schema_version"`
	Service           string         `json:"service"`
	CheckedAt         string         `json:"checked_at"`
	Status            string         `json:"status"`
	WindowOpen        bool           `json:"window_open"`
	Message           string         `json:"message"`
	RecommendedAction string         `json:"recommended_action"`
	CurrentWindow     *CurrentWindow `json:"current_window"`
	LastWindow        *Window        `json:"last_window"`
	Prediction        *Prediction    `json:"prediction"`
}

type CurrentWindow struct {
	State    string  `json:"state"`
	Message  string  `json:"message"`
	OpenedAt *string `json:"opened_at"`
	Source   *Source `json:"source"`
}

type Window struct {
	ID            string   `json:"id"`
	Title         string   `json:"title"`
	Status        string   `json:"status"`
	OpenedAt      string   `json:"opened_at"`
	ClosedAt      string   `json:"closed_at"`
	WindowMinutes int      `json:"window_minutes"`
	WindowHuman   string   `json:"window_human"`
	Scope         string   `json:"scope"`
	Summary       string   `json:"summary"`
	Sources       []Source `json:"sources"`
}

type Source struct {
	Type string `json:"type"`
	URL  string `json:"url"`
}

type Prediction struct {
	Level            string  `json:"level"`
	Probability24H   float64 `json:"probability_24h"`
	Probability48H   float64 `json:"probability_48h"`
	ExpectedWindow   string  `json:"expected_window"`
	ReasoningSummary string  `json:"reasoning_summary"`
}

func (c Client) Fetch(ctx context.Context) (Current, error) {
	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	urls := c.urls()
	var lastErr error
	for _, url := range urls {
		current, err := fetchURL(ctx, client, url)
		if err == nil {
			return current, nil
		}
		lastErr = err
	}
	return Current{}, lastErr
}

func (c Client) urls() []string {
	url := c.URL
	backup := c.BackupURL
	if url == "" {
		url = DefaultURL
		if backup == "" {
			backup = BackupURL
		}
	}
	if strings.TrimSpace(backup) == "" || backup == url {
		return []string{url}
	}
	return []string{url, backup}
}

func fetchURL(ctx context.Context, client *http.Client, url string) (Current, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Current{}, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return Current{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Current{}, fmt.Errorf("radar request failed: %d", resp.StatusCode)
	}
	var current Current
	if err := json.NewDecoder(resp.Body).Decode(&current); err != nil {
		return Current{}, err
	}
	return current, nil
}

type ProbabilityAlert struct {
	ID               string    `json:"id"`
	Milestone        int       `json:"milestone"`
	Probability24H   float64   `json:"probability24h"`
	Probability48H   float64   `json:"probability48h"`
	Level            string    `json:"level"`
	ExpectedWindow   string    `json:"expectedWindow"`
	ReasoningSummary string    `json:"reasoningSummary"`
	CheckedAt        string    `json:"checkedAt"`
	DetectedAt       time.Time `json:"detectedAt"`
	SnapshotJSON     []byte    `json:"snapshotJson"`
}

func ProbabilityMilestone(probability float64) int {
	switch {
	case probability >= 0.75:
		return 75
	case probability >= 0.50:
		return 50
	case probability >= 0.25:
		return 25
	default:
		return 0
	}
}

func NewProbabilityAlert(current Current, milestone int, detectedAt time.Time) (ProbabilityAlert, error) {
	if current.Prediction == nil {
		return ProbabilityAlert{}, fmt.Errorf("radar prediction is missing")
	}
	snapshot, err := json.Marshal(current)
	if err != nil {
		return ProbabilityAlert{}, err
	}
	id := ProbabilityAlertID(current.CheckedAt, milestone)
	return ProbabilityAlert{
		ID:               id,
		Milestone:        milestone,
		Probability24H:   current.Prediction.Probability24H,
		Probability48H:   current.Prediction.Probability48H,
		Level:            current.Prediction.Level,
		ExpectedWindow:   current.Prediction.ExpectedWindow,
		ReasoningSummary: current.Prediction.ReasoningSummary,
		CheckedAt:        current.CheckedAt,
		DetectedAt:       detectedAt.UTC(),
		SnapshotJSON:     snapshot,
	}, nil
}

func ProbabilityAlertID(checkedAt string, milestone int) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d", checkedAt, milestone)))
	return "radar_alert_" + hex.EncodeToString(sum[:16])
}

func (c Client) RenderText(current Current) string {
	now := time.Now()
	if c.Now != nil {
		now = c.Now()
	}
	var b strings.Builder
	if current.WindowOpen {
		b.WriteString("Codex reset radar: window open")
	} else {
		b.WriteString("Codex reset radar: no active reset window")
	}
	if current.Status != "" {
		b.WriteString(" [")
		b.WriteString(current.Status)
		b.WriteString("]")
	}
	if current.LastWindow != nil {
		last := current.LastWindow
		b.WriteString("\nlast reset: ")
		if last.ClosedAt != "" {
			if closed, err := time.Parse(time.RFC3339, last.ClosedAt); err == nil {
				b.WriteString(humanize.RelTime(closed, now, "ago", "from now"))
			} else {
				b.WriteString(last.ClosedAt)
			}
		} else if last.OpenedAt != "" {
			if opened, err := time.Parse(time.RFC3339, last.OpenedAt); err == nil {
				b.WriteString("opened ")
				b.WriteString(humanize.RelTime(opened, now, "ago", "from now"))
			}
		}
		if last.WindowHuman != "" {
			b.WriteString(" · duration ")
			b.WriteString(durationText(last))
		}
		if last.Scope != "" {
			b.WriteString(" · ")
			b.WriteString(scopeText(last.Scope))
		}
		for _, source := range last.Sources {
			if source.URL != "" {
				b.WriteString("\nsource: ")
				b.WriteString(source.URL)
			}
		}
	}
	if current.Prediction != nil && current.Prediction.Level != "" {
		b.WriteString("\nprediction: ")
		b.WriteString(current.Prediction.Level)
		if current.Prediction.Probability24H > 0 {
			_, _ = fmt.Fprintf(&b, " · 24h %.0f%%", current.Prediction.Probability24H*100)
		}
	}
	return b.String()
}

func durationText(window *Window) string {
	if window == nil {
		return ""
	}
	if window.WindowMinutes <= 0 {
		return window.WindowHuman
	}
	d := time.Duration(window.WindowMinutes) * time.Minute
	hours := int(d.Hours())
	minutes := int(d.Minutes()) % 60
	if hours > 0 && minutes > 0 {
		return fmt.Sprintf("%dh %dm", hours, minutes)
	}
	if hours > 0 {
		return fmt.Sprintf("%dh", hours)
	}
	return fmt.Sprintf("%dm", minutes)
}

func scopeText(scope string) string {
	switch strings.TrimSpace(scope) {
	case "所有付费计划":
		return "all paid plans"
	default:
		return scope
	}
}
