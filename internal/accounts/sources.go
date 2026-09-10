package accounts

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/agensfield/scriba/internal/codexauth"
	"github.com/agensfield/scriba/internal/config"
	"github.com/agensfield/scriba/internal/resetwatch"
)

// Source is one independently observed Codex credential file. Its path and
// identifier are operator-private and must not enter public account payloads.
type Source struct {
	Ref      string `json:"-"`
	Path     string `json:"-"`
	Priority int    `json:"-"`
}

// SourceRef returns the stable private identifier for an absolute auth path.
func SourceRef(path string) string {
	clean := filepath.Clean(path)
	if absolute, err := filepath.Abs(clean); err == nil {
		clean = absolute
	}
	sum := sha256.Sum256([]byte(clean))
	return "src-" + hex.EncodeToString(sum[:10])
}

// Sources resolves configured auth paths without allowing missing explicit
// paths to fall back to ambient discovery.
func Sources(cfg config.Config) []Source {
	paths := cfg.AuthPaths()
	if cfg.CodexAuthPaths == nil {
		paths = existingDiscoveryPaths(paths)
	}
	seen := make(map[string]bool, len(paths))
	result := make([]Source, 0, len(paths))
	for _, path := range paths {
		clean := filepath.Clean(path)
		if absolute, err := filepath.Abs(clean); err == nil {
			clean = absolute
		}
		if seen[clean] {
			continue
		}
		seen[clean] = true
		result = append(result, Source{Ref: SourceRef(clean), Path: clean, Priority: len(result)})
	}
	return result
}

func existingDiscoveryPaths(paths []string) []string {
	existing := make([]string, 0, len(paths))
	for _, path := range paths {
		if _, err := os.Stat(path); err == nil {
			existing = append(existing, path)
		}
	}
	if len(existing) > 0 || len(paths) == 0 {
		return existing
	}
	return paths[:1]
}

type CredentialState string

const (
	CredentialAvailable    CredentialState = "available"
	CredentialMissing      CredentialState = "missing"
	CredentialInvalid      CredentialState = "invalid"
	CredentialAPIKey       CredentialState = "api_key"
	CredentialLoggedOut    CredentialState = "logged_out"
	CredentialUnverifiable CredentialState = "unverifiable"
)

// Inspection is a network-free snapshot of one credential source. It carries
// no bearer or refresh token.
type Inspection struct {
	Source               Source             `json:"-"`
	Account              resetwatch.Account `json:"-"`
	CredentialsAvailable bool               `json:"-"`
	State                CredentialState    `json:"-"`
}

// Inspect reads a source without refreshing credentials or making a request.
func Inspect(source Source) Inspection {
	result := Inspection{Source: source}
	file, err := codexauth.ReadFile(source.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			result.State = CredentialMissing
		} else {
			result.State = CredentialInvalid
		}
		return result
	}
	if file.Auth.OpenAIAPIKey != nil && strings.TrimSpace(*file.Auth.OpenAIAPIKey) != "" {
		result.State = CredentialAPIKey
		return result
	}
	if strings.TrimSpace(file.Auth.Tokens.AccessToken) == "" {
		result.State = CredentialLoggedOut
		return result
	}
	accountID := strings.TrimSpace(file.Auth.Tokens.AccountID)
	if accountID == "" {
		result.State = CredentialUnverifiable
		return result
	}
	result.Account = resetwatch.Account{Ref: accountID, Email: codexauth.EmailFromIDToken(file.Auth.Tokens.IDToken)}
	result.CredentialsAvailable = true
	result.State = CredentialAvailable
	return result
}
