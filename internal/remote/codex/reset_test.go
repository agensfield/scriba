package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
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
	result, err := ConsumeRateLimitResetCredit(context.Background(), server.Client(), FetchOptions{AuthPaths: []string{authPath}}, credit, "request-1")
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
			result, err := ConsumeRateLimitResetCredit(context.Background(), server.Client(), FetchOptions{AuthPaths: []string{authPath}}, remoteResetCredit(resetCredit{ID: "credit"}), "request")
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
	result, err := ConsumeRateLimitResetCredit(context.Background(), server.Client(), FetchOptions{AuthPaths: []string{authPath}}, remoteResetCredit(resetCredit{ID: "credit"}), "stable-request")
	if err != nil || result.Outcome != ResetOutcomeAlreadyRedeemed || requests != 2 {
		t.Fatalf("result=%+v requests=%d err=%v", result, requests, err)
	}
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
