package policy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"slices"
	"time"
)

const (
	PresetCurrent = "current"
	PresetCustom  = "custom"

	KindRemainingCheckpoint   RuleKind = "remaining_checkpoint"
	KindResetTransition       RuleKind = "reset_transition"
	KindGrantAvailable        RuleKind = "grant_available"
	KindGrantExpiryCheckpoint RuleKind = "grant_expiry_checkpoint"
)

type RuleKind string

var canonicalIdentifier = regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*$`)

const maxJitterSeconds = int64(math.MaxInt64 / int64(time.Second))

type Config struct {
	Preset string `json:"preset,omitempty"`
	Rules  []Rule `json:"rules,omitempty"`
}

type Rule struct {
	ID                  string   `json:"id"`
	Kind                RuleKind `json:"kind"`
	WindowKeys          []string `json:"windowKeys,omitempty"`
	SecondaryWindowKeys []string `json:"secondaryWindowKeys,omitempty"`
	Checkpoints         []int    `json:"checkpoints,omitempty"`
	ClockJitterSec      int      `json:"clockJitterSec,omitempty"`
	DueJitterSec        int      `json:"dueJitterSec,omitempty"`
}

func CurrentPreset() Config {
	return Config{Rules: []Rule{
		{ID: "current.remaining.primary", Kind: KindRemainingCheckpoint, WindowKeys: []string{"primary.five_hour", "primary.weekly"}, Checkpoints: []int{20, 10, 5, 0}, ClockJitterSec: 300},
		{ID: "current.reset.weekly", Kind: KindResetTransition, WindowKeys: []string{"primary.weekly"}, SecondaryWindowKeys: []string{"spark.weekly", "review.weekly"}, ClockJitterSec: 300, DueJitterSec: 600},
		{ID: "current.grant.available", Kind: KindGrantAvailable},
		{ID: "current.grant.expiry", Kind: KindGrantExpiryCheckpoint, Checkpoints: []int{5, 3, 1}},
	}}
}

func ParseConfig(data []byte) (Config, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode policy config: %w", err)
	}
	if err := ensureEOF(dec); err != nil {
		return Config{}, err
	}
	if cfg.Preset == "" {
		return Config{}, errors.New("policy config requires preset")
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	if cfg.Preset == PresetCurrent {
		return CurrentPreset(), nil
	}
	cfg.Preset = ""
	return cloneConfig(cfg), nil
}

func (c Config) Validate() error {
	if c.Preset == PresetCurrent {
		if c.Rules != nil {
			return errors.New("policy preset and custom rules are mutually exclusive")
		}
		return nil
	}
	if c.Preset == PresetCustom && len(c.Rules) == 0 {
		return errors.New("custom policy preset requires rules")
	}
	if c.Preset != "" && c.Preset != PresetCustom {
		return fmt.Errorf("unknown policy preset %q", c.Preset)
	}
	if len(c.Rules) == 0 {
		return errors.New("policy config requires preset or rules")
	}
	ids := map[string]bool{}
	for i, r := range c.Rules {
		if !canonicalIdentifier.MatchString(r.ID) {
			return fmt.Errorf("rules[%d].id must be a canonical identifier", i)
		}
		if ids[r.ID] {
			return fmt.Errorf("duplicate policy rule id %q", r.ID)
		}
		ids[r.ID] = true
		if err := r.validate(); err != nil {
			return fmt.Errorf("rule %q: %w", r.ID, err)
		}
	}
	return nil
}

func (r Rule) validate() error {
	switch r.Kind {
	case KindRemainingCheckpoint:
		if len(r.WindowKeys) == 0 || !validCheckpoints(r.Checkpoints, 0, 100) {
			return errors.New("remaining checkpoint requires unique windowKeys and descending checkpoints in [0,100]")
		}
		if r.ClockJitterSec < 0 || r.DueJitterSec != 0 || int64(r.ClockJitterSec) > maxJitterSeconds {
			return errors.New("remaining checkpoint accepts non-negative clock jitter only")
		}
		if len(r.SecondaryWindowKeys) != 0 {
			return errors.New("remaining checkpoint does not accept secondaryWindowKeys")
		}
	case KindResetTransition:
		if len(r.WindowKeys) == 0 || len(r.Checkpoints) != 0 {
			return errors.New("reset transition requires windowKeys and no checkpoints")
		}
		if r.ClockJitterSec < 0 || r.DueJitterSec < 0 || int64(r.ClockJitterSec) > maxJitterSeconds || int64(r.DueJitterSec) > maxJitterSeconds {
			return errors.New("jitter must fit safely in time.Duration")
		}
	case KindGrantAvailable:
		if len(r.WindowKeys) != 0 || len(r.SecondaryWindowKeys) != 0 || len(r.Checkpoints) != 0 || r.ClockJitterSec != 0 || r.DueJitterSec != 0 {
			return errors.New("grant available accepts no kind-specific fields")
		}
	case KindGrantExpiryCheckpoint:
		if len(r.WindowKeys) != 0 || len(r.SecondaryWindowKeys) != 0 || !validCheckpoints(r.Checkpoints, 1, 3650) || r.ClockJitterSec != 0 || r.DueJitterSec != 0 {
			return errors.New("grant expiry requires descending day checkpoints and accepts no other fields")
		}
	default:
		return fmt.Errorf("unknown kind %q", r.Kind)
	}
	if hasBlankOrDuplicate(r.WindowKeys) || hasBlankOrDuplicate(r.SecondaryWindowKeys) {
		return errors.New("windowKeys must be trimmed and unique")
	}
	primary := map[string]bool{}
	for _, key := range r.WindowKeys {
		primary[key] = true
	}
	for _, key := range r.SecondaryWindowKeys {
		if primary[key] {
			return errors.New("primary and secondary windowKeys must not overlap")
		}
	}
	return nil
}

func validCheckpoints(v []int, min, max int) bool {
	if len(v) == 0 {
		return false
	}
	seen := map[int]bool{}
	for i, n := range v {
		if n < min || n > max || seen[n] || (i > 0 && v[i-1] <= n) {
			return false
		}
		seen[n] = true
	}
	return true
}

func hasBlankOrDuplicate(v []string) bool {
	seen := map[string]bool{}
	for _, s := range v {
		if !canonicalIdentifier.MatchString(s) || seen[s] {
			return true
		}
		seen[s] = true
	}
	return false
}

func cloneConfig(c Config) Config {
	out := Config{Preset: c.Preset, Rules: make([]Rule, len(c.Rules))}
	copy(out.Rules, c.Rules)
	for i := range out.Rules {
		out.Rules[i].WindowKeys = slices.Clone(c.Rules[i].WindowKeys)
		out.Rules[i].SecondaryWindowKeys = slices.Clone(c.Rules[i].SecondaryWindowKeys)
		out.Rules[i].Checkpoints = slices.Clone(c.Rules[i].Checkpoints)
	}
	return out
}

func ensureEOF(dec *json.Decoder) error {
	var extra any
	if err := dec.Decode(&extra); err == io.EOF {
		return nil
	}
	return errors.New("decode policy config: multiple JSON values")
}
