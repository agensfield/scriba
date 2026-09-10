package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agensfield/scriba/internal/remote"
)

func TestPlanRateLimitResetSelectsSoonestExpiringAvailableCreditWithoutPosting(t *testing.T) {
	dir := t.TempDir()
	authPath := writeTestAuth(t, dir, "auth", "token-a", "acct-a")
	requests := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		if got := r.Header.Get("Authorization"); got != "Bearer token-a" {
			t.Errorf("authorization=%q", got)
		}
		if got := r.Header.Get("ChatGPT-Account-Id"); got != "acct-a" {
			t.Errorf("account=%q", got)
		}
		switch r.URL.Path {
		case "/usage":
			_, _ = fmt.Fprint(w, `{"rate_limit":{"primary_window":{"used_percent":86,"reset_at":1784780155,"limit_window_seconds":604800}},"rate_limit_reset_credits":{"available_count":3}}`)
		case "/credits":
			_, _ = fmt.Fprint(w, `{"available_count":2,"credits":[
				{"id":"later","status":"available","expires_at":"2026-08-01T00:00:00Z"},
				{"id":"redeemed","status":"redeemed","expires_at":"2026-07-01T00:00:00Z"},
				{"id":"soonest","status":"available","title":"Full reset","expires_at":"2026-07-18T00:29:25Z"}
			]}`)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()
	withResetTestURLs(t, server.URL)

	plan, err := PlanRateLimitReset(context.Background(), server.Client(), FetchOptions{AuthPaths: []string{authPath}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Credit.ID != "soonest" || plan.AvailableCount != 2 || plan.WeeklyUsed == nil || *plan.WeeklyUsed != 86 {
		t.Fatalf("plan=%+v", plan)
	}
	if got, want := fmt.Sprint(requests), "[GET /usage GET /credits]"; got != want {
		t.Fatalf("requests=%s, want %s", got, want)
	}
}

func TestPlanRateLimitResetHonorsExplicitAvailableCredit(t *testing.T) {
	dir := t.TempDir()
	authPath := writeTestAuth(t, dir, "auth", "token", "acct")
	server := resetPlanTestServer(t)
	defer server.Close()
	withResetTestURLs(t, server.URL)

	plan, err := PlanRateLimitReset(context.Background(), server.Client(), FetchOptions{AuthPaths: []string{authPath}}, "later")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Credit.ID != "later" {
		t.Fatalf("credit=%q", plan.Credit.ID)
	}
	if _, err := PlanRateLimitReset(context.Background(), server.Client(), FetchOptions{AuthPaths: []string{authPath}}, "missing"); err == nil {
		t.Fatal("expected unavailable explicit credit error")
	}
}

func TestConsumeRateLimitResetCreditPostsExactCreditAndIdempotencyKey(t *testing.T) {
	dir := t.TempDir()
	authPath := writeTestAuth(t, dir, "auth", "token-a", "acct-a")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/consume" {
			t.Fatalf("request=%s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer token-a" || r.Header.Get("ChatGPT-Account-Id") != "acct-a" || r.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("headers=%v", r.Header)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["credit_id"] != "credit-1" || body["redeem_request_id"] != "request-1" || len(body) != 2 {
			t.Fatalf("body=%v", body)
		}
		_, _ = fmt.Fprint(w, `{"code":"reset","windows_reset":2}`)
	}))
	defer server.Close()
	withResetTestURLs(t, server.URL)

	credit := remoteResetCredit(resetCredit{ID: "credit-1", Status: "available", Title: "Full reset"})
	result, err := ConsumeRateLimitResetCredit(context.Background(), server.Client(), FetchOptions{AuthPaths: []string{authPath}}, mustResetAccountPin(t, "acct-a"), credit, "request-1")
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != ResetOutcomeReset || result.WindowsReset != 2 || result.Credit.ID != "credit-1" {
		t.Fatalf("result=%+v", result)
	}
}

func TestConsumeRateLimitResetCreditAcceptsEveryDocumentedOutcome(t *testing.T) {
	for _, outcome := range []string{ResetOutcomeReset, ResetOutcomeNothingToReset, ResetOutcomeNoCredit, ResetOutcomeAlreadyRedeemed} {
		t.Run(outcome, func(t *testing.T) {
			dir := t.TempDir()
			authPath := writeTestAuth(t, dir, "auth", "token", "acct")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = fmt.Fprintf(w, `{"code":%q}`, outcome)
			}))
			defer server.Close()
			withResetTestURLs(t, server.URL)
			result, err := ConsumeRateLimitResetCredit(context.Background(), server.Client(), FetchOptions{AuthPaths: []string{authPath}}, mustResetAccountPin(t, "acct"), remoteResetCredit(resetCredit{ID: "credit"}), "request")
			if err != nil || result.Outcome != outcome {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestConsumeRateLimitResetCreditRetriesOnceWithSameIdempotencyKey(t *testing.T) {
	dir := t.TempDir()
	authPath := writeTestAuth(t, dir, "auth", "token", "acct")
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["redeem_request_id"] != "stable-request" || body["credit_id"] != "credit" {
			t.Errorf("request %d body=%v", requests, body)
		}
		if requests == 1 {
			http.Error(w, "temporary", http.StatusBadGateway)
			return
		}
		_, _ = fmt.Fprint(w, `{"code":"already_redeemed"}`)
	}))
	defer server.Close()
	withResetTestURLs(t, server.URL)
	result, err := ConsumeRateLimitResetCredit(context.Background(), server.Client(), FetchOptions{AuthPaths: []string{authPath}}, mustResetAccountPin(t, "acct"), remoteResetCredit(resetCredit{ID: "credit"}), "stable-request")
	if err != nil || result.Outcome != ResetOutcomeAlreadyRedeemed || requests != 2 {
		t.Fatalf("result=%+v requests=%d err=%v", result, requests, err)
	}
}

func TestConsumeRateLimitResetCreditRejectsAccountSwitchBeforePost(t *testing.T) {
	dir := t.TempDir()
	authPath := writeTestAuth(t, dir, "auth", "token-a", "acct-a")
	posts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/usage":
			_, _ = fmt.Fprint(w, `{"rate_limit_reset_credits":{"available_count":1}}`)
		case "/credits":
			_, _ = fmt.Fprint(w, `{"available_count":1,"credits":[{"id":"credit-a","status":"available"}]}`)
		case "/consume":
			posts++
			_, _ = fmt.Fprint(w, `{"code":"reset"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	withResetTestURLs(t, server.URL)

	plan, err := PlanRateLimitReset(context.Background(), server.Client(), FetchOptions{AuthPaths: []string{authPath}}, "")
	if err != nil {
		t.Fatal(err)
	}
	writeTestAuth(t, dir, "auth", "token-b", "acct-b")
	_, err = ConsumeRateLimitResetCredit(context.Background(), server.Client(), FetchOptions{AuthPaths: []string{authPath}}, plan.AccountPin, plan.Credit, "request-a")
	var bindingErr *ResetAccountBindingError
	if !errors.As(err, &bindingErr) || !bindingErr.Changed {
		t.Fatalf("error=%v, want changed-account binding error", err)
	}
	if posts != 0 {
		t.Fatalf("consume posts=%d, want 0", posts)
	}
}

func TestConsumeRateLimitResetCreditAllowsTokenRotationForPreviewAccount(t *testing.T) {
	dir := t.TempDir()
	authPath := writeTestAuth(t, dir, "auth", "token-a", "acct-a")
	server := resetPlanTestServer(t)
	defer server.Close()
	withResetTestURLs(t, server.URL)
	plan, err := PlanRateLimitReset(context.Background(), server.Client(), FetchOptions{AuthPaths: []string{authPath}}, "")
	if err != nil {
		t.Fatal(err)
	}
	server.Close()

	posts := 0
	consumeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts++
		if got := r.Header.Get("Authorization"); got != "Bearer token-b" {
			t.Errorf("authorization=%q", got)
		}
		_, _ = fmt.Fprint(w, `{"code":"reset","windows_reset":1}`)
	}))
	defer consumeServer.Close()
	consumeRateLimitResetCreditURL = consumeServer.URL
	writeTestAuth(t, dir, "auth", "token-b", "acct-a")
	result, err := ConsumeRateLimitResetCredit(context.Background(), consumeServer.Client(), FetchOptions{AuthPaths: []string{authPath}}, plan.AccountPin, plan.Credit, "request-a")
	if err != nil || result.Outcome != ResetOutcomeReset || posts != 1 {
		t.Fatalf("result=%+v posts=%d err=%v", result, posts, err)
	}
}

func TestConsumeRateLimitResetCreditRechecksAccountAfterAuthRefresh(t *testing.T) {
	dir := t.TempDir()
	authPath := writeTestAuth(t, dir, "auth", "token-a", "acct-a")
	pin := mustResetAccountPin(t, "acct-a")
	posts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts++
		if posts == 1 {
			payload := `{"tokens":{"access_token":"token-b","account_id":"acct-b"},"last_refresh":"2026-07-12T00:00:00Z"}`
			if err := os.WriteFile(authPath, []byte(payload), 0o600); err != nil {
				t.Errorf("rotate auth: %v", err)
			}
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_, _ = fmt.Fprint(w, `{"code":"reset"}`)
	}))
	defer server.Close()
	withResetTestURLs(t, server.URL)

	_, err := ConsumeRateLimitResetCredit(context.Background(), server.Client(), FetchOptions{AuthPaths: []string{authPath}}, pin, remoteResetCredit(resetCredit{ID: "credit-a"}), "request-a")
	var bindingErr *ResetAccountBindingError
	if !errors.As(err, &bindingErr) || !bindingErr.Changed {
		t.Fatalf("error=%v, want changed-account binding error", err)
	}
	if posts != 1 {
		t.Fatalf("consume posts=%d, want only the initial unauthorized post", posts)
	}
}

func TestResetAccountPinRequiresProviderAccountAndStaysOutOfJSON(t *testing.T) {
	dir := t.TempDir()
	authPath := writeTestAuth(t, dir, "auth", "token", "")
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
	defer server.Close()
	withResetTestURLs(t, server.URL)
	_, err := PlanRateLimitReset(context.Background(), server.Client(), FetchOptions{AuthPaths: []string{authPath}}, "")
	var bindingErr *ResetAccountBindingError
	if !errors.As(err, &bindingErr) || bindingErr.Changed || requests != 0 {
		t.Fatalf("error=%v requests=%d, want unverifiable identity before requests", err, requests)
	}

	authPath = writeTestAuth(t, dir, "auth", "token", "acct")
	_, err = ConsumeRateLimitResetCredit(context.Background(), server.Client(), FetchOptions{AuthPaths: []string{authPath}}, ResetAccountPin{}, remote.ResetCredit{ID: "credit"}, "request")
	if !errors.As(err, &bindingErr) || bindingErr.Changed || requests != 0 {
		t.Fatalf("missing preview pin error=%v requests=%d, want no redemption request", err, requests)
	}

	plan := RateLimitResetPlan{AccountPin: mustResetAccountPin(t, "private-account"), AuthState: remote.AuthState{OK: true, AccountID: "private-account"}}
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "private-account") || strings.Contains(string(raw), "accountPin") || strings.Contains(string(raw), "AccountPin") {
		t.Fatalf("private account identity leaked in plan JSON: %s", raw)
	}
}

func mustResetAccountPin(t *testing.T, accountID string) ResetAccountPin {
	t.Helper()
	pin, err := resetAccountPin(remote.AuthState{AccountID: accountID})
	if err != nil {
		t.Fatal(err)
	}
	return pin
}

func resetPlanTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/usage":
			_, _ = fmt.Fprint(w, `{"rate_limit_reset_credits":{"available_count":2}}`)
		case "/credits":
			_, _ = fmt.Fprint(w, `{"available_count":2,"credits":[{"id":"soon","status":"available","expires_at":"2026-07-18T00:00:00Z"},{"id":"later","status":"available","expires_at":"2026-08-01T00:00:00Z"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
}

func withResetTestURLs(t *testing.T, baseURL string) {
	t.Helper()
	oldUsage, oldCredits, oldConsume := usageURL, rateLimitResetCredits, consumeRateLimitResetCreditURL
	usageURL = baseURL + "/usage"
	rateLimitResetCredits = baseURL + "/credits"
	consumeRateLimitResetCreditURL = baseURL + "/consume"
	t.Cleanup(func() {
		usageURL, rateLimitResetCredits, consumeRateLimitResetCreditURL = oldUsage, oldCredits, oldConsume
	})
}

func TestPlanRateLimitResetDoesNotFallBackFromExplicitMissingAuth(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CODEX_HOME", filepath.Join(dir, "ambient"))
	server := resetPlanTestServer(t)
	defer server.Close()
	withResetTestURLs(t, server.URL)
	_, err := PlanRateLimitReset(context.Background(), server.Client(), FetchOptions{AuthPaths: []string{filepath.Join(dir, "missing.json")}}, "")
	if err == nil {
		t.Fatal("expected missing explicit auth error")
	}
}
