package codex

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/agensfield/scriba/internal/model"
	"github.com/agensfield/scriba/internal/remote"
)

const (
	clientID     = "app_EMoamEEZ73f0CkXaXp7hrann"
	refreshURL   = "https://auth.openai.com/oauth/token"
	usageURL     = "https://chatgpt.com/backend-api/wham/usage"
	refreshAfter = 8 * 24 * time.Hour
)

type authFile struct {
	OpenAIAPIKey *string `json:"OPENAI_API_KEY"`
	Tokens       struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
		AccountID    string `json:"account_id"`
	} `json:"tokens"`
	LastRefresh string `json:"last_refresh"`
}

type usageResponse struct {
	PlanType  string `json:"plan_type"`
	RateLimit *struct {
		PrimaryWindow   *window `json:"primary_window"`
		SecondaryWindow *window `json:"secondary_window"`
	} `json:"rate_limit"`
	CodeReviewRateLimit *struct {
		PrimaryWindow   *window `json:"primary_window"`
		SecondaryWindow *window `json:"secondary_window"`
	} `json:"code_review_rate_limit"`
	Credits *struct {
		HasCredits bool `json:"has_credits"`
		Unlimited  bool `json:"unlimited"`
		Balance    any  `json:"balance"`
	} `json:"credits"`
}

type window struct {
	UsedPercent        float64 `json:"used_percent"`
	ResetAt            int64   `json:"reset_at"`
	LimitWindowSeconds int64   `json:"limit_window_seconds"`
}

func AuthPaths() []string {
	home, _ := os.UserHomeDir()
	if codexHome := strings.TrimSpace(os.Getenv("CODEX_HOME")); codexHome != "" {
		return []string{filepath.Join(codexHome, "auth.json")}
	}
	return []string{
		filepath.Join(home, ".config", "codex", "auth.json"),
		filepath.Join(home, ".codex", "auth.json"),
	}
}

func Probe(includeHTTP bool) (remote.ProbeResult, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	auth := loadAuth()
	if !auth.OK {
		return remote.ProbeResult{
			ProviderID: "codex",
			Lines:      []model.MetricLine{{Type: "badge", Label: "Codex API", Text: "Auth unavailable"}},
			Provenance: []model.SourceProvenance{{Kind: "provider-api", ProviderID: "codex", FetchedAt: now, Error: auth.Error}},
			AuthState:  auth,
		}, nil
	}
	if !includeHTTP {
		return remote.ProbeResult{ProviderID: "codex", AuthState: auth}, nil
	}
	req, err := http.NewRequest(http.MethodGet, usageURL, nil)
	if err != nil {
		return remote.ProbeResult{}, err
	}
	req.Header.Set("Authorization", "Bearer "+auth.AccessToken)
	req.Header.Set("Accept", "application/json")
	if auth.AccountID != "" {
		req.Header.Set("ChatGPT-Account-Id", auth.AccountID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return remote.ProbeResult{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return remote.ProbeResult{}, fmt.Errorf("codex usage request failed: %d", resp.StatusCode)
	}
	var parsed usageResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return remote.ProbeResult{}, err
	}
	var lines []model.MetricLine
	if parsed.PlanType != "" {
		lines = append(lines, model.MetricLine{Type: "badge", Label: "Plan", Text: parsed.PlanType})
	}
	if parsed.RateLimit != nil && parsed.RateLimit.PrimaryWindow != nil {
		lines = append(lines, progressLine("5h limit", *parsed.RateLimit.PrimaryWindow))
	}
	if parsed.RateLimit != nil && parsed.RateLimit.SecondaryWindow != nil {
		lines = append(lines, progressLine("Weekly limit", *parsed.RateLimit.SecondaryWindow), progressLine("Spark weekly", *parsed.RateLimit.SecondaryWindow))
	}
	if parsed.RateLimit != nil && parsed.RateLimit.PrimaryWindow != nil {
		lines = append(lines, progressLine("Spark 5h", *parsed.RateLimit.PrimaryWindow))
	}
	if parsed.CodeReviewRateLimit != nil && parsed.CodeReviewRateLimit.PrimaryWindow != nil {
		lines = append(lines, progressLine("Review 5h", *parsed.CodeReviewRateLimit.PrimaryWindow))
	}
	if parsed.CodeReviewRateLimit != nil && parsed.CodeReviewRateLimit.SecondaryWindow != nil {
		lines = append(lines, progressLine("Review weekly", *parsed.CodeReviewRateLimit.SecondaryWindow))
	}
	if parsed.Credits != nil && parsed.Credits.HasCredits {
		if parsed.Credits.Unlimited {
			lines = append(lines, model.MetricLine{Type: "badge", Label: "Credits", Text: "unlimited"})
		} else if balance, ok := number(parsed.Credits.Balance); ok {
			lines = append(lines, model.MetricLine{Type: "amount", Label: "Credits left", Value: balance, Format: &model.MetricFormat{Kind: "count", Suffix: "credits"}})
		}
	}
	return remote.ProbeResult{
		ProviderID: "codex",
		Lines:      lines,
		Provenance: []model.SourceProvenance{{Kind: "provider-api", ProviderID: "codex", FetchedAt: now}},
		AuthState:  auth,
	}, nil
}

func loadAuth() remote.AuthState {
	for _, path := range AuthPaths() {
		data, err := os.ReadFile(path) // #nosec G304 -- Codex auth path is resolved from CODEX_HOME/default auth locations.
		if err != nil {
			continue
		}
		var auth authFile
		if err := json.Unmarshal(data, &auth); err != nil {
			continue
		}
		if auth.OpenAIAPIKey != nil && *auth.OpenAIAPIKey != "" {
			return remote.AuthState{OK: false, Error: "Usage not available for API key auth.", Source: path}
		}
		if auth.Tokens.AccessToken == "" {
			continue
		}
		if auth.Tokens.RefreshToken != "" && needsRefresh(auth.LastRefresh) {
			if refreshed, err := refresh(auth, auth.Tokens.RefreshToken); err == nil && refreshed.Tokens.AccessToken != "" {
				_ = os.WriteFile(path, append(pretty(refreshed), '\n'), 0o600)
				return remote.AuthState{OK: true, AccessToken: refreshed.Tokens.AccessToken, AccountID: refreshed.Tokens.AccountID, Source: path}
			}
		}
		return remote.AuthState{OK: true, AccessToken: auth.Tokens.AccessToken, AccountID: auth.Tokens.AccountID, Source: path}
	}
	return remote.AuthState{OK: false, Error: "Not logged in. Run `codex` to authenticate."}
}

func needsRefresh(lastRefresh string) bool {
	t, err := time.Parse(time.RFC3339Nano, lastRefresh)
	if err != nil {
		t, err = time.Parse(time.RFC3339, lastRefresh)
	}
	return err != nil || time.Since(t) > refreshAfter
}

func refresh(auth authFile, token string) (authFile, error) {
	body := "grant_type=refresh_token&client_id=" + clientID + "&refresh_token=" + token
	req, err := http.NewRequest(http.MethodPost, refreshURL, bytes.NewBufferString(body))
	if err != nil {
		return auth, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return auth, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return auth, fmt.Errorf("refresh failed: %d", resp.StatusCode)
	}
	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return auth, err
	}
	if text, ok := payload["access_token"].(string); ok {
		auth.Tokens.AccessToken = text
	}
	if text, ok := payload["refresh_token"].(string); ok {
		auth.Tokens.RefreshToken = text
	}
	if text, ok := payload["id_token"].(string); ok {
		auth.Tokens.IDToken = text
	}
	auth.LastRefresh = time.Now().UTC().Format(time.RFC3339Nano)
	return auth, nil
}

func progressLine(label string, w window) model.MetricLine {
	used := w.UsedPercent
	limit := 100.0
	var resetsAt string
	if w.ResetAt > 0 {
		resetsAt = time.Unix(w.ResetAt, 0).UTC().Format(time.RFC3339Nano)
	}
	var period *int64
	if w.LimitWindowSeconds > 0 {
		ms := w.LimitWindowSeconds * 1000
		period = &ms
	}
	return model.MetricLine{Type: "progress", Label: label, Used: &used, Limit: &limit, Format: &model.MetricFormat{Kind: "percent"}, ResetsAt: resetsAt, PeriodDurationMs: period}
}

func number(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case string:
		parsed, err := strconv.ParseFloat(v, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func pretty(value any) []byte {
	data, _ := json.MarshalIndent(value, "", "  ")
	return data
}
