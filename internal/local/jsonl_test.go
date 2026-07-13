package local

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOversizedJSONLContract(t *testing.T) {
	fixture := filepath.Join("..", "..", "testdata", "contracts", "codex", "oversized-jsonl.json")
	data, err := os.ReadFile(fixture) // #nosec G304 -- fixed checked-in contract fixture.
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		ProductionMaxLineBytes int `json:"productionMaxLineBytes"`
		TestMaxLineBytes       int `json:"testMaxLineBytes"`
		ExtraBytes             int `json:"extraBytes"`
	}
	if err := json.Unmarshal(data, &spec); err != nil {
		t.Fatal(err)
	}
	if spec.ProductionMaxLineBytes != maxJSONLLineBytes || spec.TestMaxLineBytes <= 0 || spec.ExtraBytes <= 0 {
		t.Fatalf("invalid oversized JSONL contract: %+v production=%d", spec, maxJSONLLineBytes)
	}

	path := filepath.Join(t.TempDir(), "oversized.jsonl")
	line := strings.Repeat("x", spec.TestMaxLineBytes+spec.ExtraBytes)
	if err := os.WriteFile(path, []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	called := false
	err = readJSONLLines(path, spec.TestMaxLineBytes, func([]byte) { called = true })
	if err == nil || called {
		t.Fatalf("oversized line err=%v callback=%v", err, called)
	}
}
