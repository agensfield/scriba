package accounts

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/agensfield/scriba/internal/config"
)

func TestSourceRefUsesCleanAbsolutePath(t *testing.T) {
	a := SourceRef("/tmp/auth.json")
	b := SourceRef("/tmp/x/../auth.json")
	if a != b || len(a) != 24 || a[:4] != "src-" {
		t.Fatalf("source refs: %q %q", a, b)
	}
}

func TestSourcesPreserveExplicitPathsAndFilterDiscoveryAlternatives(t *testing.T) {
	cfg := config.Default()
	cfg.CodexAuthPaths = []string{"/missing/first.json", "/missing/second.json"}
	got := Sources(cfg)
	if len(got) != 2 || got[0].Path != "/missing/first.json" || got[1].Path != "/missing/second.json" {
		t.Fatalf("explicit sources=%+v", got)
	}

	home := t.TempDir()
	t.Setenv("CODEX_HOME", "")
	t.Setenv("HOME", home)
	second := filepath.Join(home, ".codex", "auth.json")
	if err := os.MkdirAll(filepath.Dir(second), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg = config.Default()
	got = Sources(cfg)
	if len(got) != 1 || got[0].Path != second {
		t.Fatalf("discovered sources=%+v", got)
	}
	if err := os.Remove(second); err != nil {
		t.Fatal(err)
	}
	got = Sources(cfg)
	wantMissingDefault := filepath.Join(home, ".config", "codex", "auth.json")
	if len(got) != 1 || got[0].Path != wantMissingDefault {
		t.Fatalf("missing discovery sources=%+v", got)
	}
}

func TestInspectRequiresStrongAccountAndNeverReturnsTokens(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) Source {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return Source{Ref: SourceRef(path), Path: path}
	}
	idPayload := base64.RawURLEncoding.EncodeToString([]byte(`{"email":"a@example.com"}`))
	available := fmt.Sprintf(`{"tokens":{"access_token":"bearer-private","refresh_token":"refresh-private","account_id":"acct-a","id_token":"x.%s.x"}}`, idPayload)
	tests := []struct {
		name  string
		src   Source
		state CredentialState
		ref   string
	}{
		{"available", write("available.json", available), CredentialAvailable, "acct-a"},
		{"malformed", write("malformed.json", `{`), CredentialInvalid, ""},
		{"api key", write("api.json", `{"OPENAI_API_KEY":"private"}`), CredentialAPIKey, ""},
		{"logged out", write("logout.json", `{"tokens":{}}`), CredentialLoggedOut, ""},
		{"weak", write("weak.json", `{"tokens":{"access_token":"token"}}`), CredentialUnverifiable, ""},
		{"missing", Source{Ref: SourceRef(filepath.Join(dir, "missing.json")), Path: filepath.Join(dir, "missing.json")}, CredentialMissing, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Inspect(tt.src)
			if got.State != tt.state || got.Account.Ref != tt.ref || got.CredentialsAvailable != (tt.state == CredentialAvailable) {
				t.Fatalf("inspection=%+v", got)
			}
		})
	}
	first := Inspect(tests[0].src)
	rotated := write("rotated.json", fmt.Sprintf(`{"tokens":{"access_token":"new-token","account_id":"acct-a","id_token":"x.%s.x"}}`, idPayload))
	second := Inspect(rotated)
	if !reflect.DeepEqual(first.Account, second.Account) {
		t.Fatalf("same-account rotation changed identity: first=%+v second=%+v", first.Account, second.Account)
	}
}
