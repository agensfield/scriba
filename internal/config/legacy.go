package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// legacyAuthPaths preserves explicit v2 credential routing while upgrading the
// configuration in memory. Labels described sources, so they are not account aliases.
func legacyAuthPaths(data []byte) ([]string, error) {
	var legacy struct {
		Default  string `json:"defaultProfileId"`
		Profiles []struct {
			ID      string   `json:"id"`
			Label   string   `json:"label"`
			Enabled bool     `json:"enabled"`
			Paths   []string `json:"codexAuthPaths"`
		} `json:"profiles"`
	}
	if err := json.Unmarshal(data, &legacy); err != nil {
		return nil, err
	}
	ids, paths := map[string]bool{}, map[string]bool{}
	var primary, other []string
	defaultEnabled := false
	for _, p := range legacy.Profiles {
		if len(p.ID) > 32 || !slugPattern.MatchString(p.ID) || ids[p.ID] || strings.TrimSpace(p.Label) == "" {
			return nil, errors.New("invalid legacy profile configuration")
		}
		ids[p.ID] = true
		if p.Enabled && len(p.Paths) == 0 {
			return nil, errors.New("enabled legacy profile requires explicit auth paths")
		}
		for _, path := range p.Paths {
			clean := filepath.Clean(path)
			if !filepath.IsAbs(path) || paths[clean] {
				return nil, fmt.Errorf("invalid or duplicate legacy auth path %q", path)
			}
			paths[clean] = true
			if p.Enabled {
				if p.ID == legacy.Default {
					primary = append(primary, clean)
				} else {
					other = append(other, clean)
				}
			}
		}
		defaultEnabled = defaultEnabled || p.Enabled && p.ID == legacy.Default
	}
	if !defaultEnabled {
		return nil, errors.New("legacy defaultProfileId must identify an enabled profile")
	}
	return append(primary, other...), nil
}
