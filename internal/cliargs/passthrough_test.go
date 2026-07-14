package cliargs

import (
	"reflect"
	"testing"
)

func TestTrimLeadingPassthroughSeparator(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{name: "command without separator", args: []string{"sh", "-c", "echo hi"}, want: []string{"sh", "-c", "echo hi"}},
		{name: "separator before command", args: []string{"--", "sh", "-c", "echo hi"}, want: []string{"sh", "-c", "echo hi"}},
		{name: "separator before dash command", args: []string{"--", "--flag-first-cmd"}, want: []string{"--flag-first-cmd"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := TrimLeadingPassthroughSeparator(tt.args); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("TrimLeadingPassthroughSeparator(%q) = %q, want %q", tt.args, got, tt.want)
			}
		})
	}
}
