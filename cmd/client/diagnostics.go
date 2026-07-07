package client

import (
	"fmt"
	"strings"
)

type diagnosticLevel string

const (
	diagnosticError   diagnosticLevel = "error"
	diagnosticWarning diagnosticLevel = "warning"
)

type diagnostic struct {
	Level   diagnosticLevel
	Message string
	Hint    string
}

type diagnostics []diagnostic

func manifestDiagnostics(errors, warnings []string) diagnostics {
	out := make(diagnostics, 0, len(errors)+len(warnings))
	for _, warning := range warnings {
		out = append(out, diagnostic{Level: diagnosticWarning, Message: warning})
	}
	for _, err := range errors {
		out = append(out, diagnostic{Level: diagnosticError, Message: err, Hint: manifestHint(err)})
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

func (d diagnostics) print() {
	for _, item := range d {
		label := "Error"
		if item.Level == diagnosticWarning {
			label = "Warning"
		}
		fmt.Printf("%s: %s\n", label, item.Message)
		if item.Hint == "" {
			continue
		}
		for _, line := range strings.Split(item.Hint, "\n") {
			if line == "" {
				fmt.Println()
				continue
			}
			fmt.Printf("  %s\n", line)
		}
	}
}

func manifestHint(message string) string {
	switch {
	case strings.Contains(message, "missing a Dockerfile"):
		return "Add a Dockerfile next to ship.toml, or remove process routes for a static-only app."
	case strings.Contains(message, ".serve directory"):
		return "Run the framework build that creates the configured static directory, then retry."
	case strings.Contains(message, "env not found"):
		return "Check the [env.<name>] blocks in ship.toml and pass one with --env."
	default:
		return ""
	}
}
