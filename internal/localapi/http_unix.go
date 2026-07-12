//go:build darwin || linux

package localapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/agensfield/scriba/internal/agentcontext"
)

const (
	healthSchemaVersion = "scriba.local.health.v1"
	streamEventName     = "scriba.event.v1"
)

type HTTPConfig struct {
	MaxStreams        int
	PollInterval      time.Duration
	HeartbeatInterval time.Duration
	WriteTimeout      time.Duration
	RequestTimeout    time.Duration
	ShutdownTimeout   time.Duration
}

type HTTPServer struct {
	listener                                                       *Listener
	service                                                        *agentcontext.Service
	server                                                         *http.Server
	streams                                                        chan struct{}
	poll, heartbeat, writeTimeout, requestTimeout, shutdownTimeout time.Duration
	once                                                           sync.Once
	shutdownDone                                                   chan struct{}
	shutdownErr                                                    error
}

func NewHTTPServer(listener *Listener, service *agentcontext.Service, cfg HTTPConfig) *HTTPServer {
	if cfg.MaxStreams <= 0 {
		cfg.MaxStreams = 8
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 500 * time.Millisecond
	}
	if cfg.HeartbeatInterval <= 0 {
		cfg.HeartbeatInterval = 15 * time.Second
	}
	if cfg.WriteTimeout <= 0 {
		cfg.WriteTimeout = 5 * time.Second
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = 2 * time.Second
	}
	if cfg.ShutdownTimeout <= 0 {
		cfg.ShutdownTimeout = 5 * time.Second
	}
	s := &HTTPServer{listener: listener, service: service, streams: make(chan struct{}, cfg.MaxStreams), poll: cfg.PollInterval, heartbeat: cfg.HeartbeatInterval, writeTimeout: cfg.WriteTimeout, requestTimeout: cfg.RequestTimeout, shutdownTimeout: cfg.ShutdownTimeout, shutdownDone: make(chan struct{})}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/health", s.health)
	mux.HandleFunc("/v1/context", s.context)
	mux.HandleFunc("/v1/events", s.events)
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { writeError(w, http.StatusNotFound, "not_found") })
	s.server = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	return s
}

func (s *HTTPServer) Run(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = s.Shutdown(context.WithoutCancel(ctx))
		case <-done:
		}
	}()
	err := s.server.Serve(s.listener)
	close(done)
	shutdownErr := s.Shutdown(context.WithoutCancel(ctx))
	if errors.Is(err, http.ErrServerClosed) || (ctx.Err() != nil && errors.Is(err, context.Canceled)) {
		return shutdownErr
	}
	return errors.Join(err, shutdownErr)
}

func (s *HTTPServer) Shutdown(ctx context.Context) error {
	s.once.Do(func() {
		defer close(s.shutdownDone)
		if ctx == nil {
			ctx = context.Background()
		}
		bounded, cancel := context.WithTimeout(ctx, s.shutdownTimeout)
		defer cancel()
		s.shutdownErr = s.server.Shutdown(bounded)
		if s.shutdownErr != nil {
			s.shutdownErr = errors.Join(s.shutdownErr, s.server.Close())
		}
		s.shutdownErr = errors.Join(s.shutdownErr, s.listener.Close())
	})
	<-s.shutdownDone
	return s.shutdownErr
}

type healthResponse struct {
	SchemaVersion  string `json:"schemaVersion"`
	Status         string `json:"status"`
	ContextVersion string `json:"contextVersion"`
	EventVersion   string `json:"eventVersion"`
}
type errorResponse struct {
	Error struct {
		Code string `json:"code"`
	} `json:"error"`
}

func validGET(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return false
	}
	if r.ContentLength != 0 || len(r.TransferEncoding) > 0 {
		writeError(w, http.StatusBadRequest, "request_body_not_allowed")
		return false
	}
	return true
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func writeError(w http.ResponseWriter, status int, code string) {
	var out errorResponse
	out.Error.Code = code
	writeJSON(w, status, out)
}

func (s *HTTPServer) health(w http.ResponseWriter, r *http.Request) {
	if !validGET(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, healthResponse{healthSchemaVersion, "ok", agentcontext.SchemaVersion, agentcontext.EventsSchemaVersion})
}
func (s *HTTPServer) context(w http.ResponseWriter, r *http.Request) {
	if !validGET(w, r) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.requestTimeout)
	defer cancel()
	value, err := s.service.Context(ctx)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "context_unavailable")
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func requestedCursor(r *http.Request) (string, string) {
	headers, queries := r.Header.Values("Last-Event-ID"), r.URL.Query()["cursor"]
	if len(headers) > 1 || len(queries) > 1 {
		return "", "invalid_cursor"
	}
	header, query := "", ""
	if len(headers) == 1 {
		header = headers[0]
		if header == "" {
			return "", "invalid_cursor"
		}
	}
	if len(queries) == 1 {
		query = queries[0]
		if query == "" {
			return "", "invalid_cursor"
		}
	}
	if header != strings.TrimSpace(header) || query != strings.TrimSpace(query) {
		return "", "invalid_cursor"
	}
	if header != "" && query != "" && header != query {
		return "", "cursor_disagreement"
	}
	if header != "" {
		return header, ""
	}
	return query, ""
}
func eventError(err error) string {
	var page *agentcontext.EventPageError
	if errors.As(err, &page) {
		switch page.ReasonCode {
		case "invalid_cursor", "cursor_future", "cursor_expired", "events_unavailable", "read_error":
			return page.ReasonCode
		}
	}
	return "events_unavailable"
}
func statusForEventError(code string) int {
	if code == "cursor_expired" {
		return http.StatusGone
	}
	if code == "cursor_future" || code == "invalid_cursor" {
		return http.StatusBadRequest
	}
	return http.StatusServiceUnavailable
}

func (s *HTTPServer) events(w http.ResponseWriter, r *http.Request) {
	if !validGET(w, r) {
		return
	}
	cursor, bad := requestedCursor(r)
	if bad != "" {
		writeError(w, http.StatusBadRequest, bad)
		return
	}
	select {
	case s.streams <- struct{}{}:
		defer func() { <-s.streams }()
	default:
		writeError(w, http.StatusServiceUnavailable, "stream_limit_reached")
		return
	}
	mode := "replay"
	if cursor == "" {
		mode = "capture"
	}
	page, err := s.eventPage(r.Context(), mode, cursor)
	if err != nil {
		code := eventError(err)
		writeError(w, statusForEventError(code), code)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	controller := http.NewResponseController(w)
	if !s.writeFrame(r.Context(), controller, w, ": connected\n\n") {
		return
	}
	cursor, ok := s.drain(r.Context(), controller, w, cursor, page)
	if !ok {
		return
	}
	heartbeat := time.NewTicker(s.heartbeat)
	defer heartbeat.Stop()
	poll := time.NewTicker(s.poll)
	defer poll.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			if !s.writeFrame(r.Context(), controller, w, ": heartbeat\n\n") {
				return
			}
		case <-poll.C:
			page, err = s.eventPage(r.Context(), "replay", cursor)
			if err != nil {
				code := eventError(err)
				if code == "cursor_expired" {
					_ = s.writeFrame(r.Context(), controller, w, "event: cursor_expired\ndata: {\"reasonCode\":\"cursor_expired\"}\n\n")
				}
				return
			}
			cursor, ok = s.drain(r.Context(), controller, w, cursor, page)
			if !ok {
				return
			}
		}
	}
}

func (s *HTTPServer) eventPage(parent context.Context, mode, cursor string) (agentcontext.EventPage, error) {
	ctx, cancel := context.WithTimeout(parent, s.requestTimeout)
	defer cancel()
	return s.service.Events(ctx, agentcontext.EventPageRequest{Mode: mode, Cursor: cursor, Limit: 1})
}

func (s *HTTPServer) drain(ctx context.Context, controller *http.ResponseController, w http.ResponseWriter, cursor string, page agentcontext.EventPage) (string, bool) {
	for {
		next := page.Cursor.Next
		if len(page.Events) > 0 {
			data, err := json.Marshal(page.Events[0])
			if err != nil {
				return cursor, false
			}
			if !s.writeFrame(ctx, controller, w, fmt.Sprintf("id: %s\nevent: %s\ndata: %s\n\n", next, streamEventName, data)) {
				return cursor, false
			}
		}
		if next == cursor && next != page.Cursor.HighWater {
			return cursor, false
		}
		cursor = next
		if cursor == page.Cursor.HighWater {
			return cursor, true
		}
		var err error
		page, err = s.eventPage(ctx, "replay", cursor)
		if err != nil {
			if eventError(err) == "cursor_expired" {
				_ = s.writeFrame(ctx, controller, w, "event: cursor_expired\ndata: {\"reasonCode\":\"cursor_expired\"}\n\n")
			}
			return cursor, false
		}
	}
}

func (s *HTTPServer) writeFrame(ctx context.Context, controller *http.ResponseController, w http.ResponseWriter, frame string) bool {
	if err := controller.SetWriteDeadline(time.Now().Add(s.writeTimeout)); err != nil {
		return false
	}
	if _, err := fmt.Fprint(w, frame); err != nil {
		return false
	}
	if err := controller.Flush(); err != nil {
		return false
	}
	return ctx.Err() == nil
}
