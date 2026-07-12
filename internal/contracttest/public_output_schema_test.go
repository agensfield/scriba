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
