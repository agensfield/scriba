package updater

import "testing"

func TestLatestTagFromLsRemote(t *testing.T) {
	output := []byte(`abc	refs/tags/v0.1.0
def	refs/tags/v0.1.1-alpha.1
ghi	refs/tags/not-a-version
jkl	refs/tags/v0.2.0
`)

	tag, ok := LatestTagFromLsRemote(output)
	if !ok {
		t.Fatal("expected tag")
	}
	if tag != "v0.2.0" {
		t.Fatalf("tag = %q, want v0.2.0", tag)
	}
}

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		current string
		latest  string
		want    Status
	}{
		{current: "v0.1.0", latest: "v0.1.1", want: Outdated},
		{current: "v0.1.1", latest: "v0.1.1", want: Current},
		{current: "v0.2.0", latest: "v0.1.1", want: Ahead},
		{current: "0.1.1", latest: "v0.1.1", want: Current},
		{current: "v0.1.1-alpha.1", latest: "v0.1.1", want: Outdated},
		{current: "0.1.1-alpha.5", latest: "v0.1.1-alpha.1", want: Ahead},
		{current: "dev", latest: "v0.1.1", want: Unknown},
	}

	for _, tt := range tests {
		if got := CompareVersions(tt.current, tt.latest); got != tt.want {
			t.Fatalf("CompareVersions(%q, %q) = %v, want %v", tt.current, tt.latest, got, tt.want)
		}
	}
}

func TestResolveInstallTargetDetectsHomebrew(t *testing.T) {
	target := ResolveInstallTarget("/opt/homebrew/Cellar/scriba/0.2.0/bin/scriba")
	if target.Manager != "homebrew" {
		t.Fatalf("manager = %q, want homebrew", target.Manager)
	}
	if target.SelfUpdateSupported {
		t.Fatal("expected Homebrew install to disable self-update")
	}
	if got := UpdateCommand("v0.2.1", target); got != "brew upgrade scriba" {
		t.Fatalf("UpdateCommand() = %q, want brew upgrade", got)
	}
}
