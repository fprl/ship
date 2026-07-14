// Package names is the single policy point for user-supplied ship
// identifiers. Generated host/container artifact names live in
// internal/identity.
package names

import (
	"regexp"
	"strings"
)

const (
	AppPattern        = `^[a-z][a-z0-9-]{1,40}$`
	EnvPattern        = `^[a-z0-9][a-z0-9-]{0,32}$`
	ProcessPattern    = `^[a-z][a-z0-9-]{0,30}$`
	SystemUserPattern = `^[a-z_][a-z0-9_-]{0,31}\$?$`
	EnvKeyPattern     = `^[A-Za-z_][A-Za-z0-9_]*$`
	ProductionEnvName = "production"
)

var (
	AppRe        = regexp.MustCompile(AppPattern)
	EnvRe        = regexp.MustCompile(EnvPattern)
	ProcessRe    = regexp.MustCompile(ProcessPattern)
	SystemUserRe = regexp.MustCompile(SystemUserPattern)
	EnvKeyRe     = regexp.MustCompile(EnvKeyPattern)
)

func SanitizeBranchEnvName(branch string) string {
	branch = strings.ToLower(branch)
	var b strings.Builder
	prevDash := false
	for _, r := range branch {
		valid := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-'
		if valid {
			if r == '-' {
				if prevDash {
					continue
				}
				prevDash = true
			} else {
				prevDash = false
			}
			b.WriteRune(r)
			continue
		}
		if !prevDash {
			b.WriteByte('-')
			prevDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 28 {
		out = out[:28]
	}
	return strings.Trim(out, "-")
}

// SynthesizedHostLabel returns the one-label, app-first hostname prefix for
// an environment. Production uses the app name alone; previews carry the
// persisted suffix from their environment name.
func SynthesizedHostLabel(app, env string) string {
	app = strings.Trim(app, "-")
	if env == ProductionEnvName {
		return app
	}

	slug, suffix, ok := strings.Cut(env, "-")
	if ok {
		if index := strings.LastIndex(env, "-"); index > 0 {
			slug, suffix = env[:index], env[index+1:]
		}
	}
	if !ok || slug == "" || suffix == "" {
		return synthesizedHostLabelWithSlug(app, env, "")
	}

	return synthesizedHostLabelWithSlug(app, slug, suffix)
}

func synthesizedHostLabelWithSlug(app, slug, suffix string) string {
	slug = strings.Trim(slug, "-")
	suffix = strings.Trim(suffix, "-")
	if slug != "" {
		fixedLength := len(app) + 1
		if suffix != "" {
			fixedLength += len(suffix) + 1
		}
		slugBudget := min(28, 63-fixedLength)
		if slugBudget < 0 {
			slugBudget = 0
		}
		if len(slug) > slugBudget {
			slug = slug[:slugBudget]
		}
		slug = strings.Trim(slug, "-")
	}

	parts := make([]string, 0, 3)
	for _, part := range []string{app, slug, suffix} {
		if part != "" {
			parts = append(parts, part)
		}
	}
	return strings.Join(parts, "-")
}

func ValidGitBranch(branch string) bool {
	if strings.TrimSpace(branch) != branch || branch == "" || branch == "@" {
		return false
	}
	if strings.HasPrefix(branch, "-") || strings.HasPrefix(branch, "/") || strings.HasSuffix(branch, "/") || strings.HasSuffix(branch, ".") {
		return false
	}
	if strings.Contains(branch, "..") || strings.Contains(branch, "@{") || strings.Contains(branch, "\\") {
		return false
	}
	for _, r := range branch {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	for _, part := range strings.Split(branch, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return !strings.ContainsAny(branch, " \t\r\n~^:?*[")
}
