package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/agensfield/scriba/internal/codexauth"
	"github.com/agensfield/scriba/internal/model"
	"github.com/agensfield/scriba/internal/remote"
)

var usageURL = "https://chatgpt.com/backend-api/wham/usage"

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

func Probe(includeHTTP bool) (remote.ProbeResult, error) {
	return ProbeContext(context.Background(), includeHTTP)
}

func AuthPaths() []string {
	return codexauth.AuthPaths()
}

func ProbeContext(ctx context.Context, includeHTTP bool) (remote.ProbeResult, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	auth, err := loadAuth(ctx, nil, false)
	if err != nil {
		return remote.ProbeResult{}, err
	}
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
	result, err := FetchLimits(ctx, http.DefaultClient)
	if err != nil {
		return remote.ProbeResult{}, err
	}
	return result, nil
}

func FetchLimits(ctx context.Context, client *http.Client) (remote.ProbeResult, error) {
	if client == nil {
		client = http.DefaultClient
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	auth, err := loadAuth(ctx, client, false)
	if err != nil {
		return remote.ProbeResult{}, err
	}
	if !auth.OK {
		return remote.ProbeResult{
			ProviderID: "codex",
			Lines:      []model.MetricLine{{Type: "badge", Label: "Codex API", Text: "Auth unavailable"}},
			Provenance: []model.SourceProvenance{{Kind: "provider-api", ProviderID: "codex", FetchedAt: now, Error: auth.Error}},
			AuthState:  auth,
		}, nil
	}
	parsed, err := fetchUsage(ctx, client, auth)
	if isAuthHTTPError(err) {
		auth, err = loadAuth(ctx, client, true)
		if err != nil {
			return remote.ProbeResult{}, err
		}
		if !auth.OK {
			return remote.ProbeResult{ProviderID: "codex", AuthState: auth}, nil
		}
		parsed, err = fetchUsage(ctx, client, auth)
	}
	if err != nil {
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

type usageHTTPError struct {
	status int
}

func (e usageHTTPError) Error() string {
	return fmt.Sprintf("codex usage request failed: %d", e.status)
}

func fetchUsage(ctx context.Context, client *http.Client, auth remote.AuthState) (usageResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, usageURL, nil)
	if err != nil {
		return usageResponse{}, err
	}
	req.Header.Set("Authorization", "Bearer "+auth.AccessToken)
	req.Header.Set("Accept", "application/json")
	if auth.AccountID != "" {
		req.Header.Set("ChatGPT-Account-Id", auth.AccountID)
	}
	resp, err := client.Do(req)
	if err != nil {
		return usageResponse{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return usageResponse{}, usageHTTPError{status: resp.StatusCode}
	}
	var parsed usageResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return usageResponse{}, err
	}
	return parsed, nil
}

func isAuthHTTPError(err error) bool {
	var httpErr usageHTTPError
	return err != nil && (errors.As(err, &httpErr) && (httpErr.status == http.StatusUnauthorized || httpErr.status == http.StatusForbidden))
}

func loadAuth(ctx context.Context, client *http.Client, forceRefresh bool) (remote.AuthState, error) {
	creds, err := codexauth.Load(ctx, codexauth.LoadOptions{Client: client, ForceRefresh: forceRefresh})
	if err != nil {
		return remote.AuthState{}, err
	}
	return remote.AuthState{
		OK:          creds.OK,
		Error:       creds.Error,
		Source:      creds.Source,
		Email:       creds.Email,
		AccessToken: creds.AccessToken,
		AccountID:   creds.AccountID,
	}, nil
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
