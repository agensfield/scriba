package delivery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Ntfy struct {
	ID     string
	URL    string
	Topic  string
	Token  string
	Client *http.Client
	Now    func() time.Time
}

func (n Ntfy) Target() string { return "ntfy:" + n.ID }

func (n Ntfy) Deliver(ctx context.Context, envelope Envelope) Outcome {
	canonical, err := Marshal(envelope)
	if err != nil {
		return Outcome{Disposition: Terminal, Err: err}
	}
	endpoint, err := url.Parse(n.URL)
	if err != nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || strings.TrimSpace(n.Topic) == "" || strings.TrimSpace(n.Topic) != n.Topic {
		return Outcome{Disposition: Terminal, Err: errors.New("invalid ntfy configuration")}
	}
	payload, err := json.Marshal(struct {
		Topic   string `json:"topic"`
		Title   string `json:"title"`
		Message string `json:"message"`
	}{Topic: n.Topic, Title: "Scriba: " + envelope.EventKind, Message: string(canonical)})
	if err != nil {
		return Outcome{Disposition: Terminal, Err: err}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(payload))
	if err != nil {
		return Outcome{Disposition: Terminal, Err: err}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "scriba-ntfy/1")
	if n.Token != "" {
		req.Header.Set("Authorization", "Bearer "+n.Token)
	}
	now := time.Now().UTC()
	if n.Now != nil {
		now = n.Now().UTC()
	}
	resp, err := noRedirectClient(n.Client).Do(req)
	if err != nil {
		return Outcome{Disposition: Retryable, Err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	out := classifyHTTP(resp.StatusCode, resp.Header, now)
	if out.Disposition == Delivered {
		var ack struct {
			ID string `json:"id"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&ack)
		out.ProviderID = ack.ID
	}
	return out
}
