package claude

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/agensfield/scriba/internal/model"
	"github.com/agensfield/scriba/internal/remote"
)

const (
	clientID   = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	refreshURL = "https://platform.claude.com/v1/oauth/token"
	usageURL   = "https://api.anthropic.com/api/oauth/usage"
)

type credentialFile struct {
	ClaudeAIOAuth oauth `json:"claudeAiOauth"`
}

type oauth struct {
	AccessToken  string   `json:"accessToken"`
	RefreshToken string   `json:"refreshToken"`
	ExpiresAt    int64    `json:"expiresAt"`
	Scopes       []string `json:"scopes"`
}

type usageResponse map[string]any

func CredentialPaths() []string {
	home, _ := os.UserHomeDir()
	if configured := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")); configured != "" {
		parts := strings.Split(configured, ",")
		out := make([]string, 0, len(parts))
		for _, part := range parts {
			if trimmed := strings.TrimSpace(part); trimmed != "" {
				out = append(out, filepath.Join(trimmed, ".credentials.json"))
			}
		}
		return out
	}
	return []string{filepath.Join(home, ".claude", ".credentials.json")}
}

func KeychainServices() []string {
	base := "Claude Code" + oauthFileSuffix() + "-credentials"
	configured := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR"))
	if configured == "" {
		return []string{base}
	}
	sum := sha256.Sum256([]byte(configured))
	return []string{base + "-" + hex.EncodeToString(sum[:])[:8], base}
}

func KeychainServiceExists(service string) bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	cmd := exec.Command("security", "find-generic-password", "-s", service) // #nosec G204 -- Service is selected from Claude Code keychain candidates.
	return cmd.Run() == nil
}

func Probe(includeHTTP bool) (remote.ProbeResult, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	auth := loadAuth()
	if !auth.OK {
		return remote.ProbeResult{
			ProviderID: "claude",
			Lines:      []model.MetricLine{{Type: "badge", Label: "Claude API", Text: "Auth unavailable"}},
			Provenance: []model.SourceProvenance{{Kind: "provider-api", ProviderID: "claude", FetchedAt: now, Error: auth.Error}},
			AuthState:  auth,
		}, nil
	}
	if !includeHTTP {
		return remote.ProbeResult{ProviderID: "claude", AuthState: auth}, nil
	}
	req, err := http.NewRequest(http.MethodGet, usageURL, nil)
	if err != nil {
		return remote.ProbeResult{}, err
	}
	req.Header.Set("Authorization", "Bearer "+auth.AccessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return remote.ProbeResult{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return remote.ProbeResult{}, fmt.Errorf("claude usage request failed: %d", resp.StatusCode)
	}
	var parsed usageResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return remote.ProbeResult{}, err
	}
	lines := []model.MetricLine{peakHoursLine(time.Now())}
	pushWindow(&lines, "5h limit", parsed["five_hour"])
	pushWindow(&lines, "Weekly limit", parsed["seven_day"])
	pushWindow(&lines, "OAuth Apps", parsed["seven_day_oauth_apps"])
	pushWindow(&lines, "Sonnet", first(parsed, "seven_day_sonnet", "seven_day_opus"))
	pushWindow(&lines, "Claude Design", first(parsed, "seven_day_design", "seven_day_claude_design", "claude_design", "design", "seven_day_omelette", "omelette", "omelette_promotional"))
	pushWindow(&lines, "Claude Routines", first(parsed, "seven_day_routines", "seven_day_claude_routines", "claude_routines", "routines", "routine", "seven_day_cowork", "cowork"))
	pushWindow(&lines, "Extra Claude window", parsed["iguana_necktie"])
	return remote.ProbeResult{
		ProviderID: "claude",
		Lines:      lines,
		Provenance: []model.SourceProvenance{{Kind: "provider-api", ProviderID: "claude", FetchedAt: now}},
		AuthState:  auth,
	}, nil
}

func loadAuth() remote.AuthState {
	var credentialError *remote.AuthState
	for _, path := range CredentialPaths() {
		data, err := os.ReadFile(path) // #nosec G304 -- Claude credential path is resolved from CLAUDE_CONFIG_DIR/default auth locations.
		if err != nil {
			continue
		}
		var parsed credentialFile
		if err := json.Unmarshal(data, &parsed); err != nil {
			continue
		}
		auth, err := resolve(parsed.ClaudeAIOAuth)
		if err != nil {
			next := remote.AuthState{OK: false, Error: err.Error(), Source: "file:" + path}
			credentialError = &next
			continue
		}
		if auth.AccessToken != parsed.ClaudeAIOAuth.AccessToken {
			parsed.ClaudeAIOAuth = auth
			_ = os.WriteFile(path, append(pretty(parsed), '\n'), 0o600)
		}
		return remote.AuthState{OK: true, AccessToken: auth.AccessToken, Source: "file:" + path}
	}
	for _, service := range KeychainServices() {
		payload, ok := readKeychain(service)
		if !ok {
			continue
		}
		var parsed credentialFile
		if err := json.Unmarshal([]byte(payload), &parsed); err != nil {
			if decoded, decErr := hex.DecodeString(strings.TrimSpace(payload)); decErr == nil {
				_ = json.Unmarshal(decoded, &parsed)
			}
		}
		auth, err := resolve(parsed.ClaudeAIOAuth)
		if err != nil {
			next := remote.AuthState{OK: false, Error: err.Error(), Source: "keychain:" + service}
			credentialError = &next
			continue
		}
		if auth.AccessToken != parsed.ClaudeAIOAuth.AccessToken {
			parsed.ClaudeAIOAuth = auth
			writeKeychain(service, string(pretty(parsed)))
		}
		return remote.AuthState{OK: true, AccessToken: auth.AccessToken, Source: "keychain:" + service}
	}
	if credentialError != nil {
		return *credentialError
	}
	return remote.AuthState{OK: false, Error: "not logged in; run `claude` to authenticate"}
}

func resolve(auth oauth) (oauth, error) {
	if auth.AccessToken == "" {
		return auth, fmt.Errorf("not logged in; run `claude` to authenticate")
	}
	if auth.RefreshToken == "" || auth.ExpiresAt > 0 && time.Now().Add(5*time.Minute).UnixMilli() < auth.ExpiresAt {
		return auth, nil
	}
	next, err := refresh(auth)
	if err != nil {
		return auth, fmt.Errorf("claude OAuth credentials found but refresh failed: %v", err)
	}
	return next, nil
}

func refresh(auth oauth) (oauth, error) {
	body := map[string]string{"grant_type": "refresh_token", "client_id": clientID, "refresh_token": auth.RefreshToken}
	data, _ := json.Marshal(body)
	resp, err := http.Post(refreshURL, "application/json", bytes.NewReader(data))
	if err != nil {
		return auth, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		var payload struct {
			Error struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(body, &payload)
		if payload.Error.Message != "" {
			return auth, fmt.Errorf("refresh failed: %d %s: %s", resp.StatusCode, payload.Error.Type, payload.Error.Message)
		}
		if text := strings.TrimSpace(string(body)); text != "" {
			return auth, fmt.Errorf("refresh failed: %d %s", resp.StatusCode, text)
		}
		return auth, fmt.Errorf("refresh failed: %d", resp.StatusCode)
	}
	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return auth, err
	}
	if text, ok := payload["access_token"].(string); ok {
		auth.AccessToken = text
	}
	if text, ok := payload["refresh_token"].(string); ok {
		auth.RefreshToken = text
	}
	if expires, ok := payload["expires_in"].(float64); ok {
		auth.ExpiresAt = time.Now().Add(time.Duration(expires) * time.Second).UnixMilli()
	}
	return auth, nil
}

func readKeychain(service string) (string, bool) {
	if runtime.GOOS != "darwin" {
		return "", false
	}
	if user := strings.TrimSpace(os.Getenv("USER")); user != "" {
		cmd := exec.Command("security", "find-generic-password", "-a", user, "-s", service, "-w") // #nosec G204,G702 -- Arguments are passed without shell; service is a Claude Code candidate and USER scopes the same local keychain lookup Claude Code uses.
		if out, err := cmd.Output(); err == nil && strings.TrimSpace(string(out)) != "" {
			return strings.TrimSpace(string(out)), true
		}
	}
	cmd := exec.Command("security", "find-generic-password", "-s", service, "-w") // #nosec G204 -- Service is selected from Claude Code keychain candidates.
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err == nil && strings.TrimSpace(string(out)) != ""
}

func writeKeychain(service string, payload string) {
	if runtime.GOOS != "darwin" {
		return
	}
	cmd := exec.Command("security", "add-generic-password", "-U", "-s", service, "-w", payload) // #nosec G204 -- Service is selected from Claude Code keychain candidates.
	_ = cmd.Run()
}

func pushWindow(lines *[]model.MetricLine, label string, value any) {
	record, ok := value.(map[string]any)
	if !ok || value == nil {
		return
	}
	used, _ := record["utilization"].(float64)
	limit := 100.0
	line := model.MetricLine{Type: "progress", Label: label, Used: &used, Limit: &limit, Format: &model.MetricFormat{Kind: "percent"}}
	if resets, ok := record["resets_at"].(string); ok {
		line.ResetsAt = resets
	}
	*lines = append(*lines, line)
}

func first(record usageResponse, keys ...string) any {
	for _, key := range keys {
		if value := record[key]; value != nil {
			return value
		}
	}
	return nil
}

func peakHoursLine(now time.Time) model.MetricLine {
	loc, _ := time.LoadLocation("America/New_York")
	local := now.In(loc)
	minutes := local.Hour()*60 + local.Minute()
	start, end := 8*60, 14*60
	if minutes >= start && minutes < end {
		return model.MetricLine{Type: "badge", Label: "Peak Hours", Text: fmt.Sprintf("Peak · %s left", durationLabel(end-minutes))}
	}
	until := start - minutes
	if until < 0 {
		until += 24 * 60
	}
	return model.MetricLine{Type: "badge", Label: "Peak Hours", Text: fmt.Sprintf("Off-peak · peak in %s", durationLabel(until))}
}

func durationLabel(minutes int) string {
	hours := minutes / 60
	mins := minutes % 60
	if hours > 0 && mins > 0 {
		return fmt.Sprintf("%dh %dm", hours, mins)
	}
	if hours > 0 {
		return fmt.Sprintf("%dh", hours)
	}
	return fmt.Sprintf("%dm", mins)
}

func oauthFileSuffix() string {
	configured := strings.TrimSpace(os.Getenv("CLAUDE_CODE_OAUTH_TOKEN_FILE"))
	if configured == "" {
		return ""
	}
	return "-" + configured
}

func pretty(value any) []byte {
	data, _ := json.MarshalIndent(value, "", "  ")
	return data
}
