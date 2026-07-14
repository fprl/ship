// Package cliargs contains small command-line argument helpers shared by the
// public client and privileged helper command surfaces.
package cliargs

// TrimLeadingPassthroughSeparator removes the separator retained by Kong for a
// passthrough argument. It only consumes the first token, so a command whose
// name begins with a dash is preserved.
func TrimLeadingPassthroughSeparator(args []string) []string {
	if len(args) > 0 && args[0] == "--" {
		return args[1:]
	}
	return args
}
