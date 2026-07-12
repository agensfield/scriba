package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agensfield/scriba/internal/policy"
)

func TestPolicyConfigPayloadSchemaVersions(t *testing.T) {
	validated, err := json.Marshal(policyValidateResult{SchemaVersion: policyValidateSchemaVersion, Rules: []policy.Rule{}})
	if err != nil || !strings.Contains(string(validated), `"schemaVersion":"scriba.policy-validate.v1"`) {
		t.Fatalf("validate payload=%s err=%v", validated, err)
	}
	listed, err := json.Marshal(policyListResult{SchemaVersion: policyListSchemaVersion, Rules: []policy.Rule{}})
	if err != nil || !strings.Contains(string(listed), `"schemaVersion":"scriba.policy-list.v1"`) {
		t.Fatalf("list payload=%s err=%v", listed, err)
	}
}

func TestReadPolicyConfigPreservesDeclaredPreset(t *testing.T) {
	tests := []struct {
		file, preset string
		rules        int
	}{
		{"current.json", policy.PresetCurrent, 4},
		{"custom.json", policy.PresetCustom, 1},
	}
	for _, tt := range tests {
		t.Run(tt.preset, func(t *testing.T) {
			cfg, preset, err := readPolicyConfig(filepath.Join("testdata", "policy", tt.file))
			if err != nil {
				t.Fatal(err)
			}
			if preset != tt.preset || len(cfg.Rules) != tt.rules {
				t.Fatalf("preset=%q rules=%d", preset, len(cfg.Rules))
			}
		})
	}
}

func TestReadPolicyConfigRejectsUnknownFields(t *testing.T) {
	_, _, err := readPolicyConfig(filepath.Join("testdata", "policy", "unknown-field.json"))
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("error=%v", err)
	}
}

func TestPolicyDispatchRejectsExtrasAndUnknownFlags(t *testing.T) {
	for _, args := range [][]string{
		{"validate"},
		{"validate", "a.json", "b.json"},
		{"validate", "a.json", "--config", "b.json"},
		{"list", "extra"},
		{"list", "--redact"},
	} {
		if err := dispatchPolicy(args); err == nil {
			t.Fatalf("dispatchPolicy(%q) unexpectedly succeeded", args)
		}
	}
}

func TestPolicyValidateArgsAcceptsFlagsAfterFile(t *testing.T) {
	path, flags, err := policyValidateArgs([]string{"policy.json", "--json"})
	if err != nil || path != "policy.json" || len(flags) != 1 || flags[0] != "--json" {
		t.Fatalf("path=%q flags=%q err=%v", path, flags, err)
	}
}

func TestPolicyHelpAndSchemaPublishCommands(t *testing.T) {
	if !strings.Contains(help(), "policy") || !strings.Contains(groupHelp("policy"), "scriba policy validate <file>") {
		t.Fatal("policy commands missing from help")
	}
	if !containsCommand(commands()["root"], "policy") || !containsCommand(commands()["policy"], "validate") || !containsCommand(commands()["policy"], "list") {
		t.Fatal("policy commands missing from command schema")
	}
}

func TestPolicyValidateHelpReturnsFlagHelp(t *testing.T) {
	for _, args := range [][]string{{"validate", "--help"}, {"validate", "--help", "--json"}, {"validate", "--json", "--help"}, {"validate", "policy.json", "--help"}, {"validate", "policy.json", "--json", "--help"}} {
		if err := dispatchPolicy(args); !errors.Is(err, flag.ErrHelp) {
			t.Fatalf("args=%q error=%v", args, err)
		}
	}
}

func TestPolicyValidateInvalidJSONEmitsTypedFailure(t *testing.T) {
	stdout := captureCLIStdout(t, func() {
		err := runPolicyValidate(filepath.Join("testdata", "policy", "unknown-field.json"), options{jsonOut: true})
		if err == nil {
			t.Fatal("invalid policy unexpectedly succeeded")
		}
	})
	var payload policyValidateResult
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("decode output: %v\n%s", err, stdout)
	}
	if payload.SchemaVersion != policyValidateSchemaVersion || payload.Valid || len(payload.Errors) != 1 || payload.Rules == nil {
		t.Fatalf("payload=%#v", payload)
	}
}

func captureCLIStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	defer func() { os.Stdout = old }()
	fn()
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestPolicyValidationRedactionPreservesRuleContract(t *testing.T) {
	result := redactPolicyValidation(policyValidateResult{
		SchemaVersion: policyValidateSchemaVersion,
		Valid:         true,
		File:          "/Users/arda/private/policy.json",
		Preset:        policy.PresetCurrent,
		Rules:         policy.CurrentPreset().Rules,
	})
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "/Users/arda") || !strings.Contains(string(data), "/Users/[redacted]") {
		t.Fatalf("path was not redacted: %s", data)
	}
	if len(result.Rules) != 4 || len(result.Rules[0].WindowKeys) != 2 || result.Rules[0].WindowKeys[0] != "primary.five_hour" {
		t.Fatalf("redaction damaged rule contract: %#v", result.Rules)
	}
}

func TestRenderPolicyListIsDeterministicAndReadable(t *testing.T) {
	text := stripANSI(renderPolicyList(policyListResult{Preset: policy.PresetCurrent, Rules: policy.CurrentPreset().Rules}))
	for _, want := range []string{"Policy rules", "current preset · 4 rules", "current.remaining.primary", "remaining_checkpoint", "primary.five_hour, primary.weekly", "[20 10 5 0]", "clock 300s · due 600s"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q:\n%s", want, text)
		}
	}
}
