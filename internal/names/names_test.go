package names

import "testing"

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
