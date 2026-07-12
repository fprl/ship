package helper

import "testing"

func TestCompareShipVersions(t *testing.T) {
	tests := []struct {
		name        string
		left, right string
		want        int
	}{
		{name: "release candidate precedes release", left: "v0.4.0-rc1", right: "v0.4.0", want: -1},
		{name: "semver section 11 alpha", left: "1.0.0-alpha", right: "1.0.0-alpha.1", want: -1},
		{name: "semver section 11 alpha numeric to alphanumeric", left: "1.0.0-alpha.1", right: "1.0.0-alpha.beta", want: -1},
		{name: "semver section 11 alpha to beta", left: "1.0.0-alpha.beta", right: "1.0.0-beta", want: -1},
		{name: "semver section 11 beta", left: "1.0.0-beta", right: "1.0.0-beta.2", want: -1},
		{name: "semver section 11 beta numeric", left: "1.0.0-beta.2", right: "1.0.0-beta.11", want: -1},
		{name: "semver section 11 beta to release candidate", left: "1.0.0-beta.11", right: "1.0.0-rc.1", want: -1},
		{name: "semver section 11 release candidate to release", left: "1.0.0-rc.1", right: "1.0.0", want: -1},
		{name: "numeric identifier precedes alphanumeric identifier", left: "1.0.0-1", right: "1.0.0-alpha", want: -1},
		{name: "shorter prerelease identifier set precedes longer set", left: "1.0.0-alpha", right: "1.0.0-alpha.1", want: -1},
		{name: "build metadata is ignored", left: "1.0.0+meta.1", right: "1.0.0+meta.2", want: 0},
		{name: "leading zero core is invalid", left: "1.0.01", right: "1.0.1", want: 0},
		{name: "leading zero numeric prerelease is invalid", left: "1.0.0-alpha.01", right: "1.0.0-alpha.1", want: 0},
		{name: "empty prerelease identifier is invalid", left: "1.0.0-alpha..1", right: "1.0.0-alpha.1", want: 0},
		{name: "empty build identifier is invalid", left: "1.0.0+", right: "1.0.0", want: 0},
		{name: "non three part core is invalid", left: "1.0", right: "1.0.0", want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := compareShipVersions(tt.left, tt.right); got != tt.want {
				t.Fatalf("compareShipVersions(%q, %q) = %d, want %d", tt.left, tt.right, got, tt.want)
			}
			if got, want := compareShipVersions(tt.right, tt.left), -tt.want; got != want {
				t.Fatalf("compareShipVersions(%q, %q) = %d, want %d", tt.right, tt.left, got, want)
			}
		})
	}
}
