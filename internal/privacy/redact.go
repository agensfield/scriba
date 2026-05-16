package privacy

import (
	"encoding/json"
	"regexp"
	"strings"
)

var (
	emailRE = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	homeRE  = regexp.MustCompile(`/Users/[^/",\s]+`)
	tokenRE = regexp.MustCompile(`(?i)(token|secret|key)["=: ]+[^",\s]+`)
)

func Redact(value any) any {
	data, err := json.Marshal(value)
	if err != nil {
		return value
	}
	text := string(data)
	text = emailRE.ReplaceAllString(text, "[redacted-email]")
	text = homeRE.ReplaceAllString(text, "/Users/[redacted]")
	text = tokenRE.ReplaceAllStringFunc(text, func(match string) string {
		if idx := strings.IndexAny(match, "=: "); idx >= 0 {
			return match[:idx+1] + "[redacted]"
		}
		return "[redacted]"
	})
	var out any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		return value
	}
	return out
}
