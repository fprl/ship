package client

import (
	"fmt"
	"io"
	"strings"

	"github.com/fprl/ship/internal/config"
)

type diagnosticLevel string
type diagnosticKind string

const (
	diagnosticError   diagnosticLevel = "error"
	diagnosticWarning diagnosticLevel = "warning"

	diagnosticKindManifest          diagnosticKind = "manifest"
	diagnosticKindDockerfileMissing diagnosticKind = "dockerfile_missing"
	diagnosticKindGitNotRepo        diagnosticKind = "git_not_repo"
	diagnosticKindGitNoCommits      diagnosticKind = "git_no_commits"
	diagnosticKindGit               diagnosticKind = "git"
	diagnosticKindStaticHash        diagnosticKind = "static_hash"
	diagnosticKindDotenv            diagnosticKind = "dotenv"
)

type diagnostic struct {
	Kind    diagnosticKind
	Level   diagnosticLevel
	Message string
	Hint    string
}

type diagnostics []diagnostic

func manifestDiagnostics(errors, warnings []string) diagnostics {
	out := make(diagnostics, 0, len(errors)+len(warnings))
	for _, warning := range warnings {
		out = append(out, diagnostic{Kind: diagnosticKindManifest, Level: diagnosticWarning, Message: warning})
	}
	for _, err := range errors {
		kind := diagnosticKindManifest
		if err == config.DockerfileMissingDetail {
			kind = diagnosticKindDockerfileMissing
		}
		out = append(out, diagnostic{Kind: kind, Level: diagnosticError, Message: err})
	}
	return out
}

func (d diagnostics) hasErrors() bool {
	for _, item := range d {
		if item.Level == diagnosticError {
			return true
		}
	}
	return false
}

func (d diagnostics) errorMessages() []string {
	var out []string
	for _, item := range d {
		if item.Level == diagnosticError {
			out = append(out, item.Message)
		}
	}
	return out
}

func (d diagnostics) errors() []diagnostic {
	var out []diagnostic
	for _, item := range d {
		if item.Level == diagnosticError {
			out = append(out, item)
		}
	}
	return out
}

func (d diagnostics) printTo(w io.Writer) {
	for _, item := range d {
		label := "Error"
		if item.Level == diagnosticWarning {
			label = "Warning"
		}
		fmt.Fprintf(w, "%s: %s\n", label, item.Message)
		if item.Hint == "" {
			continue
		}
		for _, line := range strings.Split(item.Hint, "\n") {
			if line == "" {
				fmt.Fprintln(w)
				continue
			}
			fmt.Fprintf(w, "  %s\n", line)
		}
	}
}
