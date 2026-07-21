package arch

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
)

func TestImportGraphFixtures(t *testing.T) {
	cases := []struct {
		name     string
		expected []Violation
	}{
		{
			name: "sibling-module",
			expected: []Violation{{
				File: "modules/teal/teal.go", Importer: "modules/teal", Imported: "modules/amber", Rule: RuleSiblingModule,
			}},
		},
		{
			name: "raw-exec",
			expected: []Violation{{
				File: "modules/teal/teal.go", Importer: "modules/teal", Imported: "os/exec", Rule: RuleDomainRawExec,
			}},
		},
		{
			name: "adapter-import",
			expected: []Violation{{
				File: "modules/teal/teal.go", Importer: "modules/teal", Imported: "adapters/podman", Rule: RuleDomainAdapters,
			}},
		},
		{
			name: "builtin-implementation",
			expected: []Violation{{
				File: "builtin/registry.go", Importer: "builtin", Imported: "modules/teal", Rule: RuleBuiltinDefinitions,
			}},
		},
		{
			name: "kernel-domain",
			expected: []Violation{{
				File: "kernel/kernel.go", Importer: "kernel", Imported: "modules/teal", Rule: RuleKernelImports,
			}},
		},
		{name: "clean", expected: nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := filepath.Join("testdata", tc.name)
			got, err := Analyze(root)
			if err != nil {
				t.Fatal(err)
			}
			if fmt.Sprint(got) != fmt.Sprint(tc.expected) {
				t.Fatalf("violations = %#v, want %#v", got, tc.expected)
			}
		})
	}
}

func TestImportGraphRealRepository(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root, err := findRepoRoot(filepath.Dir(file))
	if err != nil {
		t.Fatal(err)
	}
	violations, err := Analyze(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 0 {
		t.Fatalf("import graph violations in %s:\n%s", root, formatViolations(violations))
	}
}

func findRepoRoot(start string) (string, error) {
	directory, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		if info, err := os.Stat(filepath.Join(directory, "go.mod")); err == nil && !info.IsDir() {
			return directory, nil
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return "", fmt.Errorf("could not find go.mod above %s", start)
		}
		directory = parent
	}
}

func formatViolations(violations []Violation) string {
	lines := make([]string, len(violations))
	for i, violation := range violations {
		lines[i] = violation.String()
	}
	sort.Strings(lines)
	return joinLines(lines)
}

func joinLines(lines []string) string {
	result := ""
	for i, line := range lines {
		if i > 0 {
			result += "\n"
		}
		result += line
	}
	return result
}
