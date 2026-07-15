package cliargs

import (
	"reflect"
	"strings"
	"testing"
)

func FuzzPassthrough(f *testing.F) {
	for _, seed := range [][]string{
		{"sh", "-c", "echo hi"},
		{"--", "sh", "-c", "echo hi"},
		{"--", "--flag-first-cmd"},
		{},
	} {
		f.Add([]byte(strings.Join(seed, "\x00")))
	}

	f.Fuzz(func(t *testing.T, encoded []byte) {
		args := strings.Split(string(encoded), "\x00")
		got := TrimLeadingPassthroughSeparator(args)
		want := args
		if len(args) > 0 && args[0] == "--" {
			want = args[1:]
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("TrimLeadingPassthroughSeparator(%q) = %q, want %q", args, got, want)
		}
	})
}
