package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/agensfield/scriba/internal/policy"
	"github.com/agensfield/scriba/internal/privacy"
)

const (
	policyValidateSchemaVersion = "scriba.policy-validate.v1"
	policyListSchemaVersion     = "scriba.policy-list.v1"
)

type policyValidateResult struct {
	SchemaVersion string        `json:"schemaVersion"`
	Valid         bool          `json:"valid"`
	File          string        `json:"file"`
	Preset        string        `json:"preset,omitempty"`
	Rules         []policy.Rule `json:"rules"`
	Errors        []string      `json:"errors,omitempty"`
}

type policyListResult struct {
	SchemaVersion string        `json:"schemaVersion"`
	Preset        string        `json:"preset"`
	Rules         []policy.Rule `json:"rules"`
}

func dispatchPolicy(args []string) error {
	if len(args) == 0 || isHelpArg(args[0]) {
		fmt.Println(groupHelp("policy"))
		return nil
	}
	switch args[0] {
	case "validate":
		for _, arg := range args[1:] {
			if isHelpArg(arg) {
				_, _, err := parse([]string{arg}, flagSpec{Use: "scriba policy validate <file> [flags]", Flags: []string{"json", "redact"}})
				return err
			}
		}
		path, flags, err := policyValidateArgs(args[1:])
		if err != nil {
			return err
		}
		opts, rest, err := parse(flags, flagSpec{Use: "scriba policy validate <file> [flags]", Flags: []string{"json", "redact"}})
		if err != nil {
			return err
		}
		if len(rest) != 0 {
			return fmt.Errorf("scriba policy validate does not accept positional arguments after the policy file")
		}
		return runPolicyValidate(path, opts)
	case "list":
		opts, rest, err := parse(args[1:], flagSpec{Use: "scriba policy list [flags]", Flags: []string{"config", "json"}})
		if err != nil {
			return err
		}
		if len(rest) != 0 {
			return fmt.Errorf("scriba policy list does not accept positional arguments")
		}
		return runPolicyList(opts)
	case "explain":
		opts, rest, err := parse(args[1:], flagSpec{
			Use:   "scriba policy explain [flags]",
			Flags: []string{"json", "config", "state-path", "env", "redact", "provider", "account", "rule", "limit"},
		})
		if err != nil {
			return err
		}
		if len(rest) != 0 {
			return fmt.Errorf("scriba policy explain does not accept positional arguments")
		}
		return runPolicyExplain(opts)
	default:
		return fmt.Errorf("unknown policy command: %s", args[0])
	}
}

func policyValidateArgs(args []string) (string, []string, error) {
	for i, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			flags := append([]string{}, args[:i]...)
			flags = append(flags, args[i+1:]...)
			return arg, flags, nil
		}
	}
	return "", nil, fmt.Errorf("scriba policy validate requires exactly one policy file")
}

func runPolicyValidate(path string, opts options) error {
	cfg, preset, err := readPolicyConfig(path)
	if err != nil {
		if opts.jsonOut {
			result := policyValidateResult{SchemaVersion: policyValidateSchemaVersion, Valid: false, File: path, Rules: []policy.Rule{}, Errors: []string{err.Error()}}
			if opts.redact {
				result = redactPolicyValidation(result)
			}
			if printErr := printJSON(result, false); printErr != nil {
				return printErr
			}
		}
		return inspectionError(opts, err)
	}
	result := policyValidateResult{SchemaVersion: policyValidateSchemaVersion, Valid: true, File: path, Preset: preset, Rules: cfg.Rules}
	if opts.redact {
		result = redactPolicyValidation(result)
		opts.redact = false
	}
	return output(opts, result, renderPolicyValidation(result))
}

func redactPolicyValidation(result policyValidateResult) policyValidateResult {
	result.File = fmt.Sprint(privacy.Redact(result.File))
	for i := range result.Errors {
		result.Errors[i] = fmt.Sprint(privacy.Redact(result.Errors[i]))
	}
	return result
}

func runPolicyList(opts options) error {
	cfg := policy.CurrentPreset()
	preset := policy.PresetCurrent
	if opts.config != "" {
		var err error
		cfg, preset, err = readPolicyConfig(opts.config)
		if err != nil {
			return err
		}
	}
	result := policyListResult{SchemaVersion: policyListSchemaVersion, Preset: preset, Rules: cfg.Rules}
	return output(opts, result, renderPolicyList(result))
}

func readPolicyConfig(path string) (policy.Config, string, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- this command intentionally validates the exact operator-supplied policy path.
	if err != nil {
		return policy.Config{}, "", fmt.Errorf("read policy config %q: %w", path, err)
	}
	cfg, err := policy.ParseConfig(data)
	if err != nil {
		return policy.Config{}, "", fmt.Errorf("validate policy config %q: %w", path, err)
	}
	var declared struct {
		Preset string `json:"preset"`
	}
	if err := json.Unmarshal(data, &declared); err != nil {
		return policy.Config{}, "", fmt.Errorf("decode policy config %q: %w", path, err)
	}
	return cfg, declared.Preset, nil
}

func renderPolicyValidation(result policyValidateResult) string {
	return fmt.Sprintf("%s\n%s valid · %s preset · %d rules", cliHeader("Policy config"), result.File, result.Preset, len(result.Rules))
}

func renderPolicyList(result policyListResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n%s preset · %d rules", cliHeader("Policy rules"), result.Preset, len(result.Rules))
	for _, rule := range result.Rules {
		fmt.Fprintf(&b, "\n\n%s\n  kind         %s", cliBold(rule.ID), rule.Kind)
		if len(rule.WindowKeys) > 0 {
			fmt.Fprintf(&b, "\n  windows      %s", strings.Join(rule.WindowKeys, ", "))
		}
		if len(rule.SecondaryWindowKeys) > 0 {
			fmt.Fprintf(&b, "\n  secondary    %s", strings.Join(rule.SecondaryWindowKeys, ", "))
		}
		if len(rule.Checkpoints) > 0 {
			fmt.Fprintf(&b, "\n  checkpoints  %v", rule.Checkpoints)
		}
		if rule.ClockJitterSec != 0 || rule.DueJitterSec != 0 {
			fmt.Fprintf(&b, "\n  jitter       clock %ds · due %ds", rule.ClockJitterSec, rule.DueJitterSec)
		}
	}
	return b.String()
}
