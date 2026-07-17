package codex

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/agensfield/scriba/internal/remote"
)

// NewRateLimitResetRequestID returns an RFC 4122 version 4 UUID suitable for
// one logical reset attempt. Reuse the returned value when retrying.
func NewRateLimitResetRequestID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(raw[:])
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32], nil
}

var consumeRateLimitResetCreditURL = "https://chatgpt.com/backend-api/wham/rate-limit-reset-credits/consume" // #nosec G101 -- Endpoint URL, not a credential.

const (
	ResetOutcomeReset           = "reset"
	ResetOutcomeNothingToReset  = "nothing_to_reset"
	ResetOutcomeNoCredit        = "no_credit"
	ResetOutcomeAlreadyRedeemed = "already_redeemed"
)

// RateLimitResetPlan describes the concrete credit Scriba would redeem.
type RateLimitResetPlan struct {
	ProviderID     string             `json:"providerId"`
	Source         string             `json:"source"`
	Mode           string             `json:"mode"`
	AvailableCount int                `json:"availableCount"`
	Credit         remote.ResetCredit `json:"credit"`
	WeeklyUsed     *float64           `json:"weeklyUsedPercent,omitempty"`
	WeeklyResetsAt string             `json:"weeklyResetsAt,omitempty"`
	AuthState      remote.AuthState   `json:"authState"`
}

// RateLimitResetResult is the bounded backend outcome of one redemption attempt.
type RateLimitResetResult struct {
	ProviderID   string             `json:"providerId"`
	Source       string             `json:"source"`
	Outcome      string             `json:"outcome"`
	WindowsReset int64              `json:"windowsReset"`
	Credit       remote.ResetCredit `json:"credit"`
	AuthState    remote.AuthState   `json:"authState"`
}

type consumeResetRequest struct {
	RedeemRequestID string `json:"redeem_request_id"`
	CreditID        string `json:"credit_id"`
}

type consumeResetResponse struct {
	Code         string `json:"code"`
	WindowsReset int64  `json:"windows_reset"`
}

// PlanRateLimitReset fetches current usage and grants, then selects either the
// requested credit or the available credit expiring soonest. It never redeems.
func PlanRateLimitReset(ctx context.Context, client *http.Client, opts FetchOptions, creditID string) (RateLimitResetPlan, error) {
	if client == nil {
		client = defaultHTTPClient
	}
	auth, err := loadAuth(ctx, client, false, opts.AuthPaths)
	if err != nil {
		return RateLimitResetPlan{}, err
	}
	if !auth.OK {
		return RateLimitResetPlan{}, fmt.Errorf("codex auth unavailable: %s", auth.Error)
	}
	usage, credits, err := fetchResetPlanData(ctx, client, auth)
	if isAuthHTTPError(err) {
		auth, err = loadAuth(ctx, client, true, opts.AuthPaths)
		if err != nil {
			return RateLimitResetPlan{}, err
		}
		if !auth.OK {
			return RateLimitResetPlan{}, fmt.Errorf("codex auth unavailable: %s", auth.Error)
		}
		usage, credits, err = fetchResetPlanData(ctx, client, auth)
	}
	if err != nil {
		return RateLimitResetPlan{}, err
	}
	selected, err := selectResetCredit(credits.Credits, creditID)
	if err != nil {
		return RateLimitResetPlan{}, err
	}
	weeklyUsed, weeklyResetsAt := primaryWeeklyWindow(usage)
	return RateLimitResetPlan{
		ProviderID:     "codex",
		Source:         "chatgpt-codex-backend",
		Mode:           "dry-run",
		AvailableCount: credits.AvailableCount,
		Credit:         remoteResetCredit(selected),
		WeeklyUsed:     weeklyUsed,
		WeeklyResetsAt: weeklyResetsAt,
		AuthState:      auth,
	}, nil
}

// ConsumeRateLimitResetCredit redeems one explicit credit. Callers must reuse
// redeemRequestID when retrying the same logical attempt.
func ConsumeRateLimitResetCredit(ctx context.Context, client *http.Client, opts FetchOptions, credit remote.ResetCredit, redeemRequestID string) (RateLimitResetResult, error) {
	if client == nil {
		client = defaultHTTPClient
	}
	if strings.TrimSpace(credit.ID) == "" {
		return RateLimitResetResult{}, errors.New("codex reset credit id is required")
	}
	if strings.TrimSpace(redeemRequestID) == "" {
		return RateLimitResetResult{}, errors.New("codex reset idempotency key is required")
	}
	auth, err := loadAuth(ctx, client, false, opts.AuthPaths)
	if err != nil {
		return RateLimitResetResult{}, err
	}
	if !auth.OK {
		return RateLimitResetResult{}, fmt.Errorf("codex auth unavailable: %s", auth.Error)
	}
	parsed, err := consumeResetCredit(ctx, client, auth, credit.ID, redeemRequestID)
	if isAuthHTTPError(err) {
		auth, err = loadAuth(ctx, client, true, opts.AuthPaths)
		if err != nil {
			return RateLimitResetResult{}, err
		}
		if !auth.OK {
			return RateLimitResetResult{}, fmt.Errorf("codex auth unavailable: %s", auth.Error)
		}
		parsed, err = consumeResetCredit(ctx, client, auth, credit.ID, redeemRequestID)
	} else if retryableResetError(err) {
		// A response may be lost after the backend commits the reset. Retrying
		// once with the same key lets the backend return already_redeemed rather
		// than risking a second logical redemption.
		parsed, err = consumeResetCredit(ctx, client, auth, credit.ID, redeemRequestID)
	}
	if err != nil {
		return RateLimitResetResult{}, err
	}
	if !validResetOutcome(parsed.Code) {
		return RateLimitResetResult{}, fmt.Errorf("codex reset returned unknown outcome %q", parsed.Code)
	}
	return RateLimitResetResult{
		ProviderID:   "codex",
		Source:       "chatgpt-codex-backend",
		Outcome:      parsed.Code,
		WindowsReset: parsed.WindowsReset,
		Credit:       credit,
		AuthState:    auth,
	}, nil
}

func retryableResetError(err error) bool {
	if err == nil {
		return false
	}
	var httpErr resetCreditsHTTPError
	if errors.As(err, &httpErr) {
		return httpErr.status >= http.StatusInternalServerError
	}
	return true
}

func fetchResetPlanData(ctx context.Context, client *http.Client, auth remote.AuthState) (usageResponse, resetCreditsResponse, error) {
	usage, err := fetchUsage(ctx, client, auth)
	if err != nil {
		return usageResponse{}, resetCreditsResponse{}, err
	}
	credits, err := fetchResetCredits(ctx, client, auth)
	return usage, credits, err
}

func selectResetCredit(credits []resetCredit, requestedID string) (resetCredit, error) {
	available := make([]resetCredit, 0, len(credits))
	for _, credit := range credits {
		if !strings.EqualFold(credit.Status, "available") {
			continue
		}
		if requestedID != "" && credit.ID == requestedID {
			return credit, nil
		}
		available = append(available, credit)
	}
	if requestedID != "" {
		return resetCredit{}, fmt.Errorf("codex reset credit %q is not available", requestedID)
	}
	if len(available) == 0 {
		return resetCredit{}, errors.New("no Codex reset credits are available")
	}
	sort.SliceStable(available, func(i, j int) bool {
		left, leftOK := parseResetTime(available[i].ExpiresAt)
		right, rightOK := parseResetTime(available[j].ExpiresAt)
		switch {
		case leftOK && rightOK && !left.Equal(right):
			return left.Before(right)
		case leftOK != rightOK:
			return leftOK
		default:
			return available[i].ID < available[j].ID
		}
	})
	return available[0], nil
}

func consumeResetCredit(ctx context.Context, client *http.Client, auth remote.AuthState, creditID, redeemRequestID string) (consumeResetResponse, error) {
	body, err := json.Marshal(consumeResetRequest{RedeemRequestID: redeemRequestID, CreditID: creditID})
	if err != nil {
		return consumeResetResponse{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, consumeRateLimitResetCreditURL, bytes.NewReader(body))
	if err != nil {
		return consumeResetResponse{}, err
	}
	req.Header.Set("Authorization", "Bearer "+auth.AccessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	if auth.AccountID != "" {
		req.Header.Set("ChatGPT-Account-Id", auth.AccountID)
	}
	resp, err := client.Do(req)
	if err != nil {
		return consumeResetResponse{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return consumeResetResponse{}, resetCreditsHTTPError{status: resp.StatusCode, operation: "consume"}
	}
	var parsed consumeResetResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return consumeResetResponse{}, err
	}
	return parsed, nil
}

func primaryWeeklyWindow(usage usageResponse) (*float64, string) {
	if usage.RateLimit == nil {
		return nil, ""
	}
	w := usage.RateLimit.SecondaryWindow
	if w == nil && usage.RateLimit.PrimaryWindow != nil && usage.RateLimit.PrimaryWindow.LimitWindowSeconds == 7*24*60*60 {
		w = usage.RateLimit.PrimaryWindow
	}
	if w == nil {
		return nil, ""
	}
	used := w.UsedPercent
	resetAt := ""
	if w.ResetAt > 0 {
		resetAt = time.Unix(w.ResetAt, 0).UTC().Format(time.RFC3339Nano)
	}
	return &used, resetAt
}

func parseResetTime(value string) (time.Time, bool) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	return parsed, err == nil
}

func remoteResetCredit(credit resetCredit) remote.ResetCredit {
	return remote.ResetCredit{
		ID: credit.ID, Status: credit.Status, ResetType: credit.ResetType,
		Title: credit.Title, GrantedAt: credit.GrantedAt, ExpiresAt: credit.ExpiresAt,
	}
}

func validResetOutcome(outcome string) bool {
	switch outcome {
	case ResetOutcomeReset, ResetOutcomeNothingToReset, ResetOutcomeNoCredit, ResetOutcomeAlreadyRedeemed:
		return true
	default:
		return false
	}
}
