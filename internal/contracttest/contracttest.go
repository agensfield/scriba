package contracttest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Manifest struct {
	Version int    `json:"version"`
	Cases   []Case `json:"cases"`
}

type Case struct {
	ID       string   `json:"id"`
	Provider string   `json:"provider"`
	Fixture  string   `json:"fixture"`
	Tags     []string `json:"tags"`
	Policy   string   `json:"policy,omitempty"`
}

func LoadManifest(root string) (Manifest, error) {
	data, err := os.ReadFile(filepath.Join(root, "manifest.json"))
	if err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return Manifest{}, err
	}
	if manifest.Version != 1 {
		return Manifest{}, fmt.Errorf("unsupported contract manifest version %d", manifest.Version)
	}
	seen := map[string]bool{}
	for _, c := range manifest.Cases {
		if c.ID == "" || c.Provider == "" || c.Fixture == "" || len(c.Tags) == 0 {
			return Manifest{}, fmt.Errorf("incomplete contract case %q", c.ID)
		}
		if seen[c.ID] {
			return Manifest{}, fmt.Errorf("duplicate contract case %q", c.ID)
		}
		seen[c.ID] = true
		clean := filepath.Clean(filepath.FromSlash(c.Fixture))
		if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return Manifest{}, fmt.Errorf("case %s: fixture escapes contract root", c.ID)
		}
		if _, err := os.Stat(filepath.Join(root, clean)); err != nil {
			return Manifest{}, fmt.Errorf("case %s: %w", c.ID, err)
		}
	}
	return manifest, nil
}

func CanonicalJSON(value any) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var decoded any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := writeCanonical(&out, decoded); err != nil {
		return nil, err
	}
	out.WriteByte('\n')
	return out.Bytes(), nil
}

func writeCanonical(out *bytes.Buffer, value any) error {
	switch v := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				out.WriteByte(',')
			}
			key, _ := json.Marshal(k)
			out.Write(key)
			out.WriteByte(':')
			if err := writeCanonical(out, v[k]); err != nil {
				return err
			}
		}
		out.WriteByte('}')
	case []any:
		out.WriteByte('[')
		for i, item := range v {
			if i > 0 {
				out.WriteByte(',')
			}
			if err := writeCanonical(out, item); err != nil {
				return err
			}
		}
		out.WriteByte(']')
	default:
		data, err := json.Marshal(v)
		if err != nil {
			return err
		}
		out.Write(data)
	}
	return nil
}
