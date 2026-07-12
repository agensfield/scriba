package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agensfield/scriba/internal/agentcontext"
)

func TestContextCommandContract(t *testing.T) {
	if err := dispatch([]string{"context"}); err == nil || !strings.Contains(err.Error(), "requires --json") {
		t.Fatalf("missing --json error = %v", err)
	}
	if err := dispatch([]string{"context", "extra", "--json"}); err == nil || !strings.Contains(err.Error(), "does not accept positional") {
		t.Fatalf("positional error = %v", err)
	}
	if err := dispatch([]string{"context", "--redact", "--json"}); err == nil || !strings.Contains(err.Error(), "flag provided but not defined") {
		t.Fatalf("unsupported flag error = %v", err)
	}
	if err := dispatch([]string{"context", "--json", "--profile="}); err == nil || !strings.Contains(err.Error(), "profile id is required") {
		t.Fatalf("empty profile error = %v", err)
	}
	if err := dispatch([]string{"context", "--json", "--profile=one", "--profile=two"}); err == nil || !strings.Contains(err.Error(), "only once") {
		t.Fatalf("duplicate profile error = %v", err)
	}
}

func TestContextInvalidFlagKeepsStdoutClean(t *testing.T) {
	stdout := captureContextOutput(t, &os.Stdout, func() {
		_ = captureContextOutput(t, &os.Stderr, func() {
			if err := dispatch([]string{"context", "--json", "--bogus"}); err == nil {
				t.Fatal("invalid flag succeeded")
			}
		})
	})
	if stdout != "" {
		t.Fatalf("invalid JSON command contaminated stdout: %q", stdout)
	}
}

func TestRunContextWithContextHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := runContextWithContext(ctx, options{jsonOut: true, cacheDir: t.TempDir(), statePath: filepath.Join(t.TempDir(), "missing.sqlite")})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestContextCommandDiscovery(t *testing.T) {
	if !containsCommand(commands()["root"], "context") {
		t.Fatal("context missing from command schema")
	}
	if text := help(); !strings.Contains(text, "context") || !strings.Contains(text, "requires --json") {
		t.Fatalf("context missing from help:\n%s", text)
	}
}

func TestContextJSONWithEmptySources(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdout
	os.Stdout = write
	err = dispatch([]string{"context", "--json", "--cache-dir", t.TempDir(), "--state-path", t.TempDir() + "/missing.sqlite"})
	os.Stdout = original
	_ = write.Close()
	if err != nil {
		t.Fatalf("context command: %v", err)
	}
	data, err := io.ReadAll(read)
	if err != nil {
		t.Fatal(err)
	}
	var payload agentcontext.Context
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("invalid context JSON: %v\n%s", err, data)
	}
	if payload.SchemaVersion != agentcontext.SchemaVersion || payload.Sources == nil || payload.Providers == nil || payload.Events == nil {
		t.Fatalf("invalid context schema shape: %#v", payload)
	}
}

func captureContextOutput(t *testing.T, target **os.File, fn func()) string {
	t.Helper()
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := *target
	*target = write
	defer func() { *target = original }()
	fn()
	_ = write.Close()
	data, err := io.ReadAll(read)
	if err != nil {
		t.Fatal(err)
	}
	_ = read.Close()
	return string(data)
}
