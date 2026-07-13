package version

import (
	"strconv"
	"strings"
)

// Version is replaced by release builds through -ldflags.
var Version = "dev"

// Compare orders two ship versions by SemVer precedence.
// ok is false when the pair cannot be safely ordered (either side
// unparseable and the strings differ); cmp is 0 in that case.
func Compare(left, right string) (cmp int, ok bool) {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	if left == right {
		return 0, true
	}
	a, okA := parse(left)
	b, okB := parse(right)
	if !okA || !okB {
		return 0, false
	}
	for i := range a.core {
		if a.core[i] < b.core[i] {
			return -1, true
		}
		if a.core[i] > b.core[i] {
			return 1, true
		}
	}
	if len(a.pre) == 0 && len(b.pre) > 0 {
		return 1, true
	}
	if len(a.pre) > 0 && len(b.pre) == 0 {
		return -1, true
	}
	for i := 0; i < len(a.pre) && i < len(b.pre); i++ {
		if cmp := comparePrereleaseIdentifier(a.pre[i], b.pre[i]); cmp != 0 {
			return cmp, true
		}
	}
	if len(a.pre) < len(b.pre) {
		return -1, true
	}
	if len(a.pre) > len(b.pre) {
		return 1, true
	}
	return 0, true
}

type shipVersion struct {
	core [3]int
	pre  []string
}

func parse(value string) (shipVersion, bool) {
	value = strings.TrimPrefix(strings.TrimSpace(value), "v")
	value, build, hasBuild := strings.Cut(value, "+")
	if hasBuild && !validSemverIdentifiers(build, false) {
		return shipVersion{}, false
	}
	value, prerelease, hasPrerelease := strings.Cut(value, "-")
	if hasPrerelease && !validSemverIdentifiers(prerelease, true) {
		return shipVersion{}, false
	}
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return shipVersion{}, false
	}
	var out shipVersion
	for i, part := range parts {
		if !isNumericIdentifier(part) {
			return shipVersion{}, false
		}
		parsed, err := strconv.Atoi(part)
		if err != nil {
			return shipVersion{}, false
		}
		out.core[i] = parsed
	}
	if hasPrerelease {
		out.pre = strings.Split(prerelease, ".")
	}
	return out, true
}

func validSemverIdentifiers(value string, rejectNumericLeadingZero bool) bool {
	for _, identifier := range strings.Split(value, ".") {
		if identifier == "" {
			return false
		}
		for _, r := range identifier {
			if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && r != '-' {
				return false
			}
		}
		if rejectNumericLeadingZero && isAllDigits(identifier) && len(identifier) > 1 && identifier[0] == '0' {
			return false
		}
	}
	return true
}

func isNumericIdentifier(value string) bool {
	return isAllDigits(value) && (len(value) == 1 || value[0] != '0')
}

func isAllDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func comparePrereleaseIdentifier(left, right string) int {
	leftNumeric := isAllDigits(left)
	rightNumeric := isAllDigits(right)
	switch {
	case leftNumeric && rightNumeric:
		if len(left) < len(right) {
			return -1
		}
		if len(left) > len(right) {
			return 1
		}
	case leftNumeric:
		return -1
	case rightNumeric:
		return 1
	}
	if left < right {
		return -1
	}
	if left > right {
		return 1
	}
	return 0
}
