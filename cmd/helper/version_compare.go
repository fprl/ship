package helper

import (
	"strconv"
	"strings"
)

// compareShipVersions returns -1 when left is older than right, 1 when it is
// newer, and 0 when they are equal or cannot be safely ordered. Release builds
// use vMAJOR.MINOR.PATCH; dev builds only compare equal to themselves.
func compareShipVersions(left, right string) int {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	if left == right {
		return 0
	}
	a, okA := parseShipVersion(left)
	b, okB := parseShipVersion(right)
	if !okA || !okB {
		return 0
	}
	for i := range a {
		if a[i] < b[i] {
			return -1
		}
		if a[i] > b[i] {
			return 1
		}
	}
	return 0
}

func parseShipVersion(value string) ([3]int, bool) {
	value = strings.TrimPrefix(strings.TrimSpace(value), "v")
	value, _, _ = strings.Cut(value, "-")
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return [3]int{}, false
	}
	var out [3]int
	for i, part := range parts {
		parsed, err := strconv.Atoi(part)
		if err != nil || parsed < 0 {
			return [3]int{}, false
		}
		out[i] = parsed
	}
	return out, true
}
