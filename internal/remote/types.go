package remote

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/agensfield/scriba/internal/model"
)

type AuthState struct {
	OK          bool   `json:"ok"`
	Error       string `json:"error,omitempty"`
	Source      string `json:"source,omitempty"`
	Email       string `json:"email,omitempty"`
	AccessToken string `json:"-"`
	AccountID   string `json:"-"`
}

func AccountRef(auth AuthState) string {
	if auth.AccountID != "" {
		return auth.AccountID
	}
	stable := auth.Email
	if stable == "" {
		stable = auth.Source
	}
	if stable == "" {
		stable = "unknown"
	}
	sum := sha256.Sum256([]byte(stable))
	return "acct_" + hex.EncodeToString(sum[:8])
}

type ProbeResult struct {
	ProviderID   string                   `json:"providerId"`
	Lines        []model.MetricLine       `json:"lines"`
	ResetCredits []ResetCredit            `json:"resetCredits,omitempty"`
	Provenance   []model.SourceProvenance `json:"provenance"`
	AuthState    AuthState                `json:"authState"`
}

type ResetCredit struct {
	ID        string `json:"id,omitempty"`
	Status    string `json:"status,omitempty"`
	ResetType string `json:"resetType,omitempty"`
	Title     string `json:"title,omitempty"`
	GrantedAt string `json:"grantedAt,omitempty"`
	ExpiresAt string `json:"expiresAt,omitempty"`
}
