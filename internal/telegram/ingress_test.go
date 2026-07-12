package telegram

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agensfield/scriba/internal/radar"
	"github.com/agensfield/scriba/internal/server/store"
	tgbot "github.com/go-telegram/bot"
)

type fakeUpdateStore struct {
	mu           sync.Mutex
	rows         []store.TelegramUpdate
	stageStarted chan struct{}
	releaseStage chan struct{}
	stageErr     error
}

func (f *fakeUpdateStore) StageTelegramUpdates(ctx context.Context, _ string, in []store.TelegramUpdateInput, now time.Time) error {
	if f.stageStarted != nil {
		close(f.stageStarted)
	}
	if f.releaseStage != nil {
		select {
		case <-f.releaseStage:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if f.stageErr != nil {
		return f.stageErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, x := range in {
		found := false
		for _, r := range f.rows {
			if r.UpdateID == x.UpdateID {
				found = true
			}
		}
		if !found {
			f.rows = append(f.rows, store.TelegramUpdate{BotRef: "default", UpdateID: x.UpdateID, RawJSON: x.RawJSON, Status: "pending", AvailableAt: now})
		}
	}
	return nil
}
func (f *fakeUpdateStore) DueTelegramUpdates(context.Context, string, time.Time, int) ([]store.TelegramUpdate, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []store.TelegramUpdate
	for _, r := range f.rows {
		if r.Status == "pending" {
			out = append(out, r)
		}
	}
	return out, nil
}
func (f *fakeUpdateStore) MarkTelegramUpdateProcessed(_ context.Context, _ string, id int64, _ time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.rows {
		if f.rows[i].UpdateID == id && f.rows[i].Status == "pending" {
			f.rows[i].Status = "processed"
			return true, nil
		}
	}
	return false, nil
}
func (f *fakeUpdateStore) MarkTelegramUpdateFailure(_ context.Context, _ string, id int64, msg string, _ time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.rows {
		if f.rows[i].UpdateID == id && f.rows[i].Status == "pending" {
			f.rows[i].Attempts++
			f.rows[i].LastError = msg
			return true, nil
		}
	}
	return false, nil
}
func (f *fakeUpdateStore) MarkTelegramUpdateDead(_ context.Context, _ string, id int64, msg string, _ time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.rows {
		if f.rows[i].UpdateID == id && f.rows[i].Status == "pending" {
			f.rows[i].Status = "dead"
			f.rows[i].LastError = msg
			return true, nil
		}
	}
	return false, nil
}

type staticClient struct {
	body   string
	called int
}

func (c *staticClient) Do(*http.Request) (*http.Response, error) {
	c.called++
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(c.body)), Header: make(http.Header)}, nil
}

func TestStagingHTTPClientBarsBodyUntilCommit(t *testing.T) {
	f := &fakeUpdateStore{stageStarted: make(chan struct{}), releaseStage: make(chan struct{})}
	c := &stagingHTTPClient{next: &staticClient{body: `{"ok":true,"result":[{"update_id":1}]}`}, store: f, botRef: "default"}
	req, _ := http.NewRequest(http.MethodPost, "https://x/bot/getUpdates", nil)
	done := make(chan *http.Response, 1)
	go func() { r, _ := c.Do(req); done <- r }()
	<-f.stageStarted
	select {
	case <-done:
		t.Fatal("body returned before stage commit")
	default:
	}
	close(f.releaseStage)
	select {
	case r := <-done:
		b, _ := io.ReadAll(r.Body)
		if string(b) != `{"ok":true,"result":[{"update_id":1}]}` {
			t.Fatalf("body=%q", b)
		}
	case <-time.After(time.Second):
		t.Fatal("barrier did not release")
	}
}

func TestStagingHTTPClientFailureDoesNotDeliverBody(t *testing.T) {
	f := &fakeUpdateStore{stageErr: errors.New("disk full")}
	c := &stagingHTTPClient{next: &staticClient{body: `{"ok":true,"result":[{"update_id":1}]}`}, store: f, botRef: "default"}
	req, _ := http.NewRequest(http.MethodPost, "https://x/bot/getUpdates", nil)
	resp, err := c.Do(req)
	if err == nil || resp != nil {
		t.Fatalf("resp=%v err=%v", resp, err)
	}
}

func TestInboxReplayProcessedIgnoredMalformedAndCancel(t *testing.T) {
	f := &fakeUpdateStore{rows: []store.TelegramUpdate{{BotRef: "default", UpdateID: 1, RawJSON: `{"update_id":1,"message":{"message_id":1,"date":0,"chat":{"id":999,"type":"private"},"text":"/help"}}`, Status: "pending"}, {BotRef: "default", UpdateID: 2, RawJSON: `{"update_id":2,"message":{"message_id":2,"date":0,"chat":{"id":123,"type":"private"}}}`, Status: "pending"}, {BotRef: "default", UpdateID: 3, RawJSON: `bad`, Status: "pending"}, {BotRef: "default", UpdateID: 4, RawJSON: `{"update_id":4}`, Status: "processed"}}}
	s := &Service{cfg: BotConfig{ChatID: 123}, updates: f, botRef: "default", logger: slog.Default()}
	if err := s.drainTelegramUpdates(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.rows[0].Status != "processed" || f.rows[1].Status != "processed" || f.rows[2].Status != "dead" || f.rows[3].Status != "processed" {
		t.Fatalf("rows=%+v", f.rows)
	}
	f.rows = append(f.rows, store.TelegramUpdate{BotRef: "default", UpdateID: 5, RawJSON: `{"update_id":5}`, Status: "pending"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.drainTelegramUpdates(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
	if f.rows[4].Status != "pending" || f.rows[4].Attempts != 0 {
		t.Fatalf("canceled row=%+v", f.rows[4])
	}
}

func TestFinalHTTPClientOptionCannotBypassStaging(t *testing.T) {
	seen := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- struct{}{}
		_, _ = w.Write([]byte(`{"ok":true,"result":[]}`))
	}))
	defer server.Close()
	bypass := &staticClient{body: `{"ok":true,"result":[]}`}
	offsets := &fakeDurableOffsets{fakeUpdateStore: fakeUpdateStore{}}
	s, err := newBotService(BotConfig{Token: "test", ChatID: 123}, nil, offsets, nil, radar.Client{}, tgbot.WithServerURL(server.URL), tgbot.WithSkipGetMe(), tgbot.WithHTTPClient(time.Second, bypass))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go s.bot.Start(ctx)
	select {
	case <-seen:
	case <-time.After(time.Second):
		t.Fatal("final client did not reach configured server")
	}
	cancel()
	if bypass.called != 0 {
		t.Fatalf("bypass client called %d times", bypass.called)
	}
}

func TestCommandFailureRetriesThenProcesses(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request := requests.Add(1)
		if request == 1 {
			time.Sleep(100 * time.Millisecond)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1,"date":0,"chat":{"id":123,"type":"private"}}}`))
	}))
	defer server.Close()
	b, err := tgbot.New("test", tgbot.WithServerURL(server.URL), tgbot.WithSkipGetMe())
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeUpdateStore{rows: []store.TelegramUpdate{{BotRef: "default", UpdateID: 9, RawJSON: `{"update_id":9,"message":{"message_id":1,"date":0,"chat":{"id":123,"type":"private"},"text":"/help"}}`, Status: "pending"}}}
	s := &Service{cfg: BotConfig{ChatID: 123}, updates: f, botRef: "default", logger: slog.Default(), bot: b, apiTimeout: 20 * time.Millisecond}
	if err = s.drainTelegramUpdates(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = s.drainTelegramUpdates(context.Background()); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 2 || f.rows[0].Attempts != 1 || f.rows[0].Status != "processed" {
		t.Fatalf("requests=%d row=%+v", requests.Load(), f.rows[0])
	}
}

func TestStartCancelsAndJoinsBothWorkersWhenBotReturns(t *testing.T) {
	retryCanceled, inboxCanceled := make(chan struct{}), make(chan struct{})
	release, done := make(chan struct{}), make(chan struct{})
	waiter := func(seen chan struct{}) func(context.Context) {
		return func(ctx context.Context) { <-ctx.Done(); close(seen); <-release }
	}
	s := &Service{logger: slog.Default(), startBot: func(context.Context) {}, retryLoop: waiter(retryCanceled), inboxLoop: waiter(inboxCanceled)}
	go func() { s.Start(context.Background()); close(done) }()
	for _, ch := range []chan struct{}{retryCanceled, inboxCanceled} {
		select {
		case <-ch:
		case <-time.After(time.Second):
			t.Fatal("worker was not canceled")
		}
	}
	select {
	case <-done:
		t.Fatal("Start returned before workers joined")
	default:
	}
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Start did not join workers")
	}
}

type fakeDurableOffsets struct{ fakeUpdateStore }

func (*fakeDurableOffsets) GetTelegramOffset(context.Context, string) (int64, bool, error) {
	return 0, false, nil
}
func (*fakeDurableOffsets) SetTelegramOffset(context.Context, string, int64) error { return nil }
