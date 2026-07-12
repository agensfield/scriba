package codex

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func FuzzParseFile(f *testing.F) {
	seedContractLines(f, contractFixture("*.jsonl"))
	f.Fuzz(func(t *testing.T, data []byte) {
		file := filepath.Join(t.TempDir(), "session.jsonl")
		if err := os.WriteFile(file, data, 0o600); err != nil {
			t.Fatal(err)
		}
		a, as, ae := ParseFile(filepath.Dir(file), file)
		b, bs, be := ParseFile(filepath.Dir(file), file)
		if (ae == nil) != (be == nil) || !reflect.DeepEqual(a, b) || !reflect.DeepEqual(as, bs) {
			t.Fatalf("nondeterministic result: (%v, %+v, %v) != (%v, %+v, %v)", a, as, ae, b, bs, be)
		}
		lines := int64(bytes.Count(data, []byte{'\n'}))
		if len(data) > 0 && data[len(data)-1] != '\n' {
			lines++
		}
		if int64(as.InvalidLines) > lines || int64(as.Events) > lines {
			t.Fatalf("stats exceed lines: %+v lines=%d", as, lines)
		}
		for _, e := range a {
			if e.InputTokens < 0 || e.OutputTokens < 0 || e.CachedInputTokens < 0 || e.ReasoningOutputTokens < 0 || e.TotalTokens < 0 {
				t.Fatalf("negative usage: %+v", e)
			}
			if e.CachedInputTokens > e.InputTokens || e.ReasoningOutputTokens > e.OutputTokens {
				t.Fatalf("invalid subcounter: %+v", e)
			}
			if e.TotalTokens != e.InputTokens+e.OutputTokens {
				t.Fatalf("invalid total: %+v", e)
			}
		}
	})
}

func seedContractLines(f *testing.F, pattern string) {
	files, err := filepath.Glob(pattern)
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
}

func TestCumulativeSnapshotsProperty(t *testing.T) {
	for resetAt := 0; resetAt < 5; resetAt++ {
		var data []byte
		input, output := int64(0), int64(0)
		wantInput, wantOutput := int64(0), int64(0)
		previousInput, previousOutput := int64(0), int64(0)
		for i := 0; i < 8; i++ {
			if i == resetAt {
				input, output = 0, 0
			}
			input += int64(i + 1)
			output += int64(i%3 + 1)
			if i == 0 || input+output < previousInput+previousOutput || (input < previousInput && output < previousOutput) {
				wantInput += input
				wantOutput += output
			} else {
				wantInput += max(input-previousInput, 0)
				wantOutput += max(output-previousOutput, 0)
			}
			previousInput, previousOutput = input, output
			line := fmt.Sprintf(`{"type":"event_msg","timestamp":"2026-01-01T00:00:%02dZ","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":%d,"cached_input_tokens":%d,"output_tokens":%d,"reasoning_output_tokens":%d}}}}`, i, input, input/2, output, output/2)
			data = append(data, line...)
			data = append(data, '\n')
			if i%2 == 0 {
				data = append(data, line...)
				data = append(data, '\n')
			}
		}
		file := filepath.Join(t.TempDir(), "snapshots.jsonl")
		if err := os.WriteFile(file, data, 0o600); err != nil {
			t.Fatal(err)
		}
		events, _, err := ParseFile(filepath.Dir(file), file)
		if err != nil {
			t.Fatal(err)
		}
		var gotInput, gotOutput int64
		for _, e := range events {
			gotInput += e.InputTokens
			gotOutput += e.OutputTokens
		}
		if gotInput != wantInput || gotOutput != wantOutput {
			t.Fatalf("reset=%d got=(%d,%d) want=(%d,%d)", resetAt, gotInput, gotOutput, wantInput, wantOutput)
		}
	}
}
