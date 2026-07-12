package contracttest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

var publicOutputSchemaNames = []string{
	"status", "codex-limits", "codex-profile", "codex-reset-grants", "budget",
	"policy-validate", "policy-list", "policy-explain", "outbox-list",
	"context", "event", "events", "local-health", "local-error",
	"profiles",
}

func TestPublicOutputSchemas(t *testing.T) {
	t.Parallel()
	root := filepath.Join("..", "..")
	for _, name := range publicOutputSchemaNames {
		name := name
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			compiler := publicOutputCompiler(t, filepath.Join(root, "schemas"))
			schema, err := compiler.Compile("https://agensfield.dev/scriba/schemas/" + name + ".schema.json")
			if err != nil {
				t.Fatalf("compile schema: %v", err)
			}
			data, err := os.ReadFile(filepath.Join(root, "testdata", "public-output", "goldens", name+".json"))
			if err != nil {
				t.Fatal(err)
			}
			var payload any
			if err := json.Unmarshal(data, &payload); err != nil {
				t.Fatalf("decode golden: %v", err)
			}
			if err := schema.Validate(payload); err != nil {
				t.Fatalf("validate golden: %v", err)
			}
			canonical, err := CanonicalJSON(payload)
			if err != nil {
				t.Fatal(err)
			}
			if string(canonical) != string(data) {
				t.Fatalf("golden is not canonical\ngot:  %s\nwant: %s", data, canonical)
			}
		})
	}
}

func TestPublicOutputSchemasAllowOptionalOmissions(t *testing.T) {
	t.Parallel()
	root := filepath.Join("..", "..", "schemas")
	cases := map[string]any{
		"status":             map[string]any{"schemaVersion": "scriba.v1", "generatedAt": "2026-07-12T09:00:00Z", "providers": []any{}},
		"codex-limits":       map[string]any{"schemaVersion": "scriba.v1", "providerId": "codex", "source": "status-cache", "mode": "fast", "lines": []any{}},
		"codex-profile":      map[string]any{"schemaVersion": "scriba.v1", "providerId": "codex", "source": "chatgpt-codex-profile-backend", "profile": map[string]any{}, "stats": map[string]any{}, "metadata": map[string]any{}, "authState": map[string]any{"ok": false}},
		"codex-reset-grants": map[string]any{"schemaVersion": "scriba.v1", "providerId": "codex", "source": "chatgpt-codex-backend", "mode": "live", "authState": map[string]any{"ok": false}, "resetCredits": []any{}, "summary": map[string]any{"available": 0}},
		"policy-validate":    map[string]any{"schemaVersion": "scriba.policy-validate.v1", "valid": false, "file": "invalid.json", "rules": []any{}, "errors": []any{"invalid policy"}},
		"context": map[string]any{"schemaVersion": "scriba.context.v1", "generatedAt": "2026-07-12T12:00:00Z", "sources": []any{
			map[string]any{"sourceId": "codex-quota", "kind": "quota", "availability": "unavailable", "provenance": []any{map[string]any{"source": "status-cache"}}, "reasonCode": "missing"},
		}, "providers": []any{}, "events": []any{}},
	}
	for name, payload := range cases {
		schema, err := publicOutputCompiler(t, root).Compile("https://agensfield.dev/scriba/schemas/" + name + ".schema.json")
		if err != nil {
			t.Fatalf("%s: compile schema: %v", name, err)
		}
		if err := schema.Validate(payload); err != nil {
			t.Errorf("%s: optional omissions rejected: %v", name, err)
		}
	}
}

func TestAgentSchemasRejectNonAllowlistedFields(t *testing.T) {
	t.Parallel()
	root := filepath.Join("..", "..", "schemas")
	cases := []struct {
		name, schema string
		payload      any
	}{
		{"context-account", "context", map[string]any{"schemaVersion": "scriba.context.v1", "generatedAt": "2026-07-12T12:00:00Z", "sources": []any{}, "providers": []any{}, "events": []any{}, "accountRef": "secret"}},
		{"context-config", "context", map[string]any{"schemaVersion": "scriba.context.v1", "generatedAt": "2026-07-12T12:00:00Z", "sources": []any{}, "providers": []any{}, "events": []any{}, "configHash": "secret"}},
		{"events-account", "events", map[string]any{"schemaVersion": "scriba.events.v1", "generatedAt": "2026-07-12T12:00:00Z", "events": []any{}, "cursor": map[string]any{"next": "v1.0000000000000000", "highWater": "v1.0000000000000000"}, "accountRef": "secret"}},
		{"profiles-auth", "profiles", map[string]any{"schemaVersion": "scriba.profiles.v1", "defaultProfileId": "default", "profiles": []any{map[string]any{"profileId": "default", "label": "Default", "isDefault": true, "status": "ok", "consecutiveFailures": 0, "isStale": false, "auth": "/secret/auth.json"}}}},
	}
	for _, field := range []string{"creditId", "grantId", "ruleId", "accountRef", "snapshot", "target", "chatId", "configHash", "semanticKey"} {
		data := map[string]any{"windowKey": "primary.weekly", "checkpointPercent": 20, "usedPercent": 80, "remainingPercentPoints": 20, field: "secret"}
		cases = append(cases, struct {
			name, schema string
			payload      any
		}{"event-" + field, "event", map[string]any{"schemaVersion": "scriba.event.v1", "id": "event-1", "providerId": "codex", "profileId": "default", "kind": "remaining_checkpoint", "detectedAt": "2026-07-12T12:00:00Z", "data": data}})
	}
	for _, tc := range cases {
		schema, err := publicOutputCompiler(t, root).Compile("https://agensfield.dev/scriba/schemas/" + tc.schema + ".schema.json")
		if err != nil {
			t.Fatalf("%s: compile schema: %v", tc.name, err)
		}
		if err := schema.Validate(tc.payload); err == nil {
			t.Errorf("%s: accepted non-allowlisted identifier", tc.name)
		}
	}
}

func publicOutputCompiler(t *testing.T, schemaDir string) *jsonschema.Compiler {
	t.Helper()
	compiler := jsonschema.NewCompiler()
	for _, name := range publicOutputSchemaNames {
		data, err := os.ReadFile(filepath.Join(schemaDir, name+".schema.json"))
		if err != nil {
			t.Fatal(err)
		}
		var document any
		if err := json.Unmarshal(data, &document); err != nil {
			t.Fatalf("decode %s schema: %v", name, err)
		}
		if err := compiler.AddResource("https://agensfield.dev/scriba/schemas/"+name+".schema.json", document); err != nil {
			t.Fatalf("add %s schema: %v", name, err)
		}
	}
	return compiler
}
