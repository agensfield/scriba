package codexauth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	ClientID     = "app_EMoamEEZ73f0CkXaXp7hrann"
	RefreshAfter = 8 * 24 * time.Hour
)

var RefreshURL = "https://auth.openai.com/oauth/token"

var errAuthChanged = errors.New("Codex auth changed during refresh; refusing to overwrite newer credentials")
var errWriteCommitted = errors.New("Codex auth rename committed before durability sync failed")

type Credentials struct {
	OK           bool
	Error        string
	Source       string
	AccessToken  string
	RefreshToken string
	IDToken      string
	AccountID    string
	Email        string
}

type LoadOptions struct {
	Paths        []string
	Client       *http.Client
	ForceRefresh bool
	Now          func() time.Time
}

type File struct {
	Path string
	Raw  map[string]json.RawMessage
	Auth Auth
}

type Auth struct {
	OpenAIAPIKey *string `json:"OPENAI_API_KEY"`
	Tokens       Tokens  `json:"tokens"`
	LastRefresh  string  `json:"last_refresh"`
}

type Tokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	AccountID    string `json:"account_id"`
}

var pathLocks sync.Map

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

func Load(ctx context.Context, opts LoadOptions) (Credentials, error) {
	paths := opts.Paths
	if len(paths) == 0 {
		paths = AuthPaths()
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	for _, path := range paths {
		file, err := ReadFile(path)
		if err != nil {
			continue
		}
		if file.Auth.OpenAIAPIKey != nil && *file.Auth.OpenAIAPIKey != "" {
			return Credentials{OK: false, Error: "Usage not available for API key auth.", Source: path}, nil
		}
		if file.Auth.Tokens.AccessToken == "" {
			continue
		}
		if file.Auth.Tokens.RefreshToken != "" && (opts.ForceRefresh || NeedsRefresh(file.Auth.LastRefresh, now())) {
			refreshed, err := refreshLocked(ctx, opts, path, tokenGeneration(file), now)
			if err == nil && refreshed.Auth.Tokens.AccessToken != "" {
				return credentials(refreshed), nil
			}
			if errors.Is(err, errAuthChanged) && refreshed.Auth.Tokens.AccessToken != "" {
				return credentials(refreshed), nil
			}
			if opts.ForceRefresh {
				return Credentials{OK: false, Error: fmt.Sprintf("Codex token refresh failed: %v", err), Source: path}, nil
			}
		}
		return credentials(file), nil
	}
	return Credentials{OK: false, Error: "Not logged in. Run `codex` to authenticate."}, nil
}

func refreshLocked(ctx context.Context, opts LoadOptions, path, initialGeneration string, now func() time.Time) (File, error) {
	lock := pathLock(path)
	if err := lock.Lock(ctx); err != nil {
		return File{}, err
	}
	defer lock.Unlock()

	flock, err := acquireFileLock(ctx, path+".scriba.lock")
	if err != nil {
		return File{}, err
	}
	defer func() { _ = flock.Close() }()

	file, err := ReadFile(path)
	if err != nil {
		return File{}, err
	}
	if tokenGeneration(file) != initialGeneration {
		return file, nil
	}
	if file.Auth.Tokens.RefreshToken == "" || (!opts.ForceRefresh && !NeedsRefresh(file.Auth.LastRefresh, now())) {
		return file, nil
	}
	generation := tokenGeneration(file)
	refreshed, err := Refresh(ctx, opts.Client, file, now)
	if err != nil {
		return file, err
	}
	current, err := ReadFile(path)
	if err != nil {
		return file, err
	}
	if tokenGeneration(current) != generation {
		return current, errAuthChanged
	}
	if err := writeFileAtomic(refreshed.Path, refreshed.Raw); err != nil {
		if errors.Is(err, errWriteCommitted) {
			if committed, readErr := ReadFile(path); readErr == nil {
				return committed, nil
			}
		}
		return file, err
	}
	return refreshed, nil
}

func acquireFileLock(ctx context.Context, path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) // #nosec G304 -- sibling of configured Codex auth path.
	if err != nil {
		return nil, err
	}
	for {
		err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			_ = f.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func tokenGeneration(file File) string {
	data, _ := json.Marshal(file.Raw)
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func ReadFile(path string) (File, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- Codex auth path is resolved from CODEX_HOME/default auth locations.
	if err != nil {
		return File{}, err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return File{}, err
	}
	var auth Auth
	if err := json.Unmarshal(data, &auth); err != nil {
		return File{}, err
	}
	return File{Path: path, Raw: raw, Auth: auth}, nil
}

func NeedsRefresh(lastRefresh string, now time.Time) bool {
	t, err := time.Parse(time.RFC3339Nano, lastRefresh)
	if err != nil {
		t, err = time.Parse(time.RFC3339, lastRefresh)
	}
	return err != nil || now.Sub(t) > RefreshAfter
}

func Refresh(ctx context.Context, client *http.Client, file File, now func() time.Time) (File, error) {
	if client == nil {
		client = http.DefaultClient
	}
	if now == nil {
		now = time.Now
	}
	body := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {ClientID},
		"refresh_token": {file.Auth.Tokens.RefreshToken},
	}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, RefreshURL, bytes.NewBufferString(body))
	if err != nil {
		return file, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return file, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var payload map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&payload); err == nil {
			if code := refreshErrorCode(payload); code != "" {
				return file, fmt.Errorf("refresh failed: %s", code)
			}
		}
		return file, fmt.Errorf("refresh failed: %d", resp.StatusCode)
	}
	var payload map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return file, err
	}
	if payload["access_token"] == "" {
		return file, errors.New("refresh response missing access_token")
	}
	return applyRefresh(file, payload, now()), nil
}

func WriteFileAtomic(path string, raw map[string]json.RawMessage) error {
	lock := pathLock(path)
	if err := lock.Lock(context.Background()); err != nil {
		return err
	}
	defer lock.Unlock()

	return writeFileAtomic(path, raw)
}

func writeFileAtomic(path string, raw map[string]json.RawMessage) error {
	data, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".auth.json.tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	dirFile, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() { _ = dirFile.Close() }()
	if err := dirFile.Sync(); err != nil {
		return fmt.Errorf("%w: %v", errWriteCommitted, err)
	}
	return nil
}

func EmailFromIDToken(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return ""
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var payload struct {
		Email string `json:"email"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return ""
	}
	return payload.Email
}

func applyRefresh(file File, payload map[string]string, refreshedAt time.Time) File {
	tokens := map[string]json.RawMessage{}
	if rawTokens, ok := file.Raw["tokens"]; ok {
		_ = json.Unmarshal(rawTokens, &tokens)
	}
	setString(tokens, "access_token", payload["access_token"])
	file.Auth.Tokens.AccessToken = payload["access_token"]
	if value := payload["refresh_token"]; value != "" {
		setString(tokens, "refresh_token", value)
		file.Auth.Tokens.RefreshToken = value
	}
	if value := payload["id_token"]; value != "" {
		setString(tokens, "id_token", value)
		file.Auth.Tokens.IDToken = value
	}
	rawTokens, _ := json.Marshal(tokens)
	file.Raw["tokens"] = rawTokens
	refreshed := refreshedAt.UTC().Format(time.RFC3339Nano)
	setString(file.Raw, "last_refresh", refreshed)
	file.Auth.LastRefresh = refreshed
	return file
}

func credentials(file File) Credentials {
	return Credentials{
		OK:           true,
		Source:       file.Path,
		AccessToken:  file.Auth.Tokens.AccessToken,
		RefreshToken: file.Auth.Tokens.RefreshToken,
		IDToken:      file.Auth.Tokens.IDToken,
		AccountID:    file.Auth.Tokens.AccountID,
		Email:        EmailFromIDToken(file.Auth.Tokens.IDToken),
	}
}

func setString(target map[string]json.RawMessage, key, value string) {
	data, _ := json.Marshal(value)
	target[key] = data
}

func refreshErrorCode(payload map[string]any) string {
	if errObj, ok := payload["error"].(map[string]any); ok {
		if code, ok := errObj["code"].(string); ok {
			return code
		}
		if code, ok := errObj["type"].(string); ok {
			return code
		}
	}
	if code, ok := payload["error"].(string); ok {
		return code
	}
	if code, ok := payload["code"].(string); ok {
		return code
	}
	return ""
}

type contextMutex struct {
	token chan struct{}
}

func newContextMutex() *contextMutex {
	m := &contextMutex{token: make(chan struct{}, 1)}
	m.token <- struct{}{}
	return m
}

func (m *contextMutex) Lock(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-m.token:
		return nil
	}
}

func (m *contextMutex) Unlock() { m.token <- struct{}{} }

func pathLock(path string) *contextMutex {
	value, _ := pathLocks.LoadOrStore(path, newContextMutex())
	return value.(*contextMutex)
}
