package agentmcp

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/agensfield/scriba/internal/agentcontext"
	"github.com/agensfield/scriba/internal/buildinfo"
	"github.com/agensfield/scriba/schemas"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	GetContextTool = "scriba_get_context"
	ListEventsTool = "scriba_list_events"
)

var (
	emptyInputSchema  = json.RawMessage(`{"type":"object","additionalProperties":false}`)
	eventsInputSchema = json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"mode":{"enum":["latest","replay"]},"cursor":{"type":"string","pattern":"^v1\\.[0-7][0-9a-f]{15}$","minLength":19,"maxLength":19},"limit":{"type":"integer","minimum":1,"maximum":100}},"oneOf":[{"properties":{"mode":{"const":"latest"}},"not":{"required":["cursor"]}},{"required":["mode","cursor"],"properties":{"mode":{"const":"replay"}}}]}`)
)

type eventInput struct {
	Mode   string `json:"mode"`
	Cursor string `json:"cursor"`
	Limit  int    `json:"limit"`
}

type contextService interface {
	Context(context.Context) (agentcontext.Context, error)
	Events(context.Context, agentcontext.EventPageRequest) (agentcontext.EventPage, error)
}

// NewServer exposes one agent-context service over any MCP transport.
func NewServer(service contextService) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "scriba", Version: buildinfo.Version}, nil)
	annotations := &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: boolPtr(false)}
	mcp.AddTool(server, &mcp.Tool{Name: GetContextTool, Description: "Return the current Scriba agent context.", InputSchema: emptyInputSchema, OutputSchema: json.RawMessage(schemas.MCPContext()), Annotations: annotations}, func(ctx context.Context, request *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, agentcontext.Context, error) {
		output, err := service.Context(ctx)
		return typedResult(output, err)
	})
	mcp.AddTool(server, &mcp.Tool{Name: ListEventsTool, Description: "List the latest or replayed Scriba policy events.", InputSchema: eventsInputSchema, OutputSchema: json.RawMessage(schemas.MCPEvents()), Annotations: annotations}, func(ctx context.Context, request *mcp.CallToolRequest, input eventInput) (*mcp.CallToolResult, agentcontext.EventPage, error) {
		if input.Limit == 0 {
			input.Limit = 20
		}
		if input.Mode == "" {
			input.Mode = "latest"
		}
		output, err := service.Events(ctx, agentcontext.EventPageRequest{Mode: input.Mode, Cursor: input.Cursor, Limit: input.Limit})
		return typedResult(output, err)
	})
	return server
}

// RunStdio serves until stdin closes or ctx is cancelled. Stdout is owned by the transport.
func RunStdio(ctx context.Context, service contextService) error {
	return NewServer(service).Run(ctx, &mcp.StdioTransport{})
}

func typedResult[T any](output T, err error) (*mcp.CallToolResult, T, error) {
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, output, err
		}
		message := "data unavailable"
		var pageErr *agentcontext.EventPageError
		if errors.As(err, &pageErr) && allowedReason(pageErr.ReasonCode) {
			message = pageErr.ReasonCode
		}
		return nil, output, errors.New(message)
	}
	return nil, output, nil
}

func allowedReason(reason string) bool {
	switch reason {
	case "invalid_limit", "invalid_mode", "invalid_cursor", "cursor_future", "cursor_expired", "events_unavailable", "read_error":
		return true
	default:
		return false
	}
}
func boolPtr(value bool) *bool { return &value }
