package contracttest

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestManifestAndCanonicalJSON(t *testing.T) {
	root := filepath.Join("..", "..", "testdata", "contracts")
	manifest, err := LoadManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Cases) < 10 {
		t.Fatalf("cases = %d, want representative corpus", len(manifest.Cases))
	}
	got, err := CanonicalJSON(map[string]any{"z": 1, "a": map[string]any{"b": 2, "a": 1}})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "{\"a\":{\"a\":1,\"b\":2},\"z\":1}\n" {
		t.Fatalf("canonical JSON = %s", got)
	}
	large, err := CanonicalJSON(map[string]any{"n": json.Number("9007199254740993")})
	if err != nil || string(large) != "{\"n\":9007199254740993}\n" {
		t.Fatalf("large integer canonical JSON = %s, %v", large, err)
	}
}
