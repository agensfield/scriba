package updater

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	ModulePath = "github.com/agensfield/scriba/cmd/scriba"
	RepoURL    = "https://github.com/agensfield/scriba.git"
)

type Status int

const (
	Unknown Status = iota
	Current
	Outdated
	Ahead
)

type Check struct {
	Current             string `json:"current"`
	Latest              string `json:"latest"`
	Status              Status `json:"-"`
	StatusText          string `json:"status"`
	InstallPath         string `json:"installPath,omitempty"`
	ResolvedInstallPath string `json:"resolvedInstallPath,omitempty"`
	InstallManager      string `json:"installManager"`
	SelfUpdateSupported bool   `json:"selfUpdateSupported"`
	SelfUpdateReason    string `json:"selfUpdateReason,omitempty"`
	UpdateCommand       string `json:"updateCommand"`
}

func CheckLatest(ctx context.Context, current string) (Check, error) {
	latest, err := LatestTag(ctx)
	if err != nil {
		return Check{}, err
	}
	target := ResolveInstallTarget("")
	status := CompareVersions(current, latest)
	return Check{
		Current:             current,
		Latest:              latest,
		Status:              status,
		StatusText:          StatusText(status),
		InstallPath:         target.Path,
		ResolvedInstallPath: target.RealPath,
		InstallManager:      target.Manager,
		SelfUpdateSupported: target.SelfUpdateSupported,
		SelfUpdateReason:    target.Reason,
		UpdateCommand:       UpdateCommand(latest, target),
	}, nil
}

func LatestTag(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", "ls-remote", "--tags", "--refs", RepoURL)
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("read scriba tags: %w", err)
	}
	tag, ok := LatestTagFromLsRemote(output)
	if !ok {
		return "", fmt.Errorf("no version tags found")
	}
	return tag, nil
}

func Install(ctx context.Context, tag string, stdout, stderr *os.File) error {
	target := ResolveInstallTarget("")
	if !target.SelfUpdateSupported {
		return fmt.Errorf("%s", target.Reason)
	}
	if strings.TrimSpace(tag) == "" {
		tag = "latest"
	} else if _, ok := parseVersion(tag); !ok {
		return fmt.Errorf("invalid release tag %q", tag)
	}
	cmd := exec.CommandContext(ctx, "go", "install", ModulePath+"@"+tag) // #nosec G204 -- tag is either "latest" or a validated Scriba semver release tag.
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

func InstallCommand(tag string) string {
	if strings.TrimSpace(tag) == "" {
		tag = "latest"
	}
	return "go install " + ModulePath + "@" + tag
}

func UpdateCommand(tag string, target InstallTarget) string {
	if target.Manager == "homebrew" {
		return "brew upgrade scriba"
	}
	return InstallCommand(tag)
}

func LatestTagFromLsRemote(output []byte) (string, bool) {
	var versions []string
	for _, line := range bytes.Split(output, []byte{'\n'}) {
		fields := bytes.Fields(line)
		if len(fields) != 2 {
			continue
		}
		ref := string(fields[1])
		tag := strings.TrimPrefix(ref, "refs/tags/")
		if _, ok := parseVersion(tag); ok {
			versions = append(versions, tag)
		}
	}
	if len(versions) == 0 {
		return "", false
	}
	sort.Slice(versions, func(i, j int) bool {
		return CompareVersions(versions[i], versions[j]) == Ahead
	})
	return versions[0], true
}

func CompareVersions(current, latest string) Status {
	cur, curOK := parseVersion(current)
	next, nextOK := parseVersion(latest)
	if !curOK || !nextOK {
		return Unknown
	}
	comparison := cur.Compare(next)
	switch {
	case comparison < 0:
		return Outdated
	case comparison > 0:
		return Ahead
	default:
		return Current
	}
}

func StatusText(status Status) string {
	switch status {
	case Current:
		return "up to date"
	case Outdated:
		return "update available"
	case Ahead:
		return "ahead of latest tag"
	default:
		return "unknown"
	}
}

type InstallTarget struct {
	Path                string
	RealPath            string
	Manager             string
	SelfUpdateSupported bool
	Reason              string
}

func ResolveInstallTarget(executable string) InstallTarget {
	path := strings.TrimSpace(executable)
	if path == "" {
		path, _ = os.Executable()
	}
	realPath := path
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		realPath = resolved
	}
	target := InstallTarget{
		Path:                path,
		RealPath:            realPath,
		Manager:             "go-install",
		SelfUpdateSupported: true,
	}
	if isHomebrewPath(realPath) {
		target.Manager = "homebrew"
		target.SelfUpdateSupported = false
		target.Reason = "installed by Homebrew; use `brew upgrade scriba`"
	}
	return target
}

func isHomebrewPath(path string) bool {
	normalized := filepath.ToSlash(path)
	return strings.Contains(normalized, "/Cellar/scriba/")
}

type version struct {
	major int
	minor int
	patch int
	pre   string
}

func (v version) Compare(other version) int {
	for _, pair := range [][2]int{{v.major, other.major}, {v.minor, other.minor}, {v.patch, other.patch}} {
		switch {
		case pair[0] < pair[1]:
			return -1
		case pair[0] > pair[1]:
			return 1
		}
	}
	switch {
	case v.pre == "" && other.pre != "":
		return 1
	case v.pre != "" && other.pre == "":
		return -1
	case v.pre < other.pre:
		return -1
	case v.pre > other.pre:
		return 1
	default:
		return 0
	}
}

func parseVersion(value string) (version, bool) {
	value = strings.TrimSpace(strings.TrimPrefix(value, "v"))
	base, pre, _ := strings.Cut(value, "-")
	parts := strings.Split(base, ".")
	if len(parts) != 3 {
		return version{}, false
	}
	var parsed version
	targets := []*int{&parsed.major, &parsed.minor, &parsed.patch}
	for i, part := range parts {
		if part == "" || strings.ContainsFunc(part, func(r rune) bool { return r < '0' || r > '9' }) {
			return version{}, false
		}
		n, err := strconv.Atoi(part)
		if err != nil {
			return version{}, false
		}
		*targets[i] = n
	}
	parsed.pre = pre
	return parsed, true
}
