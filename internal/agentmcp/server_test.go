package agentmcp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/agensfield/scriba/internal/agentcontext"
	"github.com/agensfield/scriba/internal/resetwatch"
	"github.com/agensfield/scriba/internal/server/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

func TestToolsAndCalls(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	service := agentcontext.New(agentcontext.Config{CacheDir: filepath.Join(t.TempDir(), "cache"), StorePath: filepath.Join(t.TempDir(), "store.db")})
	server := NewServer(service)
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	st, ct := mcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ss.Close() }()
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cs.Close() }()

	listed, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Tools) != 2 || listed.Tools[0].Name != GetContextTool || listed.Tools[1].Name != ListEventsTool {
		t.Fatalf("unexpected tools: %v", toolNames(listed.Tools))
	}
	for _, tool := range listed.Tools {
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint || tool.Annotations.OpenWorldHint == nil || *tool.Annotations.OpenWorldHint {
			t.Fatalf("bad annotations for %s", tool.Name)
		}
		raw, err := json.Marshal(tool.OutputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var document any
		if err := json.Unmarshal(raw, &document); err != nil {
			t.Fatal(err)
		}
		compiler := jsonschema.NewCompiler()
		if err := compiler.AddResource(tool.Name, document); err != nil {
			t.Fatal(err)
		}
		if _, err := compiler.Compile(tool.Name); err != nil {
			t.Fatalf("offline output schema %s: %v", tool.Name, err)
		}
	}

	result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: GetContextTool, Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("context error: %#v", result.Content)
	}
	text := result.Content[0].(*mcp.TextContent).Text
	structured, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if text != string(structured) {
		t.Fatalf("text and structured output differ\ntext=%s\nstructured=%s", text, structured)
	}
	var envelope struct {
		SchemaVersion string `json:"schemaVersion"`
	}
	if err := json.Unmarshal(structured, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.SchemaVersion != agentcontext.SchemaVersion {
		t.Fatalf("schemaVersion=%q", envelope.SchemaVersion)
	}

	for _, args := range []map[string]any{{"unknown": true}, {"account": "INVALID"}, {"profile": "legacy"}, {"limit": 0}, {"limit": 101}, {"cursor": "bad"}, {"mode": "latest", "cursor": "v1.0000000000000000"}} {
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: ListEventsTool, Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		if !res.IsError {
			t.Fatalf("args %#v accepted", args)
		}
	}
}

func TestSuccessfulEventsReadOnlyAndConcurrent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "store.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 7, 12, 8, 0, 0, 0, time.UTC)
	period := int64((5 * time.Hour) / time.Millisecond)
	observation := func(at time.Time, used float64) resetwatch.Observation {
		return resetwatch.Observation{ProviderID: "codex", Account: resetwatch.Account{Ref: "acct", Label: "Test"}, ObservedAt: at, SnapshotJSON: []byte(`{"fixture":true}`), Windows: []resetwatch.Window{{Label: resetwatch.LabelFiveHour, UsedPercent: &used, ResetAt: base.Add(5 * time.Hour), PeriodDurationMs: &period}}}
	}
	for _, item := range []struct {
		at   time.Time
		used float64
	}{{base, 70}, {base.Add(time.Minute), 81}} {
		if _, err := st.ApplyCodexPoll(ctx, store.CodexPollInput{Observation: observation(item.at, item.used), NotificationTarget: "test", ResetOptions: resetwatch.DefaultOptions(), CommittedAt: item.at.Add(time.Second)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	before := fileDigest(t, path)
	service := agentcontext.New(agentcontext.Config{StorePath: path, CacheDir: filepath.Join(t.TempDir(), "cache"), Clock: func() time.Time { return base.Add(time.Hour) }})
	server := NewServer(service)
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	serverT, clientT := mcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, serverT, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ss.Close() }()
	cs, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cs.Close() }()
	wantContext, err := service.Context(ctx)
	if err != nil {
		t.Fatal(err)
	}
	contextResult, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: GetContextTool, Arguments: map[string]any{}})
	if err != nil || contextResult.IsError {
		t.Fatalf("context result=%#v err=%v", contextResult, err)
	}
	contextJSON, _ := json.Marshal(contextResult.StructuredContent)
	wantContextJSON, _ := json.Marshal(wantContext)
	if !equalJSON(contextJSON, wantContextJSON) {
		t.Fatalf("MCP/direct context differ\ngot=%s\nwant=%s", contextJSON, wantContextJSON)
	}
	wantEvents, err := service.Events(ctx, agentcontext.EventPageRequest{Mode: "latest", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	eventsResult, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: ListEventsTool, Arguments: map[string]any{}})
	if err != nil || eventsResult.IsError {
		t.Fatalf("events result=%#v err=%v", eventsResult, err)
	}
	eventsJSON, _ := json.Marshal(eventsResult.StructuredContent)
	wantEventsJSON, _ := json.Marshal(wantEvents)
	if !equalJSON(eventsJSON, wantEventsJSON) {
		t.Fatalf("MCP/direct events differ\ngot=%s\nwant=%s", eventsJSON, wantEventsJSON)
	}
	call := func() {
		result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: ListEventsTool, Arguments: map[string]any{}})
		if err != nil {
			t.Error(err)
			return
		}
		if result.IsError {
			t.Errorf("event error: %#v", result.Content)
			return
		}
		text := result.Content[0].(*mcp.TextContent).Text
		raw, _ := json.Marshal(result.StructuredContent)
		if text != string(raw) {
			t.Error("event text/structured differ")
		}
		var page struct {
			Events []json.RawMessage `json:"events"`
		}
		if json.Unmarshal(raw, &page) != nil || len(page.Events) != 1 {
			t.Errorf("page=%s", raw)
		}
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); call() }()
	}
	wg.Wait()
	replay, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: ListEventsTool, Arguments: map[string]any{"mode": "replay", "cursor": "v1.0000000000000000"}})
	if err != nil || replay.IsError {
		t.Fatalf("explicit replay failed: result=%#v err=%v", replay, err)
	}
	if after := fileDigest(t, path); after != before {
		t.Fatalf("MCP reads mutated store: before=%v after=%v", before, after)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := cs.CallTool(cancelled, &mcp.CallToolParams{Name: GetContextTool, Arguments: map[string]any{}}); err == nil {
		t.Error("cancelled call succeeded")
	}
}

func equalJSON(a, b []byte) bool {
	var left, right any
	return json.Unmarshal(a, &left) == nil && json.Unmarshal(b, &right) == nil && reflect.DeepEqual(left, right)
}

type fileSnapshot struct {
	Digest  string
	Size    int64
	ModTime int64
}

func fileDigest(t *testing.T, path string) fileSnapshot {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return fileSnapshot{Digest: hex.EncodeToString(sum[:]), Size: info.Size(), ModTime: info.ModTime().UnixNano()}
}

type fakeService struct {
	contextErr     error
	eventsErr      error
	entered        chan struct{}
	contextAccount string
	eventsRequest  agentcontext.EventPageRequest
}

func (f *fakeService) Context(ctx context.Context) (agentcontext.Context, error) {
	if f.entered != nil {
		close(f.entered)
		<-ctx.Done()
		return agentcontext.Context{}, ctx.Err()
	}
	return agentcontext.Context{SchemaVersion: agentcontext.SchemaVersion, GeneratedAt: time.Now(), Sources: []agentcontext.Source{}, Providers: []agentcontext.Provider{}, Events: []agentcontext.Event{}}, f.contextErr
}

func (f *fakeService) ContextForAccount(ctx context.Context, account string) (agentcontext.Context, error) {
	f.contextAccount = account
	return f.Context(ctx)
}
func (f *fakeService) Events(_ context.Context, request agentcontext.EventPageRequest) (agentcontext.EventPage, error) {
	f.eventsRequest = request
	return agentcontext.EventPage{SchemaVersion: agentcontext.EventsSchemaVersion, GeneratedAt: time.Now(), AccountID: "acct-00000000000000000001", Events: []agentcontext.Event{}, Cursor: agentcontext.EventPageCursor{Next: "v1.0000000000000000", HighWater: "v1.0000000000000000"}}, f.eventsErr
}

func TestAccountArgumentsReachMCPService(t *testing.T) {
	fake := &fakeService{}
	cs, closeFn := testClient(t, NewServer(fake))
	defer closeFn()
	if _, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: GetContextTool, Arguments: map[string]any{"account": "work"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: ListEventsTool, Arguments: map[string]any{"account": "personal"}}); err != nil {
		t.Fatal(err)
	}
	if fake.contextAccount != "work" || fake.eventsRequest.Account != "personal" {
		t.Fatalf("context=%q events=%+v", fake.contextAccount, fake.eventsRequest)
	}
}

func TestSafeToolErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"future", &agentcontext.EventPageError{ReasonCode: "cursor_future"}, "cursor_future"},
		{"expired", &agentcontext.EventPageError{ReasonCode: "cursor_expired"}, "cursor_expired"},
		{"unavailable", &agentcontext.EventPageError{ReasonCode: "events_unavailable"}, "events_unavailable"},
		{"account", &agentcontext.AccountError{ReasonCode: "account_unavailable"}, "account_unavailable"},
		{"private", errors.New("open /Users/arda/.secrets/account-token: permission denied"), "data unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cs, closeFn := testClient(t, NewServer(&fakeService{eventsErr: tc.err}))
			defer closeFn()
			res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: ListEventsTool, Arguments: map[string]any{}})
			if err != nil {
				t.Fatal(err)
			}
			if !res.IsError || len(res.Content) != 1 || res.Content[0].(*mcp.TextContent).Text != tc.want {
				t.Fatalf("result=%#v want=%q", res, tc.want)
			}
		})
	}
}

func TestHandlerCancellationPropagates(t *testing.T) {
	fake := &fakeService{entered: make(chan struct{})}
	cs, closeFn := testClient(t, NewServer(fake))
	defer closeFn()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: GetContextTool, Arguments: map[string]any{}})
		done <- err
	}()
	<-fake.entered
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not observe cancellation")
	}
}

func testClient(t *testing.T, server *mcp.Server) (*mcp.ClientSession, func()) {
	t.Helper()
	ctx := context.Background()
	st, ct := mcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	return cs, func() { _ = cs.Close(); _ = ss.Close() }
}

func TestStdioHelper(t *testing.T) {
	if os.Getenv("SCRIBA_MCP_TEST_HELPER") != "1" {
		return
	}
	path := os.Getenv("SCRIBA_MCP_TEST_STORE")
	if err := createEventFixture(path); err != nil {
		panic(err)
	}
	service := agentcontext.New(agentcontext.Config{StorePath: path, CacheDir: path + "-cache"})
	if err := RunStdio(context.Background(), service); err != nil && !errors.Is(err, context.Canceled) {
		panic(err)
	}
}

func TestCommandTransportStdio(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stdio.db")
	var stderr bytes.Buffer
	cmd := exec.Command(os.Args[0], "-test.run=^TestStdioHelper$")
	cmd.Env = append(os.Environ(), "SCRIBA_MCP_TEST_HELPER=1", "SCRIBA_MCP_TEST_STORE="+path)
	cmd.Stderr = &stderr
	client := mcp.NewClient(&mcp.Implementation{Name: "stdio-test", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("connect: %v stderr=%s", err, stderr.String())
	}
	listed, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Tools) != 2 {
		t.Fatalf("tools=%v", toolNames(listed.Tools))
	}
	for _, name := range []string{GetContextTool, ListEventsTool} {
		res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: map[string]any{}})
		if err != nil || res.IsError {
			t.Fatalf("%s: result=%#v err=%v stderr=%s", name, res, err, stderr.String())
		}
	}
	replay, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: ListEventsTool, Arguments: map[string]any{"mode": "replay", "cursor": "v1.0000000000000000"}})
	if err != nil || replay.IsError {
		t.Fatalf("stdio replay: result=%#v err=%v stderr=%s", replay, err, stderr.String())
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for cmd.ProcessState == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if cmd.ProcessState == nil {
		t.Fatal("stdio helper did not exit after EOF")
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected helper diagnostics: %s", stderr.String())
	}
}

func createEventFixture(path string) error {
	st, err := store.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	base := time.Date(2026, 7, 12, 8, 0, 0, 0, time.UTC)
	period := int64((5 * time.Hour) / time.Millisecond)
	for i, used := range []float64{70, 81} {
		at := base.Add(time.Duration(i) * time.Minute)
		obs := resetwatch.Observation{ProviderID: "codex", Account: resetwatch.Account{Ref: "acct"}, ObservedAt: at, SnapshotJSON: []byte(`{}`), Windows: []resetwatch.Window{{Label: resetwatch.LabelFiveHour, UsedPercent: &used, ResetAt: base.Add(5 * time.Hour), PeriodDurationMs: &period}}}
		if _, err := st.ApplyCodexPoll(context.Background(), store.CodexPollInput{Observation: obs, NotificationTarget: "test", ResetOptions: resetwatch.DefaultOptions(), CommittedAt: at.Add(time.Second)}); err != nil {
			return err
		}
	}
	return nil
}

func toolNames(tools []*mcp.Tool) []string {
	out := make([]string, len(tools))
	for i, tool := range tools {
		out[i] = tool.Name
	}
	return out
}
