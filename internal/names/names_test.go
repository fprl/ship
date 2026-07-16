package names

import (
	"strings"
	"testing"
	"time"
)

func TestValidGitBranch(t *testing.T) {
	tests := []struct {
		branch string
		want   bool
	}{
		{branch: "feat/login", want: true},
		{branch: "FEAT-X", want: true},
		{branch: "mañana/Über", want: true},
		{branch: "", want: false},
		{branch: "@", want: false},
		{branch: "-feat", want: false},
		{branch: "feat..x", want: false},
		{branch: "feat/@{x", want: false},
		{branch: "feat/x.lock", want: false},
		{branch: "feat x", want: false},
		{branch: "feat\nx", want: false},
		{branch: "feat\x7fx", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.branch, func(t *testing.T) {
			if got := ValidGitBranch(tt.branch); got != tt.want {
				t.Fatalf("ValidGitBranch(%q) = %v, want %v", tt.branch, got, tt.want)
			}
		})
	}
}

func TestSynthesizedHostLabelUsesAppFirstProductionAndBudgetedPreviewShape(t *testing.T) {
	longApp := strings.Repeat("a", 41)
	longSlug := strings.Repeat("b", 28)
	trailingDashApp := strings.Repeat("c", 30)
	trailingDashSlug := strings.Repeat("d", 26) + "-more"

	tests := []struct {
		app  string
		env  string
		want string
	}{
		{app: "api", env: ProductionEnvName, want: "api"},
		{app: "api", env: "feat-x-ab12", want: "api-feat-x-ab12"},
		{app: longApp, env: longSlug + "-ab12", want: longApp + "-" + strings.Repeat("b", 16) + "-ab12"},
		{app: trailingDashApp, env: trailingDashSlug + "-ab12", want: trailingDashApp + "-" + strings.Repeat("d", 26) + "-ab12"},
	}
	for _, tt := range tests {
		t.Run(tt.app+"/"+tt.env, func(t *testing.T) {
			if got := SynthesizedHostLabel(tt.app, tt.env); got != tt.want {
				t.Fatalf("SynthesizedHostLabel(%q, %q) = %q, want %q", tt.app, tt.env, got, tt.want)
			}
		})
	}
}

func TestSynthesizedHostLabelFallbacksAreValidDNSLabels(t *testing.T) {
	longApp := strings.Repeat("a", 41)
	tests := []struct {
		name string
		app  string
		env  string
		want string
	}{
		{
			name: "long app and env",
			app:  longApp,
			env:  strings.Repeat("b", 33),
			want: longApp + "-" + strings.Repeat("b", 21),
		},
		{name: "trailing env dash", app: "api", env: "a-", want: "api-a"},
		{name: "env without dash", app: "api", env: "plain", want: "api-plain"},
		{name: "empty slug after trim", app: "api", env: "---", want: "api"},
		{name: "production trailing app dash", app: "api-", env: ProductionEnvName, want: "api"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SynthesizedHostLabel(tt.app, tt.env)
			if got != tt.want {
				t.Fatalf("SynthesizedHostLabel(%q, %q) = %q, want %q", tt.app, tt.env, got, tt.want)
			}
			if len(got) > 63 || strings.HasPrefix(got, "-") || strings.HasSuffix(got, "-") {
				t.Fatalf("SynthesizedHostLabel(%q, %q) = %q, want valid DNS label", tt.app, tt.env, got)
			}
		})
	}
}

func TestPreviewBranchSlugStripsOnlyThePersistedSuffix(t *testing.T) {
	for _, tt := range []struct {
		env  string
		want string
	}{
		{env: "feat-new-pricing-x7q2", want: "feat-new-pricing"},
		{env: "x-ab12", want: "x"},
		{env: "plain", want: "plain"},
	} {
		if got := PreviewBranchSlug(tt.env); got != tt.want {
			t.Fatalf("PreviewBranchSlug(%q) = %q, want %q", tt.env, got, tt.want)
		}
	}
}

func TestPreviewDerivations(t *testing.T) {
	if got, want := PreviewSanitizedBranch("Feature/Login"), "feature-login"; got != want {
		t.Fatalf("PreviewSanitizedBranch = %q, want %q", got, want)
	}
	if got, ok := PreviewSuffix("feature-login-a1b2"); !ok || got != "a1b2" {
		t.Fatalf("PreviewSuffix = %q, %v, want a1b2, true", got, ok)
	}
	for _, env := range []string{"plain", "feature-login-A1b2", "feature-login-a12", "feature-login-a1b2c"} {
		if got, ok := PreviewSuffix(env); ok {
			t.Fatalf("PreviewSuffix(%q) = %q, true; want invalid", env, got)
		}
	}
	if !PreviewPinned(nil) {
		t.Fatal("nil expiry should derive pinned=true")
	}
	expires := time.Now()
	if PreviewPinned(&expires) {
		t.Fatal("present expiry should derive pinned=false")
	}
}
