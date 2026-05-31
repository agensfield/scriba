package codexauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadRefreshesAndPreservesUnknownAuthFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	writeAuth(t, path, `{
  "OPENAI_API_KEY": null,
  "unknown_top": {"keep": true},
  "tokens": {
    "access_token": "old-access",
    "refresh_token": "old-refresh",
    "id_token": "`+idToken("old@example.com")+`",
    "account_id": "acct_123",
    "unknown_token": "still-here"
  },
  "last_refresh": "2026-01-01T00:00:00Z"
}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/token" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse form: %v", err)
		}
		if r.Form.Get("refresh_token") != "old-refresh" {
			t.Fatalf("unexpected refresh token: %s", r.Form.Get("refresh_token"))
		}
		_, _ = w.Write([]byte(`{"access_token":"new-access","refresh_token":"new-refresh","id_token":"` + idToken("new@example.com") + `"}`))
	}))
	defer server.Close()

	oldRefreshURL := RefreshURL
	t.Cleanup(func() { RefreshURL = oldRefreshURL })
	RefreshURL = server.URL + "/oauth/token"
	creds, err := Load(context.Background(), LoadOptions{
		Paths: []string{path},
		Now:   func() time.Time { return parseTime("2026-06-01T12:00:00Z") },
	})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !creds.OK || creds.AccessToken != "new-access" || creds.AccountID != "acct_123" || creds.Email != "new@example.com" {
		t.Fatalf("unexpected credentials: %#v", creds)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read written auth: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal written auth: %v", err)
	}
	if _, ok := raw["unknown_top"]; !ok {
		t.Fatalf("unknown top-level field was dropped: %s", data)
	}
	tokens := raw["tokens"].(map[string]any)
	if tokens["unknown_token"] != "still-here" || tokens["account_id"] != "acct_123" {
		t.Fatalf("token fields were not preserved: %#v", tokens)
	}
	if tokens["access_token"] != "new-access" || tokens["refresh_token"] != "new-refresh" {
		t.Fatalf("token fields were not updated: %#v", tokens)
	}
	if raw["last_refresh"] != "2026-06-01T12:00:00Z" {
		t.Fatalf("unexpected last_refresh: %#v", raw["last_refresh"])
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("auth file mode = %o", info.Mode().Perm())
	}
}

func TestLoadFallsBackToExistingTokenOnProactiveRefreshError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	writeAuth(t, path, `{
  "tokens": {"access_token": "old-access", "refresh_token": "old-refresh", "id_token": "`+idToken("old@example.com")+`"},
  "last_refresh": "2026-01-01T00:00:00Z"
}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":"refresh_token_reused"}}`))
	}))
	defer server.Close()
	oldRefreshURL := RefreshURL
	t.Cleanup(func() { RefreshURL = oldRefreshURL })
	RefreshURL = server.URL

	creds, err := Load(context.Background(), LoadOptions{
		Paths: []string{path},
		Now:   func() time.Time { return parseTime("2026-06-01T12:00:00Z") },
	})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !creds.OK || creds.AccessToken != "old-access" {
		t.Fatalf("expected fallback credentials, got %#v", creds)
	}
}

func TestForceRefreshReturnsAuthError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	writeAuth(t, path, `{"tokens": {"access_token": "old-access", "refresh_token": "old-refresh"}}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"refresh_token_expired"}`))
	}))
	defer server.Close()
	oldRefreshURL := RefreshURL
	t.Cleanup(func() { RefreshURL = oldRefreshURL })
	RefreshURL = server.URL

	creds, err := Load(context.Background(), LoadOptions{Paths: []string{path}, ForceRefresh: true})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if creds.OK || !strings.Contains(creds.Error, "refresh_token_expired") {
		t.Fatalf("unexpected forced refresh result: %#v", creds)
	}
}

func writeAuth(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatalf("write auth: %v", err)
	}
}

func idToken(email string) string {
	payload, _ := json.Marshal(map[string]string{"email": email})
	return "x." + base64.RawURLEncoding.EncodeToString(payload) + ".y"
}

func parseTime(value string) time.Time {
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		panic(err)
	}
	return t.UTC()
}
