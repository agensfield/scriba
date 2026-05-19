package codex

import (
	"os"
	"testing"
)

func TestLiveProbeUsageLimits(t *testing.T) {
	if os.Getenv("SCRIBA_LIVE_CODEX_LIMITS") != "1" {
		t.Skip("set SCRIBA_LIVE_CODEX_LIMITS=1 to call the live ChatGPT/Codex usage backend")
	}
	result, err := Probe(true)
	if err != nil {
		t.Fatalf("Probe returned error: %v", err)
	}
	if !result.AuthState.OK {
		t.Fatalf("auth state not ok: %#v", result.AuthState)
	}
	if len(result.Lines) == 0 {
		t.Fatal("expected at least one usage line")
	}
	var sawWindow bool
	for _, line := range result.Lines {
		if line.Type == "progress" && line.Used != nil && line.Limit != nil {
			sawWindow = true
			break
		}
	}
	if !sawWindow {
		t.Fatalf("expected at least one progress usage window: %#v", result.Lines)
	}
}
