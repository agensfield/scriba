package render

import (
	"fmt"
	"strings"

	"github.com/agensfield/scriba/internal/model"
)

func Status(snapshot model.StatusSnapshot) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Scriba status · %s\n", snapshot.GeneratedAt)
	for _, provider := range snapshot.Providers {
		fmt.Fprintf(&b, "\n%s [%s]\n", provider.DisplayName, provider.State)
		for _, line := range provider.Lines {
			switch line.Type {
			case "text":
				fmt.Fprintf(&b, "  %s: %v\n", line.Label, line.Value)
			case "badge":
				fmt.Fprintf(&b, "  %s: %s\n", line.Label, line.Text)
			case "amount":
				fmt.Fprintf(&b, "  %s: %v\n", line.Label, line.Value)
			case "progress":
				used := 0.0
				limit := 100.0
				if line.Used != nil {
					used = *line.Used
				}
				if line.Limit != nil && *line.Limit != 0 {
					limit = *line.Limit
				}
				reset := ""
				if line.ResetsAt != "" {
					reset = " · resets " + line.ResetsAt
				}
				fmt.Fprintf(&b, "  %s: %.0f%%%s\n", line.Label, used/limit*100, reset)
			}
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func Report(title string, rows int) string {
	return fmt.Sprintf("%s · %d rows", title, rows)
}

func Doctor(state string) string {
	return "Scriba doctor: " + state
}
