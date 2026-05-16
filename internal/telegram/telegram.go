package telegram

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/agensfield/scriba/internal/config"
	"github.com/agensfield/scriba/internal/model"
)

type Alert struct {
	ProviderID string `json:"providerId"`
	Label      string `json:"label"`
	Severity   string `json:"severity"`
	Message    string `json:"message"`
}

func Evaluate(snapshot model.StatusSnapshot, cfg config.TelegramConfig) []Alert {
	var alerts []Alert
	if !cfg.Enabled {
		return alerts
	}
	for _, provider := range snapshot.Providers {
		for _, line := range provider.Lines {
			if line.Type != "progress" || line.Used == nil || line.Limit == nil || *line.Limit == 0 {
				continue
			}
			percent := *line.Used / *line.Limit * 100
			threshold := cfg.Alerts.SessionPercent
			if strings.Contains(strings.ToLower(line.Label), "weekly") {
				threshold = cfg.Alerts.WeeklyPercent
			}
			if percent >= threshold {
				alerts = append(alerts, Alert{ProviderID: provider.ProviderID, Label: line.Label, Severity: "warning", Message: fmt.Sprintf("%s %s at %.0f%%", provider.DisplayName, line.Label, percent)})
			}
		}
		if cfg.Alerts.IncludeErrors {
			for _, source := range provider.Provenance {
				if source.Error != "" {
					alerts = append(alerts, Alert{ProviderID: provider.ProviderID, Label: "error", Severity: "error", Message: source.Error})
				}
			}
		}
	}
	return alerts
}

func Send(botToken, chatID string, alerts []Alert) (int, error) {
	sent := 0
	for _, alert := range alerts {
		body, _ := json.Marshal(map[string]string{"chat_id": chatID, "text": alert.Message})
		resp, err := http.Post("https://api.telegram.org/bot"+botToken+"/sendMessage", "application/json", bytes.NewReader(body))
		if err != nil {
			return sent, err
		}
		_ = resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return sent, fmt.Errorf("telegram send failed: %d", resp.StatusCode)
		}
		sent++
	}
	return sent, nil
}
