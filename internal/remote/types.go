package remote

import "github.com/agensfield/scriba/internal/model"

type AuthState struct {
	OK          bool   `json:"ok"`
	Error       string `json:"error,omitempty"`
	Source      string `json:"source,omitempty"`
	Email       string `json:"email,omitempty"`
	AccessToken string `json:"-"`
	AccountID   string `json:"-"`
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
