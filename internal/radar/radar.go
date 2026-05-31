package radar

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/dustin/go-humanize"
)

const DefaultURL = "https://codex-reset-radar.pages.dev/current.json"

type Client struct {
	URL        string
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
	url := c.URL
	if url == "" {
		url = DefaultURL
	}
	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
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
			b.WriteString(last.WindowHuman)
		}
		if last.Scope != "" {
			b.WriteString(" · ")
			b.WriteString(last.Scope)
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
