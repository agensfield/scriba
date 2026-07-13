//go:build darwin || linux

package localapi

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/agensfield/scriba/internal/agentcontext"
	"github.com/agensfield/scriba/internal/resetwatch"
	"github.com/agensfield/scriba/internal/server/store"
)

type unixHTTPFixture struct {
	path      string
	storePath string
	service   *agentcontext.Service
	store     *store.Store
	server    *HTTPServer
	client    *http.Client
	cancel    context.CancelFunc
	done      chan error
}

func newUnixHTTPFixture(t *testing.T, maxStreams int) *unixHTTPFixture {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "scriba-http-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "api.sock")
	storePath := filepath.Join(dir, "store.sqlite")
	st, err := store.Open(storePath)
	if err != nil {
		t.Fatal(err)
	}
	used := 81.0
	period := int64((5 * time.Hour) / time.Millisecond)
	now := time.Now().UTC().Truncate(time.Second)
	obs := resetwatch.Observation{ProviderID: "codex", Account: resetwatch.Account{Ref: "acct-private-sentinel"}, ObservedAt: now, Windows: []resetwatch.Window{{Label: resetwatch.LabelFiveHour, UsedPercent: &used, ResetAt: now.Add(5 * time.Hour), PeriodDurationMs: &period}}, SnapshotJSON: []byte(`{"secret":"privacy-sentinel"}`)}
	if _, err = st.ApplyDecision(t.Context(), obs, resetwatch.Decision{}); err != nil {
		t.Fatal(err)
	}
	svc := agentcontext.New(agentcontext.Config{StorePath: storePath, CacheDir: filepath.Join(dir, "missing-cache"), ProfileID: "profile", Clock: func() time.Time { return now }})
	ln, err := Listen(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	srv := NewHTTPServer(ln, svc, HTTPConfig{MaxStreams: maxStreams, PollInterval: 10 * time.Millisecond, HeartbeatInterval: 40 * time.Millisecond, RequestTimeout: time.Second, ShutdownTimeout: time.Second})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	}}
	f := &unixHTTPFixture{path: path, storePath: storePath, service: svc, store: st, server: srv, client: &http.Client{Transport: transport}, cancel: cancel, done: done}
	t.Cleanup(func() {
		transport.CloseIdleConnections()
		cancel()
		_ = srv.Shutdown(context.Background())
		_ = st.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("Run did not exit")
		}
	})
	return f
}

func (f *unixHTTPFixture) get(t *testing.T, path string) *http.Response {
	t.Helper()
	r, err := f.client.Get("http://unix" + path)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestUnixHTTPContextExactParityAndPrivacy(t *testing.T) {
	f := newUnixHTTPFixture(t, 2)
	want, err := f.service.Context(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	r := f.get(t, "/v1/context")
	defer func() { _ = r.Body.Close() }()
	var got agentcontext.Context
	if err = json.NewDecoder(r.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("HTTP context differs\ngot=%#v\nwant=%#v", got, want)
	}
	b, _ := json.Marshal(got)
	for _, s := range []string{"acct-private-sentinel", "privacy-sentinel", "snapshotJSON", "accountRef", "store.sqlite"} {
		if strings.Contains(string(b), s) {
			t.Fatalf("private sentinel leaked: %q", s)
		}
	}
	if version, err := f.store.SchemaVersion(t.Context()); err != nil || version != store.SchemaVersion {
		t.Fatalf("schema changed: %d %v", version, err)
	}
}

func readSSEFrame(t *testing.T, body io.ReadCloser, reader *bufio.Reader) string {
	t.Helper()
	result := make(chan struct {
		frame string
		err   error
	}, 1)
	go func() {
		var b strings.Builder
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				result <- struct {
					frame string
					err   error
				}{err: err}
				return
			}
			b.WriteString(line)
			if line == "\n" {
				result <- struct {
					frame string
					err   error
				}{frame: b.String()}
				return
			}
		}
	}()
	select {
	case got := <-result:
		if got.err != nil {
			t.Fatal(got.err)
		}
		return got.frame
	case <-time.After(500 * time.Millisecond):
		_ = body.Close()
		<-result
		t.Fatal("SSE frame timeout")
		return ""
	}
}

func insertWarning(t *testing.T, f *unixHTTPFixture, id string, at time.Time) {
	t.Helper()
	w := resetwatch.WarningEvent{ID: id, ProviderID: "codex", Account: resetwatch.Account{Ref: "acct-private-sentinel"}, Label: resetwatch.LabelFiveHour, ThresholdRemaining: 20, UsedPercent: 81, RemainingPercent: 19, ResetAt: at.Add(time.Hour), SnapshotJSON: []byte(`{"secret":"privacy-sentinel"}`), DetectedAt: at}
	payload, err := store.EncodeOutboxPayload("limit_warning", w)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", f.storePath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	atText := at.Format(time.RFC3339Nano)
	_, err = db.ExecContext(t.Context(), `insert into policy_events(id,semantic_key,event_kind,semantic_event_id,rule_id,subject_key,rule_kind,provider_id,account_ref,policy_revision,config_hash,payload_version,payload_json,detected_at,created_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, id, "limit_warning", id, "rule", "subject", "remaining_checkpoint", "codex", "acct-private-sentinel", "rev", "hash", 1, string(payload), atText, atText)
	if err != nil {
		t.Fatal(err)
	}
}

func execStore(t *testing.T, f *unixHTTPFixture, query string, args ...any) {
	t.Helper()
	db, err := sql.Open("sqlite", f.storePath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err = db.ExecContext(t.Context(), query, args...); err != nil {
		t.Fatal(err)
	}
}

func TestUnixSSEExplicitBacklogDrainsImmediatelyInOrder(t *testing.T) {
	f := newUnixHTTPFixture(t, 2)
	f.server.poll = time.Hour
	at := time.Now().UTC()
	insertWarning(t, f, "backlog-one", at)
	insertWarning(t, f, "backlog-two", at.Add(time.Second))
	insertWarning(t, f, "backlog-three", at.Add(2*time.Second))
	r := f.get(t, "/v1/events?cursor=v1.0000000000000000")
	defer func() { _ = r.Body.Close() }()
	reader := bufio.NewReader(r.Body)
	if got := readSSEFrame(t, r.Body, reader); got != ": connected\n\n" {
		t.Fatalf("connected=%q", got)
	}
	for _, id := range []string{"backlog-one", "backlog-two", "backlog-three"} {
		frame := readSSEFrame(t, r.Body, reader)
		if !strings.Contains(frame, "\"id\":\""+id+"\"") {
			t.Fatalf("want %s, frame=%q", id, frame)
		}
	}
}

func TestUnixSSETombstoneAndPoisonAdvanceToValidEvent(t *testing.T) {
	f := newUnixHTTPFixture(t, 1)
	at := time.Now().UTC()
	insertWarning(t, f, "tombstone", at)
	execStore(t, f, `delete from policy_events where id='tombstone'`)
	insertWarning(t, f, "poison", at.Add(time.Second))
	execStore(t, f, `update policy_events set payload_json='{}' where id='poison'`)
	insertWarning(t, f, "after-poison", at.Add(2*time.Second))
	r := f.get(t, "/v1/events?cursor=v1.0000000000000000")
	defer func() { _ = r.Body.Close() }()
	reader := bufio.NewReader(r.Body)
	_ = readSSEFrame(t, r.Body, reader)
	for i := 0; i < 20; i++ {
		frame := readSSEFrame(t, r.Body, reader)
		if strings.HasPrefix(frame, ": heartbeat") {
			continue
		}
		if strings.Contains(frame, "after-poison") {
			return
		}
		t.Fatalf("unexpected event: %q", frame)
	}
	t.Fatal("valid event not reached")
}

func TestUnixSSEExpiredCursorBeforeHeaders(t *testing.T) {
	f := newUnixHTTPFixture(t, 1)
	at := time.Now().UTC()
	insertWarning(t, f, "expired-one", at)
	insertWarning(t, f, "expired-two", at.Add(time.Second))
	execStore(t, f, `delete from policy_event_replay where replay_seq=(select min(replay_seq) from policy_event_replay)`)
	r := f.get(t, "/v1/events?cursor=v1.0000000000000000")
	defer func() { _ = r.Body.Close() }()
	if r.StatusCode != http.StatusGone {
		body, _ := io.ReadAll(r.Body)
		t.Fatalf("status=%d body=%s", r.StatusCode, body)
	}
}

func TestUnixSSECaptureLiveReconnectHeartbeatAndStreamCap(t *testing.T) {
	f := newUnixHTTPFixture(t, 1)
	r := f.get(t, "/v1/events")
	reader := bufio.NewReader(r.Body)
	if frame := readSSEFrame(t, r.Body, reader); frame != ": connected\n\n" {
		t.Fatalf("connected=%q", frame)
	}
	second := f.get(t, "/v1/events")
	if second.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("stream cap=%d", second.StatusCode)
	}
	_ = second.Body.Close()
	if frame := readSSEFrame(t, r.Body, reader); frame != ": heartbeat\n\n" {
		t.Fatalf("heartbeat=%q", frame)
	}
	insertWarning(t, f, "event-one", time.Now().UTC())
	frame := readSSEFrame(t, r.Body, reader)
	for strings.HasPrefix(frame, ": heartbeat") {
		frame = readSSEFrame(t, r.Body, reader)
	}
	if !strings.Contains(frame, "event: scriba.event.v1\n") || !strings.Contains(frame, "\"id\":\"event-one\"") || strings.Contains(frame, "privacy-sentinel") {
		t.Fatalf("event frame=%q", frame)
	}
	var cursor string
	for _, line := range strings.Split(frame, "\n") {
		if strings.HasPrefix(line, "id: ") {
			cursor = strings.TrimPrefix(line, "id: ")
		}
	}
	if cursor == "" {
		t.Fatal("missing cursor")
	}
	_ = r.Body.Close()
	insertWarning(t, f, "event-two", time.Now().UTC().Add(time.Second))
	req, _ := http.NewRequest(http.MethodGet, "http://unix/v1/events", nil)
	req.Header.Set("Last-Event-ID", cursor)
	var replay *http.Response
	deadline := time.Now().Add(time.Second)
	for {
		var err error
		replay, err = f.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if replay.StatusCode == http.StatusOK {
			break
		}
		_, _ = io.Copy(io.Discard, replay.Body)
		_ = replay.Body.Close()
		if replay.StatusCode != http.StatusServiceUnavailable || time.Now().After(deadline) {
			t.Fatalf("reconnect status=%d", replay.StatusCode)
		}
		time.Sleep(10 * time.Millisecond)
	}
	rr := bufio.NewReader(replay.Body)
	_ = readSSEFrame(t, replay.Body, rr)
	frame = readSSEFrame(t, replay.Body, rr)
	if !strings.Contains(frame, "\"id\":\"event-two\"") || strings.Contains(frame, "event-one") {
		t.Fatalf("reconnect frame=%q", frame)
	}
	_ = replay.Body.Close()
}

func TestUnixSSEFutureAndShutdownCleanup(t *testing.T) {
	f := newUnixHTTPFixture(t, 1)
	r := f.get(t, "/v1/events?cursor=v1.7fffffffffffffff")
	if r.StatusCode != http.StatusBadRequest {
		t.Fatalf("future=%d", r.StatusCode)
	}
	_, _ = io.Copy(io.Discard, r.Body)
	_ = r.Body.Close()
	f.cancel()
	select {
	case err := <-f.done:
		if err != nil {
			t.Fatal(err)
		}
		f.done <- err
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not exit")
	}
	if _, err := os.Lstat(f.path); !os.IsNotExist(err) {
		t.Fatalf("socket remains: %v", err)
	}
	ln, err := Listen(t.Context(), f.path)
	if err != nil {
		t.Fatalf("lease not released: %v", err)
	}
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
}
