package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestBuiltMCPExitsCleanlyOnEOF(t *testing.T) {
	binary := buildScriba(t)
	var stderr bytes.Buffer
	cmd := exec.Command(binary, "mcp", "--cache-dir", t.TempDir(), "--state-path", filepath.Join(t.TempDir(), "state.db"))
	cmd.Stderr = &stderr
	client := mcp.NewClient(&mcp.Implementation{Name: "cli-test", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("connect: %v stderr=%s", err, stderr.String())
	}
	if _, err := session.ListTools(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	waitProcess(t, cmd)
	if !cmd.ProcessState.Success() || stderr.Len() != 0 {
		t.Fatalf("exit=%v stderr=%q", cmd.ProcessState, stderr.String())
	}
}

func TestBuiltMCPExitsCleanlyOnSIGTERMAfterInitialize(t *testing.T) {
	binary := buildScriba(t)
	var stderr bytes.Buffer
	cmd := exec.Command(binary, "mcp", "--cache-dir", t.TempDir(), "--state-path", filepath.Join(t.TempDir(), "state.db"))
	cmd.Stderr = &stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	request := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "cli-test", "version": "1"}}}
	if err := json.NewEncoder(stdin).Encode(request); err != nil {
		t.Fatal(err)
	}
	var response map[string]any
	if err := json.NewDecoder(stdout).Decode(&response); err != nil {
		t.Fatalf("initialize response: %v stderr=%s", err, stderr.String())
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	_ = stdin.Close()
	_, _ = io.Copy(io.Discard, stdout)
	if err := cmd.Wait(); err != nil || stderr.Len() != 0 {
		t.Fatalf("wait=%v exit=%v stderr=%q", err, cmd.ProcessState, stderr.String())
	}
}

func buildScriba(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "scriba")
	cmd := exec.Command("go", "build", "-o", binary, "../../cmd/scriba")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v\n%s", err, output)
	}
	return binary
}

func waitProcess(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for cmd.ProcessState == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if cmd.ProcessState == nil {
		_ = cmd.Process.Kill()
		t.Fatal("MCP subprocess did not exit")
	}
}
