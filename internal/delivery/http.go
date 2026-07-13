package delivery

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Disposition string

const (
	Delivered     Disposition = "delivered"
	Retryable     Disposition = "retryable"
	Terminal      Disposition = "terminal"
	maxRetryAfter             = time.Hour
)

type Outcome struct {
	Disposition Disposition
	ProviderID  string
	RetryAfter  time.Duration
	Err         error
}

func classifyHTTP(status int, header http.Header, now time.Time) Outcome {
	out := Outcome{Disposition: Terminal, Err: errors.New(http.StatusText(status))}
	if status >= 200 && status < 300 {
		out.Disposition, out.Err = Delivered, nil
		return out
	}
	if status == http.StatusRequestTimeout || status == http.StatusTooEarly || status == http.StatusTooManyRequests || status >= 500 {
		out.Disposition = Retryable
		out.RetryAfter = retryAfter(header.Get("Retry-After"), now)
	}
	return out
}

func retryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		if seconds <= 0 {
			return 0
		}
		return min(time.Duration(seconds)*time.Second, maxRetryAfter)
	}
	at, err := http.ParseTime(value)
	if err != nil || !at.After(now) {
		return 0
	}
	return min(at.Sub(now), maxRetryAfter)
}

func noRedirectClient(base *http.Client) *http.Client {
	if base == nil {
		base = &http.Client{Timeout: 10 * time.Second}
	}
	client := *base
	if client.Timeout == 0 {
		client.Timeout = 10 * time.Second
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &client
}
