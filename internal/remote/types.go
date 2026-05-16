package remote

import "github.com/agensfield/scriba/internal/model"

type AuthState struct {
	OK          bool   `json:"ok"`
	Error       string `json:"error,omitempty"`
	Source      string `json:"source,omitempty"`
	AccessToken string `json:"-"`
	AccountID   string `json:"-"`
}

type ProbeResult struct {
	ProviderID string                   `json:"providerId"`
	Lines      []model.MetricLine       `json:"lines"`
	Provenance []model.SourceProvenance `json:"provenance"`
	AuthState  AuthState                `json:"authState"`
}
