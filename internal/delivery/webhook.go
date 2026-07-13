package delivery

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

type Webhook struct {
	URL    string
	Secret []byte
	Client *http.Client
	Now    func() time.Time
}

func (w Webhook) Deliver(ctx context.Context, envelope Envelope) Outcome {
	body, err := Marshal(envelope)
	if err != nil {
		return Outcome{Disposition: Terminal, Err: err}
	}
	endpoint, err := url.Parse(w.URL)
	if err != nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Host == "" || len(w.Secret) == 0 {
		return Outcome{Disposition: Terminal, Err: errors.New("invalid webhook configuration")}
	}
	now := time.Now().UTC()
	if w.Now != nil {
		now = w.Now().UTC()
	}
	timestamp := strconv.FormatInt(now.Unix(), 10)
	mac := hmac.New(sha256.New, w.Secret)
	_, _ = mac.Write([]byte(timestamp + "."))
	_, _ = mac.Write(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return Outcome{Disposition: Terminal, Err: err}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "scriba-webhook/1")
	req.Header.Set("X-Scriba-Event-ID", envelope.EventID)
	req.Header.Set("X-Scriba-Timestamp", timestamp)
	req.Header.Set("X-Scriba-Signature", "v1="+hex.EncodeToString(mac.Sum(nil)))
	resp, err := noRedirectClient(w.Client).Do(req)
	if err != nil {
		return Outcome{Disposition: Retryable, Err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return classifyHTTP(resp.StatusCode, resp.Header, now)
}
