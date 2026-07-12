package schemas

import (
	_ "embed"
	"encoding/json"
)

//go:embed context.schema.json
var contextSource []byte

//go:embed events.schema.json
var eventsSource []byte

//go:embed event.schema.json
var eventSource []byte

var contextMCP, eventsMCP string

func init() {
	contextMCP = bundle(contextSource)
	eventsMCP = bundle(eventsSource)
}

// MCPContext returns the self-contained context schema advertised over MCP.
func MCPContext() string { return contextMCP }

// MCPEvents returns the self-contained event-page schema advertised over MCP.
func MCPEvents() string { return eventsMCP }

func bundle(source []byte) string {
	var root, event map[string]any
	if json.Unmarshal(source, &root) != nil || json.Unmarshal(eventSource, &event) != nil {
		panic("invalid embedded Scriba schema")
	}
	defs, _ := root["$defs"].(map[string]any)
	if defs == nil {
		defs = map[string]any{}
		root["$defs"] = defs
	}
	for name, definition := range event["$defs"].(map[string]any) {
		defs[name] = definition
	}
	delete(event, "$schema")
	delete(event, "$id")
	delete(event, "$defs")
	defs["event"] = event
	rewriteEventRef(root)
	raw, err := json.Marshal(root)
	if err != nil {
		panic("cannot bundle Scriba schema")
	}
	return string(raw)
}

func rewriteEventRef(value any) {
	switch value := value.(type) {
	case map[string]any:
		if value["$ref"] == "event.schema.json" {
			value["$ref"] = "#/$defs/event"
		}
		for _, child := range value {
			rewriteEventRef(child)
		}
	case []any:
		for _, child := range value {
			rewriteEventRef(child)
		}
	}
}
