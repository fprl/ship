package releaseid

import (
	"fmt"
	"regexp"
	"time"
)

const (
	gitCommitPattern      = `[a-f0-9]{7,64}`
	dirtyTimestampPattern = `[0-9]{8}t[0-9]{15}z`
	staticHashPattern     = `[0-9a-f]{12}`

	Pattern = `^(?:` + gitCommitPattern + `|` + gitCommitPattern + `-dirty-` + dirtyTimestampPattern + `)(?:-s` + staticHashPattern + `)?$`
)

var (
	re             = regexp.MustCompile(Pattern)
	staticSuffixRe = regexp.MustCompile(`-s(` + staticHashPattern + `)$`)
	dirtyRe        = regexp.MustCompile(`^(` + gitCommitPattern + `)-dirty-(` + dirtyTimestampPattern + `)$`)
	gitCommitRe    = regexp.MustCompile(`^` + gitCommitPattern + `$`)
)

type Info struct {
	Base       string
	Dirty      bool
	Timestamp  string
	StaticHash string
}

func Validate(release string) error {
	if !re.MatchString(release) {
		return fmt.Errorf("invalid release id: %q", release)
	}
	return nil
}

func Parse(release string) (Info, error) {
	if err := Validate(release); err != nil {
		return Info{}, err
	}
	info := Info{}
	baseRelease := release
	if match := staticSuffixRe.FindStringSubmatch(release); match != nil {
		info.StaticHash = match[1]
		baseRelease = release[:len(release)-14]
	}
	if match := dirtyRe.FindStringSubmatch(baseRelease); match != nil {
		info.Base = match[1]
		info.Dirty = true
		info.Timestamp = match[2]
		return info, nil
	}
	info.Base = baseRelease
	return info, nil
}

func Dirty(baseCommit string, at time.Time) string {
	return fmt.Sprintf("%s-dirty-%s", baseCommit, DirtyTimestamp(at))
}

func DirtyTimestamp(at time.Time) string {
	at = at.UTC()
	return fmt.Sprintf("%s%09dz", at.Format("20060102t150405"), at.Nanosecond())
}

func WithStaticHash(release string, hash string) (string, error) {
	if err := Validate(release); err != nil {
		return "", err
	}
	if !regexp.MustCompile(`^` + staticHashPattern + `$`).MatchString(hash) {
		return "", fmt.Errorf("invalid static hash %q", hash)
	}
	if info, err := Parse(release); err != nil {
		return "", err
	} else if info.StaticHash != "" {
		return "", fmt.Errorf("release already has static hash: %q", release)
	}
	return release + "-s" + hash, nil
}

func IsGitCommit(value string) bool {
	return gitCommitRe.MatchString(value)
}
