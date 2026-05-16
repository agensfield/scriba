package privacy

import (
	"encoding/json"
	"regexp"
	"strings"
)

var (
	emailRE = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	homeRE  = regexp.MustCompile(`/Users/[^/",\s]+`)
)

func Redact(value any) any {
	data, err := json.Marshal(value)
	if err != nil {
		return value
	}
	var keyed any
	if err := json.Unmarshal(data, &keyed); err == nil {
		if redacted, err := json.Marshal(redactKeys(keyed)); err == nil {
			data = redacted
		}
	}
	text := string(data)
	text = emailRE.ReplaceAllString(text, "[redacted-email]")
	text = homeRE.ReplaceAllString(text, "/Users/[redacted]")
	var out any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		return value
	}
	return redactKeys(out)
}

func redactKeys(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			lower := strings.ToLower(key)
			if strings.Contains(lower, "token") ||
				strings.Contains(lower, "secret") ||
				strings.Contains(lower, "key") {
				typed[key] = "[redacted]"
				continue
			}
			typed[key] = redactKeys(child)
		}
		return typed
	case []any:
		for index, child := range typed {
			typed[index] = redactKeys(child)
		}
		return typed
	default:
		return value
	}
}
