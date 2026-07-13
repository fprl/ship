package version

import "testing"

func TestCompare(t *testing.T) {
	tests := []struct {
		name        string
		left, right string
		want        int
		ok          bool
	}{
		{name: "release candidate precedes release", left: "v0.4.0-rc1", right: "v0.4.0", want: -1, ok: true},
		{name: "semver section 11 alpha", left: "1.0.0-alpha", right: "1.0.0-alpha.1", want: -1, ok: true},
		{name: "semver section 11 alpha numeric to alphanumeric", left: "1.0.0-alpha.1", right: "1.0.0-alpha.beta", want: -1, ok: true},
		{name: "semver section 11 alpha to beta", left: "1.0.0-alpha.beta", right: "1.0.0-beta", want: -1, ok: true},
		{name: "semver section 11 beta", left: "1.0.0-beta", right: "1.0.0-beta.2", want: -1, ok: true},
		{name: "semver section 11 beta numeric", left: "1.0.0-beta.2", right: "1.0.0-beta.11", want: -1, ok: true},
		{name: "semver section 11 beta to release candidate", left: "1.0.0-beta.11", right: "1.0.0-rc.1", want: -1, ok: true},
		{name: "semver section 11 release candidate to release", left: "1.0.0-rc.1", right: "1.0.0", want: -1, ok: true},
		{name: "numeric identifier precedes alphanumeric identifier", left: "1.0.0-1", right: "1.0.0-alpha", want: -1, ok: true},
		{name: "shorter prerelease identifier set precedes longer set", left: "1.0.0-alpha", right: "1.0.0-alpha.1", want: -1, ok: true},
		{name: "build metadata is ignored", left: "1.0.0+meta.1", right: "1.0.0+meta.2", want: 0, ok: true},
		{name: "leading zero core is unorderable", left: "1.0.01", right: "1.0.1", want: 0, ok: false},
		{name: "leading zero numeric prerelease is unorderable", left: "1.0.0-alpha.01", right: "1.0.0-alpha.1", want: 0, ok: false},
		{name: "empty prerelease identifier is unorderable", left: "1.0.0-alpha..1", right: "1.0.0-alpha.1", want: 0, ok: false},
		{name: "empty build identifier is unorderable", left: "1.0.0+", right: "1.0.0", want: 0, ok: false},
		{name: "non three part core is unorderable", left: "1.0", right: "1.0.0", want: 0, ok: false},
		{name: "identical unparseable versions are equal", left: " dev ", right: "dev", want: 0, ok: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, ok := Compare(tt.left, tt.right); got != tt.want || ok != tt.ok {
				t.Fatalf("Compare(%q, %q) = (%d, %t), want (%d, %t)", tt.left, tt.right, got, ok, tt.want, tt.ok)
			}
			if got, ok := Compare(tt.right, tt.left); got != -tt.want || ok != tt.ok {
				t.Fatalf("Compare(%q, %q) = (%d, %t), want (%d, %t)", tt.right, tt.left, got, ok, -tt.want, tt.ok)
			}
		})
	}
}
