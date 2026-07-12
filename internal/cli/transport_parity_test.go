//go:build darwin || linux

package cli

import (
	"bufio"
	"bytes"
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
	"github.com/agensfield/scriba/internal/agentmcp"
	"github.com/agensfield/scriba/internal/cache"
	"github.com/agensfield/scriba/internal/config"
	"github.com/agensfield/scriba/internal/localapi"
	"github.com/agensfield/scriba/internal/resetwatch"
	"github.com/agensfield/scriba/internal/server/store"
	"github.com/agensfield/scriba/schemas"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

func TestAgentContextTransportParity(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "scriba-parity-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	cacheDir, storePath := filepath.Join(dir, "cache"), filepath.Join(dir, "server.sqlite")
	c, err := cache.Open(cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(storePath)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 7, 12, 8, 0, 0, 0, time.UTC)
	used, period := 81.0, int64((5*time.Hour)/time.Millisecond)
	obs := resetwatch.Observation{ProviderID: "codex", Account: resetwatch.Account{Ref: "PRIVATE_ACCOUNT"}, ObservedAt: base, SnapshotJSON: []byte(`{"secret":"PRIVATE_SNAPSHOT"}`), Windows: []resetwatch.Window{{Label: resetwatch.LabelFiveHour, UsedPercent: &used, ResetAt: base.Add(5 * time.Hour), PeriodDurationMs: &period}}}
	if _, err := st.ApplyDecision(t.Context(), obs, resetwatch.Decision{}); err != nil {
		t.Fatal(err)
	}
	versionBefore, err := st.SchemaVersion(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	seedParityEvent(t, storePath, base.Add(time.Minute))
	countsBefore := businessCounts(t, storePath)

	oldClock := agentContextClock
	agentContextClock = func() time.Time { return base.Add(time.Hour) }
	t.Cleanup(func() { agentContextClock = oldClock })
	cfg := config.Default()
	cfg.CacheDir, cfg.Server.StatePath = cacheDir, storePath
	svc := agentContextService(cfg)
	want, err := svc.Context(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	cursor := "v1.0000000000000000"
	wantEvents, err := svc.Events(t.Context(), agentcontext.EventPageRequest{Mode: "replay", Cursor: cursor, Limit: 20})
	if err != nil || len(wantEvents.Events) != 1 {
		t.Fatalf("direct events: page=%#v err=%v", wantEvents, err)
	}

	wantRaw, _ := json.Marshal(want)
	cliPayload := captureContextCLI(t, options{jsonOut: true, cacheDir: cacheDir, statePath: storePath})
	if !jsonEqual(wantRaw, cliPayload) {
		t.Fatalf("CLI context differs")
	}

	socket := filepath.Join(dir, "context.sock")
	ln, err := localapi.Listen(t.Context(), socket)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := localapi.NewHTTPServer(ln, svc, localapi.HTTPConfig{PollInterval: 10 * time.Millisecond, HeartbeatInterval: 25 * time.Millisecond, ShutdownTimeout: time.Second})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- httpServer.Run(ctx) }()
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	resp, err := (&http.Client{Transport: transport}).Get("http://unix/v1/context")
	if err != nil {
		t.Fatal(err)
	}
	httpPayload, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil || !jsonEqual(httpPayload, wantRaw) {
		t.Fatalf("HTTP context differs: %v", err)
	}
	sse, err := (&http.Client{Transport: transport}).Get("http://unix/v1/events?cursor=" + cursor)
	if err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(sse.Body)
	if frame := readParityFrame(t, reader); frame != ": connected\n\n" {
		t.Fatalf("SSE connected frame=%q", frame)
	}
	eventFrame := readParityFrame(t, reader)
	frameCursor, frameEvent := decodeParityEventFrame(t, eventFrame)
	wantEventRaw, _ := json.Marshal(wantEvents.Events[0])
	if frameCursor != wantEvents.Cursor.Next || !jsonEqual(frameEvent, wantEventRaw) {
		t.Fatalf("SSE event differs: cursor=%q event=%#v", frameCursor, frameEvent)
	}
	if next := readParityFrame(t, reader); !strings.HasPrefix(next, ": heartbeat") {
		t.Fatalf("duplicate/extra SSE event: %q", next)
	}
	_ = sse.Body.Close()
	cancel()
	_ = httpServer.Shutdown(context.Background())
	<-done

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ss, err := agentmcp.NewServer(svc).Connect(t.Context(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "parity", Version: "1"}, nil).Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	result, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: agentmcp.GetContextTool, Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if !jsonEqual(raw, wantRaw) {
		t.Fatalf("MCP context differs")
	}
	eventResult, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: agentmcp.ListEventsTool, Arguments: map[string]any{"mode": "replay", "cursor": cursor, "limit": 20}})
	if err != nil {
		t.Fatal(err)
	}
	eventRaw, err := json.Marshal(eventResult.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	wantEventsRaw, _ := json.Marshal(wantEvents)
	if !jsonEqual(eventRaw, wantEventsRaw) {
		t.Fatalf("MCP events differ")
	}

	validateContextPayload(t, raw)
	validateBundledPayload(t, schemas.MCPEvents(), eventRaw)
	validateEventPayload(t, frameEvent)
	for _, data := range [][]byte{wantRaw, cliPayload, httpPayload, raw} {
		validateContextPayload(t, data)
		if strings.Contains(string(data), "PRIVATE_") || strings.Contains(string(data), storePath) {
			t.Fatalf("private fixture data leaked: %s", data)
		}
	}
	for _, data := range [][]byte{eventRaw, frameEvent} {
		if strings.Contains(string(data), "PRIVATE_") || strings.Contains(string(data), storePath) {
			t.Fatalf("private event data leaked: %s", data)
		}
	}
	check, err := store.Open(storePath)
	if err != nil {
		t.Fatal(err)
	}
	defer check.Close()
	versionAfter, err := check.SchemaVersion(t.Context())
	if err != nil || versionAfter != versionBefore {
		t.Fatalf("schema mutated: %d -> %d (%v)", versionBefore, versionAfter, err)
	}
	if countsAfter := businessCounts(t, storePath); !reflect.DeepEqual(countsAfter, countsBefore) {
		t.Fatalf("business rows mutated: %v -> %v", countsBefore, countsAfter)
	}
}

func seedParityEvent(t *testing.T, path string, at time.Time) {
	t.Helper()
	warning := resetwatch.WarningEvent{ID: "parity-event", ProviderID: "codex", Account: resetwatch.Account{Ref: "PRIVATE_ACCOUNT"}, Label: resetwatch.LabelFiveHour, ThresholdRemaining: 20, UsedPercent: 81, RemainingPercent: 19, ResetAt: at.Add(time.Hour), SnapshotJSON: []byte(`{"secret":"PRIVATE_EVENT"}`), DetectedAt: at}
	payload, err := store.EncodeOutboxPayload("limit_warning", warning)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	stamp := at.Format(time.RFC3339Nano)
	_, err = db.ExecContext(t.Context(), `insert into policy_events(id,semantic_key,event_kind,semantic_event_id,rule_id,subject_key,rule_kind,provider_id,account_ref,policy_revision,config_hash,payload_version,payload_json,detected_at,created_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, warning.ID, warning.ID, "limit_warning", warning.ID, "rule", "subject", "remaining_checkpoint", "codex", "PRIVATE_ACCOUNT", "rev", "PRIVATE_CONFIG", 1, string(payload), stamp, stamp)
	if err != nil {
		t.Fatal(err)
	}
}

func readParityFrame(t *testing.T, reader *bufio.Reader) string {
	t.Helper()
	var out strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		out.WriteString(line)
		if line == "\n" {
			return out.String()
		}
	}
}

func decodeParityEventFrame(t *testing.T, frame string) (string, []byte) {
	t.Helper()
	var cursor, data string
	for _, line := range strings.Split(frame, "\n") {
		if strings.HasPrefix(line, "id: ") {
			cursor = strings.TrimPrefix(line, "id: ")
		}
		if strings.HasPrefix(line, "data: ") {
			data = strings.TrimPrefix(line, "data: ")
		}
	}
	if cursor == "" || data == "" {
		t.Fatalf("invalid SSE frame: %q", frame)
	}
	var event any
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		t.Fatal(err)
	}
	return cursor, []byte(data)
}

func businessCounts(t *testing.T, path string) map[string]int64 {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	counts := map[string]int64{}
	for _, table := range []string{"limit_observations", "policy_events", "notification_outbox"} {
		var count int64
		if err := db.QueryRowContext(t.Context(), "select count(*) from "+table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		counts[table] = count
	}
	return counts
}

func captureContextCLI(t *testing.T, opts options) []byte {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	err = runContextWithContext(t.Context(), opts)
	_ = w.Close()
	os.Stdout = old
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(r)
	_ = r.Close()
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.NewDecoder(bytes.NewReader(data)).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return data
}

func jsonEqual(a, b []byte) bool {
	var av, bv any
	return json.Unmarshal(a, &av) == nil && json.Unmarshal(b, &bv) == nil && reflect.DeepEqual(av, bv)
}

func validateContextPayload(t *testing.T, data []byte) {
	t.Helper()
	var document, payload any
	if err := json.Unmarshal([]byte(schemas.MCPContext()), &document); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("context", document); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("context")
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(payload); err != nil {
		t.Fatalf("invalid context schema: %v", err)
	}
}

func validateBundledPayload(t *testing.T, schemaJSON string, data []byte) {
	t.Helper()
	var document, payload any
	if err := json.Unmarshal([]byte(schemaJSON), &document); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("payload", document); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("payload")
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(payload); err != nil {
		t.Fatalf("invalid payload schema: %v", err)
	}
}

func validateEventPayload(t *testing.T, data []byte) {
	t.Helper()
	validateBundledPayload(t, schemas.MCPEvents(), mustEventPage(t, data))
}

func mustEventPage(t *testing.T, event []byte) []byte {
	t.Helper()
	var value any
	if err := json.Unmarshal(event, &value); err != nil {
		t.Fatal(err)
	}
	page, err := json.Marshal(map[string]any{"schemaVersion": agentcontext.EventsSchemaVersion, "generatedAt": time.Date(2026, 7, 12, 9, 0, 0, 0, time.UTC), "events": []any{value}, "cursor": map[string]any{"next": "v1.0000000000000001", "highWater": "v1.0000000000000001"}})
	if err != nil {
		t.Fatal(err)
	}
	return page
}
