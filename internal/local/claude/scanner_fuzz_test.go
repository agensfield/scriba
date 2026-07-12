package claude

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func FuzzParseFile(f *testing.F) {
	files, err := filepath.Glob(filepath.Join(claudeContractRoot(), "*.jsonl"))
	if err != nil {
		f.Fatal(err)
	}
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(data)
		for _, line := range bytes.Split(data, []byte{'\n'}) {
			f.Add(line)
		}
	}
	f.Add([]byte(`{"timestamp":"x","message":{"usage":{"input_tokens":-1,"output_tokens":2,"cache_creation_input_tokens":-3,"cache_read_input_tokens":4}}}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		file := filepath.Join(t.TempDir(), "session.jsonl")
		if err := os.WriteFile(file, data, 0o600); err != nil {
			t.Fatal(err)
		}
		a, as, ae := ParseFile(filepath.Dir(file), file)
		b, bs, be := ParseFile(filepath.Dir(file), file)
		if (ae == nil) != (be == nil) || !reflect.DeepEqual(a, b) || !reflect.DeepEqual(as, bs) {
			t.Fatalf("nondeterministic result")
		}
		lines := int64(bytes.Count(data, []byte{'\n'}))
		if len(data) > 0 && data[len(data)-1] != '\n' {
			lines++
		}
		if int64(as.InvalidLines) > lines || int64(len(a)) > lines {
			t.Fatalf("counts exceed lines: %+v events=%d lines=%d", as, len(a), lines)
		}
		for _, e := range a {
			if e.InputTokens < 0 || e.OutputTokens < 0 || e.CacheCreationTokens < 0 || e.CacheReadTokens < 0 || e.CachedInputTokens < 0 || e.TotalTokens < 0 {
				t.Fatalf("negative usage: %+v", e)
			}
			if e.TotalTokens != e.InputTokens+e.OutputTokens+e.CacheCreationTokens+e.CacheReadTokens {
				t.Fatalf("invalid total: %+v", e)
			}
		}
	})
}
